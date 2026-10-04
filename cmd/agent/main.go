// Command agent is the Linux backup daemon that runs next to the data to back up.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	_ "time/tzdata" // schedule timezones work in a minimal image

	"github.com/stylishseahorse/rclone-backup-manager/internal/agent"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	agent.Version = version

	cfg, err := agent.LoadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	a, err := agent.New(cfg)
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := a.Run(ctx); err != nil {
		log.Fatalf("agent: %v", err)
	}
}
