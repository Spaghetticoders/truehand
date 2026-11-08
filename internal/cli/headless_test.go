package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/appstate/webauthntest"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/humanauth"
	"github.com/spaghetticoders/truehand/internal/serve"
)

type noStdin struct{ t *testing.T }

func (r noStdin) Read([]byte) (int, error) {
	r.t.Error("serve --headless read stdin")
	return 0, io.EOF
}

func TestServeHeadless(t *testing.T) {
	s := newSandbox(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	appDir := filepath.Join(home, ".truehand", "app")
	t.Cleanup(func() {
		if p, err := appstate.SocketPath(appDir); err == nil && strings.HasPrefix(p, "/tmp/") {
			os.RemoveAll(filepath.Dir(p))
		}
	})
	// This test may itself run inside an agent, whose socket connections are read-only.
	oldAgent := serve.AgentProcess
	serve.AgentProcess = func(int) bool { return false }
	defer func() { serve.AgentProcess = oldAgent }()
	old := humanauth.Authenticate
	humanauth.Authenticate = func(string) error { t.Error("headless serve asked the OS"); return nil }
	defer func() { humanauth.Authenticate = old }()

	for _, bad := range [][]string{{"--headless", "--app"}, {"--headless", "--peer"}} {
		if code, out := s.run("", append([]string{"serve"}, bad...)...); code == 0 || !strings.Contains(out, "excludes") {
			t.Fatalf("serve %v: %d %s", bad, code, out)
		}
	}

	pr, pw := io.Pipe()
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runIn(s.dir, []string{"serve", "--headless", "--addr", "127.0.0.1:0", "--repo", s.dir}, noStdin{t}, pw, &stderr)
		pw.Close()
	}()
	line, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil {
		t.Fatalf("no start line: %v\n%s", err, stderr.String())
	}
	go io.Copy(io.Discard, pr)
	if !strings.Contains(line, "headless") || !strings.Contains(line, "passkey bootstrap") {
		t.Fatalf("start line %q", line)
	}
	if regexp.MustCompile(`[0-9a-f]{32}`).MatchString(line + stderr.String()) {
		t.Fatalf("a secret was printed: %q %q", line, stderr.String())
	}
	b, _ := os.ReadFile(filepath.Join(appDir, "auth.json"))
	if !strings.Contains(string(b), "token_sha256") {
		t.Fatalf("no token hash stored: %s", b)
	}
	addr := regexp.MustCompile(`http://(127\.0\.0\.1:\d+)`).FindStringSubmatch(line)
	if addr == nil {
		t.Fatalf("no address in %q", line)
	}
	if resp, err := http.Get("http://" + addr[1] + "/api/me"); err != nil || resp.StatusCode != 401 {
		t.Fatalf("anonymous /api/me: %v %v", resp, err)
	}

	sock, _ := appstate.SocketPath(appDir)
	c := http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	req, _ := http.NewRequest("POST", "http://truehand/api/shutdown", nil)
	if resp, err := c.Do(req); err != nil || resp.StatusCode != 204 {
		t.Fatalf("shutdown over the socket: %v %v", resp, err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serve exited %d: %s", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop")
	}
}

type ttyBuf struct{ bytes.Buffer }

func (*ttyBuf) Close() error { return nil }

func testBootstrapEnv(t *testing.T) (*bootstrapEnv, *ttyBuf, *user.User) {
	dir := t.TempDir()
	u := &user.User{Uid: "501", Gid: "20", Username: "ema", HomeDir: filepath.Join(dir, "home")}
	tty := &ttyBuf{}
	env := &bootstrapEnv{
		euid: 0, sudoUID: "501", sudoUser: "ema",
		tty:       func() (io.WriteCloser, error) { return tty, nil },
		sudoNoPwd: func(*user.User) bool { return false },
		lookup:    func(string) (*user.User, error) { return u, nil },
		anchor:    filepath.Join(dir, "etc", "truehand", "anchor.json"),
		writeFile: func(p string, b []byte) error {
			os.MkdirAll(filepath.Dir(p), 0o755)
			return os.WriteFile(p, b, 0o644)
		},
		now: time.Now, sleep: func(time.Duration) {}, host: func() string { return "box.tail1234.ts.net" }, ttl: time.Minute,
		yes: true,
	}
	return env, tty, u
}

