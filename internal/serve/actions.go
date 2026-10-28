package serve

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/agents"
	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/gate"
	"github.com/spaghetticoders/truehand/internal/gitx"
	"github.com/spaghetticoders/truehand/internal/humanauth"
	"github.com/spaghetticoders/truehand/internal/sign"
)

type actionArgs struct {
	PlanSHA256     string `json:"planSha256"`
	Reason         string `json:"reason"`
	State          string `json:"state"`
	SessionID      string `json:"sessionId"`
	PermissionMode string `json:"permissionMode"`
	// SignedIntent confirms approve-plan, push or open-pr in a repo that lists approvers.
	SignedIntent *sign.Signed `json:"signedIntent"`
	// NotifyAgent, on send-back, tells the live dev session to read the reason.
	NotifyAgent bool `json:"notifyAgent"`
}

type actResult struct {
	OK        bool        `json:"ok"`
	SessionID string      `json:"sessionId,omitempty"`
	URL       string      `json:"url,omitempty"`
	Queued    bool        `json:"queued,omitempty"`
	QueuedID  string      `json:"queuedId,omitempty"`
	Gate      []checkView `json:"gate,omitempty"`
	Output    string      `json:"output,omitempty"`
	Error     string      `json:"error,omitempty"`
	// PRRequest is set by a machine without gh: the Mac opens the PR for it.
	PRRequest *prRequest `json:"prRequest,omitempty"`
	Warning   string     `json:"warning,omitempty"`
	// Code and Reason name a failure without parsing Error, e.g. gh-failed + gh's own line.
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func gateChecks(res gate.Result) []checkView {
	out := []checkView{}
	for _, it := range res.Items {
		out = append(out, checkView{ID: it.Check, Gate: res.Target, Status: string(it.Status), Detail: it.Message})
	}
	return out
}

// act runs a task's action: the buttons of the UI, with their parameters.
func (s *Server) act(w http.ResponseWriter, r *http.Request) {
	l, ok := s.locate(w, r)
	if !ok {
		return
	}
	var args actionArgs
	if r.ContentLength != 0 && !decode(w, r, &args) {
		return
	}
	switch a := r.PathValue("action"); a {
	case actStartAgent:
		s.startTaskSession(w, l, agents.RoleDev, args.PermissionMode)
	case actStartReview:
		s.startTaskSession(w, l, agents.RoleReview, args.PermissionMode)
	case actOpenAgent:
		s.openAgent(w, l, args.SessionID)
	case actResumeAgent:
		s.resumeAgent(w, l, args.SessionID)
	case actApprovePlan:
		s.approvePlan(w, r, l, args)
	case actDeliver:
		s.moveTask(w, l, board.Delivered, "")
	case actSendBack:
		s.sendBack(w, l, args)
	case actPush:
		if _, err := pushBranch(l); err != nil {
			fail(w, err)
			return
		}
		if cerr := s.pushable(l, false); cerr != nil {
			writeJSONStatus(w, http.StatusConflict, cerr)
			return
		}
		if s.confirmed(w, r, l, sign.ActionPush, args.SignedIntent, pushPrompt(l), &appstate.QueuedAction{Kind: "push", Project: l.Project.ID, Task: l.Task.ID}) {
			s.writePush(w, l)
		}
	case actOpenPR:
		if _, err := pushBranch(l); err != nil {
			fail(w, err)
			return
		}
		if cerr := s.pushable(l, true); cerr != nil {
			writeJSONStatus(w, http.StatusConflict, cerr)
			return
		}
		p := defaultPR(l)
		if s.confirmed(w, r, l, sign.ActionOpenPR, args.SignedIntent, prPrompt(l), &appstate.QueuedAction{Kind: "pr", Project: l.Project.ID, Task: l.Task.ID, Params: p.toMap()}) {
			s.writePR(w, l, p)
		}
	case actActivate:
		httpError(w, http.StatusBadRequest, "activate the project with POST /api/projects/{pid}/activate")
	default:
		httpError(w, http.StatusNotFound, fmt.Sprintf("unknown action %q", a))
	}
}

func (s *Server) startTaskSession(w http.ResponseWriter, l *located, role, mode string) {
	if !s.needAgents(w) {
		return
	}
	mode, err := s.permissionMode(mode)
	if err != nil {
		fail(w, err)
		return
	}
	cwd := l.WS.Repo.Root
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	if err := s.checkoutBusy(cwd, l.Task.ID, role, "", l.Worktree != nil); err != nil {
		fail(w, err)
		return
	}
	rec, err := s.Agents.Start(agents.StartRequest{Project: l.Project.ID, Task: l.Task.ID, Role: role, Cwd: cwd, PermissionMode: mode, Prompt: agents.DefaultPrompt(role, l.Task.ID)})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, actResult{OK: true, SessionID: rec.ID})
}

