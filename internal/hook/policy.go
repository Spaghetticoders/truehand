// Package hook implements the agent-side locks: what an agent may run or write, and when
// it may stop. The same policy backs every agent integration; claude.go adapts it to
// Claude Code's hook protocol.
package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/gate"
	"github.com/spaghetticoders/truehand/internal/glob"
)

// Everything under .truehand is written by truehand or by humans, except the files agents
// are meant to write.
var (
	Protected     = []string{".truehand", ".truehand/**"}
	AgentWritable = []string{".truehand/tasks/*/plan.md", ".truehand/tasks/*/context.md"}
)

type Decision struct {
	Deny   bool
	Reason string
}

func allow() Decision { return Decision{} }

func deny(format string, a ...any) Decision {
	return Decision{Deny: true, Reason: "Truehand: " + fmt.Sprintf(format, a...)}
}

// ProtectedPath reports whether a path (absolute or repo-relative) is truehand-owned.
func ProtectedPath(ws *core.Workspace, p string) bool {
	rel := p
	if filepath.IsAbs(p) {
		r, err := filepath.Rel(realPath(ws.Repo.Root), realPath(p))
		if err != nil || strings.HasPrefix(r, "..") {
			return false
		}
		rel = r
	}
	// Case-folded: on a case-insensitive file system (macOS, Windows) .TRUEHAND is .truehand.
	rel = strings.ToLower(filepath.ToSlash(filepath.Clean(rel)))
	return glob.MatchAny(Protected, rel) && !glob.MatchAny(AgentWritable, rel)
}