func TestBootstrapRefuses(t *testing.T) {
	var out bytes.Buffer
	c := &ctx{stdout: &out, stderr: &out}
	cases := map[string]func(*bootstrapEnv){
		"not root":          func(e *bootstrapEnv) { e.euid = 501 },
		"root without sudo": func(e *bootstrapEnv) { e.sudoUID = "" },
		"sudo from root":    func(e *bootstrapEnv) { e.sudoUID = "0" },
		"passwordless sudo": func(e *bootstrapEnv) { e.sudoNoPwd = func(*user.User) bool { return true } },
		"no terminal":       func(e *bootstrapEnv) { e.tty = func() (io.WriteCloser, error) { return nil, errors.New("no tty") } },
	}
	for name, mut := range cases {
		env, _, _ := testBootstrapEnv(t)
		mut(env)
		if err := bootstrap(c, *env); err == nil {
			t.Errorf("%s: bootstrap ran", name)
		}
		if _, err := os.Stat(env.anchor); err == nil {
			t.Errorf("%s: anchor written", name)
		}
	}
}

func TestBootstrapAnchorsThePasskeyWithTheCode(t *testing.T) {
	env, tty, u := testBootstrapEnv(t)
	var out bytes.Buffer
	c := &ctx{stdout: &out, stderr: &out}
	uid := os.Getuid()
	env.sudoUID = "501"
	u.Uid = strconv.Itoa(uid)
	pkPath := filepath.Join(u.HomeDir, ".truehand", "app", "passkeys.json")
	os.MkdirAll(filepath.Dir(pkPath), 0o700)

	// A forged entry (no code) and the real one, registered with the code shown on the tty.
	real := webauthntest.NewES256("box.tail1234.ts.net")
	forged := webauthntest.NewES256("box.tail1234.ts.net")
	polls := 0
	env.sleep = func(time.Duration) {
		polls++
		if polls != 2 {
			return
		}
		code := strings.ReplaceAll(regexp.MustCompile(`[A-Z2-9]{4}-[A-Z2-9]{4}-[A-Z2-9]{4}-[A-Z2-9]{4}`).FindString(tty.String()), "-", "")
		if len(code) != 16 {
			t.Fatalf("no code on the tty: %q", tty.String())
		}
		b, _ := os.ReadFile(env.anchor)
		var a appstate.Anchor
		json.Unmarshal(b, &a)
		if a.CodeSHA256 != appstate.HashSecret(code) || a.UID != uid || a.Owner != nil {
			t.Fatalf("pending anchor = %s", b)
		}
		f := appstate.Passkey{ID: forged.CredentialID(), PublicKey: forged.PublicKeyPEM(), RPID: forged.RPID, Owner: true, Grant: &appstate.Grant{Kind: appstate.GrantAnchor, CodeProof: strings.Repeat("0", 64)}}
		p := appstate.Passkey{ID: real.CredentialID(), PublicKey: real.PublicKeyPEM(), RPID: real.RPID, Owner: true, Label: "S25"}
		p.Grant = &appstate.Grant{Kind: appstate.GrantAnchor, CodeProof: appstate.CodeProof(code, &p)}
		j, _ := json.Marshal(appstate.Passkeys{Passkeys: []appstate.Passkey{f, p}})
		os.WriteFile(pkPath, j, 0o600)
	}
	if err := bootstrap(c, *env); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "-") && regexp.MustCompile(`[A-Z2-9]{4}-[A-Z2-9]{4}`).MatchString(out.String()) {
		t.Fatalf("the code reached stdout: %q", out.String())
	}
	b, _ := os.ReadFile(env.anchor)
	var a appstate.Anchor
	json.Unmarshal(b, &a)
	if a.CodeSHA256 != "" || a.Owner == nil || a.Owner.Credential != real.CredentialID() || a.Owner.PublicKeySHA256 != appstate.PublicKeySHA256(real.PublicKeyPEM()) {
		t.Fatalf("anchor = %s", b)
	}
}

func TestBootstrapTimesOutAndKeepsTheOldOwner(t *testing.T) {
	env, _, u := testBootstrapEnv(t)
	u.Uid = strconv.Itoa(os.Getuid())
	prev := appstate.Anchor{Version: 1, UID: os.Getuid(), Owner: &appstate.AnchorOwner{Credential: "old", PublicKeySHA256: strings.Repeat("a", 64), RPID: "x.ts.net"}}
	b, _ := json.Marshal(prev)
	env.writeFile(env.anchor, b)
	start := time.Now()
	n := 0
	env.now = func() time.Time { n++; return start.Add(time.Duration(n) * 20 * time.Second) }
	var out bytes.Buffer
	if err := bootstrap(&ctx{stdout: &out, stderr: &out}, *env); err == nil {
		t.Fatal("bootstrap without a passkey succeeded")
	}
	b, _ = os.ReadFile(env.anchor)
	var a appstate.Anchor
	json.Unmarshal(b, &a)
	if a.CodeSHA256 != "" || a.Owner == nil || a.Owner.Credential != "old" {
		t.Fatalf("anchor after timeout = %s", b)
	}
}

