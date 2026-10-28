package serve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/gate"
	"github.com/spaghetticoders/truehand/internal/hands"
	"github.com/spaghetticoders/truehand/internal/plan"
	"github.com/spaghetticoders/truehand/internal/tickets"
)

// located is where a task lives: its own worktree, or the main checkout.
type located struct {
	Project  appstate.Project
	Main     *core.Workspace
	WS       *core.Workspace
	Worktree *core.Worktree
	Task     *board.Task
}

func (l *located) branch() string {
	if l.Worktree != nil {
		return l.Worktree.Branch
	}
	b, _ := l.Main.Repo.Git("symbolic-ref", "--short", "-q", "HEAD")
	return strings.TrimSpace(b)
}

func (s *Server) locateIn(p appstate.Project, id string) (*located, error) {
	if err := board.ValidID(id); err != nil {
		return nil, errBadRequest(err.Error())
	}
	main, err := core.Open(p.Root)
	if err != nil {
		return nil, err
	}
	l := &located{Project: p, Main: main, WS: main}
	wts, _ := core.ListWorktrees(main.Repo)
	for _, wt := range wts {
		if !wt.Main && wt.Task == id {
			wws, err := core.OpenWorktree(wt.Path)
			if err != nil {
				return nil, err
			}
			wt := wt
			l.WS, l.Worktree = wws, &wt
			break
		}
	}
	if l.Task, err = l.WS.Store.Load(id); err != nil {
		return nil, fmt.Errorf("task %s: %w", id, appstate.ErrNotFound)
	}
	return l, nil
}

func (s *Server) locate(w http.ResponseWriter, r *http.Request) (*located, bool) {
	p, ok := s.projectByID(w, r)
	if !ok {
		return nil, false
	}
	l, err := s.locateIn(p, r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return nil, false
	}
	return l, true
}

// summaryOf is the board card of one task, as the snapshot has it.
func (s *Server) summaryOf(p appstate.Project, id, branch string) (*projectData, taskSummary, bool) {
	if branch == "" {
		if l, err := s.locateIn(p, id); err == nil {
			pd, sum := s.summaryOne(l, nil)
			return pd, sum, true
		}
	}
	pd := s.load(p, s.allSessions())
	i, ok := pd.findTask(id, branch)
	if !ok {
		return pd, taskSummary{}, false
	}
	return pd, s.summarize(pd, pd.PV.Tasks[i], pd.PV.Waiting[i]), true
}

// summaryOne is one live task's card computed alone, as the project board would compute it,
// without building the whole board. next is the next gate's result when the caller already
// has it.
func (s *Server) summaryOne(l *located, next *gate.Result) (*projectData, taskSummary) {
	pd := &projectData{P: l.Project, Main: l.Main, Active: true, Prefix: l.Main.Config.TaskPrefix()}
	b, _ := l.Main.Repo.Git("symbolic-ref", "--short", "-q", "HEAD")
	pd.Branch = strings.TrimSpace(b)
	for _, x := range s.allSessions() {
		if x.Project == l.Project.ID {
			pd.Sessions = append(pd.Sessions, x)
		}
	}
	t := l.Task
	bt := core.BoardTask{Task: t, Active: l.WS.ActiveTask() == t.ID, Stale: l.WS.Stale(t), Merged: l.WS.Merged(t), Missing: []string{}}
	if target := t.State.Next(); target != "" {
		bt.Next = string(target)
		if next == nil {
			if res, err := l.WS.Check(t, target); err == nil {
				next = &res
			}
		}
		if next != nil {
			for _, it := range next.Items {
				if it.Status == gate.StatusFail {
					bt.Missing = append(bt.Missing, it.Check)
				}
			}
		}
	}
	pt := core.ProjectTask{BoardTask: bt}
	if l.Worktree != nil {
		pt.Worktree = l.WS.Repo.Root
	}
	var in core.WaitingInput
	if ins := l.WS.WaitingInputs(&core.BoardView{Tasks: []core.BoardTask{bt}}); len(ins) == 1 {
		in = ins[0]
	}
	return pd, s.summarize(pd, pt, in)
}

