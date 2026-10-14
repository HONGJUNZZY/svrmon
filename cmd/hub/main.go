// Command hub runs the svrmon central server.
package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"svrmon/internal/hub"
	"svrmon/internal/logx"
)

//go:embed all:assets
var assets embed.FS

// Config is the hub configuration file format.
type Config struct {
	Listen        string         `json:"listen"`
	DBPath        string         `json:"dbPath"`
	RetentionDays int            `json:"retentionDays"`
	StoreEverySec int            `json:"storeEverySec"`
	AgentTokens   []string       `json:"agentTokens"`
	UIToken       string         `json:"uiToken"` // optional; protects the web UI when set
	LogFile       string         `json:"logFile"`           // empty = stderr/journald
	LogRetentionDays int         `json:"logRetentionDays"`  // daily rotation, auto-delete older
	Alerts        hub.AlertConfig `json:"alerts"`
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func main() {
	cfgPath := flag.String("config", "hub.config.json", "path to hub config file")
	listen := flag.String("listen", "", "listen address (overrides config)")
	dbPath := flag.String("db", "", "sqlite db path (overrides config)")
	logFile := flag.String("log", "", "log file path with daily rotation (overrides config)")
	flag.Parse()

	cfg := Config{
		Listen: ":8080", DBPath: "svrmon.db", RetentionDays: 7, StoreEverySec: 10, LogRetentionDays: 7,
		Alerts: hub.AlertConfig{
			OfflineGraceSec: 60,
			CooldownMin:     30,
			SMTP:            hub.SMTPConfig{Host: "smtp.qq.com", Port: 465, Security: "auto"},
		},
	}

	if _, err := os.Stat(*cfgPath); os.IsNotExist(err) {
		// first run: generate a config with a random agent token
		cfg.AgentTokens = []string{randomToken()}
		b, _ := json.MarshalIndent(cfg, "", "  ")
		if err := os.WriteFile(*cfgPath, b, 0o600); err != nil {
			log.Fatalf("hub: cannot write %s: %v", *cfgPath, err)
		}
		log.Printf("hub: created %s (agent token: %s)", *cfgPath, cfg.AgentTokens[0])
	} else if b, err := os.ReadFile(*cfgPath); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			log.Fatalf("hub: parse %s: %v", *cfgPath, err)
		}
		log.Printf("hub: loaded config from %s", *cfgPath)
	}

	if *listen != "" {
		cfg.Listen = *listen
	}
	if *dbPath != "" {
		cfg.DBPath = *dbPath
	}
	if *logFile != "" {
		cfg.LogFile = *logFile
	}
	if len(cfg.AgentTokens) == 0 {
		log.Fatalf("hub: agentTokens is empty — refusing to accept agents openly; edit %s", *cfgPath)
	}
	if cfg.LogFile != "" {
		w, err := logx.Setup(cfg.LogFile, cfg.LogRetentionDays)
		if err != nil {
			log.Fatalf("hub: log file: %v", err)
		}
		defer w.Close()
		log.Printf("hub: logging to %s (daily rotation, retention %d days)", cfg.LogFile, cfg.LogRetentionDays)
	}

	store, err := hub.OpenStore(cfg.DBPath, cfg.StoreEverySec, cfg.RetentionDays)
	if err != nil {
		log.Fatalf("hub: open store: %v", err)
	}
	defer store.Close()

	h := hub.NewHub(store, cfg.AgentTokens, cfg.UIToken, cfg.Alerts)
	if cfg.Alerts.Enabled && cfg.Alerts.SMTP.Host == "" && len(cfg.Alerts.SMTP.To) == 0 && cfg.Alerts.Webhook == "" {
		log.Printf("hub: alerts enabled but no SMTP recipient or webhook configured — nothing will be sent")
	}

	var dist fs.FS
	if sub, err := fs.Sub(assets, "assets/dist"); err != nil {
		log.Printf("hub: frontend assets missing: %v", err)
	} else {
		dist = sub
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           h.Routes(dist),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		log.Printf("hub: shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	log.Printf("hub v%s listening on http://%s (db=%s, agents=%d, ui auth=%v)",
		hub.HubVersion, cfg.Listen, cfg.DBPath, len(cfg.AgentTokens), cfg.UIToken != "")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("hub: %v", err)
	}
}