func TestLinuxInstallPlan(t *testing.T) {
	p := linuxInstallPlan("/home/ema/bin/true hand", "/home/ema", "/bin/bash", "ema", 7878)
	if p.UnitPath != "/home/ema/.config/systemd/user/truehand.service" {
		t.Fatal(p.UnitPath)
	}
	for _, want := range []string{
		`ExecStart=/bin/bash -lc 'exec '\''/home/ema/bin/true hand'\'' serve --headless --addr 127.0.0.1:7878'`,
		"NoNewPrivileges=yes", "UMask=0077", "WantedBy=default.target", "Restart=on-failure",
	} {
		if !strings.Contains(p.Unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, p.Unit)
		}
	}
	got := []string{}
	for _, c := range p.Commands {
		got = append(got, strings.Join(c, " "))
	}
	want := []string{"systemctl --user daemon-reload", "systemctl --user enable --now truehand.service", "loginctl enable-linger ema", "tailscale serve --bg --https=443 http://127.0.0.1:7878"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands:\n%s", strings.Join(got, "\n"))
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USER", "ema")
	old := tailscaleNeedsOperator
	defer func() { tailscaleNeedsOperator = old }()
	for _, needs := range []bool{false, true} {
		tailscaleNeedsOperator = func() bool { return needs }
		var out bytes.Buffer
		if code := runIn(t.TempDir(), []string{"linux", "install", "--dry-run"}, nil, &out, &out); code != 0 {
			t.Fatalf("dry run: %d %s", code, out.String())
		}
		o := out.String()
		stable := filepath.Join(home, ".truehand", "bin", "truehand")
		exec := regexp.MustCompile(`(?m)^ExecStart=.*$`).FindString(o)
		if !strings.Contains(exec, stable) || !strings.Contains(exec, "serve --headless") || !strings.Contains(o, "# copy ") {
			t.Fatalf("dry run does not run the stable copy:\n%s", o)
		}
		hint := strings.Index(o, "sudo tailscale set --operator=ema")
		if needs != (hint >= 0) || needs && hint > strings.Index(o, "[Unit]") {
			t.Fatalf("operator hint (needed %v):\n%s", needs, o)
		}
	}
}

func TestInstallBinaryIsAtomicAndExecutable(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	os.WriteFile(src, []byte("new"), 0o600)
	dst := filepath.Join(dir, "home", ".truehand", "bin", "truehand")
	os.MkdirAll(filepath.Dir(dst), 0o755)
	os.WriteFile(dst, []byte("old"), 0o755)
	if err := installBinary(src, dst); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dst)
	fi, _ := os.Stat(dst)
	if string(b) != "new" || fi.Mode().Perm() != 0o755 {
		t.Fatalf("installed %q %v", b, fi.Mode())
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(dst), ".truehand.*"))
	if len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestNvmBin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("NVM_BIN", "")
	for _, v := range []string{"v18.20.0", "v22.3.1", "v22.11.0", "vfoo"} {
		os.MkdirAll(filepath.Join(home, ".nvm", "versions", "node", v, "bin"), 0o755)
	}
	if got := nvmBin(home); got != filepath.Join(home, ".nvm", "versions", "node", "v22.11.0", "bin") {
		t.Fatalf("nvmBin = %s", got)
	}
	t.Setenv("HOME", home)
	dirs := strings.Join(toolDirs(), ":")
	if !strings.Contains(dirs, "v22.11.0") || !strings.Contains(dirs, ".npm-global/bin") || !strings.Contains(dirs, ".local/bin") {
		t.Fatalf("toolDirs = %s", dirs)
	}
}

