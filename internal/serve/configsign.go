package serve

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/gate"
	"github.com/spaghetticoders/truehand/internal/gitx"
	"github.com/spaghetticoders/truehand/internal/sign"
)

// Signed config changes: once the base's committed config lists approvers, committing a new
// config on it and pushing the base need one of them to sign, like a task's push. Before
// that (the first enrolment) a human action does: an owner's passkey or the Mac.

func revParse(repo *gitx.Repo, ref string) string {
	out, err := repo.Git("rev-parse", "-q", "--verify", ref+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func committedApprovers(repo *gitx.Repo, commit string) []config.Approver {
	if commit == "" {
		return nil
	}
	aps, _ := gate.CommittedApprovers(repo, commit)
	return aps
}

func committedApproverIDs(repo *gitx.Repo, commit string) []string {
	aps := committedApprovers(repo, commit)
	var ids []string
	for _, a := range aps {
		ids = append(ids, a.ID)
	}
	return ids
}

// pushBaseRefs are the commits a push of the base goes from (the remote's tip, asked of the
// remote itself: a local ref is the agents' to move) and to (the local base branch).
func (s *Server) pushBaseRefs(ctx context.Context, ws *core.Workspace) (from, head, local string, cerr *codedError) {
	local = localBase(ws)
	if local == "" {
		return "", "", "", &codedError{"the base is a local branch: there is nothing to push", "base-not-remote"}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := s.Run(ctx, ws.Repo.Root, gitEnv(""), "git", "ls-remote", "--exit-code", "origin", "refs/heads/"+local)
	if f := strings.Fields(string(out)); err == nil && len(f) >= 1 && commitHex(f[0]) {
		from = f[0]
	} else {
		return "", "", "", &codedError{"cannot read " + local + " on origin: " + firstLine(string(out)), "base-unknown"}
	}
	if head = revParse(ws.Repo, local); head == "" {
		return "", "", "", &codedError{local + " is missing", "base-unknown"}
	}
	if revParse(ws.Repo, from) == "" {
		return "", "", "", &codedError{"origin's " + local + " moved on to commits not fetched yet: fetch, then look at the push again", "base-behind"}
	}
	if from == head {
		return "", "", "", &codedError{local + " is already pushed", "nothing-to-push"}
	}
	if _, err := ws.Repo.Git("merge-base", "--is-ancestor", from, head); err != nil {
		return "", "", "", &codedError{fmt.Sprintf("%s does not descend from origin's %s: pull first", local, local), "base-diverged"}
	}
	return from, head, local, nil
}

func commitHex(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// configIntent builds what an approver signs to commit the shown config, or to push the base.
func (s *Server) configIntent(w http.ResponseWriter, r *http.Request) {
	p, repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	var body struct {
		Action       string `json:"action"`
		Approver     string `json:"approver"`
		ConfigSHA256 string `json:"configSha256"`
	}
	if !decode(w, r, &body) {
		return
	}
	ws, err := core.Open(p.Root)
	if err != nil {
		// The checked-out config does not load: nothing about the base can be trusted.
		writeJSONStatus(w, http.StatusConflict, &codedError{err.Error(), "config-invalid"})
		return
	}
	in := sign.Intent{Action: body.Action, Repo: gate.RepoID(repo), Task: sign.ConfigTask, Nonce: sign.NewNonce(), Time: time.Now().UTC().Format("2006-01-02T15:04:05Z"), Approver: body.Approver}
	var ids []string
	var aps []config.Approver
	var pushInfo *pushView
	switch body.Action {
	case sign.ActionCommitConfig:
		if _, cerr := shownConfig(ws, body.ConfigSHA256); cerr != nil {
			writeJSONStatus(w, http.StatusConflict, cerr)
			return
		}
		in.PlanSHA256, in.Head = body.ConfigSHA256, revParse(repo, "HEAD")
		aps = committedApprovers(repo, in.Head)
		ids = committedApproverIDs(repo, in.Head)
	case sign.ActionPushBase:
		from, head, _, cerr := s.pushBaseRefs(r.Context(), ws)
		if cerr != nil {
			writeJSONStatus(w, http.StatusConflict, cerr)
			return
		}
		b, ok, err := repo.BlobAt(head, config.Dir+"/"+config.File)
		if err != nil || !ok {
			httpError(w, http.StatusConflict, "the base has no committed config")
			return
		}
		in.PlanSHA256, in.Head = evidence.HashBytes(b), head
		aps = committedApprovers(repo, from)
		pushInfo = pushPreview(repo, from, head)
		ids = committedApproverIDs(repo, from)
	default:
		httpError(w, http.StatusBadRequest, "action must be commit-config or push-base")
		return
	}
	if len(ids) == 0 {
		if pushInfo != nil {
			// No approver yet: the push is a human action; the preview still shows what goes.
			writeJSONStatus(w, http.StatusConflict, map[string]any{"error": "configure approvers in .truehand/config.yaml on the base branch to approve with a signature", "code": codeNoApprovers, "push": pushInfo})
			return
		}
		signedError(w, core.ErrNoApprovers)
		return
	}
	if !contains(ids, body.Approver) {
		httpError(w, http.StatusBadRequest, "approver must be one of: "+strings.Join(ids, ", "))
		return
	}
	out := s.allowFor(aps, body.Approver)
	out["intent"], out["message"] = in, base64.StdEncoding.EncodeToString(in.Message())
	if pushInfo != nil {
		out["push"] = pushInfo
	}
	writeJSON(w, out)
}

// commitConfigSigned checks an approver's signature for commit-config; the config was
// parsed and validated by shownConfig before. It reports whether to go on.
func (s *Server) commitConfigSigned(w http.ResponseWriter, ws *core.Workspace, sha string, sg *sign.Signed) bool {
	if sg.Intent.Action != sign.ActionCommitConfig {
		httpError(w, http.StatusBadRequest, "the signed intent is not a commit-config")
		return false
	}
	if _, err := ws.VerifyCommitConfig(revParse(ws.Repo, "HEAD"), sha, *sg); err != nil {
		signedError(w, err)
		return false
	}
	if err := s.spendIntent(sg.Intent.Nonce); err != nil {
		signedError(w, err)
		return false
	}
	return true
}

// pushBase pushes the local base branch (after commit-config) to its remote: signed by an
// approver of origin's base config, or a human action while that lists none.
func (s *Server) pushBase(w http.ResponseWriter, r *http.Request) {
	p, repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	var body struct {
		SignedIntent *sign.Signed `json:"signedIntent"`
		// From and Head are the push the person was shown (GET push-base): origin's tip and
		// the commit pushed, which fix the commits in between. A human confirmation (the
		// owner's passkey signs this body) covers that push and no other.
		From string `json:"from"`
		Head string `json:"head"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	ws, err := core.Open(p.Root)
	if err != nil {
		// The checked-out config does not load: nothing about the base can be trusted.
		writeJSONStatus(w, http.StatusConflict, &codedError{err.Error(), "config-invalid"})
		return
	}
	from, head, local, cerr := s.pushBaseRefs(r.Context(), ws)
	if cerr != nil {
		writeJSONStatus(w, http.StatusConflict, cerr)
		return
	}
	if body.SignedIntent == nil {
		if body.Head != head || body.From != from {
			writeJSONStatus(w, http.StatusConflict, &codedError{fmt.Sprintf("the push is now %.12s..%.12s, not the one you were shown: look at it again", from, head), "base-moved"})
			return
		}
		// The same checks as an approver's signature gets (core.CheckPushBase).
		if _, err := ws.CheckPushBase(from, head); err != nil {
			pushBaseRefused(w, err)
			return
		}
	}
	if aps := committedApprovers(repo, from); len(aps) > 0 || body.SignedIntent != nil {
		if body.SignedIntent == nil {
			writeJSONStatus(w, http.StatusConflict, map[string]any{"error": "origin's base lists approvers: one of them signs the push", "code": codeSignatureRequired, "approvers": committedApproverIDs(repo, from), "approverKeys": s.approverKeys(aps)})
			return
		}
		if body.SignedIntent.Intent.Action != sign.ActionPushBase {
			httpError(w, http.StatusBadRequest, "the signed intent is not a push-base")
			return
		}
		if _, err := ws.VerifyPushBase(from, head, *body.SignedIntent); err != nil {
			var pe *core.PushBaseError
			if errors.As(err, &pe) {
				pushBaseRefused(w, err)
			} else {
				signedError(w, err)
			}
			return
		}
		if err := s.spendIntent(body.SignedIntent.Intent.Nonce); err != nil {
			signedError(w, err)
			return
		}
	} else if !s.human(w, r, s.pushBasePrompt(p.Name, local, from, head), nil) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	// Exactly the commit that was checked, and only over the tip it was checked against.
	out, err := s.Run(ctx, repo.Root, gitEnv(s.TruehandBin), "git", "push", "--force-with-lease=refs/heads/"+local+":"+from, "origin", head+":refs/heads/"+local)
	s.broadcast(sseEvent{"changed", p.ID})
	res := actResult{OK: err == nil, Output: string(out)}
	if err != nil {
		res.Error = "git push failed" + s.hookBinNote()
	}
	writeJSON(w, res)
}

type pushCommit struct {
	SHA     string   `json:"sha"`
	Subject string   `json:"subject"`
	Files   []string `json:"files"`
}

// pushView is what a push of the base sends, for the person to see before signing.
type pushView struct {
	From       string       `json:"from"`
	Head       string       `json:"head"`
	Commits    []pushCommit `json:"commits"`
	OnlyConfig bool         `json:"onlyConfig"`
}

func pushPreview(repo *gitx.Repo, from, head string) *pushView {
	v := &pushView{From: from, Head: head, Commits: []pushCommit{}, OnlyConfig: true}
	out, err := repo.Git("log", "--reverse", "--format=%H%x00%P%x00%s", from+".."+head)
	if err != nil {
		v.OnlyConfig = false
		return v
	}
	cfg := config.Dir + "/" + config.File
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.SplitN(line, "\x00", 3)
		if len(f) != 3 {
			continue
		}
		sha, parents, subject := f[0], f[1], f[2]
		// As core.CheckPushBase: a merge or a root commit is never "only the config".
		if len(strings.Fields(parents)) != 1 {
			v.OnlyConfig = false
		}
		c := pushCommit{SHA: sha, Subject: subject, Files: []string{}}
		files, _ := repo.Git("diff-tree", "--no-commit-id", "--name-only", "-r", "-m", "--root", sha)
		for _, f := range strings.Fields(files) {
			c.Files = append(c.Files, f)
			v.OnlyConfig = v.OnlyConfig && f == cfg
		}
		v.Commits = append(v.Commits, c)
	}
	return v
}

// pushBasePreview is what a push of the base would send, with who must confirm it.
func (s *Server) pushBasePreview(w http.ResponseWriter, r *http.Request) {
	p, repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	ws, err := core.Open(p.Root)
	if err != nil {
		// The checked-out config does not load: nothing about the base can be trusted.
		writeJSONStatus(w, http.StatusConflict, &codedError{err.Error(), "config-invalid"})
		return
	}
	from, head, _, cerr := s.pushBaseRefs(r.Context(), ws)
	if cerr != nil {
		writeJSONStatus(w, http.StatusConflict, cerr)
		return
	}
	aps := committedApprovers(repo, from)
	writeJSON(w, map[string]any{"push": pushPreview(repo, from, head), "approvers": committedApproverIDs(repo, from), "approverKeys": s.approverKeys(aps)})
}

// pushBasePrompt is the OS dialog's text for a push of the base, in the app's language, with
// the range checked against origin: what the person confirms is exactly what goes.
func (s *Server) pushBasePrompt(project, local, from, head string) string {
	lang := "it"
	if st, err := s.Store.State(); err == nil {
		lang = st.Settings.Lang
	}
	if lang == "en" {
		return fmt.Sprintf("Truehand: push the rules of %s (%s): %.7s → %.7s, only .truehand/config.yaml.", promptText(project), local, from, head)
	}
	return fmt.Sprintf("Truehand: invia le regole di %s (%s): %.7s → %.7s, solo .truehand/config.yaml.", promptText(project), local, from, head)
}

// pushBaseRefused answers a push of the base core refused: 409 with its code
// (not-only-config, config-invalid, base-moved; push-base-refused otherwise) and the
// technical reason in error.
func pushBaseRefused(w http.ResponseWriter, err error) {
	code := "push-base-refused"
	var pe *core.PushBaseError
	if errors.As(err, &pe) {
		code = pe.Code
	}
	writeJSONStatus(w, http.StatusConflict, &codedError{err.Error(), code})
}
