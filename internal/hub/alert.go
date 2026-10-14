package hub

import (
	"fmt"
	"log"
	"sync"
	"time"

	"svrmon/internal/model"
)

// AlertConfig configures the abnormal-shutdown alerting engine.
type AlertConfig struct {
	Enabled               bool       `json:"enabled"`
	OfflineGraceSec       int        `json:"offlineGraceSec"` // observation window before alerting
	CooldownMin           int        `json:"cooldownMin"`     // per-server/event repeat suppression
	NotifyOnCleanShutdown bool       `json:"notifyOnCleanShutdown"`
	SMTP                  SMTPConfig `json:"smtp"`
	Webhook               string     `json:"webhook"`
	WebhookStyle          string     `json:"webhookStyle"` // json | text (DingTalk/WeCom)
}

const (
	levelAlert = "alert"
	levelInfo  = "info"
)

type srvAlertState struct {
	bootID         string
	seen           bool
	graceTimer     *time.Timer
	alertedOffline bool
	gracefulLeft   bool
	goneAt         time.Time
}

// AlertEngine watches agent presence transitions and boot evidence to detect
// abnormal shutdowns, notifying by mail and/or webhook.
type AlertEngine struct {
	cfg    AlertConfig
	online func(name string) bool

	mu    sync.Mutex
	state map[string]*srvAlertState
	cool  map[string]time.Time
	mailQ chan Mail
	hookQ chan map[string]any
}

// NewAlertEngine starts the engine; online reports live agent presence.
func NewAlertEngine(cfg AlertConfig, online func(name string) bool) *AlertEngine {
	if cfg.OfflineGraceSec <= 0 {
		cfg.OfflineGraceSec = 60
	}
	if cfg.CooldownMin <= 0 {
		cfg.CooldownMin = 30
	}
	e := &AlertEngine{
		cfg:    cfg,
		online: online,
		state:  map[string]*srvAlertState{},
		cool:   map[string]time.Time{},
		mailQ:  make(chan Mail, 64),
		hookQ:  make(chan map[string]any, 64),
	}
	go e.mailLoop()
	go e.hookLoop()
	return e
}

