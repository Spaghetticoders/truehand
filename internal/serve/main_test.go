package serve

import (
	"os"
	"testing"

	"github.com/spaghetticoders/truehand/internal/core"
)

// Tests may run inside an agent session; identity must come from TRUEHAND_ACTOR or git here.
func TestMain(m *testing.M) {
	core.AgentDetector = func() int { return 0 }
	os.Exit(m.Run())
}
