// Package hub implements the central server: it accepts agent WebSocket
// connections, keeps the latest state per server, broadcasts to browsers
// and stores history in SQLite.
package hub

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"svrmon/internal/model"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(*http.Request) bool { return true }, // LAN tool; token auth applies
}

// HubVersion is the hub build version.
const HubVersion = "0.2.0"

type agentConn struct {
	name      string
	conn      *websocket.Conn
	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[string]chan model.AgentCmdResultMsg
}

func (a *agentConn) resolve(m model.AgentCmdResultMsg) {
	a.pendingMu.Lock()
	ch := a.pending[m.ID]
	delete(a.pending, m.ID)
	a.pendingMu.Unlock()
	if ch != nil {
		select {
		case ch <- m:
		default:
		}
	}
}

func (a *agentConn) close() { _ = a.conn.Close() }

type browserConn struct {
	conn   *websocket.Conn
	send   chan []byte
	closed chan struct{}
	once   sync.Once
}

func (b *browserConn) stop() {
	b.once.Do(func() {
		close(b.closed)
		_ = b.conn.Close()
	})
}

// Hub is the central coordinating state.
type Hub struct {
	store   *Store
	uiToken string
	agentOK map[string]bool
	alert   *AlertEngine

	mu       sync.RWMutex
	servers  map[string]*model.ServerState // persistent latest state per server
	conns    map[string]*agentConn         // live agent connections
	browsers map[*browserConn]struct{}
	aliases  map[string]string // agent hostname -> display name (persisted)
}

// NewHub builds a hub. agentTokens is the allow-list for agents; when it is
// empty every agent is accepted (single-box convenience, not recommended).
func NewHub(store *Store, agentTokens []string, uiToken string, alertCfg AlertConfig) *Hub {
	h := &Hub{
		store:    store,
		uiToken:  uiToken,
		agentOK:  map[string]bool{},
		servers:  map[string]*model.ServerState{},
		conns:    map[string]*agentConn{},
		browsers: map[*browserConn]struct{}{},
	}
	for _, t := range agentTokens {
		if t != "" {
			h.agentOK[t] = true
		}
	}
	if store != nil {
		h.aliases = store.LoadNames()
	}
	if alertCfg.Enabled {
		h.alert = NewAlertEngine(alertCfg, func(name string) bool {
			h.mu.RLock()
			defer h.mu.RUnlock()
			_, ok := h.conns[name]
			return ok
		})
	}
	return h
}

// resolveName maps an agent-reported hostname to its display name.
func (h *Hub) resolveName(hostname string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if n, ok := h.aliases[hostname]; ok && n != "" {
		return n
	}
	return hostname
}

// connName reads the current (possibly renamed) name of a live agent conn.
func (h *Hub) connName(ac *agentConn) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return ac.name
}

// applyRename moves all live state from old to the new display name.
func (h *Hub) applyRename(old, new string) {
	h.mu.Lock()
	if st, ok := h.servers[old]; ok {
		st.Name = new
		h.servers[new] = st
		delete(h.servers, old)
		h.aliases[st.Info.Hostname] = new
	}
	if ac, ok := h.conns[old]; ok {
		ac.name = new
		h.conns[new] = ac
		delete(h.conns, old)
	}
	h.mu.Unlock()
	if h.alert != nil {
		h.alert.Rename(old, new)
	}
}

// ---------- agent endpoint ----------

func (h *Hub) handleAgent(w http.ResponseWriter, r *http.Request) {
	if len(h.agentOK) > 0 && !h.agentOK[r.URL.Query().Get("token")] {
		http.Error(w, "invalid agent token", http.StatusUnauthorized)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	// First message must be hello.
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		_ = conn.Close()
		return
	}
	var hello model.AgentHelloMsg
	if err := json.Unmarshal(raw, &hello); err != nil || hello.Type != model.TypeHello || hello.Info.Hostname == "" {
		_ = conn.Close()
		return
	}

	info := hello.Info
	if i := strings.LastIndex(r.RemoteAddr, ":"); i > 0 {
		info.RemoteAddr = r.RemoteAddr[:i]
	}
	name := h.resolveName(info.Hostname)
	ac := &agentConn{name: name, conn: conn, pending: map[string]chan model.AgentCmdResultMsg{}}

	h.mu.Lock()
	old := h.conns[name]
	h.conns[name] = ac
	st, ok := h.servers[name]
	if !ok {
		st = &model.ServerState{Name: name}
		h.servers[name] = st
	}
	st.Info = info
	st.Online = true
	st.LastSeen = time.Now().Unix()
	h.mu.Unlock()
	if old != nil {
		old.close() // take over from a stale connection
	}
	log.Printf("hub: agent %q connected from %s", name, info.RemoteAddr)
	h.broadcast(model.BrowserServerMsg{Type: model.TypeServerJoin, Server: name, Info: &info})
	if h.alert != nil {
		h.alert.OnHello(name, &info)
	}

	defer func() {
		h.mu.RLock()
		cur := ac.name
		h.mu.RUnlock()
		h.mu.Lock()
		dead := false
		if h.conns[cur] == ac {
			delete(h.conns, cur)
			dead = true
		}
		if dead {
			if st := h.servers[cur]; st != nil {
				st.Online = false
			}
		}
		h.mu.Unlock()
		ac.close()
		h.broadcast(model.BrowserOfflineMsg{Type: model.TypeOffline, Server: cur})
		log.Printf("hub: agent %q disconnected", cur)
		if dead && h.alert != nil {
			h.alert.OnDisconnect(cur)
		}
	}()

	// Idle timeout: refreshed on every frame received from the agent.
	// NOTE: the pong handler alone is NOT enough — the hub never sends pings,
	// so it never receives pongs, and that handler would never run. Only the
	// agent pings (every 20s) and gorilla's default ping handler replies with
	// a pong, which keeps the *agent's* read deadline alive, not this one.
	const agentIdleTimeout = 120 * time.Second
	_ = conn.SetReadDeadline(time.Now().Add(agentIdleTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(agentIdleTimeout))
	})
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		// Any frame (snapshot / config / cmd result / ping) counts as liveness.
		_ = conn.SetReadDeadline(time.Now().Add(agentIdleTimeout))
		var env struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &env) != nil {
			continue
		}
		switch env.Type {
		case model.TypeSnapshot:
			var m model.AgentSnapshotMsg
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			h.onSnapshot(h.connName(ac), &m.Data)
		case model.TypeCmdResult:
			var m model.AgentCmdResultMsg
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			ac.resolve(m)
		case model.TypeGoodbye:
			if h.alert != nil {
				h.alert.OnGoodbye(h.connName(ac))
			}
		case model.TypeConfig:
			var m model.AgentConfigMsg
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			h.onConfig(h.connName(ac), &m.Data)
		}
	}
}

