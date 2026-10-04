// Package proto holds the wire types shared by the dashboard server and the
// Linux agent, so both sides compile against a single definition of the API.
package proto

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/robfig/cron/v3"
)

// ---- WebSocket control channel (server <-> agent) ---------------------------
//
// The agent dials the server (outbound only, so it works behind NAT/firewalls)
// and keeps one WebSocket open. The server uses it as an RPC tunnel to ask the
// agent things it cannot know on its own, e.g. "list /host/home".

const (
	MsgBrowse        = "browse"         // server -> agent, request: BrowseRequest, reply: BrowseResult
	MsgRunNow        = "run_now"        // server -> agent, request: RunNowRequest
	MsgCancelRun     = "cancel_run"     // server -> agent, request: CancelRunRequest
	MsgConfigChanged = "config_changed" // server -> agent, notification (agent re-fetches config)
	MsgHello         = "hello"          // agent -> server, first message: Hello
	MsgPing          = "ping"           // agent -> server, keepalive
	MsgResult        = "result"         // reply to any request
)

// Envelope is the single frame type on the WebSocket. Requests carry an ID that
// the matching MsgResult echoes back.
type Envelope struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
}

type Hello struct {
	Hostname    string   `json:"hostname"`
	Version     string   `json:"version"`
	OS          string   `json:"os"`
	Arch        string   `json:"arch"`
	RcloneVer   string   `json:"rclone_version"`
	BrowseRoots []string `json:"browse_roots"`
	ConfigRev   int64    `json:"config_rev"`
	RunningRuns []string `json:"running_runs,omitempty"`
}

type BrowseRequest struct {
	Path string `json:"path"`
}

type RunNowRequest struct {
	JobID string `json:"job_id"`
}

type CancelRunRequest struct {
	RunID string `json:"run_id"`
}

// ---- File browser -----------------------------------------------------------

// Entry is one file or directory returned by the agent's file scanner.
type Entry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time"` // unix seconds
	Mode    string `json:"mode"`
	Symlink bool   `json:"symlink,omitempty"`
}

// BrowseResult is the JSON payload behind GET /api/agents/{id}/browse?path=...
type BrowseResult struct {
	Path      string  `json:"path"`
	Parent    string  `json:"parent"` // "" when Path is the virtual top level
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated,omitempty"`
}

// ---- Config sync (GET /api/agent/config) -------------------------------------

// AgentConfig is everything an agent needs to do its work. It is fetched over
// the authenticated channel and held in memory only: credentials are never
// written to the agent's disk.
type AgentConfig struct {
	Revision int64       `json:"revision"`
	AgentID  string      `json:"agent_id"`
	Name     string      `json:"name"`
	Jobs     []JobConfig `json:"jobs"`
}

type JobConfig struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Enabled    bool           `json:"enabled"`
	DestPrefix string         `json:"dest_prefix"`
	Paths      []PathConfig   `json:"paths"`
	Schedules  []ScheduleSpec `json:"schedules"`
	Wasabi     WasabiConfig   `json:"wasabi"`
}

// PathConfig is one selected source on the agent. Mode is "copy" (never
// deletes at the destination) or "sync" (makes destination mirror the source).
type PathConfig struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
}

type ScheduleSpec struct {
	ID       string `json:"id"`
	Cron     string `json:"cron"`
	Timezone string `json:"timezone"`
}

type WasabiConfig struct {
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	Endpoint  string `json:"endpoint"`
}

// ---- Run reporting (agent -> server over HTTPS) ------------------------------

const (
	StatusRunning   = "running"
	StatusSuccess   = "success"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

type StartRunRequest struct {
	JobID   string `json:"job_id"`
	Trigger string `json:"trigger"` // "schedule" | "manual"
}

type StartRunResponse struct {
	RunID string `json:"run_id"`
}

type LogLine struct {
	Seq    int64  `json:"seq"`
	TS     int64  `json:"ts"`     // unix milliseconds
	Stream string `json:"stream"` // "stdout" | "stderr" | "agent"
	Line   string `json:"line"`
}

type LogBatch struct {
	Lines []LogLine `json:"lines"`
}

type FinishRunRequest struct {
	Status   string `json:"status"`
	ExitCode int    `json:"exit_code"`
	Summary  string `json:"summary"`
}

// ---- Schedules --------------------------------------------------------------

// ScheduleParser is the single cron dialect both sides agree on: standard
// 5-field cron plus descriptors such as "@daily" or "@every 6h". A timezone is
// applied by prefixing "CRON_TZ=<zone> ".
var ScheduleParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// FullSpec builds the spec string handed to robfig/cron, including timezone.
func FullSpec(expr, tz string) string {
	expr = strings.TrimSpace(expr)
	tz = strings.TrimSpace(tz)
	if tz == "" || tz == "UTC" || strings.HasPrefix(expr, "@every") {
		return expr
	}
	return "CRON_TZ=" + tz + " " + expr
}

// ValidateSchedule reports whether expr/tz is a spec the agent will accept.
func ValidateSchedule(expr, tz string) error {
	if strings.ContainsAny(expr+tz, "\r\n\x00") {
		return fmt.Errorf("invalid characters in schedule")
	}
	if strings.HasPrefix(strings.TrimSpace(expr), "CRON_TZ=") || strings.HasPrefix(strings.TrimSpace(expr), "TZ=") {
		return fmt.Errorf("set the timezone in the timezone field, not in the expression")
	}
	if _, err := ScheduleParser.Parse(FullSpec(expr, tz)); err != nil {
		return fmt.Errorf("invalid schedule: %w", err)
	}
	return nil
}
