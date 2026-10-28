package serve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/gitx"
	"github.com/spaghetticoders/truehand/internal/sign"
)

func (s *Server) projectByID(w http.ResponseWriter, r *http.Request) (appstate.Project, bool) {
	st, err := s.Store.State()
	if err != nil {
		fail(w, err)
		return appstate.Project{}, false
	}
	p, ok := st.Project(r.PathValue("pid"))
	if !ok {
		httpError(w, http.StatusNotFound, "unknown project")
		return appstate.Project{}, false
	}
	return *p, true
}

func sessionStates(list []appstate.Session) []core.SessionState {
	out := []core.SessionState{}
	for _, x := range list {
		since := x.Updated
		if since.IsZero() {
			since = x.Started
		}
		code := x.ExitCode
		if x.Stopped {
			code = nil // stopped on request: not a failure
		}
		out = append(out, core.SessionState{ID: x.ID, Task: x.Task, Role: x.Role, Status: x.Status, ExitCode: code, Since: since})
	}
	return out
}

func sessionRefs(list []appstate.Session) []core.SessionRef {
	var out []core.SessionRef
	for _, x := range list {
		out = append(out, core.SessionRef{ClaudeSessionID: x.ClaudeSessionID, Role: x.Role, Task: x.Task})
	}
	return out
}

// queuedTTL is how long a request from a phone waits for the Mac.
const queuedTTL = 24 * time.Hour

// queuedActions are the requests waiting for the Mac, after dropping those that expired and
// approvals of a plan that changed since it was read: nobody may approve unseen text.
func (s *Server) queuedActions() []appstate.QueuedAction {
	a, err := s.Store.Auth()
	if err != nil {
		return nil
	}
	var drop map[string]bool
	for _, q := range a.Queued {
		if !s.queuedValid(q) {
			if drop == nil {
				drop = map[string]bool{}
			}
			drop[q.ID] = true
		}
	}
	if drop == nil {
		return a.Queued
	}
	var kept []appstate.QueuedAction
	_ = s.Store.UpdateAuth(func(a *appstate.Auth) error {
		kept = kept[:0]
		for _, q := range a.Queued {
			if !drop[q.ID] {
				kept = append(kept, q)
			}
		}
		a.Queued = kept
		return nil
	})
	return kept
}

func (s *Server) queuedValid(q appstate.QueuedAction) bool {
	if time.Since(q.Time) > queuedTTL {
		return false
	}
	if q.Kind == "config" {
		yaml, _ := q.Params["yaml"].(string)
		return core.ValidateConfig([]byte(yaml)) == nil
	}
	if q.Kind != "approve" {
		return true
	}
	st, err := s.Store.State()
	if err != nil {
		return true
	}
	p, ok := st.Project(q.Project)
	if !ok {
		return false
	}
	l, err := s.locateIn(*p, q.Task)
	if err != nil {
		return false
	}
	return l.Task.State == board.Planned && currentPlan(l.WS, l.Task.ID, q.PlanSHA256) == nil
}

// ProjectAllowed, when set, vets a repo before it is registered: the E2E build keeps real
// repos out of reach of its fake authentication.
var ProjectAllowed func(root string) error

func (s *Server) projectView(p appstate.Project) projectView {
	return s.load(p, nil).view()
}

