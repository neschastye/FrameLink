package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"time"
)

func main() {
	ip := flag.String("ip", "", "LAN adapter IPv4 (automatic by default)")
	port := flag.Int("port", 8443, "LAN HTTPS port")
	localPort := flag.Int("local-port", 8765, "loopback dashboard and OBS port")
	dir := flag.String("data", "", "private configuration directory")
	noBrowser := flag.Bool("no-browser", false, "do not open the dashboard")
	noTray := flag.Bool("no-tray", false, "disable the Windows notification icon")
	notices := flag.Bool("licenses", false, "print bundled license notices")
	flag.Parse()
	if *notices {
		for _, name := range []string{"LICENSE", "licenses/THIRD_PARTY.txt"} {
			b, _ := assets.ReadFile(name)
			fmt.Println(string(b))
		}
		return
	}
	if *ip != "" && net.ParseIP(*ip).To4() == nil {
		log.Fatal("expected an IPv4 address")
	}
	if *port < 1 || *port > 65535 || *localPort < 1 || *localPort > 65535 {
		log.Fatal("invalid port")
	}
	if *dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			log.Fatal(err)
		}
		*dir = filepath.Join(base, "FrameLink")
	}
	var err error
	*dir, err = filepath.Abs(*dir)
	if err != nil {
		log.Fatal(err)
	}
	if err = os.MkdirAll(*dir, 0700); err != nil {
		log.Fatal(err)
	}
	listener, err := newLocal(*localPort)
	if err != nil {
		log.Fatal("dashboard: ", err)
	}
	obsKey, err := obsSecret(*dir)
	if err != nil {
		log.Fatal(err)
	}
	a := &app{hub: newHub(), deskKey: token(), camKey: token(), obsKey: obsKey, localHost: listener.Addr().String(), dir: *dir, lanError: "Подготовка HTTPS…"}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	local := httpServer(a.handler(true, a.localHost))
	go func() {
		if err := local.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Print(err)
			stop()
		}
	}()
	url := "http://" + a.localHost + "/dashboard"
	fmt.Printf("FrameLink\nDashboard: %s\nPublic iPhone CA: %s\nCtrl+C to exit.\n", url, filepath.Join(*dir, "FrameLink-CA.cer"))
	open := func() {
		go func() {
			if err := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url).Run(); err != nil {
				log.Print(err)
			}
		}()
	}
	if !*noBrowser {
		open()
	}
	if !*noTray {
		go runTray(ctx, open, stop)
	}
	a.serveLAN(ctx, *ip, *port)
	local.Close()
}

func (a *app) serveLAN(ctx context.Context, explicit string, port int) {
	var server *http.Server
	active := ""
	var renewal time.Time
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	update := func(origin, problem string) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if problem != "" && a.lanError != problem {
			log.Print(problem)
		}
		a.camOrigin = origin
		a.lanError = problem
	}
	defer func() {
		if server != nil {
			server.Close()
		}
	}()
	for {
		ip, err := chooseIP(explicit)
		if err != nil || active != ip || time.Now().After(renewal) {
			if server != nil {
				server.Close()
				server = nil
				// Upgraded WebSockets are independent of the HTTP listener.
				a.hub.mu.Lock()
				if c := a.hub.clients["camera"]; c != nil {
					c.cancel()
				}
				a.hub.mu.Unlock()
			}
			active = ""
			if err == nil {
				var cert tls.Certificate
				cert, err = certificate(a.dir, ip)
				if err == nil {
					host := net.JoinHostPort(ip, strconv.Itoa(port))
					var ln net.Listener
					ln, err = net.Listen("tcp4", host)
					if err == nil {
						server = httpServer(a.handler(false, host))
						tlsListener := tls.NewListener(ln, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})
						go func(s *http.Server) {
							if err := s.Serve(tlsListener); err != nil && err != http.ErrServerClosed {
								log.Print(err)
							}
						}(server)
						active = ip
						renewal = cert.Leaf.NotAfter.Add(-48 * time.Hour)
						update("https://"+host, "")
						log.Printf("Camera listening on https://%s", host)
					}
				}
			}
			if err != nil {
				update("", err.Error())
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
