package serve

import (
	"context"
	"os"
	"testing"

	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/humanauth"
)

func TestMain(m *testing.M) {
	core.AgentDetector = func() int { return 0 }
	os.Unsetenv("TRUEHAND_SESSION")
	// Never ask the real Claude Code about its plugins from a test.
	GlobalPlugin = func(context.Context) (bool, error) { return false, nil }
	// Nobody is at the Mac during tests: every server gets a fake, and reaching the real
	// dialog is a bug.
	humanauth.Authenticate = func(string) error { panic("the real OS authentication was called in a test") }
	os.Exit(m.Run())
}
