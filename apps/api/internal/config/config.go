// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the process configuration.
type Config struct {
	Addr           string        // K8SFRONT_ADDR, default :8080
	DataDir        string        // K8SFRONT_DATA_DIR, default ./data
	DBPath         string        // K8SFRONT_DB_PATH, default <data>/k8sfront.db
	MasterKey      string        // K8SFRONT_MASTER_KEY (base64, 32 bytes)
	MasterKeyFile  string        // K8SFRONT_MASTER_KEY_FILE, default <data>/master.key
	AdminToken     string        // K8SFRONT_ADMIN_TOKEN; generated into <data>/admin.token when empty
	WebDir         string        // K8SFRONT_WEB_DIR: built SPA to serve (optional)
	AllowedOrigins []string      // K8SFRONT_ALLOWED_ORIGINS: extra WebSocket origins (host patterns)
	Workers        int           // K8SFRONT_WORKERS, default 4
	StrictHostKeys bool          // K8SFRONT_STRICT_HOST_KEYS: reject unknown hosts instead of TOFU
	SessionTTL     time.Duration // K8SFRONT_SESSION_TTL, default 12h
	SecureCookies  bool          // K8SFRONT_SECURE_COOKIES (set behind HTTPS)
	LogLevel       string        // K8SFRONT_LOG_LEVEL: debug|info|warn|error
}

// Load reads the environment.
func Load() (*Config, error) {
	c := &Config{
		Addr:           env("K8SFRONT_ADDR", ":8080"),
		DataDir:        env("K8SFRONT_DATA_DIR", "./data"),
		MasterKey:      os.Getenv("K8SFRONT_MASTER_KEY"),
		AdminToken:     os.Getenv("K8SFRONT_ADMIN_TOKEN"),
		WebDir:         os.Getenv("K8SFRONT_WEB_DIR"),
		LogLevel:       env("K8SFRONT_LOG_LEVEL", "info"),
		SecureCookies:  envBool("K8SFRONT_SECURE_COOKIES", false),
		StrictHostKeys: envBool("K8SFRONT_STRICT_HOST_KEYS", false),
	}
	c.DBPath = env("K8SFRONT_DB_PATH", filepath.Join(c.DataDir, "k8sfront.db"))
	c.MasterKeyFile = env("K8SFRONT_MASTER_KEY_FILE", filepath.Join(c.DataDir, "master.key"))
	for _, o := range strings.Split(os.Getenv("K8SFRONT_ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			c.AllowedOrigins = append(c.AllowedOrigins, o)
		}
	}
	var err error
	if c.Workers, err = strconv.Atoi(env("K8SFRONT_WORKERS", "4")); err != nil || c.Workers < 1 {
		return nil, fmt.Errorf("K8SFRONT_WORKERS must be a positive integer")
	}
	if c.SessionTTL, err = time.ParseDuration(env("K8SFRONT_SESSION_TTL", "12h")); err != nil {
		return nil, fmt.Errorf("K8SFRONT_SESSION_TTL: %w", err)
	}
	return c, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if v, err := strconv.ParseBool(os.Getenv(k)); err == nil {
		return v
	}
	return def
}
