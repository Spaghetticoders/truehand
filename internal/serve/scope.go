package serve

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/spaghetticoders/truehand/internal/appstate"
)

// Widening a task's scope says which files are its work: a human decision, like approving
// its plan. Only additions, and only files the task already changed outside its scope.

type scopeView struct {
	Task         string   `json:"task"`
	Scope        []string `json:"scope"`
	Outside      []string `json:"outside"`
	OutsideCount int      `json:"outsideCount"`
	Add          []string `json:"add"`
}

func (s *Server) scopeView(l *located) (*scopeView, error) {
	plan, err := l.WS.PlanScope(l.Task.ID)
	if err != nil {
		return nil, err
	}
	v := &scopeView{Task: l.Task.ID, Scope: append([]string{}, l.Task.Scope...), Outside: append([]string{}, plan.Outside...), Add: append([]string{}, plan.Add...)}
	v.OutsideCount = len(v.Outside)
	return v, nil
}

func (s *Server) getScope(w http.ResponseWriter, r *http.Request) {
	l, ok := s.locate(w, r)
	if !ok {
		return
	}
	v, err := s.scopeView(l)
	if err != nil {
		writeJSONStatus(w, http.StatusConflict, &codedError{err.Error(), "scope-unavailable"})
		return
	}
	writeJSON(w, v)
}

// readable shows the files add names as paths (Add is Outside with glob metacharacters
// escaped, in the same order).
func (v *scopeView) readable(add []string) []string {
	out := make([]string, 0, len(add))
	for _, a := range add {
		if i := slices.Index(v.Add, a); i >= 0 && i < len(v.Outside) {
			a = v.Outside[i]
		}
		out = append(out, a)
	}
	return out
}

func scopePrompt(l *located, add []string) string {
	shown := add
	if len(shown) > 5 {
		shown = shown[:5]
	}
	p := fmt.Sprintf("Truehand: add %d file(s) to the scope of task %s: %s", len(add), promptText(l.Task.ID), promptText(strings.Join(shown, ", ")))
	if len(add) > len(shown) {
		p += fmt.Sprintf(" and %d more", len(add)-len(shown))
	}
	return p + "."
}

// postScope widens the scope by add, which must be files the preview lists outside it now
// (409 scope-moved otherwise). The owner's signature covers the body, so the globs.
func (s *Server) postScope(w http.ResponseWriter, r *http.Request) {
	l, ok := s.locate(w, r)
	if !ok {
		return
	}
	var body struct {
		Add []string `json:"add"`
	}
	if !decode(w, r, &body) {
		return
	}
	v, err := s.scopeView(l)
	if err != nil {
		writeJSONStatus(w, http.StatusConflict, &codedError{err.Error(), "scope-unavailable"})
		return
	}
	if len(v.Outside) == 0 {
		writeJSONStatus(w, http.StatusConflict, &codedError{"no changed file is outside the task's scope", "nothing-outside"})
		return
	}
	if len(body.Add) == 0 {
		httpError(w, http.StatusBadRequest, "add names the files to add")
		return
	}
	for _, g := range body.Add {
		if !slices.Contains(v.Add, g) {
			writeJSONStatus(w, http.StatusConflict, &codedError{fmt.Sprintf("%s is not among the files outside the scope now: look at it again", g), "scope-moved"})
			return
		}
	}
	q := &appstate.QueuedAction{Kind: "scope", Project: l.Project.ID, Task: l.Task.ID, Params: map[string]any{"add": strings.Join(body.Add, "\n")}}
	if !s.human(w, r, scopePrompt(l, v.readable(body.Add)), q) {
		return
	}
	s.doWidenScope(w, l, body.Add, restackVia(identityOf(r)), "")
}

func (s *Server) doWidenScope(w http.ResponseWriter, l *located, add []string, via, device string) {
	detail := map[string]any{"via": via}
	if device != "" {
		detail["requested_from"] = device
	}
	t, err := l.WS.WidenScope(l.Task.ID, add, "human:"+l.WS.Repo.ConfigValue("user.email"), detail)
	if err != nil {
		writeJSONStatus(w, http.StatusConflict, &codedError{err.Error(), "scope-refused"})
		return
	}
	s.broadcast(sseEvent{"changed", l.Project.ID})
	out := map[string]any{"ok": true, "scope": t.Scope, "gate": []checkView{}}
	if next := t.State.Next(); next != "" {
		if res, err := l.WS.Check(t, next); err == nil {
			out["gate"] = gateChecks(res)
		}
	}
	writeJSON(w, out)
}

// confirmScope is a queued widening confirmed at the Mac: still files outside the scope.
func (s *Server) confirmScope(w http.ResponseWriter, r *http.Request, p appstate.Project, q appstate.QueuedAction) {
	l, err := s.locateIn(p, q.Task)
	if err != nil {
		fail(w, err)
		return
	}
	raw, _ := q.Params["add"].(string)
	add := strings.Split(raw, "\n")
	v, err := s.scopeView(l)
	ok := err == nil && raw != ""
	for _, g := range add {
		ok = ok && slices.Contains(v.Add, g)
	}
	if !ok {
		_, _ = s.takeQueued(q.ID, true)
		writeJSONStatus(w, http.StatusConflict, &codedError{"the files outside the scope changed since it was asked for: ask again", "scope-moved"})
		return
	}
	if !s.human(w, r, scopePrompt(l, v.readable(add)), nil) {
		return
	}
	if _, err := s.takeQueued(q.ID, true); err != nil {
		fail(w, err)
		return
	}
	s.doWidenScope(w, l, add, "queue", q.Device)
}
