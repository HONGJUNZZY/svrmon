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
	"testing/fstest"
	"time"

	"svrmon/internal/hub"
	"svrmon/internal/logx"
)

//go:embed all:assets
var assets embed.FS

// placeholderPage is served when the binary was built without the frontend.
const placeholderPage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>svrmon hub</title></head>
<body style="background:#0b0f14;color:#d5dee8;font-family:system-ui,sans-serif;padding:2.5rem">
<h1>svrmon hub</h1>
<p>此二进制内没有前端页面：请先运行 <code>deploy/build.ps1</code> 构建前端后重新编译 hub。</p>
<p style="color:#7b8a9c">API 与 agent 接入不受影响：<code>/api/servers</code>、<code>/ws</code>、<code>/agent</code> 均可正常使用。</p>
</body></html>`

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

	// 内嵌前端：仅在 index.html 与它引用的 assets 同时存在时才可用；
	// 源码运行（未构建前端）时退化为内置提示页，避免出现 404 或白屏。
	var dist fs.FS
	if sub, err := fs.Sub(assets, "assets/dist"); err != nil {
		log.Printf("hub: frontend assets missing: %v", err)
	} else if _, err := fs.Stat(sub, "index.html"); err != nil {
		log.Printf("hub: frontend not built into this binary — serving placeholder page (run deploy/build.ps1)")
		dist = fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(placeholderPage)}}
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
