// Command agent runs the svrmon monitoring agent.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"svrmon/internal/agent"
	"svrmon/internal/logx"
)

func main() {
	cfgPath := flag.String("config", "agent.config.json", "path to agent config file")
	hubURL := flag.String("hub", "", "hub ws url, e.g. ws://192.168.1.10:8080/agent (overrides config)")
	token := flag.String("token", "", "agent token (overrides config)")
	name := flag.String("name", "", "server display name (overrides config, default: hostname)")
	interval := flag.Int("interval", 0, "snapshot interval in milliseconds (overrides config)")
	logFile := flag.String("log", "", "log file path with daily rotation (overrides config)")
	flag.Parse()

	cfg := agent.DefaultConfig()
	if b, err := os.ReadFile(*cfgPath); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			log.Fatalf("agent: parse %s: %v", *cfgPath, err)
		}
		log.Printf("agent: loaded config from %s", *cfgPath)
	}
	if *hubURL != "" {
		cfg.HubURL = *hubURL
	}
	if *token != "" {
		cfg.Token = *token
	}
	if *name != "" {
		cfg.Name = *name
	}
	if *interval > 0 {
		cfg.IntervalMS = *interval
	}
	if *logFile != "" {
		cfg.LogFile = *logFile
	}
	if cfg.Token == "" || cfg.Token == "change-me" {
		log.Printf("agent: WARNING: token not configured; the hub will reject this agent")
	}
	if cfg.LogFile != "" {
		w, err := logx.Setup(cfg.LogFile, cfg.LogRetentionDays)
		if err != nil {
			log.Fatalf("agent: log file: %v", err)
		}
		defer w.Close()
		log.Printf("agent: logging to %s (daily rotation, retention %d days)", cfg.LogFile, cfg.LogRetentionDays)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("agent v%s starting: hub=%s interval=%dms gpu=%v ipmi=%v",
		agent.Version, cfg.HubURL, cfg.IntervalMS, cfg.GPU.Enable, cfg.IPMI.Enable)
	agent.New(cfg).Run(ctx)
}
