package server

import (
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/stylishseahorse/rclone-backup-manager/internal/proto"
)

// GET /api/overview?days=7&agent_id=&tz=Europe/London
//
// Everything the overview dashboard shows, computed in one pass so every tile,
// chart and table describes the same time window. "Backups" means real backup
// runs (manual or scheduled); dry runs and verifies are tests and are reported
// separately so they never skew success rates or data totals.

type overviewTotals struct {
	Runs        int     `json:"runs"`
	Success     int     `json:"success"`
	Failed      int     `json:"failed"`
	Cancelled   int     `json:"cancelled"`
	SuccessRate float64 `json:"success_rate"` // 0..1 of finished runs; -1 = no finished runs
	Bytes       int64   `json:"bytes"`
	Transferred int     `json:"files_transferred"`
	Deleted     int     `json:"files_deleted"`
	Versioned   int     `json:"files_versioned"`
	AvgDuration float64 `json:"avg_duration_sec"`
}

type overviewBucket struct {
	Start     int64 `json:"start"` // unix seconds, local midnight (or hour)
	Success   int   `json:"success"`
	Failed    int   `json:"failed"`
	Cancelled int   `json:"cancelled"`
	Bytes     int64 `json:"bytes"`
}

type stripRun struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	StartedAt int64  `json:"started_at"`
}

type jobHealth struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	AgentID       string     `json:"agent_id"`
	AgentName     string     `json:"agent_name"`
	AgentOnline   bool       `json:"agent_online"`
	Enabled       bool       `json:"enabled"`
	BackupType    string     `json:"backup_type"`
	Health        string     `json:"health"` // ok | failing | overdue | offline | running | never | paused
	Reason        string     `json:"reason"`
	LastRun       *runView   `json:"last_run"`
	LastSuccessAt *int64     `json:"last_success_at"`
	NextRun       *int64     `json:"next_run"`
	Schedule      string     `json:"schedule"` // first enabled cron, for display
	Runs          int        `json:"runs"`
	Success       int        `json:"success"`
	Failed        int        `json:"failed"`
	Bytes         int64      `json:"bytes"`
	AvgDuration   float64    `json:"avg_duration_sec"`
	Recent        []stripRun `json:"recent"` // newest last
}

type attentionItem struct {
	Severity string `json:"severity"` // critical | warning
	Kind     string `json:"kind"`
	Text     string `json:"text"`
	JobID    string `json:"job_id,omitempty"`
	AgentID  string `json:"agent_id,omitempty"`
	RunID    string `json:"run_id,omitempty"`
}

