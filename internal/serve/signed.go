package serve

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/gate"
	"github.com/spaghetticoders/truehand/internal/sign"
)

// Signed intents: in a repo that lists approvers on its base branch, approving a plan,
// pushing and opening a PR need an intent signed by an approver's key (the Mac app's Secure
// Enclave key behind Touch ID). The signature is the human factor, so it works the same on
// this Mac and, forwarded, on a remote machine.

const (
	codeSignatureRequired = "signature-required"
	codeNoApprovers       = "no-approvers"
	codeSignatureRefused  = "signature-refused"
)

func (l *located) approvers() []string {
	aps, err := l.WS.Approvers(l.Task)
	if err != nil {
		return nil
	}
	var ids []string
	for _, a := range aps {
		ids = append(ids, a.ID)
	}
	return ids
}

// signatureRequired names the repo's approvers, so the app knows which of its keys to use.
func (s *Server) signatureRequired(w http.ResponseWriter, l *located) {
	aps, _ := l.WS.Approvers(l.Task)
	writeJSONStatus(w, http.StatusConflict, map[string]any{"error": "this repo lists approvers on its base branch: confirm with a signed intent", "code": codeSignatureRequired, "approvers": l.approvers(), "approverKeys": s.approverKeys(aps)})
}

type approverKey struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	RPID string `json:"rpId,omitempty"`
	// CredentialID is the passkey registered at this server with the approver's key, if any.
	CredentialID string `json:"credentialId,omitempty"`
}

func (s *Server) approverKeys(aps []config.Approver) []approverKey {
	ps, _ := s.Store.Passkeys()
	out := []approverKey{}
	for _, a := range aps {
		k := approverKey{ID: a.ID, Name: a.Name, RPID: a.RPID}
		if ps != nil {
			fp := keyFingerprint(a.PublicKey)
			for _, p := range ps.Passkeys {
				if fp != "invalid" && keyFingerprint(p.PublicKey) == fp && (a.RPID == "" || a.RPID == p.RPID) {
					k.CredentialID = p.ID
				}
			}
		}
		out = append(out, k)
	}
	return out
}

// allowFor is what navigator.credentials.get needs to sign with approver id: its rp_id and
// the passkey registered here with its key, when there is one.
func (s *Server) allowFor(aps []config.Approver, id string) map[string]any {
	for _, k := range s.approverKeys(aps) {
		if k.ID == id {
			allow := []map[string]string{}
			if k.CredentialID != "" {
				allow = append(allow, map[string]string{"type": "public-key", "id": k.CredentialID})
			}
			return map[string]any{"rpId": k.RPID, "allowCredentials": allow}
		}
	}
	return map[string]any{"allowCredentials": []map[string]string{}}
}

func signedError(w http.ResponseWriter, err error) {
	if errors.Is(err, core.ErrNoApprovers) {
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": "configure approvers in .truehand/config.yaml on the base branch to approve with a signature", "code": codeNoApprovers})
		return
	}
	writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": "signed intent refused: " + err.Error(), "code": codeSignatureRefused})
}