func (s *Server) addProject(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Path       string `json:"path"`
		ClientName string `json:"clientName"`
		Name       string `json:"name"`
		MachineID  string `json:"machineId"`
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	if !decode(w, r, &body) {
		return
	}
	if body.MachineID != "" && body.MachineID != appstate.LocalMachine {
		if s.Remotes == nil {
			httpError(w, http.StatusBadRequest, "no remote machines here")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
		s.forward(w, r, body.MachineID, "/api/projects")
		return
	}
	if !filepath.IsAbs(body.Path) {
		httpError(w, http.StatusBadRequest, "path must be absolute")
		return
	}
	repo, err := gitx.Open(body.Path)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if ProjectAllowed != nil {
		if err := ProjectAllowed(repo.Root); err != nil {
			httpError(w, http.StatusForbidden, err.Error())
			return
		}
	}
	p, err := s.Store.AddProject(repo.Root, body.ClientName, body.Name)
	if err != nil {
		fail(w, err)
		return
	}
	s.broadcast(sseEvent{"changed", p.ID})
	writeJSONStatus(w, http.StatusCreated, s.projectView(p))
}

func (s *Server) patchProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientName *string `json:"clientName"`
		Name       *string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	var out appstate.Project
	err := s.Store.UpdateState(func(st *appstate.State) error {
		p, ok := st.Project(r.PathValue("pid"))
		if !ok {
			return appstate.ErrNotFound
		}
		if body.ClientName != nil {
			p.Client = strings.TrimSpace(*body.ClientName)
		}
		if body.Name != nil {
			if strings.TrimSpace(*body.Name) == "" {
				return errBadRequest("name must not be empty")
			}
			p.Name = strings.TrimSpace(*body.Name)
		}
		out = *p
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	s.broadcast(sseEvent{"changed", out.ID})
	writeJSON(w, s.projectView(out))
}

type badRequest string

func (e badRequest) Error() string { return string(e) }

func errBadRequest(msg string) error { return badRequest(msg) }

// deleteProject takes the project off the list; the repo is not touched.
func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectByID(w, r)
	if !ok {
		return
	}
	for _, x := range s.allSessions() {
		if x.Project == p.ID && appstate.Live(x.Status) {
			httpError(w, http.StatusConflict, "stop the project's agent sessions first")
			return
		}
	}
	if err := s.Store.RemoveProject(p.ID); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type dirEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsGit bool   `json:"isGit"`
}

// fsDirs lists directories under the user's home, for adding a project from a phone.
func (s *Server) fsDirs(w http.ResponseWriter, r *http.Request) {
	home, err := filepath.EvalSymlinks(s.Home)
	if err != nil {
		fail(w, err)
		return
	}
	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = home
	}
	if !filepath.IsAbs(dir) {
		httpError(w, http.StatusBadRequest, "path must be absolute")
		return
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		httpError(w, http.StatusNotFound, "no such directory")
		return
	}
	if real != home && !strings.HasPrefix(real, home+string(filepath.Separator)) {
		httpError(w, http.StatusForbidden, "only directories under your home folder are listed")
		return
	}
	entries, err := os.ReadDir(real)
	if err != nil {
		fail(w, err)
		return
	}
	out := []dirEntry{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(real, e.Name())
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			continue
		}
		_, gerr := os.Stat(filepath.Join(p, ".git"))
		out = append(out, dirEntry{Name: e.Name(), Path: p, IsGit: gerr == nil})
		if len(out) == 1000 {
			break
		}
	}
	writeJSON(w, out)
}

func (s *Server) repoOf(w http.ResponseWriter, r *http.Request) (appstate.Project, *gitx.Repo, bool) {
	p, ok := s.projectByID(w, r)
	if !ok {
		return p, nil, false
	}
	repo, err := gitx.Open(p.Root)
	if err != nil {
		httpError(w, http.StatusConflict, err.Error())
		return p, nil, false
	}
	return p, repo, true
}

type activationView struct {
	HasConfig       bool              `json:"hasConfig"`
	ConfigYAML      string            `json:"configYaml"`
	ConfigError     string            `json:"configError,omitempty"`
	Stack           string            `json:"stack"`
	Base            string            `json:"base"`
	GitHooks        map[string]string `json:"gitHooks"`
	ConfigCommitted bool              `json:"configCommitted"`
	// ConfigSHA256 names the config shown, for commit-config.
	ConfigSHA256 string `json:"configSha256,omitempty"`
	// What committing it would change on the base branch.
	ConfigDiff       string       `json:"configDiff,omitempty"`
	Approvers        []approverID `json:"approvers"`
	ApproversAdded   []approverID `json:"approversAdded"`
	ApproversRemoved []approverID `json:"approversRemoved"`
}

type approverID struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Key is the start of the sha256 of the public key: the name is whatever the config's
	// author wrote, the key is what signs.
	Key string `json:"key"`
}

func keyFingerprint(pemKey string) string {
	pub, err := sign.ParsePublicKey(pemKey)
	if err != nil {
		return "invalid"
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "invalid"
	}
	h := sha256.Sum256(der)
	return hex.EncodeToString(h[:])[:12]
}

// promptText makes text from a config safe to show in the OS dialog: one line, no control
// characters, at most 40 characters.
func promptText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 40 {
		s = string(r[:40]) + "…"
	}
	return s
}

