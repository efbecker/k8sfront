// Command k8sfront-api is the control plane of the k8sfront platform.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/api"
	"github.com/efbecker/k8sfront/apps/api/internal/config"
	"github.com/efbecker/k8sfront/apps/api/internal/jobs"
	"github.com/efbecker/k8sfront/apps/api/internal/k8sver"
	"github.com/efbecker/k8sfront/apps/api/internal/ops"
	"github.com/efbecker/k8sfront/apps/api/internal/remote"
	"github.com/efbecker/k8sfront/apps/api/internal/secrets"
	"github.com/efbecker/k8sfront/apps/api/internal/sshx"
	"github.com/efbecker/k8sfront/apps/api/internal/store/sqlite"
	"github.com/efbecker/k8sfront/apps/api/internal/vault"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "k8sfront-api:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.LogLevel))
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	key, err := vault.LoadKey(cfg.MasterKey, cfg.MasterKeyFile)
	if err != nil {
		return err
	}
	v, err := vault.New(key)
	if err != nil {
		return err
	}
	adminToken, err := resolveAdminToken(cfg, logger)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := sqlite.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	mode, _ := db.JournalMode(ctx)
	logger.Info("database ready", "path", cfg.DBPath, "journal_mode", mode, "vault_key_id", v.KeyID())

	sec := secrets.NewManager(db, v)
	dialer := sshx.NewDialer(db)
	if cfg.StrictHostKeys {
		dialer.Policy = sshx.PolicyStrict
	}
	conn := &remote.Connector{Store: db, Secrets: sec, Dialer: dialer}

	hub := jobs.NewHub(db, logger)
	engine := jobs.New(db, hub, logger, cfg.Workers)
	(&ops.Ops{Store: db, Connector: conn, Secrets: sec}).Register(engine)
	if err := engine.Start(ctx); err != nil {
		return fmt.Errorf("start job engine: %w", err)
	}

	srv := &api.Server{
		Store: db, Secrets: sec, Engine: engine, Connector: conn, Log: logger,
		Auth:           api.NewAuth(adminToken, key, cfg.SessionTTL, cfg.SecureCookies),
		Versions:       k8sver.NewFetcher(),
		WebDir:         cfg.WebDir,
		AllowedOrigins: cfg.AllowedOrigins,
	}
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Addr, "version", api.Version)
		errCh <- httpSrv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	logger.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(sctx)
	engine.Shutdown(sctx)
	return nil
}

// resolveAdminToken uses K8SFRONT_ADMIN_TOKEN or a token file generated in the
// data directory on first start (never logged).
func resolveAdminToken(cfg *config.Config, logger *slog.Logger) (string, error) {
	if cfg.AdminToken != "" {
		if len(cfg.AdminToken) < 16 {
			return "", errors.New("K8SFRONT_ADMIN_TOKEN must be at least 16 characters")
		}
		return cfg.AdminToken, nil
	}
	path := filepath.Join(cfg.DataDir, "admin.token")
	if b, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write admin token: %w", err)
	}
	logger.Warn("generated admin token; read it from the file to log in", "file", path)
	return tok, nil
}