// realPath resolves symlinks in the longest existing prefix of p (e.g. /tmp → /private/tmp).
func realPath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for dir := p; ; dir = filepath.Dir(dir) {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(r, rest)
		}
		if filepath.Dir(dir) == dir {
			return p
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

func abs(cwd, p string) string {
	if filepath.IsAbs(p) || cwd == "" {
		return p
	}
	return filepath.Join(cwd, p)
}

// CheckWrite guards file-writing tools.
func CheckWrite(ws *core.Workspace, path string) Decision {
	if ProtectedPath(ws, path) {
		return deny("%s is written only by truehand. Use the truehand commands (hands, review, task move) instead of editing it; humans edit config.yaml", path)
	}
	return allow()
}

// CheckBash guards shell commands.
// approveRe catches truehand approve wherever it appears: behind sh -c, go run, a path.
var approveRe = regexp.MustCompile(`truehand\S*\s+approve\b`)

func CheckBash(ws *core.Workspace, cwd, line string) Decision {
	return checkBash(ws, cwd, line, 0)
}

func checkBash(ws *core.Workspace, cwd, line string, depth int) Decision {
	if approveRe.MatchString(line) {
		return deny("approvals are for humans. Ask the user to run `truehand approve` in their terminal or approve from the Truehand app")
	}
	segs := segments(line)
	heredoc := strings.Contains(line, "<<")
	for _, raw := range segs {
		// An interpreter's script (inline or a heredoc) is opaque: any mention of truehand's
		// own files in it is refused, since what the script does with them cannot be seen. A
		// heredoc's body is on other lines, so then the whole line counts.
		words := stripEnv(raw)
		if len(words) == 0 || !interpreters[filepath.Base(words[0])] {
			continue
		}
		if heredoc && protectedRe.MatchString(line) || protectedRe.MatchString(strings.Join(words, " ")) {
			return deny("`%s` scripts may not touch truehand's files (config.yaml, task.yaml, events, approvals, evidence). Read them with `truehand task show --json` or cat; edit plan.md with the editor; config.yaml is for humans", words[0])
		}
	}
	for _, raw := range segs {
		words := stripEnv(raw)
		if len(words) == 0 {
			continue
		}
		switch {
		case words[0] == "cd" && len(words) > 1:
			cwd = abs(cwd, words[1])
			continue
		case (words[0] == "sh" || words[0] == "bash" || words[0] == "zsh" || strings.HasSuffix(words[0], "/sh") || strings.HasSuffix(words[0], "/bash")) && len(words) > 2 && words[1] == "-c":
			if depth < 3 {
				if d := checkBash(ws, cwd, words[2], depth+1); d.Deny {
					return d
				}
			}
			continue
		}
		if sub, args, global := gitCommand(words); sub != "" {
			if d := checkGit(ws, cwd, sub, args, global); d.Deny {
				return d
			}
			continue
		}
		if d := checkShellWrite(ws, cwd, words); d.Deny {
			return d
		}
	}
	return allow()
}

func checkGit(ws *core.Workspace, cwd, sub string, args, global []string) Decision {
	for i, g := range global {
		if g == "-c" && i+1 < len(global) && strings.Contains(strings.ToLower(global[i+1]), "hookspath") {
			return deny("overriding core.hooksPath disables the repo's hooks")
		}
	}
	switch sub {
	case "checkout", "restore", "apply", "rm", "mv", "am", "stash":
		for _, a := range args {
			if mentionsTruehand(a) || ProtectedPath(ws, abs(cwd, a)) {
				return deny("`git %s` on %s would rewrite truehand's files", sub, a)
			}
		}
		if sub == "apply" || sub == "am" {
			return deny("`git %s` can write truehand's files; edit the source files directly", sub)
		}
	case "push":
		if !ws.Config.Locks.AgentPush {
			return deny("agents do not push. Leave the commits for the user to push")
		}
	case "commit":
		for _, a := range args {
			if a == "--no-verify" || a == "-n" {
				return deny("--no-verify skips the repo's hooks; commit without it")
			}
		}
		return checkCommitState(ws)
	case "config":
		for _, a := range args {
			if strings.Contains(a, "hooksPath") {
				return deny("changing core.hooksPath disables the repo's hooks")
			}
		}
	}
	return allow()
}

func checkCommitState(ws *core.Workspace) Decision {
	from, err := board.ParseState(ws.Config.CommitFrom())
	if err != nil {
		return deny("locks.commit_from: %v", err)
	}
	id := ws.ActiveTask()
	if id == "" {
		return deny("no active task. Commits happen inside a task: `truehand task new <id> --title ...` or `truehand task use <id>`")
	}
	t, err := ws.Store.Load(id)
	if err != nil {
		return deny("active task %s: %v", id, err)
	}
	if t.State.Index() < from.Index() {
		return deny("task %s is %s; commits are allowed from %s on. Next: `truehand gate check` to see what is missing", t.ID, t.State, from)
	}
	return allow()
}

var interpreters = map[string]bool{
	"python": true, "python3": true, "node": true, "ruby": true, "perl": true, "php": true, "deno": true, "bun": true, "osascript": true,
}

var protectedRe = regexp.MustCompile(`(?i)\.truehand/(config\.ya?ml|tasks/[^/\s'"]+/(task\.yaml|events\.jsonl|approvals\.jsonl|evidence))`)

var writeCmds = map[string]bool{
	"cp": true, "mv": true, "rm": true, "tee": true, "truncate": true, "touch": true, "dd": true,
	"sed": true, "perl": true, "python": true, "python3": true, "ruby": true, "node": true, "install": true, "ln": true, "chmod": true,
}

func checkShellWrite(ws *core.Workspace, cwd string, words []string) Decision {
	// A redirect writes only its target; the command's own arguments are written only by
	// commands that write.
	for i, w := range words {
		if w == ">" && i+1 < len(words) {
			if ProtectedPath(ws, abs(cwd, words[i+1])) || mentionsTruehand(words[i+1]) && !agentWritable(words[i+1]) {
				return deny("redirecting into %s: only truehand writes it", words[i+1])
			}
		}
	}
	if words[0] == "ln" {
		for _, w := range words[1:] {
			if mentionsTruehand(w) || ProtectedPath(ws, abs(cwd, w)) {
				return deny("no links under .truehand: they could expose files outside the task")
			}
		}
	}
	if words[0] == "find" && findWrites(words[1:]) {
		for _, sp := range findStarts(words[1:]) {
			if findReachesTruehand(ws, cwd, sp) {
				return deny("`find` from %s with -delete/-exec could change truehand's files: name a directory that does not contain .truehand", sp)
			}
		}
	}
	if !writeCmds[words[0]] {
		return allow()
	}
	if words[0] == "cp" || words[0] == "install" {
		for _, w := range copyTargets(words[1:]) {
			if ProtectedPath(ws, abs(cwd, w)) || mentionsTruehand(w) && !agentWritable(w) {
				return deny("`%s` would modify %s: only truehand writes it", words[0], w)
			}
		}
		return allow()
	}
	if words[0] == "sed" && !sedWrites(words[1:], ProtectedPath(ws, cwd) || mentionsTruehand(cwd)) {
		return allow()
	}
	for _, w := range words[1:] {
		if ProtectedPath(ws, abs(cwd, w)) || mentionsTruehand(w) && !agentWritable(w) {
			return deny("`%s` would modify %s: only truehand writes it", words[0], w)
		}
	}
	return allow()
}

// findWrites reports whether find acts on what it finds rather than only listing it.
func findWrites(args []string) bool {
	for _, a := range args {
		switch a {
		case "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls":
			return true
		}
	}
	return false
}

// findStarts are find's starting points: the operands before the first expression ("." when
// there is none).
func findStarts(args []string) []string {
	var out []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") || a == "(" || a == "!" {
			break
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		out = []string{"."}
	}
	return out
}

// findReachesTruehand reports whether a find starting at sp may reach a .truehand: it names
// one, it cannot be resolved ($VAR, a backquote), or it contains the repo or ~/.truehand.
func findReachesTruehand(ws *core.Workspace, cwd, sp string) bool {
	if mentionsTruehand(sp) || strings.ContainsAny(sp, "$`") {
		return true
	}
	home, _ := os.UserHomeDir()
	if sp == "~" || strings.HasPrefix(sp, "~/") {
		if home == "" {
			return true
		}
		sp = filepath.Join(home, strings.TrimPrefix(sp, "~"))
	}
	dir := realPath(abs(cwd, sp))
	targets := []string{realPath(ws.Repo.Root)}
	if home != "" {
		targets = append(targets, realPath(filepath.Join(home, ".truehand")))
	}
	for _, t := range targets {
		if r, err := filepath.Rel(strings.ToLower(dir), strings.ToLower(t)); err == nil && !strings.HasPrefix(r, "..") {
			return true
		}
	}
	return false
}

// copyTargets are what cp or install write: the -t directory, or the last operand. Reading
// truehand's files is fine; any other option value is not a path written.
func copyTargets(args []string) []string {
	var operands []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			operands = append(operands, args[i+1:]...)
			i = len(args)
		case a == "-t" || a == "--target-directory":
			if i+1 < len(args) {
				return []string{args[i+1]}
			}
		case strings.HasPrefix(a, "--target-directory="):
			return []string{strings.TrimPrefix(a, "--target-directory=")}
		case a == "--suffix" || a == "--mode" || a == "--owner" || a == "--group":
			i++
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-") && a != "-":
			// A cluster of short flags: t takes the rest (or the next word) as the target;
			// S, m, o and g take a value the same way.
			for j := 1; j < len(a); j++ {
				if !strings.ContainsRune("tSmog", rune(a[j])) {
					continue
				}
				val := a[j+1:]
				if val == "" && i+1 < len(args) {
					i++
					val = args[i]
				}
				if a[j] == 't' {
					return []string{val}
				}
				break
			}
		default:
			operands = append(operands, a)
		}
	}
	if len(operands) == 0 {
		return nil
	}
	return operands[len(operands)-1:]
}

