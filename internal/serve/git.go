package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type prState struct {
	State    string `json:"state"` // OPEN | CLOSED | MERGED
	URL      string `json:"url"`
	MergedAt string `json:"mergedAt,omitempty"`
}

// pushBranch is the branch a task's work is pushed from: never the base branch.
func pushBranch(l *located) (string, error) {
	b := l.branch()
	if b == "" {
		return "", errBadRequest("the checkout is not on a branch")
	}
	base := strings.TrimPrefix(l.WS.Config.Base, "origin/")
	if base == "" {
		base = "main"
	}
	if b == base {
		return "", errBadRequest(fmt.Sprintf("%s is the base branch: tasks are pushed from their own branch", b))
	}
	return b, nil
}

func pushPrompt(l *located) string {
	b, _ := pushBranch(l)
	return fmt.Sprintf("Truehand asks you to push branch %s of task %s.", b, l.Task.ID)
}

func prPrompt(l *located) string {
	b, _ := pushBranch(l)
	return fmt.Sprintf("Truehand asks you to open a pull request from branch %s for task %s.", b, l.Task.ID)
}

// writePush runs git push with this binary first on PATH: the pre-push hook skips its check
// when it cannot find truehand.
func (s *Server) writePush(w http.ResponseWriter, l *located) {
	branch, err := pushBranch(l)
	if err != nil {
		fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	out, err := s.Run(ctx, l.WS.Repo.Root, gitEnv(s.TruehandBin), "git", "push", "-u", "origin", branch)
	s.broadcast(sseEvent{"changed", l.Project.ID})
	res := actResult{OK: err == nil, Output: string(out)}
	if err != nil {
		res.Error = "git push failed"
	}
	writeJSON(w, res)
}

type prParams struct {
	Title string
	Body  string
	Draft bool
}

func (p prParams) toMap() map[string]any {
	return map[string]any{"title": p.Title, "body": p.Body, "draft": p.Draft}
}

func (p *prParams) fromMap(m map[string]any) {
	p.Title, _ = m["title"].(string)
	p.Body, _ = m["body"].(string)
	p.Draft, _ = m["draft"].(bool)
}

func defaultPR(l *located) prParams {
	desc, _ := l.WS.Store.Description(l.Task.ID)
	return prParams{Title: l.Task.Title, Body: strings.TrimSpace(desc + "\n\nTruehand task " + l.Task.ID)}
}

func (s *Server) writePR(w http.ResponseWriter, l *located, p prParams) {
	branch, err := pushBranch(l)
	if err != nil {
		fail(w, err)
		return
	}
	// --flag=value: a title starting with "-" stays a title.
	if _, err := s.LookPath("gh"); err != nil && s.Peer {
		req, err := s.prRequestOf(l, p)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, actResult{PRRequest: &req, Error: "gh is not installed on this machine"})
		return
	}
	args := []string{"pr", "create", "--head=" + branch, "--title=" + p.Title, "--body=" + p.Body}
	if base := strings.TrimPrefix(l.WS.Config.Base, "origin/"); base != "" {
		args = append(args, "--base="+base)
	}
	if p.Draft {
		args = append(args, "--draft")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := s.Run(ctx, l.WS.Repo.Root, gitEnv(""), "gh", args...)
	if err != nil {
		reason := ghReason(out)
		writeJSONStatus(w, http.StatusBadGateway, actResult{Error: "gh pr create failed: " + reason, Code: "gh-failed", Reason: reason, Output: string(out)})
		return
	}
	url := lastLine(string(out))
	s.mu.Lock()
	if s.prs == nil {
		s.prs = map[string]prState{}
	}
	s.prs[branch] = prState{State: "OPEN", URL: url}
	s.mu.Unlock()
	s.broadcast(sseEvent{"changed", l.Project.ID})
	writeJSON(w, actResult{OK: true, URL: url})
}

// ghReason is the line of gh's output that says what to do, e.g. "run gh auth login".
func ghReason(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, l := range lines {
		if strings.Contains(l, "gh auth login") || strings.Contains(strings.ToLower(l), "error") {
			return strings.TrimSpace(l)
		}
	}
	return firstLine(string(out))
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func (s *Server) getPR(w http.ResponseWriter, r *http.Request) {
	l, ok := s.locate(w, r)
	if !ok {
		return
	}
	branch := l.branch()
	if branch == "" {
		httpError(w, http.StatusNotFound, "no branch")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := s.Run(ctx, l.WS.Repo.Root, gitEnv(""), "gh", "pr", "view", branch, "--json", "state,url,mergedAt")
	if err != nil {
		httpError(w, http.StatusNotFound, "no pull request for "+branch)
		return
	}
	var v struct {
		State    string `json:"state"`
		URL      string `json:"url"`
		MergedAt string `json:"mergedAt"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		httpError(w, http.StatusBadGateway, "unexpected gh output")
		return
	}
	pr := prState{State: v.State, URL: v.URL, MergedAt: v.MergedAt}
	s.mu.Lock()
	if s.prs == nil {
		s.prs = map[string]prState{}
	}
	s.prs[branch] = pr
	s.mu.Unlock()
	writeJSON(w, pr)
}

// pushable says, before any dialog or signature, why a push or a PR cannot work.
func (s *Server) pushable(l *located, pr bool) *codedError {
	if l.WS.Merged(l.Task) {
		return &codedError{fmt.Sprintf("task %s is already merged", l.Task.ID), "already-merged"}
	}
	origin := strings.TrimSpace(l.WS.Repo.ConfigValue("remote.origin.url"))
	if origin == "" {
		return &codedError{"the repo has no origin remote", "no-remote"}
	}
	if !pr {
		return nil
	}
	if strings.HasPrefix(origin, "/") || strings.HasPrefix(origin, ".") || strings.HasPrefix(origin, "~") || strings.HasPrefix(origin, "file:") {
		return &codedError{"origin is a local path (" + origin + "): pull requests need a GitHub remote", "remote-not-github"}
	}
	// A machine without gh hands the PR to the Mac, which checks its own gh.
	if _, err := s.LookPath("gh"); err != nil && !s.Peer {
		return &codedError{"gh is not installed: install it and run `gh auth login`", "gh-missing"}
	}
	return nil
}
