package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"comstac/internal/api"
	"comstac/internal/auth"
	"comstac/internal/config"
	"comstac/internal/imap"
	"comstac/internal/ingest"
	"comstac/internal/push"
	"comstac/internal/relay"
	"comstac/internal/securitymetrics"
	"comstac/internal/smtpserver"
	"comstac/internal/storageguard"
	"comstac/internal/store"
	syncer "comstac/internal/sync"
	"comstac/internal/ui"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "authorize":
			runAuthorize()
			return
		case "rotate-password":
			if err := runRotatePassword(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "password rotation failed: %v\n", err)
				os.Exit(1)
			}
			return
		case "vapid":
			runVAPID()
			return
		case "check-config":
			runCheckConfig()
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
			os.Exit(2)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("load configuration", "err", err)
		os.Exit(1)
	}
	tlsReloader, err := smtpserver.NewCertificateReloader(cfg.SMTPTLSCert, cfg.SMTPTLSKey, cfg.SMTPDomain, time.Now())
	if err != nil {
		slog.Error("configure inbound SMTP TLS", "err", err)
		os.Exit(1)
	}
	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)
	defer signal.Stop(hupCh)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hupCh:
				if err := tlsReloader.Reload(time.Now()); err != nil {
					slog.Error("reload inbound SMTP TLS certificate", "err", err)
					continue
				}
				slog.Info("inbound SMTP TLS certificate reloaded")
			}
		}
	}()
	if err := api.ValidateACMEChallengeDir(cfg.ACMEChallengeDir); err != nil {
		slog.Error("configure ACME HTTP-01 challenge", "err", err)
		os.Exit(1)
	}

	capacity, err := storageguard.New(filepath.Dir(cfg.DBPath), storageguard.Limits{
		MaxStateBytes: cfg.StorageMaxBytes,
		MinFreeBytes:  cfg.StorageMinFreeBytes,
		WarnFreeBytes: cfg.StorageWarnFreeBytes,
	})
	if err != nil {
		slog.Error("configure storage guard", "err", err)
		os.Exit(1)
	}

	db, err := store.OpenAndMigrateWithLimit(ctx, cfg.DBPath, cfg.StorageMaxBytes)
	if err != nil {
		slog.Error("open store", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	if snapshot, snapshotErr := capacity.Snapshot(); snapshotErr != nil {
		slog.Error("measure storage capacity", "component", "storage", "err", snapshotErr)
	} else {
		level := slog.LevelInfo
		if snapshot.Warning {
			level = slog.LevelWarn
		}
		slog.Log(ctx, level, "storage capacity", "component", "storage",
			"state_bytes", snapshot.StateBytes, "available_bytes", snapshot.AvailableBytes,
			"max_state_bytes", snapshot.MaxStateBytes, "min_free_bytes", snapshot.MinFreeBytes,
			"warn_free_bytes", snapshot.WarnFreeBytes, "warning", snapshot.Warning,
			"rejecting", snapshot.Rejecting, "reason", snapshot.Reason)
	}

	csrfSecret := cfg.CSRFSecret
	if csrfSecret == "" {
		h := sha256.Sum256([]byte("comstac-csrf:" + cfg.AdminPassword))
		csrfSecret = hex.EncodeToString(h[:])
	}

	authMgr := auth.NewManager(db, "", cfg.SessionTTL, csrfSecret)
	if err := authMgr.BootstrapUser(ctx, cfg.AdminUsername, cfg.AdminPassword); err != nil {
		slog.Error("bootstrap auth user", "err", err)
		os.Exit(1)
	}
	if err := store.EnsureLocalAccounts(ctx, db, cfg.LocalRecipients); err != nil {
		slog.Error("ensure local accounts", "err", err)
		os.Exit(1)
	}

	securityCounters := &securitymetrics.Counters{}
	ingestor := ingest.NewService(db)
	ingestor.SetSecurityCounters(securityCounters)
	ingestor.SetStorageGuard(capacity)

	var agentClients *push.AgentClients
	if cfg.AgentToken != "" {
		agentClients = push.NewAgentClients(cfg.AgentToken)
		slog.Info("agent SSE endpoint enabled", "component", "push", "path", "/api/push/sse")
	} else {
		slog.Info("agent SSE not configured", "component", "push", "hint", "set COMSTAC_AGENT_TOKEN to enable")
	}

	var pushNotifier *push.Notifier
	if cfg.VAPIDPublicKey != "" && cfg.VAPIDPrivateKey != "" && cfg.VAPIDSubject != "" {
		pushNotifier, err = push.New(db, cfg.VAPIDPublicKey, cfg.VAPIDPrivateKey, cfg.VAPIDSubject, agentClients)
		if err != nil {
			slog.Error("configure push notifications", "err", err)
			os.Exit(1)
		}
		ingestor.SetNotifier(pushNotifier)
		slog.Info("push notifications enabled", "component", "push")
	} else {
		slog.Info("push notifications not configured", "component", "push", "hint", "run 'comstac vapid' to generate keys")
	}

	smtpSrv := smtpserver.New(cfg.SMTPAddr, ingestor, smtpserver.Options{
		Domain:          cfg.SMTPDomain,
		LocalDomains:    cfg.LocalDomains,
		LocalRecipients: cfg.LocalRecipients,
		ResolveAccountID: func(ctx context.Context, recipient string) (sql.NullInt64, error) {
			return store.ResolveLocalAccountID(ctx, db, recipient)
		},
		CheckStorage:     capacity.CheckPayload,
		TLSConfig:        tlsReloader.TLSConfig(),
		SecurityCounters: securityCounters,
	})

	var outRelay *relay.Relay
	if cfg.RelayHost != "" && cfg.RelayUsername != "" && cfg.RelayPassword != "" {
		outRelay = relay.New(relay.Config{
			Host:     cfg.RelayHost,
			Port:     cfg.RelayPort,
			Username: cfg.RelayUsername,
			Password: cfg.RelayPassword,
			From:     cfg.RelayFrom,
		})
		slog.Info("outbound relay configured", "component", "relay", "host", cfg.RelayHost, "port", cfg.RelayPort, "from", cfg.RelayFrom)
	} else {
		slog.Warn("outbound relay not configured", "component", "relay", "hint", "set COMSTAC_RELAY_HOST/USERNAME/PASSWORD")
	}

	syncRunner := syncer.NewRunner(db, 3*time.Second, imap.NewNoopStateSyncAdapter(false))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Build TokenSource: DB-persisted token takes priority over env (allows
	// web-UI re-authorization to survive service restarts without .env edits).
	var ts *imap.TokenSource
	if cfg.IMAPClientID != "" && cfg.IMAPClientSecret != "" && cfg.IMAPUsername != "" {
		refreshToken := cfg.IMAPRefreshToken
		if dbToken, ok, err := store.GetSetting(ctx, db, "imap.refresh_token"); err == nil && ok && dbToken != "" {
			refreshToken = dbToken
		}
		if refreshToken != "" {
			ts = &imap.TokenSource{
				ClientID:     cfg.IMAPClientID,
				ClientSecret: cfg.IMAPClientSecret,
				RefreshToken: refreshToken,
			}
		}
	}

	var uiCfg *ui.UIConfig
	if ts != nil || pushNotifier != nil {
		uiCfg = &ui.UIConfig{
			TokenSource:    ts,
			VAPIDPublicKey: cfg.VAPIDPublicKey,
		}
	}

	apiSrv := api.New(cfg.HTTPAddr, db, authMgr, outRelay, uiCfg, agentClients)
	apiSrv.SetSecurityCounters(securityCounters)
	apiSrv.SetStorageGuard(capacity)
	apiSrv.SetACMEChallengeDir(cfg.ACMEChallengeDir)

	workers := 3
	errCh := make(chan error, 5)
	go func() { errCh <- smtpSrv.Run(runCtx) }()
	go func() { errCh <- apiSrv.Run(runCtx) }()
	go func() { errCh <- syncRunner.Run(runCtx) }()
	if pushNotifier != nil {
		workers++
		go func() { errCh <- pushNotifier.Run(runCtx) }()
	}

	if ts != nil {
		workers++
		fetcher := imap.NewFetcher(imap.FetcherConfig{
			Addr:         cfg.IMAPAddr,
			Username:     cfg.IMAPUsername,
			Mailbox:      cfg.IMAPMailbox,
			PollInterval: cfg.IMAPPollInterval,
			TokenSource:  ts,
		}, db, ingestor)
		go func() { errCh <- fetcher.Run(runCtx) }()
	} else {
		slog.Warn("imap fetcher not started", "component", "imap", "hint", "run 'comstac authorize' to configure")
	}

	for i := 0; i < workers; i++ {
		err := <-errCh
		if err == nil || errors.Is(err, context.Canceled) {
			continue
		}
		cancel()
		slog.Error("server stopped with error", "err", err)
		os.Exit(1)
	}
}

func runCheckConfig() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration invalid: %v\n", err)
		os.Exit(1)
	}
	if _, err := smtpserver.NewCertificateReloader(cfg.SMTPTLSCert, cfg.SMTPTLSKey, cfg.SMTPDomain, time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "configuration invalid: %v\n", err)
		os.Exit(1)
	}
	if err := api.ValidateACMEChallengeDir(cfg.ACMEChallengeDir); err != nil {
		fmt.Fprintf(os.Stderr, "configuration invalid: %v\n", err)
		os.Exit(1)
	}
	if cfg.VAPIDPublicKey != "" {
		if _, err := push.New(nil, cfg.VAPIDPublicKey, cfg.VAPIDPrivateKey, cfg.VAPIDSubject, nil); err != nil {
			fmt.Fprintf(os.Stderr, "configuration invalid: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Println("configuration=valid")
}

func runAuthorize() {
	clientID := os.Getenv("COMSTAC_IMAP_CLIENT_ID")
	clientSecret := os.Getenv("COMSTAC_IMAP_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		fmt.Fprintln(os.Stderr, "error: COMSTAC_IMAP_CLIENT_ID and COMSTAC_IMAP_CLIENT_SECRET must be set")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Steps:")
		fmt.Fprintln(os.Stderr, "  1. Go to https://console.cloud.google.com/")
		fmt.Fprintln(os.Stderr, "  2. Create a project and enable the Gmail API")
		fmt.Fprintln(os.Stderr, "  3. Create OAuth 2.0 credentials (Desktop app type)")
		fmt.Fprintln(os.Stderr, "  4. Set COMSTAC_IMAP_CLIENT_ID and COMSTAC_IMAP_CLIENT_SECRET in your .env")
		fmt.Fprintln(os.Stderr, "  5. Re-run: source .env && comstac authorize")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	refreshToken, err := imap.Authorize(ctx, clientID, clientSecret)
	if err != nil {
		fmt.Fprintf(os.Stderr, "authorization failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("Authorization successful! Add this to your .env:")
	fmt.Println()
	fmt.Printf("  export COMSTAC_IMAP_REFRESH_TOKEN=%s\n", refreshToken)
	fmt.Println()
}

func runVAPID() {
	pub, priv, err := push.GenerateVAPIDKeys()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to generate VAPID keys: %v\n", err)
		os.Exit(1)
	}
	fmt.Println()
	fmt.Println("VAPID keys generated. Add these to your environment:")
	fmt.Println()
	fmt.Printf("  export COMSTAC_VAPID_PUBLIC=%s\n", pub)
	fmt.Printf("  export COMSTAC_VAPID_PRIVATE=%s\n", priv)
	fmt.Println()
	fmt.Println("You also need to set COMSTAC_VAPID_SUBJECT (e.g. mailto:you@example.com)")
	fmt.Println("to a contact address for your domain's VAPID configuration.")
	fmt.Println()
}
