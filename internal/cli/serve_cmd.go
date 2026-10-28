package cli

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spaghetticoders/truehand"
	"github.com/spaghetticoders/truehand/internal/agents"
	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/claudeplugin"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/gitx"
	"github.com/spaghetticoders/truehand/internal/machines"
	"github.com/spaghetticoders/truehand/internal/serve"
	"github.com/spaghetticoders/truehand/internal/sign"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func init() {
	// A socket connection from an agent session, attached or detached with setsid, is read-only.
	serve.AgentProcess = core.PeerIsAgent
	register("serve", "Serve the Truehand app: projects, boards and agent sessions on this Mac", cmdServe)
	register("peer-proxy", "Connect stdin/stdout to this machine's serve.sock (used by the Truehand Mac over ssh)", cmdPeerProxy)
}

// peerDownExit tells the Mac, through ssh's exit status, that no peer listens yet.
const peerDownExit = 3

func cmdPeerProxy(c *ctx, args []string) error {
	p, err := socketPath()
	if err != nil {
		return err
	}
	conn, err := net.Dial("unix", p)
	if err != nil {
		fmt.Fprintln(c.stderr, "truehand peer is not running")
		return exitError{peerDownExit}
	}
	defer conn.Close()
	// Requests end with the Mac closing stdin; answers end when the server closes its side.
	go func() {
		_, _ = io.Copy(conn, c.stdin)
		if uc, ok := conn.(*net.UnixConn); ok {
			_ = uc.CloseWrite()
		}
	}()
	_, _ = io.Copy(c.stdout, conn)
	return nil
}

// appCredentials is what the Mac app writes on serve's stdin: never in argv or the
// environment, where other processes can read them.
type appCredentials struct {
	Token       string `json:"token"`
	ShellSecret string `json:"shell_secret"`
	// ApproverPublicKey is the app's signing key (PEM), when it already has one.
	ApproverPublicKey string `json:"approver_public_key"`
}

func readAppCredentials(r io.Reader) (appCredentials, error) {
	line, err := bufio.NewReader(io.LimitReader(r, 4096)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return appCredentials{}, err
	}
	var cr appCredentials
	if err := json.Unmarshal([]byte(line), &cr); err != nil {
		return appCredentials{}, errors.New(`--app reads {"token":"…","shell_secret":"…"} as one JSON line on stdin`)
	}
	if len(cr.Token) < 32 || len(cr.ShellSecret) < 32 {
		return appCredentials{}, errors.New("token and shell_secret must be at least 32 characters")
	}
	return cr, nil
}