func (s *Server) taskSessions(l *located) []appstate.Session {
	var out []appstate.Session
	for _, x := range s.allSessions() {
		if x.Project == l.Project.ID && x.Task == l.Task.ID {
			out = append(out, x)
		}
	}
	return out
}

func (s *Server) openAgent(w http.ResponseWriter, l *located, sid string) {
	var pick *appstate.Session
	for _, x := range s.taskSessions(l) {
		x := x
		if !appstate.Live(x.Status) || (sid != "" && x.ID != sid) {
			continue
		}
		if pick == nil || x.Role == agents.RoleDev {
			pick = &x
		}
	}
	if pick == nil {
		httpError(w, http.StatusNotFound, "no running session for this task")
		return
	}
	writeJSON(w, actResult{OK: true, SessionID: pick.ID})
}

func (s *Server) resumeAgent(w http.ResponseWriter, l *located, sid string) {
	if !s.needAgents(w) {
		return
	}
	var pick *appstate.Session
	for _, x := range s.taskSessions(l) {
		x := x
		if sid != "" {
			if x.ID == sid {
				pick = &x
			}
			continue
		}
		if x.Role == agents.RoleDev && x.Resumable && (pick == nil || x.Started.After(pick.Started)) {
			pick = &x
		}
	}
	if pick == nil {
		httpError(w, http.StatusNotFound, "no session of this task to resume")
		return
	}
	rec, err := s.resume(*pick, 0, 0)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, actResult{OK: true, SessionID: rec.ID})
}

// resume continues an ended session; a review session never continues one that worked on
// the task it reviews.
func (s *Server) resume(rec appstate.Session, cols, rows uint16) (appstate.Session, error) {
	if rec.Task != "" {
		if err := s.reviewIsIndependent(rec); err != nil {
			return appstate.Session{}, err
		}
	}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	if err := s.checkoutBusy(rec.Cwd, rec.Task, rec.Role, rec.ID, s.inWorktree(rec)); err != nil {
		return appstate.Session{}, err
	}
	return s.Agents.Resume(rec.ID, cols, rows)
}

// confirmed is the human factor of push and open-pr: a signed intent where the repo lists
// approvers, else the person at the Mac (or the queue).
func (s *Server) confirmed(w http.ResponseWriter, r *http.Request, l *located, action string, sg *sign.Signed, reason string, q *appstate.QueuedAction) bool {
	if sg != nil {
		return s.verifyPushIntent(w, l, action, sg)
	}
	if len(l.approvers()) > 0 {
		signatureRequired(w, l)
		return false
	}
	return s.human(w, r, reason, q)
}

func (s *Server) approvePlan(w http.ResponseWriter, r *http.Request, l *located, args actionArgs) {
	if args.SignedIntent != nil {
		s.approveSigned(w, l, args.SignedIntent)
		return
	}
	if len(l.approvers()) > 0 {
		signatureRequired(w, l)
		return
	}
	sha := args.PlanSHA256
	if sha == "" {
		httpError(w, http.StatusBadRequest, "planSha256 is required")
		return
	}
	if l.Task.State != board.Planned {
		httpError(w, http.StatusConflict, fmt.Sprintf("task %s is %s, not planned", l.Task.ID, l.Task.State))
		return
	}
	if err := currentPlan(l.WS, l.Task.ID, sha); err != nil {
		httpError(w, http.StatusConflict, err.Error())
		return
	}
	q := &appstate.QueuedAction{Kind: "approve", Project: l.Project.ID, Task: l.Task.ID, PlanSHA256: sha}
	if !s.human(w, r, humanauth.ApprovalPrompt(l.Task.ID, l.Task.Title), q) {
		return
	}
	writeJSON(w, s.doApprove(l, sha, humanMethod(identityOf(r)), ""))
}

