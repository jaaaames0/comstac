package imap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	googleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL = "https://oauth2.googleapis.com/token"
	gmailScope     = "https://mail.google.com/"
)

// TokenSource caches a Google OAuth2 access token and refreshes it as needed.
type TokenSource struct {
	ClientID     string
	ClientSecret string
	RefreshToken string

	mu          sync.Mutex
	accessToken string
	expiry      time.Time
	authErr     error     // set when the last refresh attempt got invalid_grant
	authErrAt   time.Time // when authErr was first recorded
}

// UpdateRefreshToken replaces the stored refresh token (e.g. after web-based re-authorization).
// Clears any cached access token and any recorded auth error.
func (ts *TokenSource) UpdateRefreshToken(token string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.RefreshToken = token
	ts.accessToken = ""
	ts.expiry = time.Time{}
	ts.authErr = nil
	ts.authErrAt = time.Time{}
}

// AuthError returns the most recent auth failure error and when it occurred,
// or (nil, zero) if the token source is healthy.
func (ts *TokenSource) AuthError() (error, time.Time) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.authErr, ts.authErrAt
}

// AccessToken returns a valid access token, refreshing if necessary.
func (ts *TokenSource) AccessToken(ctx context.Context) (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ts.accessToken != "" && time.Until(ts.expiry) > 60*time.Second {
		return ts.accessToken, nil
	}

	token, expiry, err := refreshAccessToken(ctx, ts.ClientID, ts.ClientSecret, ts.RefreshToken)
	if err != nil {
		// Track invalid_grant specifically so the UI can surface it.
		if strings.Contains(err.Error(), "invalid_grant") {
			if ts.authErr == nil {
				ts.authErrAt = time.Now()
			}
			ts.authErr = err
		}
		return "", err
	}
	// Clear any prior auth error on success.
	ts.authErr = nil
	ts.authErrAt = time.Time{}
	ts.accessToken = token
	ts.expiry = expiry
	return token, nil
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

func refreshAccessToken(ctx context.Context, clientID, clientSecret, refreshToken string) (string, time.Time, error) {
	body := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL,
		strings.NewReader(body.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("token refresh request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read token response: %w", err)
	}

	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return "", time.Time{}, fmt.Errorf("decode token response: %w", err)
	}
	if tr.Error != "" {
		return "", time.Time{}, fmt.Errorf("token error %s: %s", tr.Error, tr.ErrorDesc)
	}
	expiry := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	return tr.AccessToken, expiry, nil
}

// Authorize runs the OAuth2 loopback authorization flow for Gmail.
// It prints an authorization URL, waits for the browser callback, exchanges
// the code for tokens, and returns the refresh token.
func Authorize(ctx context.Context, clientID, clientSecret string) (refreshToken string, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("start callback listener: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	params := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {gmailScope},
		"access_type":   {"offline"},
		"prompt":        {"consent"},
	}
	authURL := googleAuthURL + "?" + params.Encode()

	fmt.Println("Open this URL in your browser to authorize Gmail access:")
	fmt.Println()
	fmt.Println(" ", authURL)
	fmt.Println()
	fmt.Println("Waiting for callback...")

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errMsg := r.URL.Query().Get("error")
			http.Error(w, "authorization failed: "+errMsg, http.StatusBadRequest)
			errCh <- fmt.Errorf("authorization denied: %s", errMsg)
			return
		}
		fmt.Fprintln(w, "<html><body><p>Authorization successful. You can close this tab.</p></body></html>")
		codeCh <- code
	})

	srv := &http.Server{Handler: mux}
	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- serveErr
		}
	}()
	defer srv.Shutdown(context.Background()) //nolint

	select {
	case code := <-codeCh:
		return exchangeCode(ctx, clientID, clientSecret, code, redirectURI)
	case err := <-errCh:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type codeExchangeResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// ExchangeCode exchanges an OAuth2 authorization code for a refresh token.
// Used by both the CLI authorize flow and the web UI callback handler.
func ExchangeCode(ctx context.Context, clientID, clientSecret, code, redirectURI string) (string, error) {
	return exchangeCode(ctx, clientID, clientSecret, code, redirectURI)
}

func exchangeCode(ctx context.Context, clientID, clientSecret, code, redirectURI string) (string, error) {
	body := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL,
		strings.NewReader(body.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("code exchange request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read exchange response: %w", err)
	}

	var cr codeExchangeResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return "", fmt.Errorf("decode exchange response: %w", err)
	}
	if cr.Error != "" {
		return "", fmt.Errorf("exchange error %s: %s", cr.Error, cr.ErrorDesc)
	}
	if cr.RefreshToken == "" {
		return "", fmt.Errorf("no refresh token returned — ensure access_type=offline and prompt=consent")
	}
	return cr.RefreshToken, nil
}