// onConfig stores the machine inventory and forwards it to browsers.
func (h *Hub) onConfig(name string, cfg *model.MachineConfig) {
	h.mu.Lock()
	if st := h.servers[name]; st != nil {
		st.Config = cfg
	}
	h.mu.Unlock()
	h.broadcast(model.BrowserConfigMsg{Type: model.TypeConfig, Server: name, Data: cfg})
}

func (h *Hub) onSnapshot(name string, snap *model.Snapshot) {	now := time.Now().Unix()
	h.mu.Lock()
	if st := h.servers[name]; st != nil {
		st.Online = true
		st.LastSeen = now
		cp := *snap
		st.Latest = &cp
	}
	h.mu.Unlock()

	h.store.Insert(name, snap)
	if len(snap.Logs) > 0 {
		h.store.InsertLogs(name, snap.Logs)
	}
	h.broadcast(model.BrowserServerMsg{Type: model.TypeSnapshot, Server: name, Data: snap})
}

// SendCmd sends a command to a connected agent and waits for its result.
func (h *Hub) SendCmd(server, cmd, action string, timeout time.Duration) (model.AgentCmdResultMsg, error) {
	h.mu.RLock()
	ac := h.conns[server]
	h.mu.RUnlock()
	if ac == nil {
		return model.AgentCmdResultMsg{}, fmt.Errorf("server %q is not connected", server)
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	ch := make(chan model.AgentCmdResultMsg, 1)
	ac.pendingMu.Lock()
	ac.pending[id] = ch
	ac.pendingMu.Unlock()
	defer func() {
		ac.pendingMu.Lock()
		delete(ac.pending, id)
		ac.pendingMu.Unlock()
	}()

	ac.writeMu.Lock()
	err := ac.conn.WriteJSON(model.AgentCmdMsg{Type: model.TypeCmd, ID: id, Cmd: cmd, Action: action})
	ac.writeMu.Unlock()
	if err != nil {
		return model.AgentCmdResultMsg{}, err
	}
	select {
	case r := <-ch:
		return r, nil
	case <-time.After(timeout):
		return model.AgentCmdResultMsg{}, fmt.Errorf("agent did not answer within %s", timeout)
	}
}

// ---------- browser endpoint ----------

func (h *Hub) handleBrowser(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	bc := &browserConn{conn: conn, send: make(chan []byte, 128), closed: make(chan struct{})}

	h.mu.Lock()
	h.browsers[bc] = struct{}{}
	init := model.BrowserInitMsg{Type: model.TypeInit, HubVer: HubVersion, Servers: map[string]*model.ServerState{}}
	for k, v := range h.servers {
		init.Servers[k] = v
	}
	bInit, mErr := json.Marshal(init)
	h.mu.Unlock()
	if mErr == nil {
		select {
		case bc.send <- bInit:
		default:
		}
	}

	// writer: outbound frames + periodic pings
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-bc.closed:
				return
			case msg := <-bc.send:
				_ = bc.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := bc.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					h.removeBrowser(bc)
					return
				}
			case <-ticker.C:
				if err := bc.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					h.removeBrowser(bc)
					return
				}
			}
		}
	}()

	// reader: discard browser frames until the socket closes
	go func() {
		bc.conn.SetReadLimit(4096)
		for {
			if _, _, err := bc.conn.ReadMessage(); err != nil {
				h.removeBrowser(bc)
				return
			}
		}
	}()
}

func (h *Hub) removeBrowser(bc *browserConn) {
	bc.stop()
	h.mu.Lock()
	delete(h.browsers, bc)
	h.mu.Unlock()
}

func (h *Hub) broadcast(msg any) {
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.RLock()
	targets := make([]*browserConn, 0, len(h.browsers))
	for bc := range h.browsers {
		targets = append(targets, bc)
	}
	h.mu.RUnlock()
	for _, bc := range targets {
		select {
		case bc.send <- b:
		case <-bc.closed:
		default: // slow browser: drop this frame, next snapshot resyncs
		}
	}
}