func TestPasskeyRevokeWritesRootsList(t *testing.T) {
	env, _, u := testBootstrapEnv(t)
	u.Uid = strconv.Itoa(os.Getuid())
	a := webauthntest.NewES256("box.tail1234.ts.net")
	p := appstate.Passkey{ID: a.CredentialID(), PublicKey: a.PublicKeyPEM(), RPID: a.RPID, Owner: true, Label: "S25"}
	pk := filepath.Join(u.HomeDir, ".truehand", "app", "passkeys.json")
	os.MkdirAll(filepath.Dir(pk), 0o700)
	j, _ := json.Marshal(appstate.Passkeys{Passkeys: []appstate.Passkey{p}})
	os.WriteFile(pk, j, 0o600)
	var out bytes.Buffer
	c := &ctx{stdout: &out, stderr: &out}
	notRoot := *env
	notRoot.euid = 501
	if err := revoke(c, notRoot, p.ID); err == nil {
		t.Fatal("revoke without root")
	}
	if err := revoke(c, *env, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := revoke(c, *env, "unknown-id"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(env.anchor), "revoked.json"))
	var l appstate.RevokedList
	json.Unmarshal(b, &l)
	if l.Version != 1 || l.UID != os.Getuid() || len(l.Revoked) != 2 || l.Revoked[0].PublicKeySHA256 != appstate.PublicKeySHA256(p.PublicKey) || l.Revoked[1].Credential != "unknown-id" {
		t.Fatalf("revoked.json = %s", b)
	}
	// What the server reads.
	old, oldOK := appstate.AnchorPath, appstate.AnchorFileOK
	defer func() { appstate.AnchorPath, appstate.AnchorFileOK = old, oldOK }()
	appstate.AnchorPath, appstate.AnchorFileOK = env.anchor, func(os.FileInfo) error { return nil }
	got, err := appstate.ReadRevoked(os.Getuid())
	if err != nil || len(got) != 2 {
		t.Fatalf("ReadRevoked = %v %v", got, err)
	}
	// The real check refuses a list the user could have written.
	appstate.AnchorFileOK = oldOK
	if os.Getuid() != 0 {
		if _, err := appstate.ReadRevoked(os.Getuid()); err == nil {
			t.Fatal("a user-owned revocation list was trusted")
		}
	}
}

// The repo's pre-push hook runs `truehand`: with an old one on PATH, a server binary named
// otherwise must still be the one that runs.
func TestHookBinDirWinsOverAnOldTruehand(t *testing.T) {
	dir := t.TempDir()
	run := func(d string, env []string, args ...string) error {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir, cmd.Env = d, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("%v: %s", args, out)
		}
		return err
	}
	base := []string{"HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@b", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@b"}
	remote, repo := filepath.Join(dir, "remote.git"), filepath.Join(dir, "repo")
	os.MkdirAll(repo, 0o755)
	sys := "PATH=/usr/bin:/bin"
	run(dir, append(base, sys), "git", "init", "-q", "--bare", remote)
	run(repo, append(base, sys), "git", "init", "-q", "-b", "main")
	run(repo, append(base, sys), "git", "commit", "-q", "--allow-empty", "-m", "x")
	run(repo, append(base, sys), "git", "remote", "add", "origin", remote)
	os.WriteFile(filepath.Join(repo, ".git", "hooks", "pre-push"), []byte(core.GitHooks["pre-push"]), 0o755)

	old := filepath.Join(dir, "old")
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(old, "truehand"), []byte("#!/bin/sh\necho 'old truehand: field approvers not found' >&2\nexit 1\n"), 0o755)
	self := filepath.Join(dir, "build", "truehand-e2e")
	os.MkdirAll(filepath.Dir(self), 0o755)
	os.WriteFile(self, []byte("#!/bin/sh\n[ \"$1 $2\" = \"hook pre-push\" ] && exit 0\nexit 1\n"), 0o755)

	if err := run(repo, append(base, "PATH="+old+":/usr/bin:/bin"), "git", "push", "-q", "origin", "main"); err == nil {
		t.Fatal("control: the old truehand did not refuse")
	}
	var stderr bytes.Buffer
	bin := hookBinDir(self, filepath.Join(dir, "app", "bin"), &stderr)
	if err := run(repo, append(base, "PATH="+bin+":"+old+":/usr/bin:/bin"), "git", "push", "-q", "origin", "main"); err != nil {
		t.Fatalf("push with the server's own truehand first: %v (%s)", err, stderr.String())
	}
	// A binary already named truehand is used where it is.
	if got := hookBinDir(filepath.Join(old, "truehand"), filepath.Join(dir, "app", "bin"), &stderr); got != old {
		t.Fatalf("hookBinDir = %s", got)
	}
}

