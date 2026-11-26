package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Opt-in test. The only capture device is Chromium's synthetic source.
// Each test process has a private profile, data folder and loopback ports.
func until(t *testing.T, label string, fn func() bool) {
	t.Helper()
	end := time.Now().Add(40 * time.Second)
	for time.Now().Before(end) {
		if fn() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", label)
}
func process(t *testing.T, dir, path string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(path, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		select {
		case <-done:
			return
		default:
		}
		// All descendants of this test-owned process were created with its disposable profile.
		exec.Command("taskkill.exe", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	})
	return cmd
}

type browser struct {
	ws  *websocket.Conn
	seq int
	pid int
	t   *testing.T
}

func chrome(t *testing.T, path string) *browser {
	t.Helper()
	dir := t.TempDir()
	cmd := process(t, "", path, "--headless=new", "--no-first-run", "--no-default-browser-check", "--disable-background-networking", "--disable-extensions", "--disable-sync", "--no-proxy-server", "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0", "--user-data-dir="+dir, "--use-fake-device-for-media-stream=fps=60", "--use-fake-ui-for-media-stream", "--autoplay-policy=no-user-gesture-required", "--window-size=1320,960", "about:blank")
	var endpoint string
	until(t, "Chromium DevTools", func() bool {
		b, e := os.ReadFile(filepath.Join(dir, "DevToolsActivePort"))
		if e != nil {
			return false
		}
		port := strings.TrimSpace(strings.Split(string(b), "\n")[0])
		r, e := http.Get("http://127.0.0.1:" + port + "/json/list")
		if e != nil {
			return false
		}
		defer r.Body.Close()
		var tabs []struct{ Type, WebSocketDebuggerURL string }
		if json.NewDecoder(r.Body).Decode(&tabs) != nil {
			return false
		}
		for _, tab := range tabs {
			if tab.Type == "page" {
				endpoint = tab.WebSocketDebuggerURL
				return true
			}
		}
		return false
	})
	ws, _, err := websocket.Dial(t.Context(), endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	ws.SetReadLimit(8 << 20)
	t.Cleanup(func() { ws.CloseNow() })
	return &browser{ws: ws, t: t, pid: cmd.Process.Pid}
}
func (b *browser) call(method string, params any) json.RawMessage {
	b.t.Helper()
	b.seq++
	ctx, stop := context.WithTimeout(b.t.Context(), 6*time.Second)
	defer stop()
	if err := wsjson.Write(ctx, b.ws, map[string]any{"id": b.seq, "method": method, "params": params}); err != nil {
		b.t.Fatal(err)
	}
	for {
		var response struct {
			ID            int
			Result, Error json.RawMessage
		}
		if err := wsjson.Read(ctx, b.ws, &response); err != nil {
			b.t.Fatal(err)
		}
		if response.ID == b.seq {
			if response.Error != nil {
				b.t.Fatalf("CDP %s: %s", method, response.Error)
			}
			return response.Result
		}
	}
}
func (b *browser) eval(expr string) string {
	r := b.call("Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true, "userGesture": true})
	var v struct {
		Result           struct{ Value json.RawMessage }
		ExceptionDetails json.RawMessage
	}
	json.Unmarshal(r, &v)
	if v.ExceptionDetails != nil {
		return ""
	}
	return strings.Trim(string(v.Result.Value), "\"")
}
func (b *browser) navigate(to string) { b.call("Page.navigate", map[string]string{"url": to}) }
func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
func writeFixture(t *testing.T, root, file, body string) {
	t.Helper()
	path := filepath.Join(root, file)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func portableOBS(t *testing.T, install, sourceURL string) *exec.Cmd {
	t.Helper()
	root := t.TempDir()
	copy := exec.Command("robocopy.exe", install, root, "/E", "/NFL", "/NDL", "/NJH", "/NJS", "/NC", "/NS", "/NP", "/XF", "*.pdb", "debug.log", "/XD", "config")
	if err := copy.Run(); err != nil && copy.ProcessState.ExitCode() > 7 {
		t.Fatal(err)
	}
	config := filepath.Join(root, "config", "obs-studio")
	writeFixture(t, config, "global.ini", "[General]\nLastVersion=553648385\nEnableAutoUpdates=false\n")
	writeFixture(t, config, "user.ini", "[General]\nFirstRun=true\nConfirmOnExit=false\n[Basic]\nProfile=FrameLink\nProfileDir=FrameLink\nSceneCollection=FrameLink\nSceneCollectionFile=FrameLink\n[OBSWebSocket]\nServerEnabled=false\n")
	writeFixture(t, config, "basic/profiles/FrameLink/basic.ini", "[General]\nName=FrameLink\n[Video]\nBaseCX=1920\nBaseCY=1080\nOutputCX=1920\nOutputCY=1080\nFPSType=0\nFPSCommon=30\n")
	collection := map[string]any{"name": "FrameLink", "current_scene": "Monitor", "current_program_scene": "Monitor", "sources": []any{
		map[string]any{"name": "Camera", "uuid": "e938ce7a-b41b-46b0-b407-2e7f6922ac17", "id": "browser_source", "versioned_id": "browser_source", "settings": map[string]any{"url": sourceURL, "width": 1920, "height": 1080, "fps": 30, "reroute_audio": true, "shutdown": false}},
		map[string]any{"name": "Monitor", "uuid": "d177170d-2a6f-431f-bb97-4f0ebcea78e4", "id": "scene", "versioned_id": "scene", "settings": map[string]any{"items": []any{map[string]any{"name": "Camera", "source_uuid": "e938ce7a-b41b-46b0-b407-2e7f6922ac17", "id": 1, "visible": true, "scale": map[string]int{"x": 1, "y": 1}}}}}},
	}
	data, _ := json.Marshal(collection)
	writeFixture(t, config, "basic/scenes/FrameLink.json", string(data))
	work := filepath.Join(root, "bin", "64bit")
	cmd := process(t, work, filepath.Join(work, "obs64.exe"), "--portable", "--multi", "--disable-updater", "--only-bundled-plugins", "--disable-missing-files-check", "--profile", "FrameLink", "--collection", "FrameLink", "--scene", "Monitor", "--minimize-to-tray")
	return cmd
}

func TestBrowserPipeline(t *testing.T) {
	chromePath := os.Getenv("FRAMELINK_TEST_CHROME")
	if chromePath == "" {
		t.Skip("set FRAMELINK_TEST_CHROME to opt in")
	}
	exe, _ := filepath.Abs("dist/FrameLink.exe")
	dir := t.TempDir()
	httpPort, tlsPort := freePort(t), freePort(t)
	app := process(t, "", exe, "-no-browser", "-no-tray", "-ip", "127.0.0.1", "-data", dir, "-local-port", strconv.Itoa(httpPort), "-port", strconv.Itoa(tlsPort))
	origin := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	var info map[string]string
	until(t, "EXE HTTPS setup", func() bool {
		req, _ := http.NewRequest("GET", origin+"/api/session", nil)
		req.Header.Set("X-FrameLink", "dashboard")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		defer res.Body.Close()
		return json.NewDecoder(res.Body).Decode(&info) == nil && info["camera"] != ""
	})
	upstream, _ := url.Parse(info["camera"])
	fragment := upstream.Fragment
	upstream.Fragment = ""
	upstream.Path = ""
	cert, _ := os.ReadFile(filepath.Join(dir, "ca", "rootCA.pem"))
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert) {
		t.Fatal("test root")
	}
	// Browsers trust HTTP loopback as a secure context. The bridge verifies the
	// actual HTTPS/WSS upstream using its private test CA, including the IP SAN.
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Host = upstream.Host
		if r.Header.Get("Origin") != "" {
			r.Header.Set("Origin", upstream.String())
		}
	}
	bridge := httptest.NewUnstartedServer(nil)
	cameraSockets := make(chan net.Conn, 8)
	bridge.Config.ConnState = func(c net.Conn, state http.ConnState) {
		if state == http.StateHijacked {
			cameraSockets <- c
		}
	}
	host := bridge.Listener.Addr().String()
	bridge.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host || r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+host {
			http.Error(w, "origin", 403)
			return
		}
		proxy.ServeHTTP(w, r)
	})
	bridge.Start()
	defer bridge.Close()
	// CSP must describe the browser-facing origin of this disposable loopback bridge.
	proxy.ModifyResponse = func(r *http.Response) error {
		csp := r.Header.Get("Content-Security-Policy")
		r.Header.Set("Content-Security-Policy", strings.ReplaceAll(csp, "wss://"+upstream.Host, "ws://"+host))
		return nil
	}
	cam, desk := chrome(t, chromePath), chrome(t, chromePath)
	desk.navigate(origin + "/dashboard")
	cam.navigate(bridge.URL + "/camera#" + fragment)
	until(t, "camera authorized", func() bool { return cam.eval("document.querySelector('#stream')?.disabled===false") == "true" })
	cam.eval("document.querySelector('#stream').click()")
	until(t, "capture", func() bool { return cam.eval("document.querySelector('video').videoWidth>0") == "true" })
	var obsPID int
	var browserReceiver *browser
	if install := os.Getenv("FRAMELINK_TEST_OBS"); install != "" {
		obsPID = portableOBS(t, install, info["obs"]).Process.Pid
	} else {
		obs := chrome(t, chromePath)
		browserReceiver = obs
		obsPID = obs.pid
		obs.navigate(info["obs"])
		until(t, "clean video-only output", func() bool {
			return obs.eval("document.body.children.length===1 && document.querySelector('video').videoWidth>0") == "true"
		})
	}
	actual := func() bool {
		return desk.eval("document.querySelector('#statsSource')?.textContent==='Приёмник OBS' && parseInt(document.querySelector('#metric-fps').textContent)>0") == "true"
	}
	until(t, "OBS decoded frames", actual)
	sample := func() string {
		return desk.eval("[document.querySelector('#frameSize').textContent,...Array.from(document.querySelectorAll('.stats strong'),e=>e.textContent)].join(' | ')")
	}
	desk.eval("document.querySelector('#preview').click()")
	until(t, "concurrent preview", func() bool { return desk.eval("document.querySelector('video').videoWidth>0") == "true" && actual() })
	desk.eval("document.querySelector('#preview').click()")
	until(t, "preview closed; OBS live", func() bool {
		return desk.eval("document.querySelector('video').srcObject===null") == "true" && actual()
	})
	if seconds := os.Getenv("FRAMELINK_MEASURE_SECONDS"); seconds != "" {
		until(t, "OBS full-resolution reception", func() bool { return actual() && strings.Contains(sample(), "1080") })
		t.Log("Before measurement (size, fps, Mbps, RTT ms, loss %):", sample())
		output, err := exec.Command("powershell.exe", "-NoProfile", "-File", "tests/measure.ps1", "-ServerId", strconv.Itoa(app.Process.Pid), "-DashboardId", strconv.Itoa(desk.pid), "-ObsId", strconv.Itoa(obsPID), "-Seconds", seconds).CombinedOutput()
		if err != nil {
			t.Fatal(err, string(output))
		}
		t.Log(string(output))
		t.Log("After measurement:", sample())
	}
	if duration := os.Getenv("FRAMELINK_SOAK_SECONDS"); duration != "" {
		seconds, err := strconv.Atoi(duration)
		if err != nil || seconds < 5 {
			t.Fatal("invalid soak duration")
		}
		checked := 0
		end := time.Now().Add(time.Duration(seconds) * time.Second)
		for time.Now().Before(end) {
			time.Sleep(3 * time.Second)
			if !actual() {
				t.Fatalf("OBS metrics stopped: %s", sample())
			}
			checked++
		}
		t.Logf("Continuous OBS reception: %d s, %d checks; last sample %s", seconds, checked, sample())
	}
	desk.eval("const f=document.querySelector('#height');f.value='720';f.dispatchEvent(new Event('change'))")
	until(t, "remote quality change", func() bool {
		return actual() && desk.eval("document.querySelector('#frameSize').textContent.includes('720')") == "true"
	})
	if browserReceiver != nil {
		desk.eval("const a=document.querySelector('#audio');a.click()")
		until(t, "optional audio track", func() bool {
			return browserReceiver.eval("document.querySelector('video').srcObject?.getAudioTracks().length===1") == "true"
		})
		desk.eval("const rotation=document.querySelector('#rotation');rotation.value='90';rotation.dispatchEvent(new Event('change'));")
		until(t, "receiver rotation", func() bool {
			return browserReceiver.eval("document.querySelector('video').style.transform.includes('90deg')") == "true"
		})
		desk.eval("document.querySelector('#mirror').click()")
		until(t, "receiver mirror", func() bool {
			return browserReceiver.eval("document.querySelector('video').style.transform.includes('scaleX(-1)')") == "true"
		})
	}
	desk.eval("document.querySelector('#stream').click()")
	until(t, "remote stop", func() bool { return cam.eval("document.querySelector('video').srcObject===null") == "true" })
	desk.eval("document.querySelector('#stream').click()")
	until(t, "remote restart", actual)
	desk.call("Page.reload", map[string]any{})
	until(t, "dashboard reconnect", actual)
	// Close only the camera WSS tunnel. Safari's equivalent should keep the capture track.
	trackID := cam.eval("document.querySelector('video').srcObject.getVideoTracks()[0].id")
	select {
	case c := <-cameraSockets:
		c.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("camera socket missing")
	}
	until(t, "automatic camera signaling reconnect", func() bool { return len(cameraSockets) > 0 && actual() })
	if cam.eval("document.querySelector('video').srcObject.getVideoTracks()[0].id") != trackID {
		t.Fatal("capture restarted after network loss")
	}
	cam.call("Page.reload", map[string]any{})
	until(t, "camera session restored", func() bool { return cam.eval("document.querySelector('#stream')?.disabled===false") == "true" })
	cam.eval("document.querySelector('#stream').click()")
	until(t, "OBS after camera reconnect", actual)
}