// configChange compares a config with the one on the base branch: the unified diff and who
// becomes or stops being an approver there.
func configChange(ws *core.Workspace, current []byte) (diff string, now, added, removed []approverID) {
	rel := filepath.ToSlash(config.Path(""))
	base, _ := ws.Repo.Git("show", baseRef(ws)+":"+rel)
	ids := func(b []byte) map[string]approverID {
		out := map[string]approverID{}
		if cfg, err := config.Parse(b); err == nil {
			for _, a := range cfg.Approvers {
				out[a.ID] = approverID{ID: a.ID, Name: a.Name, Key: keyFingerprint(a.PublicKey)}
			}
		}
		return out
	}
	was, is := ids([]byte(base)), ids(current)
	now, added, removed = []approverID{}, []approverID{}, []approverID{}
	for id, a := range is {
		now = append(now, a)
		// A changed key under the same id is a new approver.
		if old, ok := was[id]; !ok || old.Key != a.Key {
			added = append(added, a)
		}
	}
	for id, a := range was {
		if cur, ok := is[id]; !ok || cur.Key != a.Key {
			removed = append(removed, a)
		}
	}
	for _, l := range [][]approverID{now, added, removed} {
		sort.Slice(l, func(i, j int) bool { return l[i].ID < l[j].ID })
	}
	return unifiedDiff(base, string(current), rel), now, added, removed
}

// unifiedDiff runs git diff --no-index on two texts, labelled as the base and working copy.
func unifiedDiff(a, b, rel string) string {
	dir, err := os.MkdirTemp("", "truehand-diff")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(dir)
	pa, pb := filepath.Join(dir, "base"), filepath.Join(dir, "working")
	if os.WriteFile(pa, []byte(a), 0o600) != nil || os.WriteFile(pb, []byte(b), 0o600) != nil {
		return ""
	}
	cmd := exec.Command("git", "diff", "--no-index", "--no-color", "--src-prefix=base/", "--dst-prefix=working/", "--", "base", "working")
	cmd.Dir = dir
	out, _ := cmd.Output()
	return strings.NewReplacer("base/base", "base/"+rel, "working/working", "working/"+rel).Replace(string(out))
}

// commitPrompt is the OS dialog text: which approvers this commit puts on the base branch.
// macKey is the fingerprint of the Mac app's own key, when it sent it: each approver is then
// marked as this Mac's key or a foreign one.
func commitPrompt(p appstate.Project, sha, macKey string, now, added, removed []approverID) string {
	names := func(l []approverID) string {
		var s []string
		for _, a := range l {
			whose := ""
			switch {
			case macKey == "":
			case a.Key == macKey:
				whose = ", this Mac"
			default:
				whose = ", foreign key"
			}
			s = append(s, promptText(a.ID)+" (\""+promptText(a.Name)+"\", key "+a.Key+whose+")")
		}
		return strings.Join(s, ", ")
	}
	if len(sha) > 12 {
		sha = sha[:12]
	}
	msg := "Truehand: commit .truehand/config.yaml of " + p.Name + " (sha " + sha + ") to its base branch."
	if len(now) == 0 {
		msg += " No approvers."
	} else {
		msg += fmt.Sprintf(" %d approver(s): %s.", len(now), names(now))
	}
	if len(added) > 0 {
		msg += " Adds approver(s) " + names(added) + "."
	}
	if len(removed) > 0 {
		msg += " Removes approver(s) " + names(removed) + "."
	}
	return msg
}

// macKeyOf is the fingerprint of the key the Mac app says is its own; only the app (with
// its secret) may say so, since the label goes into the dialog.
func macKeyOf(r *http.Request, pemKey string) string {
	if pemKey == "" || !identityOf(r).Shell {
		return ""
	}
	if fp := keyFingerprint(pemKey); fp != "invalid" {
		return fp
	}
	return ""
}

func viewActivation(a *core.Activation) activationView {
	v := activationView{HasConfig: a.HasConfig, ConfigYAML: a.ConfigYAML, ConfigError: a.ConfigError, Stack: a.Stack, Base: a.Base, GitHooks: a.GitHooks, ConfigCommitted: a.ConfigCommitted, Approvers: []approverID{}, ApproversAdded: []approverID{}, ApproversRemoved: []approverID{}}
	if a.HasConfig {
		v.ConfigSHA256 = evidence.HashBytes([]byte(a.ConfigYAML))
	}
	return v
}

func (s *Server) activation(w http.ResponseWriter, r *http.Request) {
	_, repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	a, err := core.ActivationState(repo)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, s.activationWithChange(repo, a))
}

// activationWithChange adds what committing the config would change on the base branch.
func (s *Server) activationWithChange(repo *gitx.Repo, a *core.Activation) activationView {
	v := viewActivation(a)
	if !a.HasConfig {
		return v
	}
	if ws, err := core.Open(repo.Root); err == nil {
		v.ConfigDiff, v.Approvers, v.ApproversAdded, v.ApproversRemoved = configChange(ws, []byte(a.ConfigYAML))
	}
	return v
}

