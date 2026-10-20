package hands

import (
	"errors"
	"strings"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
)

// Review records a reviewer's verdict and notes. The judgement is the agent's or a human's;
// truehand records who gave it, on which content.
func Review(ws *core.Workspace, t *board.Task, verdict string, notes []byte) (*Result, error) {
	if verdict != evidence.Pass && verdict != evidence.Fail {
		return nil, errors.New("verdict must be pass or fail")
	}
	if len(strings.TrimSpace(string(notes))) < 20 {
		return nil, errors.New("review notes are too short to be a review: say what you read, what you ran and what you found")
	}
	s, err := Begin(ws, t, "review", "")
	if err != nil {
		return nil, err
	}
	if err := s.WriteFile("notes.md", notes); err != nil {
		return nil, err
	}
	return s.Finish(verdict, "review "+verdict+" by "+s.Rec.Actor, "")
}
