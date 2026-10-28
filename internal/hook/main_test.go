package hook

import (
	"os"
	"testing"

	"github.com/spaghetticoders/truehand/internal/core"
)

// Tests may run inside an agent session; identity must come from TRUEHAND_ACTOR or git here.
func TestMain(m *testing.M) {
	// Tests may run inside an agent the app launched; they must not count as one.
	os.Unsetenv("TRUEHAND_SESSION")
	core.AgentDetector = func() int { return 0 }
	os.Exit(m.Run())
}
