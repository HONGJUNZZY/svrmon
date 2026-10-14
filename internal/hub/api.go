package hub

import (
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"svrmon/internal/model"
)

var powerActions = map[string]bool{
	"on": true, "off": true, "soft": true, "cycle": true, "reset": true, "status": true,
}

// Routes builds the HTTP handler. distFS is the embedded frontend root
// (may be nil when the frontend was not built).
func (h *Hub) Routes(distFS fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/agent", h.handleAgent)
	mux.Handle("/ws", h.auth(http.HandlerFunc(h.handleBrowser)))
	mux.Handle("/api/servers", h.auth(http.HandlerFunc(h.handleServers)))
	mux.Handle("/api/history", h.auth(http.HandlerFunc(h.handleHistory)))
	mux.Handle("/api/power", h.auth(http.HandlerFunc(h.handlePower)))
	mux.Handle("/api/rename", h.auth(http.HandlerFunc(h.handleRename)))
	mux.Handle("/api/logs", h.auth(http.HandlerFunc(h.handleLogs)))
	if distFS != nil {
		mux.Handle("/", h.auth(serveSPA(distFS)))
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "frontend not built into this binary", http.StatusServiceUnavailable)
		})
	}
	return mux
}

// auth enforces the optional UI token (?token=, Bearer or cookie).
func (h *Hub) auth(next http.Handler) http.Handler {
	if h.uiToken == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.URL.Query().Get("token")
		if tok == "" {
			if ah := r.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
				tok = strings.TrimPrefix(ah, "Bearer ")
			}
		}
		if tok == "" {
			if c, err := r.Cookie("svrmon_token"); err == nil {
				tok = c.Value
			}
		}
		if tok != h.uiToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Hub) handleServers(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	list := make([]*model.ServerState, 0, len(h.servers))
	for _, st := range h.servers {
		list = append(list, st)
	}
	b, err := json.Marshal(map[string]any{"servers": list})
	h.mu.RUnlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(b)
}

func (h *Hub) handleHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	server := q.Get("server")
	if server == "" {
		http.Error(w, "missing server", http.StatusBadRequest)
		return
	}
	now := time.Now().Unix()
	from, _ := strconv.ParseInt(q.Get("from"), 10, 64)
	to, _ := strconv.ParseInt(q.Get("to"), 10, 64)
	points, _ := strconv.Atoi(q.Get("points"))
	if points <= 0 || points > 1440 {
		points = 720
	}
	if to <= 0 {
		to = now
	}
	if from <= 0 {
		from = to - 3600
	}
	if from >= to {
		http.Error(w, "from must be < to", http.StatusBadRequest)
		return
	}
	bucket := (to - from) / int64(points)
	if bucket < 1 {
		bucket = 1
	}
	pts, err := h.store.Query(server, from, to, bucket)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	disks, err := h.store.QueryDisks(server, from, to, bucket)
	if err != nil {
		disks = map[string][]DiskPoint{}
	}
	if counts, err := h.store.QueryLogCounts(server, from, to, bucket); err == nil {
		for i := range pts {
			if c, ok := counts[pts[i].T]; ok {
				cc := c
				pts[i].LogCount = &cc
			}
		}
	}
	writeJSON(w, map[string]any{
		"server": server, "from": from, "to": to, "bucketSec": bucket, "points": pts, "disks": disks,
	})
}

// handleLogs returns stored severe log entries for one server.
func (h *Hub) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	server := q.Get("server")
	if server == "" {
		http.Error(w, "missing server", http.StatusBadRequest)
		return
	}
	now := time.Now().Unix()
	from, _ := strconv.ParseInt(q.Get("from"), 10, 64)
	to, _ := strconv.ParseInt(q.Get("to"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	if to <= 0 {
		to = now
	}
	if from <= 0 {
		from = to - 86400
	}
	entries, err := h.store.QueryLogs(server, from, to, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []model.LogEntry{}
	}
	writeJSON(w, map[string]any{"server": server, "from": from, "to": to, "entries": entries})
}

func (h *Hub) handlePower(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Server string `json:"server"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !powerActions[req.Action] {
		http.Error(w, "invalid action (on|off|soft|cycle|reset|status)", http.StatusBadRequest)
		return
	}
	res, err := h.SendCmd(req.Server, "ipmi_power", req.Action, 45*time.Second)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": res.OK, "output": res.Output, "server": req.Server})
}

// handleRename changes a server's display name (persisted in SQLite and
// applied to live state, alerts and future agent registrations).
func (h *Hub) handleRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Server string `json:"server"` // current display name
		Name   string `json:"name"`   // new display name
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 64 {
		http.Error(w, "name must be 1-64 characters", http.StatusBadRequest)
		return
	}

	h.mu.RLock()
	st := h.servers[req.Server]
	real := ""
	dup := false
	if st != nil {
		real = st.Info.Hostname
		for k, o := range h.servers {
			if k != req.Server && (k == name || o.Info.Hostname == name) {
				dup = true
				break
			}
		}
	}
	h.mu.RUnlock()
	if st == nil {
		http.Error(w, "unknown server", http.StatusNotFound)
		return
	}
	if dup {
		http.Error(w, "name already in use", http.StatusConflict)
		return
	}
	if name == req.Server {
		writeJSON(w, map[string]any{"ok": true, "server": name, "unchanged": true})
		return
	}

	if err := h.store.SetName(real, name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.applyRename(req.Server, name)
	h.broadcast(model.BrowserRenameMsg{Type: model.TypeRename, From: req.Server, To: name})
	log.Printf("hub: server %q renamed to %q", req.Server, name)
	writeJSON(w, map[string]any{"ok": true, "server": name})
}

// serveSPA serves static files, falling back to index.html for unknown paths.
func serveSPA(distFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(distFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(distFS, p); err != nil {
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}