func (s *Server) activate(w http.ResponseWriter, r *http.Request) {
	p, repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	var body struct {
		Stack      string `json:"stack"`
		Base       string `json:"base"`
		ForceHooks bool   `json:"forceHooks"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	if body.ForceHooks && !s.human(w, r, "Truehand: replace the git hooks of "+p.Name+" that Truehand did not install", nil) {
		return
	}
	a, err := core.Activate(repo, core.ActivateOptions{Stack: body.Stack, Base: body.Base, ForceHooks: body.ForceHooks})
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.broadcast(sseEvent{"changed", p.ID})
	writeJSON(w, s.activationWithChange(repo, a))
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	p, repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	var body struct {
		YAML string `json:"yaml"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := core.ValidateConfig([]byte(body.YAML)); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The gates decide what needs a human; rewriting them is as strong as approving.
	q := &appstate.QueuedAction{Kind: "config", Project: p.ID, Params: map[string]any{"yaml": body.YAML}}
	if !s.human(w, r, configPrompt(p), q) {
		return
	}
	s.writeConfig(w, p, repo, body.YAML)
}

func configPrompt(p appstate.Project) string {
	return "Truehand: change the gates of " + p.Name + " (.truehand/config.yaml)"
}

func (s *Server) writeConfig(w http.ResponseWriter, p appstate.Project, repo *gitx.Repo, yaml string) {
	if err := core.WriteConfig(repo, []byte(yaml)); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.broadcast(sseEvent{"changed", p.ID})
	a, err := core.ActivationState(repo)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, s.activationWithChange(repo, a))
}

// gitEnv never lets git stop to ask for credentials: nobody would see the prompt.
func gitEnv(pathPrefix string) []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1")
	if pathPrefix != "" {
		for i, kv := range env {
			if strings.HasPrefix(kv, "PATH=") {
				env[i] = "PATH=" + pathPrefix + string(os.PathListSeparator) + strings.TrimPrefix(kv, "PATH=")
			}
		}
	}
	return env
}

func (s *Server) fetch(w http.ResponseWriter, r *http.Request) {
	p, repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	out, err := s.Run(ctx, repo.Root, gitEnv(""), "git", "fetch", "--prune", "--all")
	if err != nil {
		writeJSONStatus(w, http.StatusBadGateway, map[string]any{"error": "git fetch failed", "output": string(out)})
		return
	}
	s.broadcast(sseEvent{"changed", p.ID})
	writeJSON(w, map[string]any{"ok": true, "output": string(out)})
}

const (
	codeConfigNotPushed    = "config-not-pushed"
	codeConfigNotCommitted = "config-not-committed"
	configCommitMessage    = "chore(truehand): add truehand config"
)

func baseRef(ws *core.Workspace) string {
	if ws.Config.Base != "" {
		return ws.Config.Base
	}
	return "HEAD"
}

// configOnBase reports whether the base branch has a config, which task worktrees need.
func configOnBase(ws *core.Workspace) bool { return configOn(ws, baseRef(ws)) }

func configOn(ws *core.Workspace, ref string) bool {
	_, err := ws.Repo.Git("cat-file", "-e", ref+":"+filepath.ToSlash(config.Path("")))
	return err == nil
}

// localBase is the local branch of a remote base (main for origin/main), or "".
func localBase(ws *core.Workspace) string {
	if b, ok := strings.CutPrefix(baseRef(ws), "origin/"); ok {
		return b
	}
	return ""
}

// commitConfig commits .truehand/config.yaml alone, on the base branch, with a fixed message,
// and only the content the person was shown (configSha256): the gates and approvers it puts
// on the base branch are what CI trusts. It is a human action like approving: from a phone
// it waits for the Mac.
func (s *Server) commitConfig(w http.ResponseWriter, r *http.Request) {
	p, repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	var body struct {
		ConfigSHA256 string `json:"configSha256"`
		ThisMacKey   string `json:"thisMacKey"`
	}
	if !decode(w, r, &body) {
		return
	}
	ws, err := core.Open(p.Root)
	if err != nil {
		httpError(w, http.StatusConflict, err.Error())
		return
	}
	if _, err := shownConfig(ws, body.ConfigSHA256); err != nil {
		writeJSONStatus(w, http.StatusConflict, err)
		return
	}
	if err := commitConfigCheck(ws); err != nil {
		writeJSONStatus(w, http.StatusConflict, err)
		return
	}
	shown, _ := shownConfig(ws, body.ConfigSHA256)
	_, now, added, removed := configChange(ws, shown)
	if !s.human(w, r, commitPrompt(p, body.ConfigSHA256, macKeyOf(r, body.ThisMacKey), now, added, removed), &appstate.QueuedAction{Kind: "commit-config", Project: p.ID, Params: map[string]any{"configSha256": body.ConfigSHA256}}) {
		return
	}
	s.writeCommitConfig(w, p, repo, body.ConfigSHA256)
}