func TestAppLineNeedsTheRevocations(t *testing.T) {
	tok, sec := strings.Repeat("t", 40), strings.Repeat("s", 40)
	if _, err := readAppCredentials(strings.NewReader(`{"token":"` + tok + `","shell_secret":"` + sec + `"}` + "\n")); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("a line without revoked = %v", err)
	}
	if _, err := readAppCredentials(strings.NewReader(`{"token":"` + tok + `","shell_secret":"` + sec + `","revoked":"x"}` + "\n")); err == nil {
		t.Fatal("an unreadable revoked list accepted")
	}
	cr, err := readAppCredentials(strings.NewReader(`{"token":"` + tok + `","shell_secret":"` + sec + `","revoked":[]}` + "\n"))
	if err != nil || cr.Revoked == nil || len(cr.Revoked) != 0 {
		t.Fatalf("empty list = %+v %v", cr.Revoked, err)
	}
	long := `{"token":"` + tok + `","shell_secret":"` + sec + `","revoked":[],"pad":"` + strings.Repeat("x", appLineMax) + `"}` + "\n"
	if _, err := readAppCredentials(strings.NewReader(long)); err == nil || !strings.Contains(err.Error(), "longer") {
		t.Fatalf("a line over the limit = %v", err)
	}
	// serve --app refuses to start without it.
	var out bytes.Buffer
	if code := runIn(t.TempDir(), []string{"serve", "--app", "--addr", "127.0.0.1:0"}, strings.NewReader(`{"token":"`+tok+`","shell_secret":"`+sec+`"}`+"\n"), &out, &out); code == 0 || !strings.Contains(out.String(), "revoked") {
		t.Fatalf("serve --app without revoked: %d %s", code, out.String())
	}
}

func TestOperatorFromPrefs(t *testing.T) {
	prefs := func(b string, err error) func() ([]byte, error) {
		return func() ([]byte, error) { return []byte(b), err }
	}
	cases := []struct {
		name string
		p    func() ([]byte, error)
		want bool
	}{
		{"no operator (field omitted)", prefs(`{"ControlURL":"https://controlplane.tailscale.com","RouteAll":false}`, nil), true},
		{"another operator", prefs(`{"OperatorUser":"root2"}`, nil), true},
		{"this user", prefs(`{"OperatorUser":"ema"}`, nil), false},
		{"prefs unavailable", prefs("", errors.New("unknown command")), false},
		{"not JSON", prefs("Usage: tailscale debug", nil), false},
	}
	for _, c := range cases {
		if got := operatorMissing(c.p, "ema"); got != c.want {
			t.Errorf("%s: operatorMissing = %v", c.name, got)
		}
	}
}

