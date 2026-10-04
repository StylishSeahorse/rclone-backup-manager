package agent

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/stylishseahorse/rclone-backup-manager/internal/proto"
)

// Scheduler is the agent's built-in cron: it needs no host crontab and lives
// and dies with the container. Schedules come from the dashboard; whenever the
// config revision changes, the whole cron table is rebuilt.
type Scheduler struct {
	mu   sync.Mutex
	cron *cron.Cron
	fire func(jobID string) // called on every tick, in its own goroutine
}

func NewScheduler(fire func(jobID string)) *Scheduler { return &Scheduler{fire: fire} }

// Apply replaces the active schedule table with the one in cfg.
func (s *Scheduler) Apply(cfg *proto.AgentConfig) {
	next := cron.New(cron.WithParser(proto.ScheduleParser), cron.WithLocation(time.UTC))
	n := 0
	for _, job := range cfg.Jobs {
		if !job.Enabled {
			continue
		}
		for _, sc := range job.Schedules {
			jobID, spec := job.ID, proto.FullSpec(sc.Cron, sc.Timezone)
			if _, err := next.AddFunc(spec, func() { go s.fire(jobID) }); err != nil {
				log.Printf("job %q: ignoring bad schedule %q: %v", job.Name, spec, err)
				continue
			}
			n++
		}
	}

	s.mu.Lock()
	old := s.cron
	s.cron = next
	s.mu.Unlock()
	next.Start()
	if old != nil {
		old.Stop() // fire() runs in its own goroutine, so in-flight backups are unaffected
	}
	log.Printf("scheduler: %d schedule(s) active across %d job(s)", n, len(cfg.Jobs))
}

// Stop halts scheduling (in-flight runs are cancelled separately via context).
func (s *Scheduler) Stop() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron == nil {
		c, cancel := context.WithCancel(context.Background())
		cancel()
		return c
	}
	return s.cron.Stop()
}