func fmtTime(ts int64) string {
	if ts <= 0 {
		return "未知"
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
}

func (e *AlertEngine) notify(level, key, server, subject, body string) {
	if !e.cfg.Enabled {
		return
	}
	if level == levelAlert {
		e.mu.Lock()
		now := time.Now()
		if t, ok := e.cool[key]; ok && now.Sub(t) < time.Duration(e.cfg.CooldownMin)*time.Minute {
			e.mu.Unlock()
			return
		}
		e.cool[key] = now
		e.mu.Unlock()
	}
	log.Printf("alert: [%s] %s %s", level, server, subject)
	if e.cfg.SMTP.Host != "" && len(e.cfg.SMTP.To) > 0 {
		select {
		case e.mailQ <- Mail{Subject: subject, Body: body}:
		default:
			log.Printf("alert: mail queue full, dropping")
		}
	}
	if e.cfg.Webhook != "" {
		payload := map[string]any{
			"ts": time.Now().Unix(), "level": level, "server": server,
			"event": subject, "message": body,
		}
		select {
		case e.hookQ <- payload:
		default:
			log.Printf("alert: webhook queue full, dropping")
		}
	}
}

func (e *AlertEngine) mailLoop() {
	for m := range e.mailQ {
		var err error
		for attempt := 1; attempt <= 3; attempt++ {
			if err = sendMail(e.cfg.SMTP, m); err == nil {
				break
			}
			log.Printf("alert: mail attempt %d failed: %v", attempt, err)
			time.Sleep(time.Duration(attempt*attempt) * 5 * time.Second)
		}
		if err != nil {
			log.Printf("alert: giving up on mail %q: %v", m.Subject, err)
		}
	}
}

func (e *AlertEngine) hookLoop() {
	for p := range e.hookQ {
		if err := postWebhook(e.cfg.Webhook, e.cfg.WebhookStyle, p); err != nil {
			log.Printf("alert: webhook: %v", err)
		}
	}
}

// OnHello records boot evidence when an agent (re)connects.
func (e *AlertEngine) OnHello(name string, info *model.ServerInfo) {
	e.mu.Lock()
	st := e.state[name]
	first := st == nil
	if first {
		st = &srvAlertState{}
		e.state[name] = st
	}
	if st.graceTimer != nil {
		st.graceTimer.Stop()
		st.graceTimer = nil
	}
	bootChanged := st.seen && info.BootID != st.bootID
	alerted := st.alertedOffline
	gone := st.goneAt
	wasGraceful := st.gracefulLeft
	st.bootID = info.BootID
	st.seen = true
	st.alertedOffline = false
	st.gracefulLeft = false
	st.goneAt = time.Time{}
	e.mu.Unlock()

	now := time.Now().Unix()
	switch {
	case first && info.LastShutdownUnclean && info.BootAt > now-600:
		// Hub (re)started shortly after the incident.
		e.notify(levelAlert, name+"|unclean", name,
			fmt.Sprintf("[svrmon告警] %s 异常关机（启动记录显示上次未正常关闭）", name),
			fmt.Sprintf("服务器 %s 已上线，但系统启动记录显示上一次关机没有正常关闭记录\n（异常断电 / 死机 / 强制重启）。\n\n本次启动时间: %s\n\n— svrmon 自动告警", name, fmtTime(info.BootAt)))

	case bootChanged && info.LastShutdownUnclean:
		e.notify(levelAlert, name+"|unclean", name,
			fmt.Sprintf("[svrmon告警] %s 确认异常关机", name),
			fmt.Sprintf("服务器 %s 已重新上线，系统启动记录（wtmp）显示上一次关机没有正常\n关闭记录 → 确认为异常关机（断电 / 死机 / 强制重启）。\n\n失联时间: %s\n恢复时间: %s\n本次启动: %s\n\n— svrmon 自动告警",
				name, fmtTime(gone.Unix()), fmtTime(now), fmtTime(info.BootAt)))

	case alerted && bootChanged:
		e.notify(levelInfo, "", name,
			fmt.Sprintf("[svrmon] %s 已恢复在线（期间发生重启，关机记录正常）", name),
			fmt.Sprintf("服务器 %s 已重新上线。\n失联时间: %s\n恢复时间: %s\n\n— svrmon 自动通知", name, fmtTime(gone.Unix()), fmtTime(now)))

	case alerted:
		e.notify(levelInfo, "", name,
			fmt.Sprintf("[svrmon] %s 已恢复在线（未见重启，应为网络中断）", name),
			fmt.Sprintf("服务器 %s 恢复在线，启动 ID 未变化，此前的失联应为网络中断。\n失联时间: %s\n恢复时间: %s\n\n— svrmon 自动通知", name, fmtTime(gone.Unix()), fmtTime(now)))

	case wasGraceful && bootChanged && e.cfg.NotifyOnCleanShutdown:
		e.notify(levelInfo, "", name,
			fmt.Sprintf("[svrmon] %s 正常重启完成", name),
			fmt.Sprintf("服务器 %s 已正常重启并恢复监控。\n停止时间: %s\n恢复时间: %s\n\n— svrmon 自动通知", name, fmtTime(gone.Unix()), fmtTime(now)))
	}
}

// OnGoodbye marks that the agent announced a graceful stop.
func (e *AlertEngine) OnGoodbye(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if st := e.state[name]; st != nil {
		st.gracefulLeft = true
	}
}

// Rename moves the per-server alert state after a display-name change,
// so subsequent mails/webhooks use the new name.
func (e *AlertEngine) Rename(old, new string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if st, ok := e.state[old]; ok {
		e.state[new] = st
		delete(e.state, old)
	}
}

// OnDisconnect starts the offline observation window (or notes a clean leave).
func (e *AlertEngine) OnDisconnect(name string) {
	e.mu.Lock()
	st := e.state[name]
	if st == nil {
		e.mu.Unlock()
		return
	}
	graceful := st.gracefulLeft
	st.gracefulLeft = false
	st.goneAt = time.Now()
	gone := st.goneAt
	var timer *time.Timer
	if !graceful && e.cfg.Enabled {
		grace := time.Duration(e.cfg.OfflineGraceSec) * time.Second
		timer = time.AfterFunc(grace, func() { e.checkOffline(name) })
	}
	st.graceTimer = timer
	e.mu.Unlock()

	if graceful && e.cfg.Enabled && e.cfg.NotifyOnCleanShutdown {
		e.notify(levelInfo, "", name,
			fmt.Sprintf("[svrmon] %s 正常关机/停止", name),
			fmt.Sprintf("agent 已正常退出（收到关机告别帧）。\n时间: %s\n\n— svrmon 自动通知", fmtTime(gone.Unix())))
	}
}

// checkOffline fires when the grace period elapsed without reconnect.
func (e *AlertEngine) checkOffline(name string) {
	if e.online == nil || e.online(name) {
		return
	}
	e.mu.Lock()
	st := e.state[name]
	if st == nil || st.graceTimer == nil || st.alertedOffline {
		e.mu.Unlock()
		return
	}
	st.alertedOffline = true
	st.graceTimer = nil
	gone := st.goneAt
	e.mu.Unlock()

	e.notify(levelAlert, name+"|offline", name,
		fmt.Sprintf("[svrmon告警] %s 失联（疑似异常关机/断电/宕机）", name),
		fmt.Sprintf("服务器 %s 于 %s 失去联系：未收到正常关机告别帧，已超过 %d 秒观察期仍未恢复。\n\n可能原因：异常断电 / 系统死机 / 网络完全中断。\n若该机器随后重启，将收到第二封确认邮件（依据系统启动记录判定是否异常关机）。\n\n— svrmon 自动告警（同一事件 %d 分钟内不重复）",
			name, fmtTime(gone.Unix()), e.cfg.OfflineGraceSec, e.cfg.CooldownMin))
}
