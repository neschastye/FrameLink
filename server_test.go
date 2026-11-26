package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRoutes(t *testing.T) {
	a := app{hub: newHub(), localHost: "127.0.0.1:8765", camOrigin: "https://192.168.1.10:8443", camKey: token(), deskKey: token(), obsKey: token()}
	cases := []struct {
		local  bool
		path   string
		header bool
		host   string
		code   int
	}{
		{true, "/dashboard", false, a.localHost, 200}, {true, "/obs", false, a.localHost, 200}, {false, "/camera", false, "192.168.1.10:8443", 200},
		{false, "/api/session", true, "192.168.1.10:8443", 404}, {false, "/obs", false, "192.168.1.10:8443", 404}, {true, "/camera", false, a.localHost, 404},
		{true, "/api/session", false, a.localHost, 403}, {true, "/api/session", true, "other.example", 421},
		{true, "/api/session", true, a.localHost, 200}, {true, "/api/qr", true, a.localHost, 200}, {false, "/ca/rootCA-key.pem", false, "192.168.1.10:8443", 404},
	}
	for _, tc := range cases {
		host := "192.168.1.10:8443"
		if tc.local {
			host = a.localHost
		}
		req := httptest.NewRequest("GET", "http://"+tc.host+tc.path, nil)
		if tc.header {
			req.Header.Set("X-FrameLink", "dashboard")
		}
		w := httptest.NewRecorder()
		a.handler(tc.local, host).ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Errorf("%v %s: %d", tc.local, tc.path, w.Code)
		}
		if tc.path == "/api/qr" && w.Code == 200 && !bytes.HasPrefix(w.Body.Bytes(), []byte("\x89PNG")) {
			t.Fatal("invalid QR")
		}
	}
}
func TestSocketAuthorization(t *testing.T) {
	a := app{hub: newHub(), camKey: token(), deskKey: token(), obsKey: token()}
	ts := httptest.NewUnstartedServer(nil)
	host := ts.Listener.Addr().String()
	ts.Config.Handler = a.handler(false, host)
	ts.StartTLS()
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	u.Scheme = "wss"
	u.Path = "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, tc := range []struct {
		role, origin, key string
		ok                bool
	}{
		{"camera", ts.URL, a.camKey, true}, {"camera", "https://other.example", a.camKey, false}, {"camera", "", a.camKey, false},
		{"camera", ts.URL, a.obsKey, false}, {"desk", ts.URL, a.deskKey, false}, {"obs", ts.URL, a.obsKey, false},
	} {
		u.RawQuery = "role=" + tc.role
		ws, res, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: ts.Client(), HTTPHeader: http.Header{"Origin": {tc.origin}}, Subprotocols: []string{"framelink.v1", "auth." + tc.key}})
		if tc.ok {
			if err != nil {
				t.Fatal(err)
			}
			if err = ws.Write(ctx, websocket.MessageText, []byte(`{"type":"state","junk":1}`)); err != nil {
				t.Fatal(err)
			}
			for err == nil {
				_, _, err = ws.Read(ctx)
			}
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatal(err)
			}
			ws.CloseNow()
		} else if err == nil || res == nil || res.StatusCode != 403 {
			t.Fatal("unauthorized upgrade", tc.role, err)
		}
	}
}
func TestNegotiationLifecycle(t *testing.T) {
	h := newHub()
	makeClient := func(role string) *client { return &client{role: role, out: make(chan any, 100), cancel: func() {}} }
	cam, obs, desk := makeClient("camera"), makeClient("obs"), makeClient("desk")
	for _, c := range []*client{obs, desk, cam} {
		if !h.join(c) {
			t.Fatal("join")
		}
	}
	if h.join(makeClient("camera")) {
		t.Fatal("second camera")
	}
	s := settings{Facing: "environment", Height: 1080, FPS: 30, Mbps: 6}
	if !h.handle(cam, message{Type: "state", Enabled: true, Settings: &s}) || obs.id == "" || desk.id != "" {
		t.Fatal("initial pairing")
	}
	first := obs.id
	if !h.handle(desk, message{Type: "preview", Enabled: true}) || desk.id == "" || obs.id != first {
		t.Fatal("preview changed OBS")
	}
	if h.handle(desk, message{Type: "answer", Peer: "obs", ID: obs.id, SDP: "v=0"}) || h.handle(obs, message{Type: "control", Action: "start"}) {
		t.Fatal("role confusion")
	}
	if !h.handle(obs, message{Type: "retry", Peer: "obs", ID: first}) || first == obs.id {
		t.Fatal("retry")
	}
	before := len(cam.out)
	if !h.handle(obs, message{Type: "answer", Peer: "obs", ID: first, SDP: "v=0"}) || len(cam.out) != before {
		t.Fatal("stale SDP")
	}
	h.handle(desk, message{Type: "preview"})
	if desk.id != "" || obs.id == "" {
		t.Fatal("preview teardown")
	}
	h.leave(cam)
	if h.live || obs.id != "" || h.config != nil {
		t.Fatal("stale state")
	}
}
func TestBoundsAndPersistence(t *testing.T) {
	for _, raw := range []string{`{"type":"preview"}{}`, `{"enabled":8}`, `{"unknown":"x"}`} {
		if _, err := decodeMessage([]byte(raw)); err == nil {
			t.Fatal(raw)
		}
	}
	if (settings{Facing: "user", Height: 720, FPS: 200, Mbps: 1}).valid() {
		t.Fatal("invalid fps")
	}
	dir := t.TempDir()
	a, err := obsSecret(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := obsSecret(dir)
	if err != nil || a != b || a == token() {
		t.Fatal("token persistence")
	}
}
func TestTLSRenewal(t *testing.T) {
	dir := t.TempDir()
	verify := func(ip string) tls.Certificate {
		pair, err := certificate(dir, ip)
		if err != nil {
			t.Fatal(err)
		}
		public, err := os.ReadFile(filepath.Join(dir, "FrameLink-CA.cer"))
		if err != nil {
			t.Fatal(err)
		}
		root, err := x509.ParseCertificate(public)
		if err != nil {
			t.Fatal(err)
		}
		pool := x509.NewCertPool()
		pool.AddCert(root)
		if _, err = pair.Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: ip}); err != nil {
			t.Fatal(err)
		}
		return pair
	}
	first := verify("192.168.2.2")
	if !bytes.Equal(first.Certificate[0], verify("192.168.2.2").Certificate[0]) {
		t.Fatal("unnecessary reissue")
	}
	second := verify("192.168.2.3")
	if bytes.Equal(first.Certificate[0], second.Certificate[0]) || !bytes.Equal(first.Leaf.AuthorityKeyId, second.Leaf.AuthorityKeyId) {
		t.Fatal("renewal")
	}
	ca, _ := os.ReadFile(filepath.Join(dir, "ca", "rootCA.pem"))
	b, _ := pem.Decode(ca)
	if b == nil {
		t.Fatal("missing public root")
	}
}