func (s *Server) task(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectByID(w, r)
	if !ok {
		return
	}
	id, branch := r.PathValue("id"), r.URL.Query().Get("branch")
	if branch != "" {
		pd, sum, ok := s.summaryOf(p, id, branch)
		if !ok {
			httpError(w, http.StatusNotFound, "no such task")
			return
		}
		d, err := pd.Main.BranchDetail(branch, id)
		if err != nil {
			httpError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, s.detail(pd, sum, d, pd.Main, nil))
		return
	}
	l, err := s.locateIn(p, id)
	if err != nil {
		fail(w, err)
		return
	}
	d, err := l.WS.Detail(l.Task)
	if err != nil {
		fail(w, err)
		return
	}
	pd, sum := s.summaryOne(l, d.NextGate)
	writeJSON(w, s.detail(pd, sum, d, l.WS, l.WS.Config))
}

// checkSource refuses ticket references that would read local files outside the repo.
func checkSource(ws *core.Workspace, source string) error {
	if source == "" {
		return nil
	}
	adapter, id := tickets.ParseRef(source, ws.Config.Tickets.Adapter)
	if adapter == "file" && (filepath.IsAbs(id) || id == ".." || strings.HasPrefix(filepath.Clean(id), ".."+string(filepath.Separator))) {
		return errBadRequest("a file ticket must be a path inside the repo")
	}
	return nil
}

func (s *Server) newTask(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectByID(w, r)
	if !ok {
		return
	}
	var body struct {
		Title          string   `json:"title"`
		Description    string   `json:"description"`
		ExternalTicket string   `json:"externalTicket"`
		Scope          []string `json:"scope"`
		ID             string   `json:"id"`
		Worktree       *bool    `json:"worktree"`
	}
	if !decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		httpError(w, http.StatusBadRequest, "title is required")
		return
	}
	main, err := core.Open(p.Root)
	if err != nil {
		httpError(w, http.StatusConflict, err.Error())
		return
	}
	if err := checkSource(main, body.ExternalTicket); err != nil {
		fail(w, err)
		return
	}
	id := body.ID
	if id == "" {
		if id, err = main.NextTaskID(); err != nil {
			fail(w, err)
			return
		}
	}
	if err := board.ValidID(id); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	ws := main
	var t *board.Task
	if body.Worktree == nil || *body.Worktree {
		if !configOnBase(main) {
			if local := localBase(main); local != "" && configOn(main, local) {
				writeJSONStatus(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf(".truehand/config.yaml is committed on %s but not on %s yet: push %s, task worktrees start from %s", local, baseRef(main), local, baseRef(main)), "code": codeConfigNotPushed})
				return
			}
			writeJSONStatus(w, http.StatusConflict, map[string]string{"error": ".truehand/config.yaml is not committed on the base branch: commit it first, task worktrees start from there", "code": codeConfigNotCommitted})
			return
		}
		if s.WorktreesDir == "" {
			httpError(w, http.StatusInternalServerError, "no worktrees directory")
			return
		}
		ws, t, err = main.NewTaskWorktree(filepath.Join(s.WorktreesDir, p.ID, id), id, body.Title, body.ExternalTicket, body.Scope...)
	} else {
		t, err = main.NewTask(id, body.Title, body.ExternalTicket, body.Scope...)
	}
	if errors.Is(err, core.ErrBadBranchName) {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		httpError(w, http.StatusConflict, err.Error())
		return
	}
	if strings.TrimSpace(body.Description) != "" {
		if err := ws.Store.SetDescription(t.ID, body.Description); err != nil {
			fail(w, err)
			return
		}
	}
	var fetchErr string
	if body.ExternalTicket != "" || strings.TrimSpace(body.Description) != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		if _, err := hands.FetchPlanContext(ctx, ws, t, ""); err != nil {
			fetchErr = err.Error()
		}
	}
	s.broadcast(sseEvent{"changed", p.ID})
	_, sum, _ := s.summaryOf(p, t.ID, "")
	writeJSONStatus(w, http.StatusCreated, struct {
		taskSummary
		TicketFetchError string `json:"ticketFetchError,omitempty"`
	}{sum, fetchErr})
}

