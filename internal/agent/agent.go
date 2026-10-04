// Package agent implements the Linux backup daemon: it keeps an outbound
// WebSocket to the dashboard, serves file-browser requests, runs rclone on a
// schedule, and streams the output back.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/stylishseahorse/rclone-backup-manager/internal/proto"
)

var Version = "dev"

type Agent struct {
	cfg     *Config
	api     *apiClient
	scanner *Scanner
	sched   *Scheduler
	rclone  string // version string reported to the dashboard
	sem     chan struct{}

	refetch chan struct{} // poked when the dashboard says config changed

	mu      sync.RWMutex
	conf    *proto.AgentConfig // in memory only, never persisted
	running map[string]*activeRun
	baseCtx context.Context
	wg      sync.WaitGroup // in-flight runs
}

func New(cfg *Config) (*Agent, error) {
	api, err := newAPIClient(cfg)
	if err != nil {
		return nil, err
	}
	sc, err := NewScanner(cfg.BrowseRoots)
	if err != nil {
		return nil, err
	}
	ver, err := rcloneVersion(cfg.RclonePath)
	if err != nil {
		return nil, err
	}
	a := &Agent{
		cfg: cfg, api: api, scanner: sc, rclone: ver,
		sem:     make(chan struct{}, cfg.MaxConcurrent),
		refetch: make(chan struct{}, 1),
		running: map[string]*activeRun{},
	}
	a.sched = NewScheduler(func(jobID string) { a.startRun(jobID, "schedule") })
	return a, nil
}

func (a *Agent) name() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.conf != nil {
		return a.conf.Name
	}
	return a.cfg.Hostname
}

func (a *Agent) jobByID(id string) *proto.JobConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.conf == nil {
		return nil
	}
	for i := range a.conf.Jobs {
		if a.conf.Jobs[i].ID == id {
			j := a.conf.Jobs[i] // copy: the config may be swapped mid-run
			return &j
		}
	}
	return nil
}

func (a *Agent) revision() int64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.conf == nil {
		return 0
	}
	return a.conf.Revision
}

func (a *Agent) startRun(jobID, trigger string) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.runJob(a.baseCtx, jobID, trigger)
	}()
}

// Run blocks until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	a.baseCtx = ctx
	log.Printf("agent %s starting: %s, browse roots %v", Version, a.rclone, a.scanner.Roots())
	if strings.HasPrefix(a.cfg.ServerURL, "http://") {
		log.Printf("WARNING: AGENT_SERVER_URL is plain http; credentials and your API key cross the network unencrypted. Use https:// outside a trusted network.")
	}

	go a.configLoop(ctx)

	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := a.session(ctx)
		if ctx.Err() != nil {
			break
		}
		var ce websocket.CloseError
		if errors.As(err, &ce) && ce.Code == websocket.StatusPolicyViolation {
			log.Printf("server closed the connection: %s", ce.Reason)
		} else {
			log.Printf("disconnected: %v", err)
		}
		if time.Since(started) > 30*time.Second {
			backoff = time.Second // it was a healthy session; retry fast
		}
		sleep := backoff/2 + time.Duration(rand.Int63n(int64(backoff))) // jitter avoids thundering herd
		select {
		case <-ctx.Done():
		case <-time.After(sleep):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}

	// Shutdown: cancel in-flight rclone processes and let them report back.
	a.sched.Stop()
	done := make(chan struct{})
	go func() { a.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(90 * time.Second):
		log.Printf("timed out waiting for runs to stop")
	}
	return nil
}

// configLoop keeps the in-memory config fresh: on dashboard notification, and
// every 5 minutes as a safety net against a missed message.
func (a *Agent) configLoop(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	a.fetchConfig(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.refetch:
		case <-t.C:
		}
		a.fetchConfig(ctx)
	}
}

func (a *Agent) fetchConfig(ctx context.Context) {
	cfg, err := a.api.GetConfig(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("config fetch failed: %v", err)
		}
		return
	}
	a.mu.Lock()
	changed := a.conf == nil || a.conf.Revision != cfg.Revision
	a.conf = cfg
	a.mu.Unlock()
	if changed {
		log.Printf("config revision %d loaded (%d job(s))", cfg.Revision, len(cfg.Jobs))
		a.sched.Apply(cfg)
	}
}

func (a *Agent) wsURL() string {
	u := a.cfg.ServerURL + "/api/agent/ws"
	if strings.HasPrefix(u, "https://") {
		return "wss://" + strings.TrimPrefix(u, "https://")
	}
	return "ws://" + strings.TrimPrefix(u, "http://")
}

// session runs one WebSocket connection until it breaks.
func (a *Agent) session(ctx context.Context) error {
	dctx, dcancel := context.WithTimeout(ctx, 20*time.Second)
	ws, _, err := websocket.Dial(dctx, a.wsURL(), &websocket.DialOptions{
		HTTPHeader: a.api.header(),
		HTTPClient: &http.Client{Transport: a.api.http.Transport}, // reuse custom CA, no overall timeout
	})
	dcancel()
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	ws.SetReadLimit(1 << 20)
	defer ws.CloseNow()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wmu sync.Mutex
	send := func(env proto.Envelope) error {
		b, _ := json.Marshal(env)
		wctx, c := context.WithTimeout(ctx, 15*time.Second)
		defer c()
		wmu.Lock()
		defer wmu.Unlock()
		return ws.Write(wctx, websocket.MessageText, b)
	}

	hello, _ := json.Marshal(proto.Hello{
		Hostname: a.cfg.Hostname, Version: Version, OS: runtime.GOOS, Arch: runtime.GOARCH,
		RcloneVer: a.rclone, BrowseRoots: a.scanner.Roots(), ConfigRev: a.revision(),
	})
	if err := send(proto.Envelope{Type: proto.MsgHello, Payload: hello}); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	log.Printf("connected to %s", a.cfg.ServerURL)

	go func() { // application-level keepalive; also refreshes "last seen" on the dashboard
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if send(proto.Envelope{Type: proto.MsgPing}) != nil {
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return err
		}
		var env proto.Envelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		switch env.Type {
		case proto.MsgBrowse:
			go func() { // don't block the read loop on a slow disk
				var req proto.BrowseRequest
				_ = json.Unmarshal(env.Payload, &req)
				res, err := a.scanner.Browse(req.Path)
				reply := proto.Envelope{ID: env.ID, Type: proto.MsgResult}
				if err != nil {
					reply.Error = userFacing(err)
				} else {
					reply.Payload, _ = json.Marshal(res)
				}
				_ = send(reply)
			}()
		case proto.MsgRunNow:
			var req proto.RunNowRequest
			if json.Unmarshal(env.Payload, &req) == nil {
				a.startRun(req.JobID, "manual")
			}
		case proto.MsgCancelRun:
			var req proto.CancelRunRequest
			if json.Unmarshal(env.Payload, &req) == nil {
				a.cancelRun(req.RunID)
			}
		case proto.MsgConfigChanged:
			select {
			case a.refetch <- struct{}{}:
			default:
			}
		}
	}
}

func (a *Agent) cancelRun(runID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, ar := range a.running {
		if ar.runID == runID {
			ar.user = true
			ar.cancel()
		}
	}
}

// userFacing trims OS errors to something safe and readable for the dashboard
// (e.g. "open /host/x: permission denied" -> "permission denied").
func userFacing(err error) string {
	var pe interface{ Unwrap() error }
	if errors.As(err, &pe) {
		if inner := pe.Unwrap(); inner != nil {
			return inner.Error()
		}
	}
	return err.Error()
}
