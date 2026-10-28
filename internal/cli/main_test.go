package cli

import (
	"os"
	"testing"

	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/humanauth"
)

// Tests may run inside an agent session; identity must come from TRUEHAND_ACTOR or git here.
func TestMain(m *testing.M) {
	// Tests may run inside an agent the app launched; they must not count as one.
	os.Unsetenv("TRUEHAND_SESSION")
	core.AgentDetector = func() int { return 0 }
	humanauth.Authenticate = func(string) error { return nil }
	os.Exit(m.Run())
}