// doApprove records the approval of the plan with that hash and moves the task to approved,
// as `truehand approve` does.
func (s *Server) doApprove(l *located, sha, via, device string) actResult {
	ws := l.WS
	a := board.Approval{State: board.Approved, Name: ws.Repo.ConfigValue("user.name"), Email: ws.Repo.ConfigValue("user.email"), PlanSHA: sha, Via: via}
	if err := ws.Store.AppendApproval(l.Task.ID, a); err != nil {
		return actResult{Error: err.Error()}
	}
	detail := map[string]any{"via": "app"}
	if device != "" {
		detail["requested_from"] = device
	}
	_ = ws.Store.AppendEvent(l.Task.ID, board.Event{Type: "approved", Actor: "human:" + a.Email, Detail: detail})
	res, err := ws.Move(l.Task, board.Approved)
	s.broadcast(sseEvent{"changed", l.Project.ID})
	out := actResult{OK: err == nil, Gate: gateChecks(res)}
	if err != nil {
		out.Error = err.Error()
	}
	return out
}

// moveTask moves a task; a closed gate answers 409 with its checks.
func (s *Server) moveTask(w http.ResponseWriter, l *located, target board.State, devSession string) {
	res, err := l.WS.Move(l.Task, target)
	var me *core.MoveError
	if errors.As(err, &me) {
		writeJSONStatus(w, http.StatusConflict, actResult{Error: err.Error(), Gate: gateChecks(me.Result)})
		return
	}
	if err != nil {
		httpError(w, http.StatusConflict, err.Error())
		return
	}
	s.broadcast(sseEvent{"changed", l.Project.ID})
	writeJSON(w, actResult{OK: true, Gate: gateChecks(res), SessionID: devSession})
}

// sendBack moves a task back with its reason saved first as a comment. The answer names the
// live dev session, so the UI can offer to tell the agent.
func (s *Server) sendBack(w http.ResponseWriter, l *located, args actionArgs) {
	target, err := board.ParseState(args.State)
	if err != nil {
		httpError(w, http.StatusBadRequest, "state: "+err.Error())
		return
	}
	forward, err := board.CheckMove(l.Task.State, target)
	if err != nil || forward || target == l.Task.State {
		httpError(w, http.StatusBadRequest, fmt.Sprintf("send-back goes to an earlier state than %s", l.Task.State))
		return
	}
	if strings.TrimSpace(args.Reason) == "" {
		httpError(w, http.StatusBadRequest, "say why the task goes back: reason is required")
		return
	}
	if err := l.WS.Comment(l.Task.ID, args.Reason); err != nil {
		fail(w, err)
		return
	}
	dev := ""
	for _, x := range s.taskSessions(l) {
		if x.Role == agents.RoleDev && appstate.Live(x.Status) {
			dev = x.ID
		}
	}
	res, err := l.WS.Move(l.Task, target)
	if err != nil {
		httpError(w, http.StatusConflict, err.Error())
		return
	}
	s.broadcast(sseEvent{"changed", l.Project.ID})
	// The reason is the comment saved above; the agent is only told where to look, so no
	// second comment buries it.
	if args.NotifyAgent && dev != "" {
		if p, err := s.taskProc(l, dev); err == nil {
			_ = paste(p, fmt.Sprintf("Task %s went back to %s: read the latest comment on it (truehand task show %s).", l.Task.ID, target, l.Task.ID))
		}
	}
	writeJSON(w, actResult{OK: true, Gate: gateChecks(res), SessionID: dev})
}

func (s *Server) takeQueued(qid string, remove bool) (appstate.QueuedAction, error) {
	var out appstate.QueuedAction
	err := s.Store.UpdateAuth(func(a *appstate.Auth) error {
		for i, q := range a.Queued {
			if q.ID == qid {
				out = q
				if remove {
					a.Queued = append(a.Queued[:i], a.Queued[i+1:]...)
				}
				return nil
			}
		}
		return appstate.ErrNotFound
	})
	if err == nil && remove {
		s.waitingDirty.Store(true)
	}
	return out, err
}

