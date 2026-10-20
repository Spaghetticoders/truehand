package hands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
)

const maxAttachment = 20 << 20

// Attach keeps files for the humans to look at (screenshots of a flow, a recording, a
// report) as evidence of the task, with a note on what they show. It proves nothing by
// itself: it records what the agent saw, on which content.
func Attach(ws *core.Workspace, t *board.Task, note string, paths []string) (*Result, error) {
	if len(paths) == 0 {
		return nil, errors.New("nothing to attach: pass one or more files")
	}
	if strings.TrimSpace(note) == "" {
		return nil, errors.New("say what the files show in the note")
	}
	s, err := Begin(ws, t, "attach", "")
	if err != nil {
		return nil, err
	}
	if err := s.WriteFile("note.md", []byte(strings.TrimSpace(note)+"\n")); err != nil {
		return nil, err
	}
	seen := map[string]bool{"note.md": true}
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(ws.Repo.Root, p)
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", p)
		}
		if fi.Size() > maxAttachment {
			return nil, fmt.Errorf("%s is larger than %d MB", p, maxAttachment>>20)
		}
		name := filepath.Base(p)
		if seen[name] {
			return nil, fmt.Errorf("two attachments are named %s: rename one", name)
		}
		seen[name] = true
		if err := copyInto(s, name, p); err != nil {
			return nil, err
		}
	}
	return s.Finish(evidence.Pass, fmt.Sprintf("%d file(s) attached: %s", len(paths), firstLine(note)), "")
}

func copyInto(s *Session, name, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(s.Log.Dir(), 0o755); err != nil {
		return err
	}
	out, err := os.Create(s.File(name))
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if len(line) > 120 {
		line = line[:117] + "..."
	}
	return line
}
