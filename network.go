package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed bundle/mkcert.bin
var mkcert string

//go:embed bundle/mkcert.sha256
var mkcertHash string

func chooseIP(explicit string) (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	type address struct {
		ip    string
		score int
	}
	var choices []address
	for _, i := range interfaces {
		if i.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil || ip.To4() == nil {
				continue
			}
			if ip.String() == explicit {
				return explicit, nil
			}
			if !ip.IsPrivate() || ip.IsLoopback() {
				continue
			}
			score := 0
			name := strings.ToLower(i.Name)
			for _, s := range []string{"virtual", "vpn", "vethernet", "vmware", "host-only", "wsl", "tunnel", "radmin"} {
				if strings.Contains(name, s) {
					score += 10
				}
			}
			if strings.HasPrefix(ip.String(), "192.168.") {
				score--
			}
			choices = append(choices, address{ip.String(), score})
		}
	}
	if explicit != "" {
		return "", fmt.Errorf("IPv4 %s is not assigned to an active adapter", explicit)
	}
	if len(choices) == 0 {
		return "", errors.New("no private LAN IPv4 address; connect the PC to a LAN")
	}
	// Consult the routing table without writing any network packet.
	if c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 9}); err == nil {
		preferred := c.LocalAddr().(*net.UDPAddr).IP.String()
		c.Close()
		for k := range choices {
			if choices[k].ip == preferred {
				choices[k].score -= 3
			}
		}
	}
	sort.SliceStable(choices, func(i, j int) bool { return choices[i].score < choices[j].score })
	return choices[0].ip, nil
}

func certificate(dir, ip string) (tls.Certificate, error) {
	var empty tls.Certificate
	caDir := filepath.Join(dir, "ca")
	if err := os.MkdirAll(caDir, 0700); err != nil {
		return empty, err
	}
	certPath := filepath.Join(dir, "server.pem")
	keyPath := filepath.Join(dir, "server-key.pem")
	hostname, _ := os.Hostname()
	names := []string{ip, "localhost", "127.0.0.1", "::1"}
	if hostname != "" {
		names = append(names, hostname)
	}
	caPath := filepath.Join(caDir, "rootCA.pem")
	load := func() (tls.Certificate, error) {
		pair, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return empty, err
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return empty, err
		}
		rootPEM, err := os.ReadFile(caPath)
		if err != nil {
			return empty, err
		}
		block, _ := pem.Decode(rootPEM)
		if block == nil {
			return empty, errors.New("invalid CA")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(rootPEM) {
			return empty, errors.New("invalid CA")
		}
		if _, err = leaf.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: time.Now().Add(48 * time.Hour)}); err != nil {
			return empty, err
		}
		for _, name := range names {
			if err = leaf.VerifyHostname(name); err != nil {
				return empty, err
			}
		}
		public := filepath.Join(dir, "FrameLink-CA.cer")
		if current, _ := os.ReadFile(public); !bytes.Equal(current, block.Bytes) {
			if err = os.WriteFile(public, block.Bytes, 0600); err != nil {
				return empty, err
			}
		}
		pair.Leaf = leaf
		return pair, nil
	}
	if pair, err := load(); err == nil {
		return pair, nil
	}
	// Extract only for issuance. Hash the same bytes that will be executed.
	f, err := os.CreateTemp(dir, "mkcert-*.exe")
	if err != nil {
		return empty, err
	}
	toolPath := f.Name()
	defer os.Remove(toolPath)
	digest := sha256.New()
	reader := struct{ io.Reader }{strings.NewReader(mkcert)}
	_, err = io.Copy(io.MultiWriter(f, digest), reader)
	closeErr := f.Close()
	if err != nil {
		return empty, err
	}
	if closeErr != nil {
		return empty, closeErr
	}
	if fmt.Sprintf("%x", digest.Sum(nil)) != strings.TrimSpace(mkcertHash) {
		return empty, errors.New("bundled mkcert checksum mismatch")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	args := append([]string{"-ecdsa", "-cert-file", certPath, "-key-file", keyPath}, names...)
	cmd := exec.CommandContext(ctx, toolPath, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CAROOT="+caDir, "TRUST_STORES=system")
	if out, err := cmd.CombinedOutput(); err != nil {
		return empty, fmt.Errorf("mkcert: %w: %s", err, out)
	}
	return load()
}
