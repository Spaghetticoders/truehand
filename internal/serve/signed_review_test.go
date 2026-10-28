package serve

import (
	"os"
	"testing"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/sign"
)

// A signature for one task never approves another, even with the same plan.
func TestReviewSignedIntentStaysOnItsTask(t *testing.T) {
	e := newEnv(t, false)
	k, pub := newKey(t)
	withApprovers(t, e.root, pub)
	ws, _ := core.Open(e.root)
	if _, err := ws.NewTask("T-2", "Other", ""); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(ws.Store.PlanPath("T-2"), []byte(goodPlan), 0o644)
	forT1 := e.signIntent(e.taskPath("T-1"), sign.ActionApprovePlan, k)
	if code := e.act("T-2", "approve-plan", forT1, asToken, nil); code < 400 || e.state("T-2") != board.Planned {
		t.Fatalf("T-1's signature approved T-2: %d", code)
	}
	if code := e.act("T-1", "push", forT1, asShell, nil); code < 400 {
		t.Fatalf("an approve-plan signature authorised a push: %d", code)
	}
	if code := e.act("T-1", "approve-plan", forT1, asToken, nil); code != 200 || e.state("T-1") != board.Approved {
		t.Fatalf("the genuine use: %d", code)
	}
}