const backupTriggers = `('manual','schedule')`

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	days := 7
	if n, err := strconv.Atoi(q.Get("days")); err == nil && (n == 1 || n == 7 || n == 30 || n == 90) {
		days = n
	}
	loc := time.UTC
	if tz := q.Get("tz"); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	agentID := q.Get("agent_id")
	nowT := time.Now()
	// Buckets: hours for the last 24h, local days otherwise.
	bucket := int64(86400)
	start := time.Date(nowT.In(loc).Year(), nowT.In(loc).Month(), nowT.In(loc).Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(days - 1))
	if days == 1 {
		bucket = 3600
		start = nowT.Truncate(time.Hour).Add(-23 * time.Hour)
	}
	from := start.Unix()
	prevFrom := from - (nowT.Unix() - from)

	agentFilter, args := "", []any{}
	if agentID != "" {
		agentFilter = " AND r.agent_id = ?"
		args = append(args, agentID)
	}
	withArgs := func(first ...any) []any { return append(first, args...) }

	out := map[string]any{"range": map[string]any{"from": from, "to": nowT.Unix(), "days": days, "bucket_sec": bucket}}

	// ---- totals (this period and the previous one, for deltas)
	totals := func(lo, hi int64) (overviewTotals, error) {
		var t overviewTotals
		rows, err := s.db.Query(`SELECT r.status, COUNT(*), COALESCE(SUM(r.bytes),0), COALESCE(SUM(r.files_transferred),0),
			COALESCE(SUM(r.files_deleted),0), COALESCE(SUM(r.files_versioned),0),
			COALESCE(AVG(CASE WHEN r.finished_at IS NOT NULL THEN r.finished_at - r.started_at END),0)
			FROM runs r WHERE r.started_at >= ? AND r.started_at < ? AND r.trigger IN `+backupTriggers+agentFilter+` GROUP BY r.status`, withArgs(lo, hi)...)
		if err != nil {
			return t, err
		}
		defer rows.Close()
		var durSum float64
		var durN int
		for rows.Next() {
			var st string
			var n, tr, del, ver int
			var b int64
			var avg float64
			if err := rows.Scan(&st, &n, &b, &tr, &del, &ver, &avg); err != nil {
				return t, err
			}
			t.Runs += n
			t.Bytes += b
			t.Transferred += tr
			t.Deleted += del
			t.Versioned += ver
			switch st {
			case proto.StatusSuccess:
				t.Success = n
			case proto.StatusFailed:
				t.Failed = n
			case proto.StatusCancelled:
				t.Cancelled = n
			}
			if st != proto.StatusRunning {
				durSum += avg * float64(n)
				durN += n
			}
		}
		t.SuccessRate = -1
		if fin := t.Success + t.Failed + t.Cancelled; fin > 0 {
			t.SuccessRate = float64(t.Success) / float64(fin)
		}
		if durN > 0 {
			t.AvgDuration = durSum / float64(durN)
		}
		return t, rows.Err()
	}
	cur, err := totals(from, nowT.Unix()+1)
	if err != nil {
		dbErr(w, err)
		return
	}
	prev, err := totals(prevFrom, from)
	if err != nil {
		dbErr(w, err)
		return
	}
	out["totals"], out["previous"] = cur, prev

	// ---- time series
	_, offset := nowT.In(loc).Zone()
	if bucket == 3600 {
		offset = 0
	}
	n := int((nowT.Unix()-from)/bucket) + 1
	series := make([]overviewBucket, n)
	for i := range series {
		if bucket == 86400 {
			series[i].Start = start.AddDate(0, 0, i).Unix()
		} else {
			series[i].Start = from + int64(i)*bucket
		}
	}
	rows, err := s.db.Query(`SELECT (r.started_at + ?) / ? AS b, r.status, COUNT(*), COALESCE(SUM(r.bytes),0)
		FROM runs r WHERE r.started_at >= ? AND r.trigger IN `+backupTriggers+agentFilter+` GROUP BY b, r.status`,
		withArgs(offset, bucket, from)...)
	if err != nil {
		dbErr(w, err)
		return
	}
	firstB := (from + int64(offset)) / bucket
	for rows.Next() {
		var b int64
		var st string
		var cnt int
		var by int64
		if rows.Scan(&b, &st, &cnt, &by) != nil {
			continue
		}
		i := int(b - firstB)
		if i < 0 || i >= n {
			continue
		}
		series[i].Bytes += by
		switch st {
		case proto.StatusSuccess:
			series[i].Success += cnt
		case proto.StatusFailed:
			series[i].Failed += cnt
		case proto.StatusCancelled:
			series[i].Cancelled += cnt
		}
	}
	rows.Close()
	out["series"] = series

	// ---- agents
	type agentRow struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Online     bool   `json:"online"`
		LastSeenAt int64  `json:"last_seen_at"`
	}
	var agents []agentRow
	arows, err := s.db.Query(`SELECT id, name, last_seen_at FROM agents ORDER BY name`)
	if err != nil {
		dbErr(w, err)
		return
	}
	for arows.Next() {
		var a agentRow
		if arows.Scan(&a.ID, &a.Name, &a.LastSeenAt) == nil {
			a.Online = s.hub.Online(a.ID)
			agents = append(agents, a)
		}
	}
	arows.Close()
	online := 0
	for _, a := range agents {
		if a.Online {
			online++
		}
	}
	out["agents"] = map[string]any{"total": len(agents), "online": online, "list": agents}

	var running int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM runs r WHERE r.status = 'running'`+agentFilter, args...).Scan(&running)
	out["running"] = running

	// ---- per-job health
	jq, jargs := jobSelect, []any{}
	if agentID != "" {
		jq += ` WHERE j.agent_id = ?`
		jargs = append(jargs, agentID)
	}
	jrows, err := s.db.Query(jq+` ORDER BY a.name, j.name`, jargs...)
	if err != nil {
		dbErr(w, err)
		return
	}
	jobs, err := s.scanJobs(jrows)
	if err != nil {
		dbErr(w, err)
		return
	}
	created := map[string]int64{}
	if crow, err := s.db.Query(`SELECT id, created_at FROM backup_jobs`); err == nil {
		for crow.Next() {
			var id string
			var c int64
			if crow.Scan(&id, &c) == nil {
				created[id] = c
			}
		}
		crow.Close()
	}
	onlineByID := map[string]bool{}
	for _, a := range agents {
		onlineByID[a.ID] = a.Online
	}

	// Last 20 backup runs per job in one query (window function).
	recent := map[string][]stripRun{}
	if rr, err := s.db.Query(`SELECT job_id, id, status, started_at FROM (
			SELECT r.job_id, r.id, r.status, r.started_at, ROW_NUMBER() OVER (PARTITION BY r.job_id ORDER BY r.started_at DESC) AS rn
			FROM runs r WHERE r.trigger IN `+backupTriggers+agentFilter+`) WHERE rn <= 20 ORDER BY started_at`, args...); err == nil {
		for rr.Next() {
			var jid string
			var sr stripRun
			if rr.Scan(&jid, &sr.ID, &sr.Status, &sr.StartedAt) == nil {
				recent[jid] = append(recent[jid], sr)
			}
		}
		rr.Close()
	}

	var health []jobHealth
	var attention []attentionItem
	for _, j := range jobs {
		h := jobHealth{ID: j.ID, Name: j.Name, AgentID: j.AgentID, AgentName: j.AgentName, AgentOnline: onlineByID[j.AgentID],
			Enabled: j.Enabled, BackupType: j.BackupType, NextRun: j.NextRun, Recent: recent[j.ID]}
		if h.Recent == nil {
			h.Recent = []stripRun{}
		}
		for _, sc := range j.Schedules {
			if sc.Enabled {
				h.Schedule = sc.CronExpr
				break
			}
		}
		if v, err := scanRun(s.db.QueryRow(runSelect+` WHERE r.job_id = ? AND r.trigger IN `+backupTriggers+` ORDER BY r.started_at DESC LIMIT 1`, j.ID)); err == nil {
			h.LastRun = v
		}
		var ls sql.NullInt64
		_ = s.db.QueryRow(`SELECT MAX(COALESCE(finished_at, started_at)) FROM runs WHERE job_id = ? AND status = 'success' AND trigger IN `+backupTriggers, j.ID).Scan(&ls)
		if ls.Valid {
			h.LastSuccessAt = &ls.Int64
		}
		_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(status = 'success'),0), COALESCE(SUM(status = 'failed'),0), COALESCE(SUM(bytes),0),
			COALESCE(AVG(CASE WHEN finished_at IS NOT NULL THEN finished_at - started_at END),0)
			FROM runs WHERE job_id = ? AND started_at >= ? AND trigger IN `+backupTriggers, j.ID, from).
			Scan(&h.Runs, &h.Success, &h.Failed, &h.Bytes, &h.AvgDuration)

		h.Health, h.Reason = classify(j, h, created[j.ID], nowT)
		if h.Health == "ok" && !h.AgentOnline && h.NextRun != nil {
			h.Health, h.Reason = "offline", "agent is offline, so scheduled runs will be missed"
		}
		health = append(health, h)

		switch h.Health {
		case "failing":
			attention = append(attention, attentionItem{Severity: "critical", Kind: "failing", JobID: j.ID, RunID: h.LastRun.ID,
				Text: fmt.Sprintf("%s on %s: %s", j.Name, j.AgentName, h.Reason)})
		case "overdue":
			attention = append(attention, attentionItem{Severity: "warning", Kind: "overdue", JobID: j.ID, Text: fmt.Sprintf("%s on %s: %s", j.Name, j.AgentName, h.Reason)})
		}
		// A failed verify that hasn't been followed by a passing one means the backup doesn't match the machine.
		var vid, vst string
		if s.db.QueryRow(`SELECT id, status FROM runs WHERE job_id = ? AND trigger = 'verify' ORDER BY started_at DESC LIMIT 1`, j.ID).Scan(&vid, &vst) == nil && vst == proto.StatusFailed {
			attention = append(attention, attentionItem{Severity: "warning", Kind: "verify", JobID: j.ID, RunID: vid,
				Text: fmt.Sprintf("%s on %s: last verify found differences between the machine and Wasabi", j.Name, j.AgentName)})
		}
	}
	for _, a := range agents {
		if agentID != "" && a.ID != agentID {
			continue
		}
		if !a.Online {
			sev, scheduled := "warning", 0
			for _, h := range health {
				if h.AgentID == a.ID && h.Enabled && h.NextRun != nil {
					scheduled++
				}
			}
			text := fmt.Sprintf("Agent %s is offline", a.Name)
			if a.LastSeenAt > 0 {
				text += " (last seen " + time.Unix(a.LastSeenAt, 0).In(loc).Format("Jan 2 15:04") + ")"
			}
			if scheduled > 0 {
				sev = "critical"
				text += fmt.Sprintf("; %d scheduled job(s) cannot run", scheduled)
			}
			attention = append(attention, attentionItem{Severity: sev, Kind: "offline", AgentID: a.ID, Text: text})
		}
	}
	sort.SliceStable(attention, func(i, j int) bool { return attention[i].Severity == "critical" && attention[j].Severity != "critical" })
	if health == nil {
		health = []jobHealth{}
	}
	if attention == nil {
		attention = []attentionItem{}
	}
	out["jobs"], out["attention"] = health, attention

	// ---- recent failures (any trigger, so failed verifies show up too)
	fr, err := s.db.Query(runSelect+` WHERE r.status = 'failed' AND r.started_at >= ?`+agentFilter+` ORDER BY r.started_at DESC LIMIT 8`, withArgs(from)...)
	if err != nil {
		dbErr(w, err)
		return
	}
	failures := []*runView{}
	for fr.Next() {
		if v, err := scanRun(fr); err == nil {
			failures = append(failures, v)
		}
	}
	fr.Close()
	out["recent_failures"] = failures

	writeJSON(w, http.StatusOK, out)
}

