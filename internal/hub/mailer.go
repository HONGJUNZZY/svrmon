package hub

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTPConfig configures the outbound alert mail connection.
type SMTPConfig struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Security string   `json:"security"` // auto | tls | starttls | none
	Username string   `json:"username"`
	Password string   `json:"password"` // QQ/163 邮箱请填授权码，不是登录密码
	From     string   `json:"from"`     // 显示名 <addr@host>
	To       []string `json:"to"`
}

// Mail is one outbound notification.
type Mail struct {
	Subject string
	Body    string
}

// loginAuth implements the legacy LOGIN SASL mechanism used by some servers.
type loginAuth struct{ username, password string }

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	return "LOGIN", []byte(a.username), nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(fromServer))) {
	case "username:":
		return []byte(a.username), nil
	case "password:":
		return []byte(a.password), nil
	}
	return nil, errors.New("unexpected server challenge")
}

// sendMail delivers one mail over SMTP (implicit TLS, STARTTLS or plain).
func sendMail(cfg SMTPConfig, m Mail) error {
	if cfg.Host == "" || len(cfg.To) == 0 {
		return fmt.Errorf("smtp not configured")
	}
	port := cfg.Port
	if port == 0 {
		port = 465
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	sec := strings.ToLower(cfg.Security)
	if sec == "" {
		sec = "auto"
	}
	implicitTLS := sec == "tls" || (sec == "auto" && port == 465)

	var c *smtp.Client
	if implicitTLS {
		tlsConn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.Host})
		if err != nil {
			return fmt.Errorf("smtp tls dial: %w", err)
		}
		c, err = smtp.NewClient(tlsConn, cfg.Host)
		if err != nil {
			return fmt.Errorf("smtp client: %w", err)
		}
	} else {
		conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
		if err != nil {
			return fmt.Errorf("smtp dial: %w", err)
		}
		c, err = smtp.NewClient(conn, cfg.Host)
		if err != nil {
			return fmt.Errorf("smtp client: %w", err)
		}
		if sec == "starttls" || sec == "auto" {
			if ok, _ := c.Extension("STARTTLS"); ok {
				if err := c.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
					return fmt.Errorf("smtp starttls: %w", err)
				}
			} else if sec == "starttls" {
				return fmt.Errorf("smtp: server does not support STARTTLS")
			}
		}
	}
	defer c.Close()

	if cfg.Username != "" {
		if ok, mechs := c.Extension("AUTH"); ok {
			if strings.Contains(mechs, "PLAIN") {
				if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
					return fmt.Errorf("smtp auth: %w", err)
				}
			} else if strings.Contains(mechs, "LOGIN") {
				if err := c.Auth(&loginAuth{cfg.Username, cfg.Password}); err != nil {
					return fmt.Errorf("smtp auth: %w", err)
				}
			}
		}
	}

	fromAddr := cfg.From
	if l := strings.LastIndex(fromAddr, "<"); l >= 0 {
		if r := strings.LastIndex(fromAddr, ">"); r > l {
			fromAddr = fromAddr[l+1 : r]
		}
	}
	if err := c.Mail(fromAddr); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	for _, to := range cfg.To {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("smtp rcpt %s: %w", to, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: %s\r\n", cfg.From)
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(cfg.To, ", "))
	fmt.Fprintf(&buf, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", m.Subject))
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: text/plain; charset=utf-8\r\n\r\n")
	buf.WriteString(m.Body)
	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close data: %w", err)
	}
	return c.Quit()
}

// postWebhook delivers an alert payload as JSON. style "text" wraps it into
// the msgtype/text shape understood by DingTalk and WeCom text robots.
func postWebhook(url, style string, payload map[string]any) error {
	if url == "" {
		return nil
	}
	body := payload
	if strings.ToLower(style) == "text" {
		body = map[string]any{
			"msgtype": "text",
			"text": map[string]any{
				"content": fmt.Sprintf("[svrmon] %s %s", payload["server"], payload["event"]),
			},
		}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook status %s", resp.Status)
	}
	return nil
}
