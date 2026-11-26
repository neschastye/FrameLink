package main

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

//go:embed web/* licenses/* LICENSE
var assets embed.FS

type app struct {
	hub                                     *hub
	deskKey, camKey, obsKey, localHost, dir string
	mu                                      sync.RWMutex
	camOrigin                               string
	lanError                                string
}

func obsSecret(dir string) (string, error) {
	path := filepath.Join(dir, "obs-token")
	b, err := os.ReadFile(path)
	if err == nil {
		key := strings.TrimSpace(string(b))
		if len(key) != 32 || strings.ContainsAny(key, " /?#\r\n\t") {
			return "", fmt.Errorf("invalid OBS token in %s", path)
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	key := token()
	_, err = f.WriteString(key)
	cerr := f.Close()
	if err != nil {
		return "", err
	}
	return key, cerr
}
func (a *app) cameraURL() (string, string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.camOrigin == "" {
		return "", a.lanError
	}
	return a.camOrigin + "/camera#" + a.camKey, a.lanError
}
func (a *app) handler(local bool, host string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		scheme := "http"
		ws := "ws"
		if r.TLS != nil {
			scheme = "https"
			ws = "wss"
		}
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' blob:; media-src 'self' blob:; connect-src 'self' "+ws+"://"+host+"; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		policy := "camera=(), microphone=()"
		if !local {
			policy = "camera=(self), microphone=(self)"
		}
		w.Header().Set("Permissions-Policy", policy)
		if !strings.EqualFold(r.Host, host) {
			http.Error(w, "unknown host", 421)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method", 405)
			return
		}
		path := r.URL.Path
		switch {
		case local && path == "/api/session":
			if r.Header.Get("X-FrameLink") != "dashboard" {
				http.Error(w, "forbidden", 403)
				return
			}
			camera, problem := a.cameraURL()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"camera": camera, "problem": problem, "obs": "http://" + a.localHost + "/obs#" + a.obsKey, "desk": a.deskKey, "ca": filepath.Join(a.dir, "FrameLink-CA.cer")})
		case local && path == "/api/qr":
			if r.Header.Get("X-FrameLink") != "dashboard" {
				http.Error(w, "forbidden", 403)
				return
			}
			url, _ := a.cameraURL()
			if url == "" {
				http.Error(w, "LAN offline", 503)
				return
			}
			png, err := qrcode.Encode(url, qrcode.Medium, 240)
			if err != nil {
				http.Error(w, "QR", 500)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Write(png)
		case path == "/ws":
			if r.Header.Get("Origin") != scheme+"://"+host {
				http.Error(w, "origin", 403)
				return
			}
			role := r.URL.Query().Get("role")
			secret := ""
			switch {
			case local && role == "desk":
				secret = a.deskKey
			case local && role == "obs":
				secret = a.obsKey
			case !local && role == "camera":
				secret = a.camKey
			}
			protos := strings.Split(strings.Join(r.Header.Values("Sec-WebSocket-Protocol"), ","), ",")
			for i := range protos {
				protos[i] = strings.TrimSpace(protos[i])
			}
			if secret == "" || len(protos) != 2 || protos[0] != "framelink.v1" || subtle.ConstantTimeCompare([]byte(protos[1]), []byte("auth."+secret)) != 1 {
				http.Error(w, "forbidden", 403)
				return
			}
			a.hub.socket(w, r, role)
		case (local && (path == "/dashboard" || path == "/obs")) || (!local && path == "/camera"):
			serveAsset(w, "web"+path+".html", "text/html; charset=utf-8")
		case path == "/style.css":
			serveAsset(w, "web/style.css", "text/css; charset=utf-8")
		case path == "/mark.svg":
			serveAsset(w, "web/mark.svg", "image/svg+xml")
		case path == "/link.js" || path == "/controls.js" || path == "/rtc.js" || (!local && path == "/camera.js") || (local && (path == "/viewer.js" || path == "/dashboard.js" || path == "/obs.js")):
			serveAsset(w, "web"+path, "text/javascript; charset=utf-8")
		case local && path == "/":
			http.Redirect(w, r, "/dashboard", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	})
}
func serveAsset(w http.ResponseWriter, name, kind string) {
	data, err := assets.ReadFile(name)
	if err != nil {
		http.Error(w, "missing asset", 500)
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Write(data)
}
func httpServer(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 40 * time.Second, MaxHeaderBytes: 16 << 10}
}
func newLocal(port int) (net.Listener, error) {
	return net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
}