// classify decides a job's health and a one-line reason.
func classify(j jobView, h jobHealth, createdAt int64, now time.Time) (string, string) {
	if !j.Enabled {
		return "paused", "job is paused"
	}
	if h.LastRun != nil && h.LastRun.Status == proto.StatusRunning {
		return "running", "backup in progress"
	}
	if h.LastRun != nil && h.LastRun.Status == proto.StatusFailed {
		reason := "last backup failed"
		if h.LastRun.Summary != "" {
			reason += " (" + h.LastRun.Summary + ")"
		}
		return "failing", reason
	}
	if iv := expectedInterval(j.Schedules, now); iv > 0 {
		grace := 2*iv + time.Hour
		since := createdAt
		if h.LastSuccessAt != nil {
			since = *h.LastSuccessAt
		}
		if since > 0 && now.Sub(time.Unix(since, 0)) > grace {
			if h.LastSuccessAt == nil {
				return "overdue", "scheduled but has never completed a backup"
			}
			return "overdue", "no successful backup for " + humanDuration(now.Sub(time.Unix(*h.LastSuccessAt, 0)))
		}
	}
	if h.LastRun == nil {
		return "never", "has not run yet"
	}
	if h.LastRun.Status == proto.StatusCancelled {
		return "ok", "last backup was cancelled"
	}
	return "ok", ""
}

// expectedInterval is the gap between consecutive runs of the most frequent
// enabled schedule; 0 when the job has no schedule.
func expectedInterval(schedules []scheduleView, now time.Time) time.Duration {
	var best time.Duration
	for _, sc := range schedules {
		if !sc.Enabled {
			continue
		}
		sched, err := proto.ScheduleParser.Parse(proto.FullSpec(sc.CronExpr, sc.Timezone))
		if err != nil {
			continue
		}
		// Use the largest gap over the next week so "weekdays only" isn't flagged every Monday.
		var maxGap time.Duration
		t := sched.Next(now)
		for i := 0; i < 50 && !t.IsZero() && t.Before(now.Add(8*24*time.Hour)); i++ {
			nt := sched.Next(t)
			if nt.IsZero() {
				break
			}
			if g := nt.Sub(t); g > maxGap {
				maxGap = g
			}
			t = nt
		}
		if maxGap == 0 && !t.IsZero() { // fires less than weekly, e.g. monthly
			maxGap = sched.Next(t).Sub(t)
		}
		if maxGap > 0 && (best == 0 || maxGap < best) {
			best = maxGap
		}
	}
	return best
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}
