package serve

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/gitx"
)

type sseEvent struct {
	Name string
	Data any // a string is sent as is, anything else as JSON
}

// events streams: "changed" with a project id, "session" with {id,status}, "waiting" with
// {count}.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		httpError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch := make(chan sseEvent, 64)
	s.mu.Lock()
	if s.clients == nil {
		s.clients = map[chan sseEvent]bool{}
	}
	s.clients[ch] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, ch)
		s.mu.Unlock()
	}()
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	closing := s.closingCh()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-closing:
			return
		case ev := <-ch:
			data, isString := ev.Data.(string)
			if !isString {
				b, _ := json.Marshal(ev.Data)
				data = string(b)
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Name, strings.ReplaceAll(data, "\n", " "))
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func (s *Server) broadcast(ev sseEvent) {
	if ev.Name == "changed" {
		s.waitingDirty.Store(true)
		if pid, ok := ev.Data.(string); ok {
			s.forget(pid)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- ev:
		default:
		}
	}
}

// SessionChanged is the agent manager's OnChange.
func (s *Server) SessionChanged(rec appstate.Session) {
	v := viewSession(rec, nil)
	s.broadcast(sseEvent{"session", map[string]string{"id": rec.ID, "status": v.Status, "waitingFor": v.WaitingFor}})
	s.waitingDirty.Store(true)
}

func (s *Server) broadcastWaiting() {
	s.broadcast(sseEvent{"waiting", map[string]int{"count": s.waitingCount()}})
}

// Watch polls each project's .truehand trees, its main checkout's and its worktrees', and
// announces changes; the waiting count is recomputed at most once per tick. Polling keeps
// the binary free of platform watchers and is cheap at this scale.
func (s *Server) Watch(stop <-chan struct{}, every time.Duration) {
	last := map[string]string{}
	stuck := map[string]bool{}
	for {
		for _, x := range s.allSessions() {
			if effective(x).Status != x.Status && !stuck[x.ID] {
				stuck[x.ID] = true
				s.SessionChanged(x)
			}
		}
		if st, err := s.Store.State(); err == nil {
			for _, p := range st.Projects {
				sig := projectSignature(p.Root)
				if prev, ok := last[p.ID]; ok && prev != sig {
					s.broadcast(sseEvent{"changed", p.ID})
				}
				last[p.ID] = sig
			}
		}
		if s.waitingDirty.Swap(false) {
			s.broadcastWaiting()
		}
		select {
		case <-stop:
			return
		case <-time.After(every):
		}
	}
}

func projectSignature(root string) string {
	var b strings.Builder
	b.WriteString(signature(filepath.Join(root, ".truehand")))
	if repo, err := gitx.Open(root); err == nil {
		head, _ := repo.Git("rev-parse", "HEAD")
		b.WriteString("\n" + strings.TrimSpace(head))
		if wts, err := core.ListWorktrees(repo); err == nil {
			for _, wt := range wts {
				if !wt.Main {
					fmt.Fprintf(&b, "\n%s %s %s", wt.Path, wt.Head, signature(filepath.Join(wt.Path, ".truehand")))
				}
			}
		}
	}
	return evidence.HashBytes([]byte(b.String()))
}

func signature(dir string) string {
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			fmt.Fprintf(&b, "%s %d %d\n", p, fi.Size(), fi.ModTime().UnixNano())
		}
		return nil
	})
	return evidence.HashBytes([]byte(b.String()))
}
