// Package humanauth proves a human is present: it asks the operating system to authenticate
// the user (password or Touch ID). An agent running as the same OS user can run any command
// but cannot answer that dialog.
package humanauth

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// ErrUnavailable means this platform has no way to ask for authentication.
var ErrUnavailable = errors.New("no OS authentication available on this system (macOS needs osascript; Linux needs pkexec with a polkit agent)")

// Authenticate is replaceable in tests.
var Authenticate = authenticate

// Method names the mechanism, recorded with every approval.
func Method() string {
	switch runtime.GOOS {
	case "darwin":
		return "os-auth:macos"
	case "linux":
		return "os-auth:polkit"
	}
	return "os-auth:none"
}

// Absolute paths in system-protected locations (SIP on macOS, root-owned on Linux): the
// caller's PATH is the agent's to change, so it must never pick the program.
const (
	osascriptPath = "/usr/bin/osascript"
	pkexecPath    = "/usr/bin/pkexec"
)

// authCommand builds the authentication command with a minimal environment, so nothing the
// caller exported (PATH, DYLD_*, LD_*) reaches it.
func authCommand(reason string) (*exec.Cmd, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		// "with administrator privileges" shows the system authentication dialog (password or
		// Touch ID) every time a new process asks.
		script := fmt.Sprintf(`do shell script "/usr/bin/true" with prompt %s with administrator privileges`, appleString(reason))
		cmd = exec.Command(osascriptPath, "-e", script)
	case "linux":
		if _, err := os.Stat(pkexecPath); err != nil {
			return nil, ErrUnavailable
		}
		cmd = exec.Command(pkexecPath, "/bin/true")
	default:
		return nil, ErrUnavailable
	}
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	if home := os.Getenv("HOME"); home != "" {
		cmd.Env = append(cmd.Env, "HOME="+home)
	}
	if d := os.Getenv("DISPLAY"); d != "" {
		cmd.Env = append(cmd.Env, "DISPLAY="+d)
	}
	return cmd, nil
}

func authenticate(reason string) error {
	cmd, err := authCommand(reason)
	if err != nil {
		return err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "-128") {
			return errors.New("authentication cancelled")
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("authentication failed: %s", msg)
	}
	return nil
}

func appleString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// ApprovalPrompt is the dialog text for approving a plan. The title is written by agents, so
// it is quoted and truncated after a fixed sentence and cannot pose as a system message.
func ApprovalPrompt(taskID, title string) string {
	r := []rune(strings.Join(strings.Fields(title), " "))
	if len(r) > 60 {
		r = append(r[:60], '…')
	}
	return fmt.Sprintf("Truehand asks you to approve the plan of task %s, titled “%s”. Read the plan before you authenticate.", taskID, string(r))
}
