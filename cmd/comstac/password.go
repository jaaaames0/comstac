package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"comstac/internal/auth"
	"comstac/internal/store"
	"golang.org/x/term"
)

func runRotatePassword(args []string) error {
	flags := flag.NewFlagSet("rotate-password", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databasePath := flags.String("database", "", "absolute SQLite database path")
	username := flags.String("username", "", "existing username")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("usage: comstac rotate-password --database /absolute/path --username USER")
	}
	if !filepath.IsAbs(*databasePath) {
		return fmt.Errorf("database must be an absolute path")
	}
	info, err := os.Lstat(*databasePath)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("database must name an existing regular file")
	}
	if strings.TrimSpace(*username) == "" {
		return fmt.Errorf("username is required")
	}

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("password input requires an interactive terminal")
	}
	first, err := readSecret(fd, "new password: ")
	if err != nil {
		return err
	}
	defer clearBytes(first)
	second, err := readSecret(fd, "confirm new password: ")
	if err != nil {
		return err
	}
	defer clearBytes(second)
	if !bytes.Equal(first, second) {
		return fmt.Errorf("passwords do not match")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.OpenAndMigrate(ctx, *databasePath)
	if err != nil {
		return fmt.Errorf("open database")
	}
	defer db.Close()

	mgr := auth.NewManager(db, "", 0, "")
	if err := mgr.RotatePassword(ctx, *username, first); err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			return fmt.Errorf("user not found")
		}
		return fmt.Errorf("update credentials")
	}
	fmt.Println("password rotated; all sessions revoked")
	return nil
}

func readSecret(fd int, prompt string) ([]byte, error) {
	_, _ = fmt.Fprint(os.Stderr, prompt)
	secret, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("read password")
	}
	return secret, nil
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
