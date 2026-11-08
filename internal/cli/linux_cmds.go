package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func init() {
	register("linux install", "Run Truehand as a headless service on this Linux machine: systemd user unit, linger, tailscale serve", cmdLinuxInstall)
}

const linuxUnitName = "truehand.service"

// linuxPlan is what `linux install` writes and runs, computed without touching anything.
type linuxPlan struct {
	UnitPath string
	Unit     string
	Commands [][]string
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// linuxInstallPlan builds the unit and the commands for bin, the user's home and login
// shell, and the loopback port.
func linuxInstallPlan(bin, home, shell, username string, port int) linuxPlan {
	if shell == "" {
		shell = "/bin/sh"
	}
	// The login shell reads the profile, so PATH has what the user installed (nvm, npm,
	// ~/.local/bin); serve completes it too.
	exec := fmt.Sprintf("%s -lc %s", shell, shellQuote(fmt.Sprintf("exec %s serve --headless --addr 127.0.0.1:%d", shellQuote(bin), port)))
	unit := fmt.Sprintf(`[Unit]
Description=Truehand (headless)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=5
UMask=0077
# Nothing the server starts, agents included, can gain privileges (sudo, setuid).
NoNewPrivileges=yes

[Install]
WantedBy=default.target
`, exec)
	return linuxPlan{
		UnitPath: filepath.Join(home, ".config", "systemd", "user", linuxUnitName),
		Unit:     unit,
		Commands: [][]string{
			{"systemctl", "--user", "daemon-reload"},
			{"systemctl", "--user", "enable", "--now", linuxUnitName},
			{"loginctl", "enable-linger", username},
			{"tailscale", "serve", "--bg", "--https=443", fmt.Sprintf("http://127.0.0.1:%d", port)},
		},
	}
}

func cmdLinuxInstall(c *ctx, args []string) error {
	fs := flag.NewFlagSet("linux install", flag.ContinueOnError)
	port := fs.Int("port", 7878, "loopback port the server listens on")
	dry := fs.Bool("dry-run", false, "print the unit and the commands, change nothing")
	if _, _, err := parse(fs, args); err != nil {
		return err
	}
	if *port < 1024 || *port > 65535 {
		return errors.New("--port must be 1024-65535")
	}
	if os.Geteuid() == 0 {
		return errors.New("run it as your own user, not root: the service is yours")
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(bin); err == nil {
		bin = real
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	username := os.Getenv("USER")
	// The unit runs a copy in a stable place: the binary may have been started from a
	// download or cache directory that goes away (and /tmp may be noexec).
	stable := filepath.Join(home, ".truehand", "bin", "truehand")
	plan := linuxInstallPlan(stable, home, loginShell(), username, *port)
	operator := tailscaleNeedsOperator()
	if *dry {
		if operator {
			fmt.Fprintf(c.stdout, "# first, once: let your user configure tailscale serve\nsudo tailscale set --operator=%s\n\n", username)
		}
		if bin != stable {
			fmt.Fprintf(c.stdout, "# copy %s to %s (0755)\n", bin, stable)
		}
		fmt.Fprintf(c.stdout, "# %s\n%s\n", plan.UnitPath, plan.Unit)
		for _, cmd := range plan.Commands {
			fmt.Fprintln(c.stdout, strings.Join(cmd, " "))
		}
		return nil
	}
	if runtime.GOOS != "linux" {
		return errors.New("linux install runs on Linux (try --dry-run to see what it would do)")
	}
	if operator {
		return fmt.Errorf("tailscale serve needs your user as operator: run `sudo tailscale set --operator=%s` once, then this again", username)
	}
	if bin != stable {
		if err := installBinary(bin, stable); err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "copied %s to %s\n", bin, stable)
	}
	if err := os.MkdirAll(filepath.Dir(plan.UnitPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(plan.UnitPath, []byte(plan.Unit), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "wrote %s\n", plan.UnitPath)
	for _, cmd := range plan.Commands {
		out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
		if err != nil {
			hint := ""
			if cmd[0] == "tailscale" && strings.Contains(strings.ToLower(string(out)), "access denied") {
				hint = " (allow your user to configure tailscale once: `sudo tailscale set --operator=$USER`)"
			}
			return fmt.Errorf("%s: %v%s\n%s", strings.Join(cmd, " "), err, hint, out)
		}
		fmt.Fprintf(c.stdout, "ran %s\n", strings.Join(cmd, " "))
	}
	return linuxChecks(c, *port)
}

func loginShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/sh"
}

// linuxChecks confirms what install set up is really there.
func linuxChecks(c *ctx, port int) error {
	var problems []string
	if out, err := exec.Command("systemctl", "--user", "is-active", linuxUnitName).Output(); err != nil || strings.TrimSpace(string(out)) != "active" {
		problems = append(problems, "the service is not active: `journalctl --user -u truehand`")
	}
	ok := false
	for i := 0; i < 10 && !ok; i++ {
		ok = serverAnswers(port)
		if !ok {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if !ok {
		problems = append(problems, fmt.Sprintf("nothing answers on 127.0.0.1:%d", port))
	}
	host := ""
	if self, ok := tailscaleSelfInfo(); ok {
		host = strings.TrimSuffix(self.DNSName, ".")
	}
	if host == "" {
		problems = append(problems, "tailscale has no MagicDNS name for this machine")
	} else if out, err := exec.Command("tailscale", "serve", "status").CombinedOutput(); err != nil || !strings.Contains(string(out), strconv.Itoa(port)) {
		problems = append(problems, "tailscale serve does not forward to the server")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	fmt.Fprintf(c.stdout, "Truehand runs at https://%s\nNext, at this machine's terminal: sudo truehand passkey bootstrap\n", host)
	return nil
}

func serverAnswers(port int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+net.JoinHostPort("127.0.0.1", strconv.Itoa(port))+"/api/version", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusOK
}

// installBinary copies src to dst (0755) through a file beside dst, so a running service
// never sees half a binary.
func installBinary(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".truehand.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// tailscalePrefs is `tailscale debug prefs`; tests replace it.
var tailscalePrefs = func() ([]byte, error) {
	return exec.Command("tailscale", "debug", "prefs").Output()
}

// tailscaleNeedsOperator reports whether this user may not configure tailscale serve yet.
// Reading the serve status is allowed to anyone, so the prefs tell: an operator other than
// this user (and not root) means `tailscale serve --bg` will be refused. Without prefs it
// cannot tell; the install then explains an "access denied" when it happens.
var tailscaleNeedsOperator = func() bool {
	if runtime.GOOS != "linux" || os.Geteuid() == 0 {
		return false
	}
	return operatorMissing(tailscalePrefs, os.Getenv("USER"))
}

func operatorMissing(prefs func() ([]byte, error), user string) bool {
	b, err := prefs()
	if err != nil {
		return false
	}
	var p struct {
		OperatorUser string `json:"OperatorUser"`
	}
	if json.Unmarshal(b, &p) != nil {
		return false
	}
	return p.OperatorUser != user
}
