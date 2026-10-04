package agent

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config is read from environment variables (see docker-compose.yml).
type Config struct {
	ServerURL     string   // AGENT_SERVER_URL, e.g. https://backup.example.com
	APIKey        string   // AGENT_API_KEY, or the contents of AGENT_API_KEY_FILE (Docker/K8s secret)
	BrowseRoots   []string // AGENT_BROWSE_ROOTS, default "/host,/data"
	RclonePath    string   // AGENT_RCLONE_PATH, default "rclone"
	MaxConcurrent int      // AGENT_MAX_CONCURRENT, default 1
	CAFile        string   // AGENT_CA_FILE: extra CA bundle for a private-CA dashboard
	Hostname      string   // AGENT_HOSTNAME override for what the dashboard shows
}

func LoadConfig() (*Config, error) {
	c := &Config{
		ServerURL:  strings.TrimRight(os.Getenv("AGENT_SERVER_URL"), "/"),
		APIKey:     strings.TrimSpace(os.Getenv("AGENT_API_KEY")),
		RclonePath: env("AGENT_RCLONE_PATH", "rclone"),
		CAFile:     os.Getenv("AGENT_CA_FILE"),
		Hostname:   os.Getenv("AGENT_HOSTNAME"),
	}
	if f := os.Getenv("AGENT_API_KEY_FILE"); f != "" && c.APIKey == "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read AGENT_API_KEY_FILE: %w", err)
		}
		c.APIKey = strings.TrimSpace(string(b))
	}
	if c.ServerURL == "" {
		return nil, errors.New("AGENT_SERVER_URL is required")
	}
	if !strings.HasPrefix(c.ServerURL, "http://") && !strings.HasPrefix(c.ServerURL, "https://") {
		return nil, errors.New("AGENT_SERVER_URL must start with http:// or https://")
	}
	if !strings.HasPrefix(c.APIKey, "wbk_") {
		return nil, errors.New("AGENT_API_KEY (or AGENT_API_KEY_FILE) is required; create an agent in the dashboard to get one")
	}
	for _, r := range strings.Split(env("AGENT_BROWSE_ROOTS", "/host,/data"), ",") {
		if r = strings.TrimSpace(r); r != "" {
			c.BrowseRoots = append(c.BrowseRoots, r)
		}
	}
	c.MaxConcurrent = 1
	if v := os.Getenv("AGENT_MAX_CONCURRENT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 16 {
			return nil, errors.New("AGENT_MAX_CONCURRENT must be 1-16")
		}
		c.MaxConcurrent = n
	}
	if c.Hostname == "" {
		c.Hostname, _ = os.Hostname()
	}
	return c, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