// shownConfig returns the config on disk if it is still the one with that hash.
func shownConfig(ws *core.Workspace, sha string) ([]byte, *codedError) {
	b, err := os.ReadFile(config.Path(ws.Repo.Root))
	if err != nil || sha == "" || evidence.HashBytes(b) != sha {
		return nil, &codedError{"the config changed since you read it: reload and read it again", "config-changed"}
	}
	if err := core.ValidateConfig(b); err != nil {
		return nil, &codedError{err.Error(), "config-invalid"}
	}
	return b, nil
}

type codedError struct {
	Message string `json:"error"`
	Code    string `json:"code"`
}

func (e *codedError) Error() string { return e.Message }

func commitConfigCheck(ws *core.Workspace) *codedError {
	rel := filepath.ToSlash(config.Path(""))
	if configOnBase(ws) {
		if out, _ := ws.Repo.Git("status", "--porcelain", "--", rel); strings.TrimSpace(out) == "" {
			return &codedError{"the config is already committed", "nothing-to-commit"}
		}
	}
	branch, _ := ws.Repo.Git("symbolic-ref", "--short", "-q", "HEAD")
	if base := strings.TrimPrefix(baseRef(ws), "origin/"); base != "HEAD" && strings.TrimSpace(branch) != base {
		return &codedError{fmt.Sprintf("check out %s first: the config belongs on the base branch", base), "not-on-base"}
	}
	staged, _ := ws.Repo.Git("diff", "--cached", "--name-only")
	for _, f := range strings.Fields(staged) {
		if f != rel {
			return &codedError{"other changes are staged: commit or unstage them first", "other-changes-staged"}
		}
	}
	return nil
}

func (s *Server) writeCommitConfig(w http.ResponseWriter, p appstate.Project, repo *gitx.Repo, sha string) {
	ws, err := core.Open(repo.Root)
	if err != nil {
		httpError(w, http.StatusConflict, err.Error())
		return
	}
	b, cerr := shownConfig(ws, sha)
	if cerr != nil {
		writeJSONStatus(w, http.StatusConflict, cerr)
		return
	}
	rel := filepath.ToSlash(config.Path(""))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// The verified bytes become the blob and the commit is made from the index: a file
	// rewritten after the check never gets in.
	hash := exec.CommandContext(ctx, "git", "hash-object", "-w", "--stdin")
	hash.Dir, hash.Stdin = repo.Root, bytes.NewReader(b)
	blob, err := hash.Output()
	if err != nil {
		httpError(w, http.StatusInternalServerError, "git hash-object: "+err.Error())
		return
	}
	// truehand first on PATH: the repo's commit-msg hook runs it.
	env := gitEnv(s.TruehandBin)
	out, err := s.Run(ctx, repo.Root, env, "git", "update-index", "--add", "--cacheinfo", "100644,"+strings.TrimSpace(string(blob))+","+rel)
	if err == nil {
		// The commit takes the whole index: nothing but the config may be in it.
		if staged, _ := repo.Git("diff", "--cached", "--name-only"); strings.TrimSpace(staged) != rel {
			_, _ = repo.Git("reset", "-q", "--", rel)
			writeJSONStatus(w, http.StatusConflict, &codedError{"other changes were staged meanwhile: nothing committed", "other-changes-staged"})
			return
		}
		var more []byte
		more, err = s.Run(ctx, repo.Root, env, "git", "commit", "-m", configCommitMessage)
		out = append(out, more...)
	}
	s.broadcast(sseEvent{"changed", p.ID})
	res := actResult{OK: err == nil, Output: string(out)}
	if err != nil {
		res.Error = "git commit failed: " + firstLine(string(out))
	} else if local := localBase(ws); local != "" {
		res.Warning = fmt.Sprintf("committed on %s; task worktrees start from %s, so push %s", local, baseRef(ws), local)
	}
	writeJSON(w, res)
}
