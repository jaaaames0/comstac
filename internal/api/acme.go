package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	acmeChallengePrefix = "/.well-known/acme-challenge/"
	maxACMETokenBytes   = 4096
	maxACMETokenLength  = 200
)

// ValidateACMEChallengeDir checks only the dedicated public-token directory.
// Errors intentionally omit its configured path.
func ValidateACMEChallengeDir(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return errors.New("ACME challenge directory is unreadable or invalid")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		return errors.New("ACME challenge directory is unreadable or invalid")
	}
	return nil
}

func (s *Server) handleACMEChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, acmeChallengePrefix)
	if s.acmeDir == "" || !validACMEToken(token) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.acmeDir, token)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxACMETokenBytes {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > maxACMETokenBytes {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

func validACMEToken(token string) bool {
	if len(token) == 0 || len(token) > maxACMETokenLength {
		return false
	}
	for _, char := range token {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}
