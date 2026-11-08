package serve

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/spaghetticoders/truehand/internal/appstate"
)

// Restacking moves a task's base onto the commit that closes the task it builds on. Which
// work counts as already reviewed is a human decision, never an agent's.

type restackCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

type restackView struct {
	Task      string          `json:"task"`
	From      string          `json:"from"`
	Point     string          `json:"point"`
	StackedOn string          `json:"stackedOn"`
	PredState string          `json:"predState"`
	PredTitle string          `json:"predTitle"`
	Covered   []string        `json:"covered"` // the tasks under the point: stackedOn and what it stacks on
	Skipped   []restackCommit `json:"skipped"` // commits from the old base to the point, no longer the task's own
	// Ignore are files the predecessor's evidence saw outside its change: confirming the
	// restack (IgnoreHash) says they are not part of it.
	Ignore      []string `json:"ignore"`
	IgnoreCount int      `json:"ignoreCount"`
	IgnoreHash  string   `json:"ignoreHash"`
}

func (s *Server) restackView(l *located) (*restackView, error) {
	plan, err := l.WS.PlanRestack(l.Task.ID)
	if err != nil {
		return nil, err
	}
	point, pred := plan.Point, plan.Pred
	v := &restackView{Task: l.Task.ID, From: l.Task.Base, Point: point, StackedOn: pred.ID, PredState: string(pred.State), PredTitle: pred.Title, Covered: []string{}, Skipped: []restackCommit{},
		Ignore: append([]string{}, plan.Ignore...), IgnoreCount: len(plan.Ignore), IgnoreHash: plan.IgnoreHash}
	seen := map[string]bool{}
	for t := pred; t != nil && !seen[t.ID]; {
		seen[t.ID] = true
		v.Covered = append(v.Covered, t.ID)
		if t.StackedOn == "" {
			break
		}
		next, err := l.WS.Store.Load(t.StackedOn)
		if err != nil {
			break
		}
		t = next
	}
	if l.Task.Base != "" {
		out, err := l.WS.Repo.Git("log", "--reverse", "--format=%H%x00%s", l.Task.Base+".."+point)
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				if sha, subject, ok := strings.Cut(line, "\x00"); ok {
					v.Skipped = append(v.Skipped, restackCommit{SHA: sha, Subject: subject})
				}
			}
		}
	}
	return v, nil
}

func (s *Server) getRestack(w http.ResponseWriter, r *http.Request) {
	l, ok := s.locate(w, r)
	if !ok {
		return
	}
	v, err := s.restackView(l)
	if err != nil {
		writeJSONStatus(w, http.StatusConflict, &codedError{err.Error(), "no-restack-point"})
		return
	}
	writeJSON(w, v)
}

func restackPrompt(l *located, v *restackView) string {
	p := fmt.Sprintf("Truehand: stack task %s on %s (%s) at %.12s; %d commit(s) become %s's, already reviewed.",
		promptText(l.Task.ID), promptText(v.StackedOn), v.PredState, v.Point, len(v.Skipped), promptText(v.StackedOn))
	if v.IgnoreCount > 0 {
		p += fmt.Sprintf(" %d file(s) its evidence saw are set aside as not its work (list %.12s).", v.IgnoreCount, v.IgnoreHash)
	}
	return p
}

// postRestack restacks as the person was shown: point and stackedOn must still be what the
// preview gives (409 restack-moved otherwise).
func (s *Server) postRestack(w http.ResponseWriter, r *http.Request) {
	l, ok := s.locate(w, r)
	if !ok {
		return
	}
	var body struct {
		Point      string `json:"point"`
		StackedOn  string `json:"stackedOn"`
		IgnoreHash string `json:"ignoreHash"`
	}
	if !decode(w, r, &body) {
		return
	}
	v, err := s.restackView(l)
	if err != nil {
		writeJSONStatus(w, http.StatusConflict, &codedError{err.Error(), "no-restack-point"})
		return
	}
	if body.Point != v.Point || body.StackedOn != v.StackedOn || body.IgnoreHash != v.IgnoreHash {
		writeJSONStatus(w, http.StatusConflict, &codedError{fmt.Sprintf("the restack point is now %.12s on %s: look at it again", v.Point, v.StackedOn), "restack-moved"})
		return
	}
	q := &appstate.QueuedAction{Kind: "restack", Project: l.Project.ID, Task: l.Task.ID, Params: map[string]any{"point": v.Point, "stackedOn": v.StackedOn, "ignoreHash": v.IgnoreHash}}
	if !s.human(w, r, restackPrompt(l, v), q) {
		return
	}
	s.doRestack(w, l, v.Point, v.StackedOn, v.IgnoreHash, restackVia(identityOf(r)), "")
}

// restackVia names how the person confirmed: their passkey, or Touch ID at the Mac ("queue"
// when they confirmed at the Mac what a phone asked for).
func restackVia(id identity) string {
	if id.proof != nil && id.proof.owner != "" {
		return "passkey"
	}
	return "touchid"
}

// doRestack records the person who confirmed as the restack's author, as approvals do.
func (s *Server) doRestack(w http.ResponseWriter, l *located, point, pred, ignoreHash, via, device string) {
	detail := map[string]any{"via": via}
	if device != "" {
		detail["requested_from"] = device
	}
	actor := "human:" + l.WS.Repo.ConfigValue("user.email")
	t, err := l.WS.RestackWith(l.Task.ID, point, pred, ignoreHash, actor, detail)
	if err != nil {
		writeJSONStatus(w, http.StatusConflict, &codedError{err.Error(), "restack-moved"})
		return
	}
	s.broadcast(sseEvent{"changed", l.Project.ID})
	writeJSON(w, map[string]any{"ok": true, "task": t.ID, "base": t.Base, "stackedOn": t.StackedOn})
}

// confirmRestack is a queued restack confirmed at the Mac: still the same point, or nothing.
func (s *Server) confirmRestack(w http.ResponseWriter, r *http.Request, p appstate.Project, q appstate.QueuedAction) {
	l, err := s.locateIn(p, q.Task)
	if err != nil {
		fail(w, err)
		return
	}
	point, _ := q.Params["point"].(string)
	pred, _ := q.Params["stackedOn"].(string)
	ignoreHash, _ := q.Params["ignoreHash"].(string)
	v, err := s.restackView(l)
	if err != nil || v.Point != point || v.StackedOn != pred || v.IgnoreHash != ignoreHash {
		_, _ = s.takeQueued(q.ID, true)
		writeJSONStatus(w, http.StatusConflict, &codedError{"the restack point changed since it was asked for: ask again", "restack-moved"})
		return
	}
	if !s.human(w, r, restackPrompt(l, v), nil) {
		return
	}
	if _, err := s.takeQueued(q.ID, true); err != nil {
		fail(w, err)
		return
	}
	s.doRestack(w, l, point, pred, ignoreHash, "queue", q.Device)
}