func cmdServe(c *ctx, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:7878", "loopback address to listen on (reach it from a phone through the tailnet setting in the app)")
	app := fs.Bool("app", false, `started by the Truehand Mac app: credentials as one JSON line on stdin, {"ready":…} as one JSON line on stdout`)
	readOnly := fs.Bool("read-only", false, "serve boards only: no changes, no agent sessions")
	web := fs.String("web", "", "serve the web app from this directory instead of the embedded one")
	peer := fs.Bool("peer", false, "serve this machine to a Truehand Mac over ssh: only on ~/.truehand/app/serve.sock, no network port")
	var repos multiFlag
	fs.Var(&repos, "repo", "register a repo as a project (repeatable)")
	if _, _, err := parse(fs, args); err != nil {
		return err
	}
	if *peer && *app {
		return errors.New("--peer and --app exclude each other")
	}
	// A peer starts in the user's login shell, PATH included; the Mac app's sidecar does not.
	if !*peer {
		completePath()
	}
	inAgent := !e2eBuild && (core.InAgent() || os.Getenv("TRUEHAND_ACTOR") != "")
	// The E2E build lets its tester agents type on the socket, unless the run checks exactly
	// that the socket stays read-only for agents.
	if e2eBuild && os.Getenv("TRUEHAND_E2E_SOCKET_GUARD") == "" {
		serve.AgentProcess = func(int) bool { return false }
	}
	if inAgent && *app {
		return errors.New("refusing to start the app server inside an agent session: the Truehand app starts it")
	}
	if inAgent && !*readOnly {
		fmt.Fprintln(c.stderr, "truehand serve: inside an agent session, serving read-only")
		*readOnly = true
	}
	host, _, err := net.SplitHostPort(*addr)
	if !*peer && (err != nil || host != "127.0.0.1") {
		return errors.New("--addr must be 127.0.0.1:<port>; to reach the app from a phone, turn on tailnet access in its settings")
	}

	dir, err := appstate.DefaultDir()
	if err != nil {
		return err
	}
	store, err := appstate.Open(dir)
	if err != nil {
		return err
	}
	release, err := store.Lock()
	if err != nil {
		return err
	}
	defer release()

	var token, shellHash string
	var macKey *ecdsa.PublicKey
	if *app {
		cr, err := readAppCredentials(c.stdin)
		if err != nil {
			return err
		}
		token, shellHash = cr.Token, appstate.HashSecret(cr.ShellSecret)
		if cr.ApproverPublicKey != "" {
			if macKey, err = sign.ParsePublicKey(cr.ApproverPublicKey); err != nil {
				return fmt.Errorf("approver_public_key: %w", err)
			}
		}
	} else if !*peer {
		token = appstate.NewSecret()
	}
	if token != "" {
		if err := store.SetToken(token); err != nil {
			return err
		}
	}
	for _, r := range repos {
		repo, err := gitx.Open(r)
		if err != nil {
			return fmt.Errorf("%s: %w", r, err)
		}
		if serve.ProjectAllowed != nil {
			if err := serve.ProjectAllowed(repo.Root); err != nil {
				return err
			}
		}
		if _, err := store.AddProject(repo.Root, "", ""); err != nil && !errors.Is(err, appstate.ErrExists) {
			return err
		}
	}
	st, err := store.State()
	if err != nil {
		return err
	}

	home, _ := os.UserHomeDir()
	self, _ := os.Executable()
	if real, err := filepath.EvalSymlinks(self); err == nil {
		self = real
	}
	srv := &serve.Server{
		Store: store, ReadOnly: *readOnly, Version: Version, ShellSecretHash: shellHash,
		TruehandBin: filepath.Dir(self), WorktreesDir: filepath.Join(home, ".truehand", "worktrees"),
		Web: truehand.WebDist(), Peer: *peer, MacKey: macKey,
	}
	if *web != "" {
		if _, err := os.Stat(*web); err != nil {
			return err
		}
		srv.Web = os.DirFS(*web)
	}

	sock, err := listenSocket(store)
	if err != nil {
		return err
	}
	defer sock.Close()
	var listeners []net.Listener
	var ln net.Listener
	if !*peer {
		if ln, err = net.Listen("tcp", *addr); err != nil {
			return err
		}
		listeners = append(listeners, ln)
	}
	_, port, _ := net.SplitHostPort(*addr)
	if ln != nil {
		_, port, _ = net.SplitHostPort(ln.Addr().String())
	}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	remote := &remoteAccess{hs: hs, port: port}
	if !*peer {
		srv.ApplyRemote = remote.apply
		srv.RemoteStatus = remote.status
		srv.TailnetIP = tailnetIP
		if hosts, err := remote.apply(st.Settings.Remote); err != nil {
			fmt.Fprintf(c.stderr, "truehand serve: remote access off: %v\n", err)
		} else {
			srv.SetHosts(hosts)
		}
		defer remote.close()
	}

	if !*readOnly {
		if err := startAgents(c, srv, store, agents.SocketAddr); err != nil {
			fmt.Fprintf(c.stderr, "truehand serve: agent sessions unavailable: %v\n", err)
		}
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	// The Mac app may close the pipe it read the ready line from. Catching SIGPIPE (rather
	// than ignoring it, which agent sessions would inherit) keeps a late write from killing
	// the server.
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)
	stopReq := make(chan struct{}, 1)
	srv.Shutdown = func() {
		select {
		case stopReq <- struct{}{}:
		default:
		}
	}
	defer signal.Stop(sigs)
	if ssh, err := exec.LookPath("ssh"); err == nil && !*peer {
		srv.Remotes = &serve.Remotes{SSH: &machines.SSH{Bin: ssh, ControlDir: filepath.Join(home, ".truehand", "ssh")}, BinaryFor: machines.BinaryFor}
	}
	stop := make(chan struct{})
	defer close(stop)
	go srv.Watch(stop, time.Second)
	go srv.WatchMachines(stop, 15*time.Second)

	switch {
	case *peer:
		fmt.Fprintf(c.stdout, "Truehand peer %s serving %d project(s) on %s\n", Version, len(st.Projects), sock.Addr())
	case *app:
		url := "http://" + ln.Addr().String()
		_ = json.NewEncoder(c.stdout).Encode(map[string]any{"ready": true, "url": url, "pid": os.Getpid()})
	default:
		mode := ""
		if *readOnly {
			mode = " (read-only)"
		}
		fmt.Fprintf(c.stdout, "Truehand serving %d project(s) on http://%s%s\nAccess token (paste it in the app to sign in; keep it to yourself): %s\n", len(st.Projects), ln.Addr(), mode, token)
	}

	hsock := &http.Server{Handler: srv.SocketHandler(), ConnContext: srv.SocketConnContext, ReadHeaderTimeout: 10 * time.Second}
	hs.RegisterOnShutdown(srv.Close)
	hsock.RegisterOnShutdown(srv.Close)
	errs := make(chan error, len(listeners)+1)
	for _, l := range listeners {
		go func(l net.Listener) { errs <- hs.Serve(l) }(l)
	}
	go func() { errs <- hsock.Serve(sock) }()
	select {
	case err = <-errs:
	case <-sigs:
	case <-stopReq:
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if serr := hs.Shutdown(sctx); err == nil {
		err = serr
	}
	if serr := hsock.Shutdown(sctx); err == nil {
		err = serr
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// remoteAccess is the listener on this Mac's Tailscale address, opened and closed as the
// settings change.
type remoteAccess struct {
	mu    sync.Mutex
	hs    *http.Server
	port  string
	ln    net.Listener
	state string // off | listening | error
	err   string
}

func (ra *remoteAccess) status() (string, string) {
	ra.mu.Lock()
	defer ra.mu.Unlock()
	if ra.state == "" {
		return "off", ""
	}
	return ra.state, ra.err
}

func (ra *remoteAccess) set(state, err string) {
	ra.mu.Lock()
	ra.state, ra.err = state, err
	ra.mu.Unlock()
}

func (ra *remoteAccess) close() {
	ra.mu.Lock()
	defer ra.mu.Unlock()
	if ra.ln != nil {
		ra.ln.Close()
		ra.ln = nil
	}
}

// apply switches remote access to rm and returns the Host names it brings: the tailnet
// address with this Mac's MagicDNS names, or the name tailscale serve forwards.
func (ra *remoteAccess) apply(rm appstate.Remote) ([]string, error) {
	ra.close()
	ra.set("off", "")
	if err := serve.ValidRemote(rm); err != nil {
		ra.set("error", err.Error())
		return nil, err
	}
	switch rm.Mode {
	case "tailnet":
		ln, err := net.Listen("tcp", net.JoinHostPort(rm.Addr, ra.port))
		if err != nil {
			ra.set("error", err.Error())
			return nil, err
		}
		ra.mu.Lock()
		ra.ln = ln
		ra.mu.Unlock()
		ra.set("listening", "")
		// Closing it on a settings change ends this Serve; that is not a server failure.
		go ra.hs.Serve(ln)
		return append([]string{rm.Addr}, magicDNSNames()...), nil
	case "tailscale-serve":
		ra.set("listening", "")
		return []string{rm.Addr}, nil
	}
	return nil, nil
}

// toolDirs are where Homebrew, installers and Claude Code put tools. A server started by
// launchd (the Mac app's sidecar) gets only /usr/bin:/bin, which has none of them.
func toolDirs() []string {
	dirs := []string{"/opt/homebrew/bin", "/usr/local/bin"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	return dirs
}

// completePath appends the tool directories missing from PATH, so gh, claude, tailscale
// and the agents' own tools are found however the server was started.
func completePath() {
	have := map[string]bool{}
	path := os.Getenv("PATH")
	for _, d := range filepath.SplitList(path) {
		have[d] = true
	}
	for _, d := range toolDirs() {
		if !have[d] {
			if fi, err := os.Stat(d); err == nil && fi.IsDir() {
				path += string(os.PathListSeparator) + d
			}
		}
	}
	os.Setenv("PATH", path)
}

// tailscaleStatus is replaced in tests.
var tailscaleStatus = func(ctx context.Context) ([]byte, error) {
	bin, err := exec.LookPath("tailscale")
	if err != nil {
		// The app bundle's CLI, when no tailscale is installed on PATH.
		bin = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
	}
	return exec.CommandContext(ctx, bin, "status", "--json", "--self=true", "--peers=false").Output()
}

type tailscaleSelf struct {
	DNSName      string   `json:"DNSName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
}

func tailscaleSelfInfo() (tailscaleSelf, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := tailscaleStatus(ctx)
	if err != nil {
		return tailscaleSelf{}, false
	}
	var st struct {
		Self tailscaleSelf `json:"Self"`
	}
	if json.Unmarshal(out, &st) != nil {
		return tailscaleSelf{}, false
	}
	return st.Self, true
}

// magicDNSNames are this machine's names in the tailnet and nothing else: the full MagicDNS
// name and its first label, which the tailnet's search domain resolves. Not the OS host
// name: when two machines share it, MagicDNS renames one, and the plain name is the other's.
func magicDNSNames() []string {
	self, ok := tailscaleSelfInfo()
	full := strings.TrimSuffix(self.DNSName, ".")
	if !ok || full == "" {
		return nil
	}
	short, _, _ := strings.Cut(full, ".")
	return []string{full, short}
}

// tailnetIP is this Mac's Tailscale IPv4 address, when the settings do not name one: from
// the CLI, else from the network interfaces (Tailscale's range is 100.64.0.0/10).
func tailnetIP() string {
	self, _ := tailscaleSelfInfo()
	for _, ip := range self.TailscaleIPs {
		if p := net.ParseIP(ip); p != nil && p.To4() != nil {
			return ip
		}
	}
	return interfaceTailnetIP(net.InterfaceAddrs)
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func interfaceTailnetIP(addrs func() ([]net.Addr, error)) string {
	list, err := addrs()
	if err != nil {
		return ""
	}
	for _, a := range list {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && cgnat.Contains(n.IP) {
			return n.IP.String()
		}
	}
	return ""
}

// socketPath is the local server's socket, normally ~/.truehand/app/serve.sock.
func socketPath() (string, error) {
	dir, err := appstate.DefaultDir()
	if err != nil {
		return "", err
	}
	return appstate.SocketPath(dir)
}

// listenSocket listens on serve.sock, private to the user. A socket left by a dead server is
// replaced: whoever holds serve.lock owns it.
func listenSocket(store *appstate.Store) (net.Listener, error) {
	p, err := appstate.SocketPath(store.Dir)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", p)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(p, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// startAgents sets up what sessions are launched with: this binary's plugin in its own
// versioned directory, the status hooks settings, and the manager, after settling the
// sessions a previous server left behind.
func startAgents(c *ctx, srv *serve.Server, store *appstate.Store, hookAddr string) error {
	claude, err := exec.LookPath("claude")
	if err != nil {
		return errors.New("Claude Code (claude) is not on PATH")
	}
	versions, err := claudeplugin.VersionsDir()
	if err != nil {
		return err
	}
	pluginDir, err := claudeplugin.ExtractVersioned(versions)
	if err != nil {
		return err
	}
	settings, err := claudeplugin.StatusSettings(filepath.Join(pluginDir, "bin", "truehand"))
	if err != nil {
		return err
	}
	if err := appstate.WriteFileAtomic(store.HooksSettingsPath(), settings); err != nil {
		return err
	}
	mgr, err := agents.NewManager(agents.Config{
		Store: store, Claude: claude, PluginDir: pluginDir, Settings: store.HooksSettingsPath(), HookAddr: hookAddr,
		AllowCwd: registeredCheckout(store), OnChange: srv.SessionChanged,
	})
	if err != nil {
		return err
	}
	if err := mgr.CleanupOrphans(); err != nil {
		fmt.Fprintf(c.stderr, "truehand serve: %v\n", err)
	}
	if inUse, err := store.PluginDirsInUse(); err == nil {
		_, _ = claudeplugin.Prune(versions, pluginDir, inUse)
	}
	srv.Agents = mgr
	return nil
}

// registeredCheckout allows a session only in a registered project's root or one of its
// worktrees.
func registeredCheckout(store *appstate.Store) func(string) bool {
	return func(dir string) bool {
		st, err := store.State()
		if err != nil {
			return false
		}
		for _, p := range st.Projects {
			if sameDir(dir, p.Root) {
				return true
			}
			repo, err := gitx.Open(p.Root)
			if err != nil {
				continue
			}
			wts, _ := core.ListWorktrees(repo)
			for _, wt := range wts {
				if sameDir(dir, wt.Path) {
					return true
				}
			}
		}
		return false
	}
}

func sameDir(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}
