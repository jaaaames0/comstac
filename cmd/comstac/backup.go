package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"comstac/internal/config"
	_ "modernc.org/sqlite"
)

func runBackup() {
	// Load env file so `comstac backup` works standalone without the caller
	// having to source the file first. Explicit env vars always take precedence.
	loadEnvFile("/etc/comstac/comstac.env")
	loadEnvFile("comstac.env") // dev fallback (cwd)
	cfg := config.FromEnv()
	ctx := context.Background()

	if err := os.MkdirAll(cfg.BackupDir, 0750); err != nil {
		slog.Error("backup: create backup dir", "component", "backup", "dir", cfg.BackupDir, "err", err)
		os.Exit(1)
	}

	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		slog.Error("backup: open db", "component", "backup", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	timestamp := time.Now().UTC().Format("20060102_150405")
	filename := fmt.Sprintf("comstac_%s.sqlite", timestamp)
	localPath := filepath.Join(cfg.BackupDir, filename)

	slog.Info("backup: creating snapshot", "component", "backup", "src", cfg.DBPath, "dest", localPath)
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", localPath); err != nil {
		slog.Error("backup: vacuum into failed", "component", "backup", "err", err)
		os.Exit(1)
	}
	slog.Info("backup: snapshot created", "component", "backup", "path", localPath)

	if cfg.BackupDest != "" {
		slog.Info("backup: transferring to remote", "component", "backup", "dest", cfg.BackupDest)
		if err := scpFile(localPath, cfg.BackupDest, cfg.BackupKey); err != nil {
			slog.Error("backup: remote transfer failed", "component", "backup", "err", err)
			os.Exit(1)
		}
		slog.Info("backup: remote transfer complete", "component", "backup", "dest", cfg.BackupDest)
	}

	pruneBackups(cfg.BackupDir, cfg.BackupRetain)
	slog.Info("backup: done", "component", "backup")
}

func scpFile(localPath, dest, keyPath string) error {
	// StrictHostKeyChecking=no avoids needing root's known_hosts populated.
	// Acceptable here: the destination is explicitly operator-configured.
	args := []string{"-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes"}
	if keyPath != "" {
		args = append(args, "-i", keyPath)
	}
	args = append(args, localPath, dest)
	cmd := exec.Command("scp", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// loadEnvFile reads a KEY=value env file and sets any unset env vars from it.
// Lines starting with # and blank lines are ignored. Explicit env vars win.
func loadEnvFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		val = strings.Trim(val, `"'`)
		if key != "" && os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}

func pruneBackups(dir string, retain int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "comstac_") && strings.HasSuffix(e.Name(), ".sqlite") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files) // lexicographic = chronological given timestamp format
	for len(files) > retain {
		if err := os.Remove(files[0]); err == nil {
			slog.Info("backup: pruned old snapshot", "component", "backup", "file", filepath.Base(files[0]))
		}
		files = files[1:]
	}
}