// confirmQueued carries out an action asked for from a phone, now that the person is at the
// Mac: the OS authenticates them for that very action.
func (s *Server) confirmQueued(w http.ResponseWriter, r *http.Request) {
	q, err := s.takeQueued(r.PathValue("qid"), false)
	if err != nil {
		fail(w, err)
		return
	}
	if time.Since(q.Time) > queuedTTL {
		_, _ = s.takeQueued(q.ID, true)
		httpError(w, http.StatusNotFound, "the request expired: ask again")
		return
	}
	st, err := s.Store.State()
	if err != nil {
		fail(w, err)
		return
	}
	p, ok := st.Project(q.Project)
	if !ok {
		_, _ = s.takeQueued(q.ID, true)
		httpError(w, http.StatusNotFound, "the project is no longer registered")
		return
	}
	if q.Kind == "config" {
		s.confirmConfig(w, r, *p, q)
		return
	}
	if q.Kind == "commit-config" {
		s.confirmCommitConfig(w, r, *p, q)
		return
	}
	var body struct {
		SignedIntent *sign.Signed `json:"signedIntent"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	l, err := s.locateIn(*p, q.Task)
	if err != nil {
		fail(w, err)
		return
	}
	var reason string
	switch q.Kind {
	case "approve":
		if err := currentPlan(l.WS, l.Task.ID, q.PlanSHA256); err != nil || l.Task.State != board.Planned {
			_, _ = s.takeQueued(q.ID, true)
			if err == nil {
				err = fmt.Errorf("task %s is no longer planned", l.Task.ID)
			}
			httpError(w, http.StatusConflict, err.Error())
			return
		}
		reason = humanauth.ApprovalPrompt(l.Task.ID, l.Task.Title)
	case "push":
		reason = pushPrompt(l)
	case "pr":
		reason = prPrompt(l)
	default:
		httpError(w, http.StatusConflict, "unknown queued action")
		return
	}
	signed := body.SignedIntent != nil
	switch {
	case signed && q.Kind == "approve":
	case signed:
		action := map[string]string{"push": sign.ActionPush, "pr": sign.ActionOpenPR}[q.Kind]
		if !s.verifyPushIntent(w, l, action, body.SignedIntent) {
			return
		}
	case len(l.approvers()) > 0:
		signatureRequired(w, l)
		return
	case !s.human(w, r, reason, nil):
		return
	}
	if _, err := s.takeQueued(q.ID, true); err != nil {
		fail(w, err)
		return
	}
	switch q.Kind {
	case "approve":
		if signed {
			s.approveSigned(w, l, body.SignedIntent)
			return
		}
		writeJSON(w, s.doApprove(l, q.PlanSHA256, humanMethod(identityOf(r)), q.Device))
	case "push":
		s.writePush(w, l)
	case "pr":
		var p prParams
		p.fromMap(q.Params)
		s.writePR(w, l, p)
	}
}

func (s *Server) confirmConfig(w http.ResponseWriter, r *http.Request, p appstate.Project, q appstate.QueuedAction) {
	yaml, _ := q.Params["yaml"].(string)
	repo, err := gitx.Open(p.Root)
	if err != nil {
		fail(w, err)
		return
	}
	if err := core.ValidateConfig([]byte(yaml)); err != nil {
		_, _ = s.takeQueued(q.ID, true)
		httpError(w, http.StatusConflict, err.Error())
		return
	}
	if !s.human(w, r, configPrompt(p), nil) {
		return
	}
	if _, err := s.takeQueued(q.ID, true); err != nil {
		fail(w, err)
		return
	}
	s.writeConfig(w, p, repo, yaml)
}

func (s *Server) confirmCommitConfig(w http.ResponseWriter, r *http.Request, p appstate.Project, q appstate.QueuedAction) {
	var body struct {
		ThisMacKey string `json:"thisMacKey"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	ws, err := core.Open(p.Root)
	if err != nil {
		httpError(w, http.StatusConflict, err.Error())
		return
	}
	sha, _ := q.Params["configSha256"].(string)
	shown, cerr := shownConfig(ws, sha)
	if cerr != nil {
		_, _ = s.takeQueued(q.ID, true)
		writeJSONStatus(w, http.StatusConflict, cerr)
		return
	}
	if cerr := commitConfigCheck(ws); cerr != nil {
		_, _ = s.takeQueued(q.ID, true)
		writeJSONStatus(w, http.StatusConflict, cerr)
		return
	}
	_, now, added, removed := configChange(ws, shown)
	if !s.human(w, r, commitPrompt(p, sha, macKeyOf(r, body.ThisMacKey), now, added, removed), nil) {
		return
	}
	if _, err := s.takeQueued(q.ID, true); err != nil {
		fail(w, err)
		return
	}
	s.writeCommitConfig(w, p, ws.Repo, sha)
}

func (s *Server) discardQueued(w http.ResponseWriter, r *http.Request) {
	if _, err := s.takeQueued(r.PathValue("qid"), true); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) queued(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.queuedViews()) }
