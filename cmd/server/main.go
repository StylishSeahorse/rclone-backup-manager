// Command server is the central dashboard: web UI, admin API and agent gateway.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "time/tzdata" // schedule timezones work even in a scratch/distroless image

	"github.com/stylishseahorse/rclone-backup-manager/internal/server"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck": // used by Docker HEALTHCHECK; the final image has no curl
			os.Exit(healthcheck())
		case "version":
			fmt.Println(version)
			return
		}
	}

	cfg, err := server.LoadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := srv.Run(ctx); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
}

func healthcheck() int {
	addr := os.Getenv("LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	if host == "" || strings.HasPrefix(host, "0.0.0.0") {
		host = "127.0.0.1"
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		return 1
	}
	return 0
}
