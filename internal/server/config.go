package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/hkdf"
)

// Config is loaded from environment variables (12-factor style, Docker friendly).
type Config struct {
	Listen        string // LISTEN, default ":8080"
	DataDir       string // DATA_DIR, default "/data"
	AdminUser     string // ADMIN_USER, default "admin"
	AdminPassword string // ADMIN_PASSWORD; generated and logged once if empty on first boot
	CookieSecure  bool   // COOKIE_SECURE=true when served over HTTPS (reverse proxy)

	// Optional: pre-enrol an agent so `docker compose up` works with no manual step.
	BootstrapAgentName string // BOOTSTRAP_AGENT_NAME
	BootstrapAgentKey  string // BOOTSTRAP_AGENT_KEY ("wbk_" + >= 32 chars)

	// Derived keys (from SERVER_SECRET, or a generated key persisted in DataDir).
	jwtKey []byte
	encKey []byte
}

func LoadConfig() (*Config, error) {
	c := &Config{
		Listen:             env("LISTEN", ":8080"),
		DataDir:            env("DATA_DIR", "/data"),
		AdminUser:          env("ADMIN_USER", "admin"),
		AdminPassword:      os.Getenv("ADMIN_PASSWORD"),
		CookieSecure:       strings.EqualFold(os.Getenv("COOKIE_SECURE"), "true"),
		BootstrapAgentName: env("BOOTSTRAP_AGENT_NAME", "demo-agent"),
		BootstrapAgentKey:  os.Getenv("BOOTSTRAP_AGENT_KEY"),
	}
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	if k := c.BootstrapAgentKey; k != "" && (!strings.HasPrefix(k, "wbk_") || len(k) < 36) {
		return nil, fmt.Errorf("BOOTSTRAP_AGENT_KEY must start with \"wbk_\" followed by at least 32 characters (try: echo wbk_$(openssl rand -hex 32))")
	}

	master, err := loadMasterSecret(c.DataDir)
	if err != nil {
		return nil, err
	}
	if c.jwtKey, err = derive(master, "wasabi-backup/jwt/v1"); err != nil {
		return nil, err
	}
	if c.encKey, err = derive(master, "wasabi-backup/credential-encryption/v1"); err != nil {
		return nil, err
	}
	return c, nil
}

// loadMasterSecret returns SERVER_SECRET, or a random secret persisted at
// DATA_DIR/server.secret (0600) so restarts keep sessions and stored
// credentials decryptable. Operators should set SERVER_SECRET explicitly and
// back it up: losing it makes the stored Wasabi secrets unreadable.
func loadMasterSecret(dataDir string) ([]byte, error) {
	if s := os.Getenv("SERVER_SECRET"); s != "" {
		if len(s) < 32 {
			return nil, fmt.Errorf("SERVER_SECRET must be at least 32 characters (try: openssl rand -hex 32)")
		}
		return []byte(s), nil
	}
	p := filepath.Join(dataDir, "server.secret")
	if b, err := os.ReadFile(p); err == nil && len(b) >= 32 {
		return b, nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	b := []byte(hex.EncodeToString(raw))
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return nil, fmt.Errorf("persist generated secret: %w", err)
	}
	log.Printf("SERVER_SECRET not set: generated one at %s (set SERVER_SECRET in production and back it up)", p)
	return b, nil
}

func derive(master []byte, info string) ([]byte, error) {
	out := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, master, nil, []byte(info)), out); err != nil {
		return nil, err
	}
	return out, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