// sedWrites reports whether sed may write a protected file: in place, or through a w/W
// command in a script that names one (or any w/W when run from inside .truehand). A script
// file is opaque, so it counts as writing.
func sedWrites(args []string, inProtected bool) bool {
	var scripts []string
	explicit := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			if !explicit && i+1 < len(args) {
				scripts = append(scripts, args[i+1])
			}
			i = len(args)
		case a == "-e" || a == "--expression":
			explicit = true
			if i+1 < len(args) {
				scripts = append(scripts, args[i+1])
				i++
			}
		case strings.HasPrefix(a, "--expression="):
			explicit = true
			scripts = append(scripts, strings.TrimPrefix(a, "--expression="))
		case a == "-f" || a == "--file" || strings.HasPrefix(a, "--file="):
			return true
		case strings.HasPrefix(a, "--in-place"):
			return true
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-") && len(a) > 1:
			flags := a[1:]
			if j := strings.IndexAny(flags, "ef"); j >= 0 {
				if strings.ContainsAny(flags[:j], "iI") {
					return true
				}
				if flags[j] == 'f' {
					return true
				}
				explicit = true
				if v := flags[j+1:]; v != "" {
					scripts = append(scripts, v)
				} else if i+1 < len(args) {
					scripts = append(scripts, args[i+1])
					i++
				}
				continue
			}
			if strings.ContainsAny(flags, "iI") {
				return true
			}
		case !explicit:
			scripts = append(scripts, a)
			explicit = true
		}
	}
	for _, sc := range scripts {
		if mentionsTruehand(sc) || inProtected && strings.ContainsAny(sc, "wW") {
			return true
		}
	}
	return false
}