// The app's revocations reach the server through stdin: listed from the first request, and
// the server is not waiting for them (owners on the Mac depend on it).
func TestServeAppTakesTheRevocationsFromStdin(t *testing.T) {
	s := newSandbox(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Cleanup(func() {
		if p, err := appstate.SocketPath(filepath.Join(home, ".truehand", "app")); err == nil && strings.HasPrefix(p, "/tmp/") {
			os.RemoveAll(filepath.Dir(p))
		}
	})
	tok, sec := strings.Repeat("t", 40), strings.Repeat("s", 40)
	line := `{"token":"` + tok + `","shell_secret":"` + sec + `","revoked":[{"credential":"lost-phone","public_key_sha256":"` + strings.Repeat("ab", 32) + `","label":"S25","time":"2026-11-08T10:00:00Z"}]}` + "\n"
	pr, pw := io.Pipe()
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runIn(s.dir, []string{"serve", "--app", "--addr", "127.0.0.1:0"}, strings.NewReader(line), pw, &stderr)
		pw.Close()
	}()
	ready, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil {
		t.Fatalf("no ready line: %v %s", err, stderr.String())
	}
	go io.Copy(io.Discard, pr)
	var r struct {
		URL string `json:"url"`
	}
	json.Unmarshal([]byte(ready), &r)
	get := func() map[string]any {
		req, _ := http.NewRequest("GET", r.URL+"/api/passkeys", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var v map[string]any
		json.NewDecoder(resp.Body).Decode(&v)
		return v
	}
	v := get()
	revs, _ := v["revocations"].([]any)
	if v["revocationsError"] != nil || len(revs) != 1 || revs[0].(map[string]any)["id"] != "lost-phone" || revs[0].(map[string]any)["revoked"] != "durable" {
		t.Fatalf("passkeys after stdin = %v", v)
	}
	req, _ := http.NewRequest(http.MethodPost, r.URL+"/api/shutdown", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Truehand-Shell", sec)
	http.DefaultClient.Do(req)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func TestRevokeListsWhatTheBackupHad(t *testing.T) {
	env, _, u := testBootstrapEnv(t)
	u.Uid = strconv.Itoa(os.Getuid())
	path := filepath.Join(filepath.Dir(env.anchor), "revoked.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"version":1,"uid":`), 0o644)
	var out bytes.Buffer
	c := &ctx{stdout: &out, stderr: &out}
	if err := revoke(c, *env, "new-one"); err == nil || !strings.Contains(err.Error(), "sudo truehand passkey revoke --repair") || !strings.Contains(err.Error(), "sudo mv "+path+" "+path+".bak") {
		t.Fatalf("revoke over a broken list = %v", err)
	}
	// Broken (truncated), but the ids it names are still readable.
	os.WriteFile(path+".bak", []byte(`{"version":1,"uid":501,"revoked":[{"credential":"lost-a","time":"x"},{"credential":"lost-b"`), 0o644)
	os.Remove(path)
	out.Reset()
	if err := revoke(c, *env, "lost-a"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "sudo truehand passkey revoke lost-b") || strings.Contains(out.String(), "revoke lost-a\n") {
		t.Fatalf("output = %s", out.String())
	}
}

func TestRevokeRepair(t *testing.T) {
	env, _, u := testBootstrapEnv(t)
	u.Uid = strconv.Itoa(os.Getuid())
	path := filepath.Join(filepath.Dir(env.anchor), "revoked.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	sha := strings.Repeat("ab", 32)
	broken := `{"version":1,"uid":` + u.Uid + `,"revoked":[{"credential":"lost-a","public_key_sha256":"` + sha + `","time":"x"},{"credential":"lost-b"`
	os.WriteFile(path, []byte(broken), 0o666)
	var writes []string
	inner := env.writeFile
	env.writeFile = func(p string, b []byte) error { writes = append(writes, filepath.Base(p)); return inner(p, b) }
	var out bytes.Buffer
	c := &ctx{stdout: &out, stderr: &out}
	if err := repairRevoked(c, *env); err != nil {
		t.Fatal(err)
	}
	// The new list replaces the old in one write: never a moment without it.
	if strings.Join(writes, ",") != "revoked.json.bak,revoked.json" {
		t.Fatalf("writes = %v", writes)
	}
	bak, _ := os.ReadFile(path + ".bak")
	if string(bak) != broken {
		t.Fatal("the .bak is not the old file")
	}
	if fi, _ := os.Stat(path + ".bak"); fi.Mode().Perm() != 0o600 {
		t.Fatalf(".bak mode = %v, want 0600", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	var l appstate.RevokedList
	if err := json.Unmarshal(b, &l); err != nil || l.Version != 1 || l.UID != os.Getuid() || len(l.Revoked) != 2 ||
		l.Revoked[0].Credential != "lost-a" || l.Revoked[0].PublicKeySHA256 != sha || l.Revoked[1].Credential != "lost-b" {
		t.Fatalf("repaired list = %s (%v)", b, err)
	}
	// What the server reads now holds both.
	old, oldOK := appstate.AnchorPath, appstate.AnchorFileOK
	defer func() { appstate.AnchorPath, appstate.AnchorFileOK = old, oldOK }()
	appstate.AnchorPath, appstate.AnchorFileOK = env.anchor, func(os.FileInfo) error { return nil }
	if got, err := appstate.ReadRevoked(os.Getuid()); err != nil || len(got) != 2 {
		t.Fatalf("server reads %v %v", got, err)
	}
	// Someone else's valid list is left alone.
	os.WriteFile(path, []byte(`{"version":1,"uid":4242,"revoked":[]}`), 0o644)
	if err := repairRevoked(c, *env); err == nil {
		t.Fatal("another user's list rewritten")
	}
	// Not root: refused.
	notRoot := *env
	notRoot.euid = 501
	if err := repairRevoked(c, notRoot); err == nil {
		t.Fatal("repair without root")
	}
	// The command line: --repair or an id, not both.
	if code := runIn(t.TempDir(), []string{"passkey", "revoke", "--repair", "x"}, nil, &out, &out); code == 0 {
		t.Fatal("--repair with an id accepted")
	}
}

func TestRevokeRefusesALinkedList(t *testing.T) {
	env, _, u := testBootstrapEnv(t)
	u.Uid = strconv.Itoa(os.Getuid())
	path := filepath.Join(filepath.Dir(env.anchor), "revoked.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	userFile := filepath.Join(u.HomeDir, "mine.json")
	os.MkdirAll(u.HomeDir, 0o755)
	os.WriteFile(userFile, []byte(`{"version":1,"uid":`+u.Uid+`,"revoked":[]}`), 0o644)
	os.Symlink(userFile, path)
	var out bytes.Buffer
	c := &ctx{stdout: &out, stderr: &out}
	if err := revoke(c, *env, "x"); err == nil || !strings.Contains(err.Error(), "is a link") {
		t.Fatalf("revoke through a link = %v", err)
	}
	if err := repairRevoked(c, *env); err == nil || !strings.Contains(err.Error(), "is a link") {
		t.Fatalf("repair through a link = %v", err)
	}
	if fi, _ := os.Lstat(path); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced")
	}
}

// A revocation recovered by --repair has only the id: revoking it again while the passkey is
// still in the user's file adds its key, so the key under a new id is revoked too.
func TestRevokeCompletesARecoveredEntry(t *testing.T) {
	env, _, u := testBootstrapEnv(t)
	u.Uid = strconv.Itoa(os.Getuid())
	a := webauthntest.NewES256("box.tail1234.ts.net")
	p := appstate.Passkey{ID: a.CredentialID(), PublicKey: a.PublicKeyPEM(), RPID: a.RPID, Owner: true, Label: "S25"}
	pk := filepath.Join(u.HomeDir, ".truehand", "app", "passkeys.json")
	os.MkdirAll(filepath.Dir(pk), 0o700)
	j, _ := json.Marshal(appstate.Passkeys{Passkeys: []appstate.Passkey{p}})
	os.WriteFile(pk, j, 0o600)
	path := filepath.Join(filepath.Dir(env.anchor), "revoked.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"version":1,"uid":`+u.Uid+`,"revoked":[{"credential":"`+p.ID+`","label":"recovered","time":"2026-11-08T01:00:00Z"}]}`), 0o644)
	var out bytes.Buffer
	c := &ctx{stdout: &out, stderr: &out}
	if err := revoke(c, *env, p.ID); err != nil || !strings.Contains(out.String(), "Key added") {
		t.Fatalf("revoke = %v %s", err, out.String())
	}
	b, _ := os.ReadFile(path)
	var l appstate.RevokedList
	json.Unmarshal(b, &l)
	if len(l.Revoked) != 1 || l.Revoked[0].PublicKeySHA256 != appstate.PublicKeySHA256(p.PublicKey) || l.Revoked[0].Time.Format(time.RFC3339) != "2026-11-08T01:00:00Z" {
		t.Fatalf("list = %s", b)
	}
	renamed := p
	renamed.ID = "renamed"
	if !(appstate.Trust{Revoked: l.Revoked}).Durable(&renamed) {
		t.Fatal("the same key under a new id is not revoked")
	}
	out.Reset()
	if err := revoke(c, *env, p.ID); err != nil || !strings.Contains(out.String(), "already revoked") {
		t.Fatalf("second revoke = %v %s", err, out.String())
	}
}

// The user's file is writable by agents: they cannot make root revoke the owner's real key
// by putting it under another id.
func TestRevokeRefusesATamperedKey(t *testing.T) {
	setup := func(t *testing.T) (*bootstrapEnv, *user.User, appstate.Passkey, string) {
		env, _, u := testBootstrapEnv(t)
		u.Uid = strconv.Itoa(os.Getuid())
		owner := webauthntest.NewES256("box.tail1234.ts.net")
		op := appstate.Passkey{ID: owner.CredentialID(), PublicKey: owner.PublicKeyPEM(), RPID: owner.RPID, Owner: true, Label: "S25"}
		os.MkdirAll(filepath.Dir(env.anchor), 0o755)
		return env, u, op, filepath.Join(filepath.Dir(env.anchor), "revoked.json")
	}
	write := func(u *user.User, ps ...appstate.Passkey) {
		pk := filepath.Join(u.HomeDir, ".truehand", "app", "passkeys.json")
		os.MkdirAll(filepath.Dir(pk), 0o700)
		j, _ := json.Marshal(appstate.Passkeys{Passkeys: ps})
		os.WriteFile(pk, j, 0o600)
	}
	var out bytes.Buffer
	c := &ctx{stdout: &out, stderr: &out}

	// Another passkey with the owner's key.
	env, u, op, list := setup(t)
	decoy := op
	decoy.ID, decoy.Label = "lost-phone", "Lost phone"
	write(u, op, decoy)
	if err := revoke(c, *env, "lost-phone"); err == nil || !strings.Contains(err.Error(), "also belongs to "+op.ID) {
		t.Fatalf("revoke of a key shared with another passkey = %v", err)
	}
	if _, err := os.Stat(list); err == nil {
		t.Fatal("something was revoked")
	}

	// The anchor owner's key under another id (the owner itself not in the file).
	env, u, op, list = setup(t)
	a, _ := json.Marshal(appstate.Anchor{Version: 1, UID: os.Getuid(), Owner: &appstate.AnchorOwner{Credential: op.ID, PublicKeySHA256: appstate.PublicKeySHA256(op.PublicKey), RPID: op.RPID}})
	os.WriteFile(env.anchor, a, 0o644)
	decoy = op
	decoy.ID, decoy.Label = "lost-phone", "Lost phone"
	write(u, decoy)
	if err := revoke(c, *env, "lost-phone"); err == nil || !strings.Contains(err.Error(), "anchor owner") {
		t.Fatalf("revoke of the anchor owner's key under another id = %v", err)
	}
	// Completing a recovered entry is checked the same way.
	os.WriteFile(list, []byte(`{"version":1,"uid":`+u.Uid+`,"revoked":[{"credential":"lost-phone","time":"2026-11-08T01:00:00Z"}]}`), 0o644)
	if err := revoke(c, *env, "lost-phone"); err == nil {
		t.Fatal("a recovered entry was completed with the owner's key")
	}
	if b, _ := os.ReadFile(list); strings.Contains(string(b), appstate.PublicKeySHA256(op.PublicKey)) {
		t.Fatalf("owner's key in the list: %s", b)
	}
	// The anchor owner revoking itself stays possible (then bootstrap again).
	write(u, op)
	if err := revoke(c, *env, op.ID); err != nil {
		t.Fatalf("revoking the anchor owner itself = %v", err)
	}
}

func TestRevokeAsksOnTheTerminal(t *testing.T) {
	env, _, u := testBootstrapEnv(t)
	u.Uid = strconv.Itoa(os.Getuid())
	a := webauthntest.NewES256("box.tail1234.ts.net")
	p := appstate.Passkey{ID: a.CredentialID(), PublicKey: a.PublicKeyPEM(), RPID: a.RPID, Owner: true, Label: "Lost phone"}
	pk := filepath.Join(u.HomeDir, ".truehand", "app", "passkeys.json")
	os.MkdirAll(filepath.Dir(pk), 0o700)
	j, _ := json.Marshal(appstate.Passkeys{Passkeys: []appstate.Passkey{p}})
	os.WriteFile(pk, j, 0o600)
	os.MkdirAll(filepath.Dir(env.anchor), 0o755)
	list := filepath.Join(filepath.Dir(env.anchor), "revoked.json")
	var asked string
	env.yes = false
	env.confirm = func(q string) (bool, error) { asked = q; return false, nil }
	var out bytes.Buffer
	c := &ctx{stdout: &out, stderr: &out}
	if err := revoke(c, *env, p.ID); err == nil || !strings.Contains(err.Error(), "not revoked") {
		t.Fatalf("answered no = %v", err)
	}
	if _, err := os.Stat(list); err == nil {
		t.Fatal("revoked without a yes")
	}
	if !strings.Contains(asked, `"Lost phone"`) || !strings.Contains(asked, groupKey(appstate.KeyFingerprint(p.PublicKey))) || !strings.Contains(asked, "Settings → Passkeys on a device you still have") || !strings.Contains(asked, "Type yes") {
		t.Fatalf("question = %q", asked)
	}
	env.confirm = func(string) (bool, error) { return true, nil }
	if err := revoke(c, *env, p.ID); err != nil {
		t.Fatal(err)
	}
	env.confirm = nil
	if err := revoke(c, *env, "other"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("no terminal = %v", err)
	}
	if g := groupKey("abcdef012345"); g != "abcd ef01 2345" {
		t.Fatalf("groupKey = %q", g)
	}
}

// A file cut off inside its last entry: that entry's key is recovered with its own id, and a
// fragment without a readable id gives its key to nobody.
func TestRepairReadsTheCutOffEntry(t *testing.T) {
	env, _, u := testBootstrapEnv(t)
	u.Uid = strconv.Itoa(os.Getuid())
	path := filepath.Join(filepath.Dir(env.anchor), "revoked.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	shaA, shaB := strings.Repeat("aa", 32), strings.Repeat("bb", 32)
	var out bytes.Buffer
	c := &ctx{stdout: &out, stderr: &out}
	read := func() []appstate.Revocation {
		b, _ := os.ReadFile(path)
		var l appstate.RevokedList
		json.Unmarshal(b, &l)
		return l.Revoked
	}
	os.WriteFile(path, []byte(`{"version":1,"uid":`+u.Uid+`,"revoked":[{"credential":"a","public_key_sha256":"`+shaA+`"},{"credential":"b","public_key_sha256":"`+shaB+`","ti`), 0o644)
	if err := repairRevoked(c, *env); err != nil {
		t.Fatal(err)
	}
	if r := read(); len(r) != 2 || r[0].PublicKeySHA256 != shaA || r[1].Credential != "b" || r[1].PublicKeySHA256 != shaB {
		t.Fatalf("recovered = %+v", r)
	}
	// The cut fragment has a key but its id is unreadable: the key is not given to "a".
	os.WriteFile(path, []byte(`{"version":1,"uid":`+u.Uid+`,"revoked":[{"credential":"a"},{"public_key_sha256":"`+shaB+`","credential":"b`), 0o644)
	if err := repairRevoked(c, *env); err != nil {
		t.Fatal(err)
	}
	if r := read(); len(r) != 1 || r[0].Credential != "a" || r[0].PublicKeySHA256 != "" {
		t.Fatalf("recovered = %+v", r)
	}
}
