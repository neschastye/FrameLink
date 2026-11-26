package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

func token() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

type settings struct {
	Facing   string `json:"facing"`
	Height   int    `json:"height"`
	FPS      int    `json:"fps"`
	Mbps     int    `json:"mbps"`
	Rotation int    `json:"rotation"`
	Mirror   bool   `json:"mirror"`
	Audio    bool   `json:"audio"`
}

func (s settings) valid() bool {
	return slices.Contains([]string{"user", "environment"}, s.Facing) && slices.Contains([]int{480, 720, 1080}, s.Height) && slices.Contains([]int{24, 30, 60}, s.FPS) && s.Mbps >= 1 && s.Mbps <= 30 && slices.Contains([]int{0, 90, 180, 270}, s.Rotation)
}

type metrics struct {
	FPS    *float64 `json:"fps"`
	Width  *float64 `json:"width"`
	Height *float64 `json:"height"`
	Mbps   *float64 `json:"mbps"`
	RTT    *float64 `json:"rtt"`
	Lost   *float64 `json:"lost"`
}

func (m metrics) valid() bool {
	for _, v := range []*float64{m.FPS, m.Width, m.Height, m.Mbps, m.RTT, m.Lost} {
		if v != nil && (*v < 0 || *v > 1e9) {
			return false
		}
	}
	return true
}

type message struct {
	Type     string    `json:"type"`
	Peer     string    `json:"peer,omitempty"`
	ID       string    `json:"id,omitempty"`
	SDP      string    `json:"sdp,omitempty"`
	Enabled  bool      `json:"enabled,omitempty"`
	Settings *settings `json:"settings,omitempty"`
	Action   string    `json:"action,omitempty"`
	Metrics  *metrics  `json:"metrics,omitempty"`
	Note     string    `json:"note,omitempty"`
}
type client struct {
	role   string
	conn   *websocket.Conn
	out    chan any
	cancel context.CancelFunc
	want   bool
	id     string
}
type hub struct {
	mu      sync.Mutex
	clients map[string]*client
	live    bool
	config  *settings
	note    string
}

func newHub() *hub { return &hub{clients: map[string]*client{}} }
func send(c *client, m any) {
	if c == nil {
		return
	}
	select {
	case c.out <- m:
	default:
		c.cancel()
	}
}
func (h *hub) status() {
	for _, c := range h.clients {
		send(c, map[string]any{"type": "status", "camera": h.clients["camera"] != nil, "obs": h.clients["obs"] != nil, "preview": h.clients["desk"] != nil && h.clients["desk"].want, "live": h.live, "settings": h.config, "note": h.note})
	}
}
func (h *hub) pair(c *client) {
	if c == nil || c.role == "camera" {
		return
	}
	cam := h.clients["camera"]
	if c.id != "" {
		m := message{Type: "reset", Peer: c.role, ID: c.id}
		send(cam, m)
		send(c, m)
		c.id = ""
	}
	if !h.live || !c.want || cam == nil {
		return
	}
	c.id = token()
	m := message{Type: "pair", Peer: c.role, ID: c.id}
	send(c, m)
	send(cam, m)
}
func (h *hub) join(c *client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[c.role] != nil {
		return false
	}
	h.clients[c.role] = c
	c.want = c.role == "obs"
	h.status()
	h.pair(c)
	return true
}
func (h *hub) leave(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[c.role] != c {
		return
	}
	delete(h.clients, c.role)
	if c.role == "camera" {
		h.live = false
		h.config = nil
		h.note = ""
		h.pair(h.clients["desk"])
		h.pair(h.clients["obs"])
	} else if c.id != "" {
		send(h.clients["camera"], message{Type: "reset", Peer: c.role, ID: c.id})
	}
	h.status()
}
func (h *hub) handle(c *client, m message) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[c.role] != c {
		return false
	}
	switch m.Type {
	case "state":
		if c.role != "camera" || m.Settings == nil || !m.Settings.valid() || len(m.Note) > 600 {
			return false
		}
		changed := h.live != m.Enabled
		h.live = m.Enabled
		h.config = m.Settings
		h.note = m.Note
		h.status()
		if changed {
			h.pair(h.clients["desk"])
			h.pair(h.clients["obs"])
		}
	case "preview":
		if c.role != "desk" {
			return false
		}
		if c.want != m.Enabled {
			c.want = m.Enabled
			h.pair(c)
			h.status()
		}
	case "control":
		if c.role != "desk" || !slices.Contains([]string{"start", "stop", "settings"}, m.Action) {
			return false
		}
		if m.Action == "settings" && (m.Settings == nil || !m.Settings.valid()) {
			return false
		}
		send(h.clients["camera"], message{Type: "control", Action: m.Action, Settings: m.Settings})
	case "offer", "answer", "retry", "metrics":
		if m.Peer != "desk" && m.Peer != "obs" || m.ID == "" || len(m.ID) > 64 {
			return false
		}
		if c.role != "camera" && c.role != m.Peer {
			return false
		}
		viewer := h.clients[m.Peer]
		if viewer == nil || viewer.id != m.ID {
			return true
		} // Late packet from a closed negotiation.
		switch m.Type {
		case "offer", "answer":
			if (c.role == "camera") != (m.Type == "offer") || !strings.HasPrefix(m.SDP, "v=0") || len(m.SDP) > 60*1024 {
				return false
			}
			target := viewer
			if m.Type == "answer" {
				target = h.clients["camera"]
			}
			send(target, message{Type: m.Type, Peer: m.Peer, ID: m.ID, SDP: m.SDP})
		case "retry":
			h.pair(viewer)
		case "metrics":
			if c.role == "camera" || m.Metrics == nil || !m.Metrics.valid() {
				return false
			}
			send(h.clients["desk"], message{Type: "metrics", Peer: m.Peer, ID: m.ID, Metrics: m.Metrics})
		}
	default:
		return false
	}
	return true
}
func decodeMessage(data []byte) (message, error) {
	var m message
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, err
	}
	if d.Decode(new(any)) != io.EOF {
		return m, errors.New("trailing data")
	}
	return m, nil
}
func (h *hub) socket(w http.ResponseWriter, r *http.Request, role string) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"framelink.v1"}})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	c := &client{role: role, conn: ws, out: make(chan any, 32), cancel: cancel}
	ws.SetReadLimit(64 * 1024)
	if !h.join(c) {
		ws.Close(4009, "role in use")
		return
	}
	defer h.leave(c)
	go func() {
		defer cancel()
		timer := time.NewTicker(15 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				ping, done := context.WithTimeout(ctx, 8*time.Second)
				err := ws.Ping(ping)
				done()
				if err != nil {
					return
				}
			case m := <-c.out:
				data, err := json.Marshal(m)
				if err != nil {
					return
				}
				wr, done := context.WithTimeout(ctx, 4*time.Second)
				err = ws.Write(wr, websocket.MessageText, data)
				done()
				if err != nil {
					return
				}
			}
		}
	}()
	start := time.Now()
	count := 0
	for {
		kind, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if time.Since(start) > time.Second {
			start = time.Now()
			count = 0
		}
		count++
		m, err := decodeMessage(data)
		if count > 50 || kind != websocket.MessageText || err != nil || !h.handle(c, m) {
			ws.Close(websocket.StatusPolicyViolation, "invalid message")
			return
		}
	}
}