func headOf(l *located) (string, error) {
	out, err := l.WS.Repo.Git("rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// intent builds what the Mac app signs for an action on a task: this repo, the plan as it is
// now, the commit for push and open-pr, a fresh nonce and the time.
func (s *Server) intent(w http.ResponseWriter, r *http.Request) {
	l, ok := s.locate(w, r)
	if !ok {
		return
	}
	var body struct {
		Action   string `json:"action"`
		Approver string `json:"approver"`
	}
	if !decode(w, r, &body) {
		return
	}
	switch body.Action {
	case sign.ActionApprovePlan, sign.ActionPush, sign.ActionOpenPR:
	default:
		httpError(w, http.StatusBadRequest, "action must be approve-plan, push or open-pr")
		return
	}
	ids := l.approvers()
	if len(ids) == 0 {
		signedError(w, core.ErrNoApprovers)
		return
	}
	known := false
	for _, id := range ids {
		known = known || id == body.Approver
	}
	if !known {
		httpError(w, http.StatusBadRequest, "approver must be one of: "+strings.Join(ids, ", "))
		return
	}
	b, err := os.ReadFile(l.WS.Store.PlanPath(l.Task.ID))
	if err != nil {
		httpError(w, http.StatusConflict, "the task has no plan.md")
		return
	}
	in := sign.Intent{
		Action: body.Action, Repo: gate.RepoID(l.WS.Repo), Task: l.Task.ID, PlanSHA256: evidence.HashBytes(b),
		Nonce: sign.NewNonce(), Time: time.Now().UTC().Format("2006-01-02T15:04:05Z"), Approver: body.Approver,
	}
	if body.Action != sign.ActionApprovePlan {
		if in.Head, err = headOf(l); err != nil {
			fail(w, err)
			return
		}
	}
	branch := ""
	if body.Action != sign.ActionApprovePlan {
		branch, _ = pushBranch(l)
	}
	aps, _ := l.WS.Approvers(l.Task)
	out := s.allowFor(aps, body.Approver)
	out["intent"], out["message"], out["title"], out["branch"] = in, base64.StdEncoding.EncodeToString(in.Message()), l.Task.Title, branch
	writeJSON(w, out)
}

func (s *Server) approveSigned(w http.ResponseWriter, l *located, sg *sign.Signed) {
	if sg.Intent.Action != sign.ActionApprovePlan {
		httpError(w, http.StatusBadRequest, "the signed intent is not an approve-plan")
		return
	}
	if _, _, err := l.WS.ApproveSigned(l.Task.ID, *sg); err != nil {
		signedError(w, err)
		return
	}
	s.broadcast(sseEvent{"changed", l.Project.ID})
	writeJSON(w, actResult{OK: true})
}

// verifyPushIntent checks a signed push or open-pr for the commit about to leave, and spends
// its nonce.
func (s *Server) verifyPushIntent(w http.ResponseWriter, l *located, action string, sg *sign.Signed) bool {
	if sg.Intent.Action != action {
		httpError(w, http.StatusBadRequest, fmt.Sprintf("the signed intent is not a %s", action))
		return false
	}
	head, err := headOf(l)
	if err != nil {
		fail(w, err)
		return false
	}
	if _, err := l.WS.VerifyIntent(l.Task, action, head, *sg); err != nil {
		signedError(w, err)
		return false
	}
	// The repo's record of spent nonces lives in .git, where an agent can delete it; the
	// server's own record survives that.
	if err := s.spendIntent(sg.Intent.Nonce); err != nil {
		signedError(w, err)
		return false
	}
	return true
}

// prRequest is what a machine without gh hands back, so the Mac opens the PR with its own gh.
type prRequest struct {
	Repo  string `json:"repo"`
	Head  string `json:"head"`
	Base  string `json:"base,omitempty"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Draft bool   `json:"draft,omitempty"`
}

func (s *Server) prRequestOf(l *located, p prParams) (prRequest, error) {
	branch, err := pushBranch(l)
	if err != nil {
		return prRequest{}, err
	}
	return prRequest{Repo: gate.RepoID(l.WS.Repo), Head: branch, Base: strings.TrimPrefix(l.WS.Config.Base, "origin/"), Title: p.Title, Body: p.Body, Draft: p.Draft}, nil
}

// openPRHere runs gh on this machine for a PR that a remote machine asked for.
func (s *Server) openPRHere(ctx context.Context, req prRequest) (string, []byte, error) {
	if _, err := s.LookPath("gh"); err != nil {
		return "", []byte("gh is not installed on this Mac: install it and run `gh auth login`"), err
	}
	args := []string{"pr", "create", "--repo=" + req.Repo, "--head=" + req.Head, "--title=" + req.Title, "--body=" + req.Body}
	if req.Base != "" {
		args = append(args, "--base="+req.Base)
	}
	if req.Draft {
		args = append(args, "--draft")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := s.Run(ctx, "", gitEnv(""), "gh", args...)
	if err != nil {
		return "", out, err
	}
	return lastLine(string(out)), out, nil
}
