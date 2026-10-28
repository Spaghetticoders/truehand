package core

import (
	"os"
	"sync"
)

// ProjectTask is a card of the project board; Worktree is the checkout the task lives in
// when that is not the main one.
type ProjectTask struct {
	BoardTask
	Worktree string `json:"worktree,omitempty"`
}

type WorktreeError struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// ProjectView is one board for the whole project: the main checkout's tasks, the tasks of
// branches nobody has checked out, and the live task of every worktree, each id once.
type ProjectView struct {
	Root  string        `json:"root"`
	Name  string        `json:"name"`
	Tasks []ProjectTask `json:"tasks"`
	// WorktreeErrors are worktrees that could not be read; their tasks are missing.
	WorktreeErrors []WorktreeError `json:"worktree_errors,omitempty"`
	// Waiting is what WaitingFor needs, each task measured in its own checkout.
	Waiting []WaitingInput `json:"-"`
}

// ProjectBoard builds the project board from w, which should be the main checkout. A task
// found both in the main checkout and live in a worktree is the worktree's. A worktree
// deleted by hand is skipped; its committed tasks show from its branch.
func (w *Workspace) ProjectBoard() (*ProjectView, error) {
	main, err := w.Board()
	if err != nil {
		return nil, err
	}
	list, err := ListWorktrees(w.Repo)
	if err != nil {
		return nil, err
	}
	pv := &ProjectView{Root: main.Root, Name: main.Name, Tasks: []ProjectTask{}}
	index := map[string]int{}
	put := func(t ProjectTask, in WaitingInput) {
		if i, ok := index[t.ID]; ok {
			pv.Tasks[i], pv.Waiting[i] = t, in
			return
		}
		index[t.ID] = len(pv.Tasks)
		pv.Tasks = append(pv.Tasks, t)
		pv.Waiting = append(pv.Waiting, in)
	}
	for i, in := range w.WaitingInputs(main) {
		put(ProjectTask{BoardTask: main.Tasks[i]}, in)
	}
	// Worktrees are read in parallel: each is hundreds of milliseconds of git calls.
	type result struct {
		root string
		bv   *BoardView
		ins  []WaitingInput
		err  error
	}
	results := make([]*result, len(list))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, wt := range list {
		if _, err := os.Stat(wt.Path); err != nil || sameDir(wt.Path, w.Repo.Root) {
			continue
		}
		r := &result{root: wt.Path}
		results[i] = r
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ws, err := OpenWorktree(wt.Path)
			if err != nil {
				r.err = err
				return
			}
			r.root = ws.Repo.Root
			if r.bv, r.err = ws.board(false); r.err == nil {
				r.ins = ws.WaitingInputs(r.bv)
			}
		}()
	}
	wg.Wait()
	for i, r := range results {
		if r == nil {
			continue
		}
		if r.err != nil {
			pv.WorktreeErrors = append(pv.WorktreeErrors, WorktreeError{Path: list[i].Path, Error: r.err.Error()})
			continue
		}
		for j, in := range r.ins {
			put(ProjectTask{BoardTask: r.bv.Tasks[j], Worktree: r.root}, in)
		}
	}
	return pv, nil
}
