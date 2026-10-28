package machines

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Paths on the remote, relative to its $HOME. Nothing is installed outside ~/.truehand.
const remoteBin = `"$HOME/.truehand/bin/truehand"`

// Info is what a machine has.
type Info struct {
	OS      string          `json:"os"`
	Arch    string          `json:"arch"`
	Tools   map[string]bool `json:"tools"`
	Version string          `json:"version,omitempty"` // of ~/.truehand/bin/truehand, "" if absent
}

func normOS(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func normArch(s string) string {
	switch s = strings.TrimSpace(s); s {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return s
}

// Probe reads os, arch, tools and the installed truehand's version, in the login shell.
func (c *SSH) Probe(ctx context.Context, target string) (Info, error) {
	script := `uname -s; uname -m
for t in claude git gh; do if command -v "$t" >/dev/null 2>&1; then echo "tool $t"; fi; done
if [ -x ` + remoteBin + ` ]; then echo "version $(` + remoteBin + ` version 2>/dev/null)"; fi`
	out, err := c.Run(ctx, target, script, nil)
	if err != nil {
		return Info{}, err
	}
	info, err := parseProbe(out)
	if err != nil {
		return Info{}, fmt.Errorf("%s: %w", target, err)
	}
	return info, nil
}

// parseProbe reads Probe's answer. os and arch come from the remote and later name a local
// file, so only the supported platforms are accepted.
func parseProbe(out []byte) (Info, error) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) < 2 {
		return Info{}, fmt.Errorf("unexpected answer to uname: %q", out)
	}
	info := Info{OS: normOS(lines[0]), Arch: normArch(lines[1]), Tools: map[string]bool{"claude": false, "git": false, "gh": false}}
	if !supported[info.OS] || (info.Arch != "arm64" && info.Arch != "amd64") {
		return Info{}, fmt.Errorf("unsupported platform %q/%q: truehand runs on darwin and linux, arm64 and amd64", lines[0], lines[1])
	}
	for _, l := range lines[2:] {
		if t, ok := strings.CutPrefix(l, "tool "); ok {
			info.Tools[t] = true
		}
		if v, ok := strings.CutPrefix(l, "version "); ok {
			info.Version = strings.TrimSpace(v)
		}
	}
	return info, nil
}

var supported = map[string]bool{"darwin": true, "linux": true}

// BinaryFor finds the truehand to install on os/arch: this binary when they match, else a
// cross-built truehand-<os>-<arch> next to it or in ~/.truehand/dist (`make dist`).
func BinaryFor(goos, arch string) (string, error) {
	// os and arch come from the remote's uname: only known platforms may name a file here.
	if !supported[goos] || (arch != "arm64" && arch != "amd64") {
		return "", fmt.Errorf("unsupported platform %q/%q: truehand runs on darwin and linux, arm64 and amd64", goos, arch)
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(self); err == nil {
		self = real
	}
	if goos == runtime.GOOS && arch == runtime.GOARCH {
		return self, nil
	}
	name := "truehand-" + goos + "-" + arch
	dirs := []string{filepath.Dir(self)}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".truehand", "dist"))
	}
	for _, d := range dirs {
		if fi, err := os.Stat(filepath.Join(d, name)); err == nil && fi.Mode().IsRegular() {
			return filepath.Join(d, name), nil
		}
	}
	return "", fmt.Errorf("no truehand built for %s/%s: run `make dist` and keep %s next to truehand or in ~/.truehand/dist", goos, arch, name)
}

// Install copies binary to ~/.truehand/bin/truehand on target, replacing it in one rename:
// a running peer keeps its old copy until restarted.
func (c *SSH) Install(ctx context.Context, target, binary string) error {
	f, err := os.Open(binary)
	if err != nil {
		return err
	}
	defer f.Close()
	script := `set -e; umask 077; mkdir -p "$HOME/.truehand/bin"
cat > "$HOME/.truehand/bin/.truehand.new"
chmod 755 "$HOME/.truehand/bin/.truehand.new"
mv -f "$HOME/.truehand/bin/.truehand.new" ` + remoteBin
	_, err = c.Run(ctx, target, script, f)
	return err
}

// StartPeer starts `truehand serve --peer` on target, detached, in its login shell so the
// agents it launches find claude. A peer already running keeps the lock and the new one
// exits.
func (c *SSH) StartPeer(ctx context.Context, target string) error {
	script := `umask 077; mkdir -p "$HOME/.truehand/app"
nohup ` + remoteBin + ` serve --peer </dev/null >>"$HOME/.truehand/app/peer.log" 2>&1 &`
	_, err := c.Run(ctx, target, script, nil)
	return err
}

// Peer is an HTTP client of a machine's peer server, through `truehand peer-proxy`.
type Peer struct {
	SSH    *SSH
	Target string
	HTTP   *http.Client
}

// NewPeer reuses connections: each one is a `ssh … peer-proxy` over the shared master.
func NewPeer(c *SSH, target string) *Peer {
	p := &Peer{SSH: c, Target: target}
	p.HTTP = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return c.Conn(ctx, target, "exec "+remoteBin+" peer-proxy")
		},
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}}
	return p
}

// URL is the address of path on the peer; the host is a placeholder.
func URL(path string) string { return "http://peer" + path }

// Version is the peer server's version handshake.
type Version struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

func (p *Peer) Version(ctx context.Context) (Version, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, URL("/api/version"), nil)
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return Version{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Version{}, fmt.Errorf("%s: peer answered %s", p.Target, resp.Status)
	}
	var v Version
	err = json.NewDecoder(resp.Body).Decode(&v)
	return v, err
}

// Ensure makes the peer answer: if nothing listens, it starts one and waits for it.
func (p *Peer) Ensure(ctx context.Context) (Version, error) {
	v, err := p.Version(ctx)
	if err == nil || errors.Is(err, ErrAuth) {
		return v, err
	}
	if err := p.SSH.StartPeer(ctx, p.Target); err != nil {
		return Version{}, err
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		p.HTTP.CloseIdleConnections()
		if v, err = p.Version(ctx); err == nil || time.Now().After(deadline) {
			return v, err
		}
		select {
		case <-ctx.Done():
			return Version{}, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// Stop asks the peer to shut down (it is the same user over the socket).
func (p *Peer) Stop(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, URL("/api/shutdown"), nil)
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	p.HTTP.CloseIdleConnections()
	return nil
}

// Provision installs this truehand on target when its copy is not version (or force), with
// the peer restarted on the new binary, then makes the peer answer.
func Provision(ctx context.Context, p *Peer, version string, binaryFor func(goos, arch string) (string, error), force bool) (Info, Version, error) {
	info, err := p.SSH.Probe(ctx, p.Target)
	if err != nil {
		return info, Version{}, err
	}
	if force || info.Version != version {
		bin, err := binaryFor(info.OS, info.Arch)
		if err != nil {
			return info, Version{}, err
		}
		if err := p.SSH.Install(ctx, p.Target, bin); err != nil {
			return info, Version{}, err
		}
		_ = p.Stop(ctx)
		for i := 0; i < 20; i++ {
			if _, err := p.Version(ctx); err != nil {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		info.Version = version
	}
	v, err := p.Ensure(ctx)
	return info, v, err
}