// agentWritable reports the task files the agent writes itself.
func agentWritable(path string) bool {
	path = strings.ToLower(path)
	return strings.HasSuffix(path, "/plan.md") || strings.HasSuffix(path, "/context.md")
}

// mentionsTruehand reports whether a word names .truehand, in any case.
func mentionsTruehand(w string) bool { return strings.Contains(strings.ToLower(w), ".truehand") }

// CheckStop decides whether the agent may end its turn: not while the active task is being
// built and the verified gate is closed. stopHookActive avoids blocking twice in a row.
func CheckStop(ws *core.Workspace, stopHookActive bool) Decision {
	if stopHookActive {
		return allow()
	}
	id := ws.ActiveTask()
	if id == "" {
		return allow()
	}
	t, err := ws.Store.Load(id)
	if err != nil || t.State != board.Building {
		return allow()
	}
	res, err := ws.Check(t, board.Verified)
	if err != nil || res.Pass {
		return allow()
	}
	var missing []string
	for _, it := range res.Items {
		if it.Status == gate.StatusFail {
			missing = append(missing, fmt.Sprintf("- %s: %s", it.Check, it.Message))
		}
	}
	return deny("task %s is still building and cannot be verified yet:\n%s\nRun the hands above, then `truehand task move %s verified`. If you are blocked, say so explicitly to the user instead of declaring the work done.",
		t.ID, strings.Join(missing, "\n"), t.ID)
}

// Context is a short status line for the agent at session start.
func Context(ws *core.Workspace) string {
	id := ws.ActiveTask()
	if id == "" {
		return "Truehand is active in this repo. No active task: work happens inside tasks (`truehand task new <id> --title ...`). Run `truehand help` for the workflow."
	}
	t, err := ws.Store.Load(id)
	if err != nil {
		return "Truehand: active task " + id + " not found."
	}
	s := fmt.Sprintf("Truehand: active task %s (%s) is %s.", t.ID, t.Title, t.State)
	if next := t.State.Next(); next != "" {
		if res, err := ws.Check(t, next); err == nil {
			var missing []string
			for _, it := range res.Items {
				if it.Status == gate.StatusFail {
					missing = append(missing, it.Check)
				}
			}
			if len(missing) == 0 {
				s += fmt.Sprintf(" Gate %s is open: `truehand task move %s`.", next, next)
			} else {
				s += fmt.Sprintf(" To reach %s: %s. See `truehand task show`.", next, strings.Join(missing, ", "))
			}
		}
	}
	return s
}
