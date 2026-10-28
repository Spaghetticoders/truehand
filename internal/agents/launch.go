// Package agents runs Claude Code sessions in pseudo-terminals for the Truehand app: one
// process group per session, output kept in a ring buffer and on disk and fanned out to
// every viewer, state reported by async hooks.
package agents

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spaghetticoders/truehand/internal/appstate"
)

const (
	RoleDev    = "dev"
	RoleReview = "review"
	RoleFree   = "free"
)

func ValidRole(r string) bool { return r == RoleDev || r == RoleReview || r == RoleFree }

// LaunchSpec is everything that decides the claude command line. Nothing here comes from a
// client as free text except the prompt, which is passed after "--" as a single argument.
type LaunchSpec struct {
	Role            string
	PluginDir       string
	Settings        string
	ClaudeSessionID string
	Resume          bool
	PermissionMode  string
	Prompt          string
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// LaunchArgs builds the arguments for claude (without the program name).
func LaunchArgs(s LaunchSpec) ([]string, error) {
	if !ValidRole(s.Role) {
		return nil, fmt.Errorf("unknown role %q", s.Role)
	}
	if !appstate.ValidPermissionMode(s.PermissionMode) {
		return nil, fmt.Errorf("permission mode must be one of %s", strings.Join(appstate.PermissionModes, ", "))
	}
	if !uuidRe.MatchString(s.ClaudeSessionID) {
		return nil, errors.New("invalid claude session id")
	}
	if !filepath.IsAbs(s.PluginDir) || !filepath.IsAbs(s.Settings) {
		return nil, errors.New("plugin dir and settings must be absolute paths")
	}
	args := []string{"--plugin-dir", s.PluginDir, "--settings", s.Settings}
	if s.Resume {
		args = append(args, "--resume", s.ClaudeSessionID)
	} else {
		args = append(args, "--session-id", s.ClaudeSessionID)
	}
	args = append(args, "--permission-mode", s.PermissionMode)
	if p := strings.TrimSpace(s.Prompt); p != "" && !s.Resume {
		args = append(args, "--", p)
	}
	return args, nil
}

// DefaultPrompt is the first message of a session started for a task.
func DefaultPrompt(role, task string) string {
	switch {
	case task == "":
		return ""
	case role == RoleDev:
		return fmt.Sprintf("Work on Truehand task %s: read it with `truehand task show %s` and follow the truehand skill.", task, task)
	case role == RoleReview:
		return fmt.Sprintf("Review Truehand task %s as an independent reviewer: you did not build it. Read it with `truehand task show %s` and follow the truehand skill's review steps.", task, task)
	}
	return ""
}

// NewUUID returns a random version 4 UUID, the form claude --session-id expects.
func NewUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// CleanEnv drops everything Truehand-specific from base, so no credential or actor override
// reaches the agent, and marks the terminal.
func CleanEnv(base []string, session string) []string {
	out := make([]string, 0, len(base)+3)
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "TRUEHAND_") || k == "CLAUDECODE" || k == "CLAUDE_CODE_ENTRYPOINT" || k == "TERM" || k == "COLORTERM" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "TERM=xterm-256color", "COLORTERM=truecolor", "TRUEHAND_SESSION="+session)
}

// SessionEnv encodes what the status hook needs to reach the server: TRUEHAND_SESSION.
func SessionEnv(sid, secret, addr string) string { return sid + ":" + secret + "@" + addr }

// SocketAddr in TRUEHAND_SESSION means this user's ~/.truehand/app/serve.sock.
const SocketAddr = "unix"

// ParseSessionEnv decodes TRUEHAND_SESSION; the address must be on loopback or the socket.
func ParseSessionEnv(v string) (sid, secret, addr string, err error) {
	cred, addr, ok := strings.Cut(v, "@")
	if !ok {
		return "", "", "", errors.New("malformed TRUEHAND_SESSION")
	}
	sid, secret, ok = strings.Cut(cred, ":")
	if !ok || sid == "" || secret == "" {
		return "", "", "", errors.New("malformed TRUEHAND_SESSION")
	}
	if addr == SocketAddr {
		return sid, secret, addr, nil
	}
	host, port, err := net.SplitHostPort(addr)
	if n, perr := strconv.Atoi(port); err != nil || host != "127.0.0.1" || perr != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return "", "", "", errors.New("TRUEHAND_SESSION must point at 127.0.0.1:<port>")
	}
	return sid, secret, addr, nil
}
