package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/stylishseahorse/rclone-backup-manager/internal/proto"
)

// apiClient talks to the dashboard's /api/agent/* endpoints over HTTPS with the
// agent's API key as a bearer token.
type apiClient struct {
	base string
	key  string
	http *http.Client
}

func newAPIClient(cfg *Config) (*apiClient, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read AGENT_CA_FILE: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("AGENT_CA_FILE contains no valid certificates")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &apiClient{base: cfg.ServerURL, key: cfg.APIKey, http: &http.Client{Transport: tr, Timeout: 30 * time.Second}}, nil
}

func (c *apiClient) header() http.Header {
	return http.Header{"Authorization": {"Bearer " + c.key}}
}

// httpError carries the status so callers can tell "retry" from "give up".
type httpError struct {
	Status int
	Msg    string
}

func (e *httpError) Error() string { return fmt.Sprintf("server returned %d: %s", e.Status, e.Msg) }

// retryable: network errors, 5xx and 429. A 4xx (bad key, unknown run) won't fix itself.
func retryable(err error) bool {
	if he, ok := err.(*httpError); ok {
		return he.Status >= 500 || he.Status == 429
	}
	return err != nil
}

func (c *apiClient) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header = c.header()
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = http.StatusText(resp.StatusCode)
		}
		return &httpError{Status: resp.StatusCode, Msg: e.Error}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// withRetry runs fn with exponential backoff while it fails with a retryable error.
func withRetry(ctx context.Context, attempts int, fn func() error) error {
	delay := time.Second
	var err error
	for i := 0; i < attempts; i++ {
		if err = fn(); err == nil || !retryable(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < 15*time.Second {
			delay *= 2
		}
	}
	return err
}

func (c *apiClient) GetConfig(ctx context.Context) (*proto.AgentConfig, error) {
	var cfg proto.AgentConfig
	if err := c.do(ctx, http.MethodGet, "/api/agent/config", nil, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *apiClient) StartRun(ctx context.Context, jobID, trigger string) (string, error) {
	var res proto.StartRunResponse
	err := withRetry(ctx, 6, func() error {
		return c.do(ctx, http.MethodPost, "/api/agent/runs", proto.StartRunRequest{JobID: jobID, Trigger: trigger}, &res)
	})
	return res.RunID, err
}

func (c *apiClient) PostLogs(ctx context.Context, runID string, lines []proto.LogLine) error {
	return c.do(ctx, http.MethodPost, "/api/agent/runs/"+runID+"/logs", proto.LogBatch{Lines: lines}, nil)
}

func (c *apiClient) FinishRun(ctx context.Context, runID string, req proto.FinishRunRequest) error {
	return withRetry(ctx, 8, func() error {
		return c.do(ctx, http.MethodPost, "/api/agent/runs/"+runID+"/finish", req, nil)
	})
}