func (s *Server) editTask(w http.ResponseWriter, r *http.Request) {
	l, ok := s.locate(w, r)
	if !ok {
		return
	}
	var body struct {
		Title          *string `json:"title"`
		Description    *string `json:"description"`
		ExternalTicket *string `json:"externalTicket"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.ExternalTicket != nil {
		if err := checkSource(l.WS, *body.ExternalTicket); err != nil {
			fail(w, err)
			return
		}
	}
	if _, _, err := l.WS.Edit(l.Task.ID, core.TaskEdit{Title: body.Title, Description: body.Description, Source: body.ExternalTicket}); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.broadcast(sseEvent{"changed", l.Project.ID})
	_, sum, _ := s.summaryOf(l.Project, l.Task.ID, "")
	writeJSON(w, sum)
}

// paste types text into a session's terminal as one bracketed paste, then submits it.
func paste(p interface{ Write([]byte) error }, text string) error {
	return p.Write([]byte("\x1b[200~" + strings.ReplaceAll(text, "\x1b", "") + "\x1b[201~\r"))
}

func (s *Server) comment(w http.ResponseWriter, r *http.Request) {
	l, ok := s.locate(w, r)
	if !ok {
		return
	}
	var body struct {
		Body          string `json:"body"`
		SendToSession string `json:"sendToSession"`
	}
	if !decode(w, r, &body) {
		return
	}
	var target interface{ Write([]byte) error }
	if body.SendToSession != "" {
		p, err := s.taskProc(l, body.SendToSession)
		if err != nil {
			fail(w, err)
			return
		}
		target = p
	}
	if err := l.WS.Comment(l.Task.ID, body.Body); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if target != nil {
		_ = paste(target, body.Body)
	}
	s.broadcast(sseEvent{"changed", l.Project.ID})
	events, _ := l.WS.Store.Events(l.Task.ID)
	var c commentView
	n := 0
	for _, e := range events {
		if e.Type == "comment" {
			text, _ := e.Detail["text"].(string)
			c = commentView{ID: commentID(e, n), Time: e.Time, Body: text}
			c.Actor = actorOf(e.Actor, core.ResolveActors([]string{e.Actor}, nil, l.WS.Identity()))
			n++
		}
	}
	writeJSONStatus(w, http.StatusCreated, c)
}

// currentPlan checks that plan.md is complete and still has the hash the human read.
func currentPlan(ws *core.Workspace, id, sha string) error {
	b, err := os.ReadFile(ws.Store.PlanPath(id))
	if err != nil {
		return errors.New("the task has no plan.md")
	}
	if evidence.HashBytes(b) != sha {
		return errors.New("plan.md changed since you read it: reload and read it again")
	}
	if probs := plan.Check(string(b)); len(probs) > 0 {
		return errors.New("plan.md is incomplete")
	}
	return nil
}

// file serves a file from the task directory (evidence images, context.md) as an inert
// document: a planted HTML or SVG file cannot run script in the app's origin.
func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	l, ok := s.locate(w, r)
	if !ok {
		return
	}
	p, err := safeJoin(l.WS.Store.TaskDir(l.Task.ID), r.PathValue("path"))
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		httpError(w, http.StatusNotFound, "no such file")
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox")
	http.ServeFile(w, r, p)
}

// safeJoin joins rel under dir and refuses any symlink along the way, so a link planted in a
// task directory cannot expose files outside it.
func safeJoin(dir, rel string) (string, error) {
	rel = filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid path")
	}
	p := dir
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("symlinks are not served")
		}
	}
	return p, nil
}

const diffLimit = 512 << 10

// diff is the patch of one changed file (or of all, without file), asked for on demand.
func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	l, ok := s.locate(w, r)
	if !ok {
		return
	}
	file := r.URL.Query().Get("file")
	d, err := l.WS.Diff(l.Task, file, diffLimit)
	if err != nil {
		httpError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, map[string]any{"path": file, "patch": d.Patch, "truncated": d.Truncated})
}

// deleteWorktree removes a task's worktree and branch. A task never committed leaves the
// worktree dirty, so the UI confirms and sends force=1.
func (s *Server) deleteWorktree(w http.ResponseWriter, r *http.Request) {
	l, ok := s.locate(w, r)
	if !ok {
		return
	}
	if l.Worktree == nil {
		httpError(w, http.StatusNotFound, "the task has no worktree")
		return
	}
	for _, x := range s.taskSessions(l) {
		if appstate.Live(x.Status) {
			httpError(w, http.StatusConflict, "stop the task's agent sessions first")
			return
		}
	}
	if err := core.RemoveWorktree(l.Main.Repo, l.Worktree.Path, true, r.URL.Query().Get("force") == "1"); err != nil {
		httpError(w, http.StatusConflict, err.Error())
		return
	}
	s.broadcast(sseEvent{"changed", l.Project.ID})
	w.WriteHeader(http.StatusNoContent)
}
