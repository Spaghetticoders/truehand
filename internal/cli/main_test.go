package cli

import (
	"os"
	"testing"

	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/humanauth"
)

// realHome is the user's HOME before the tests replace it; only a test that checks a real
// HOME is refused may use it.
var realHome string

// Tests may run inside an agent session; identity must come from TRUEHAND_ACTOR or git here.
func TestMain(m *testing.M) {
	// No test may reach the user's ~/.truehand (a real server may hold its lock): every
	// test runs in a HOME of its own unless it sets one.
	realHome = os.Getenv("HOME")
	home, err := os.MkdirTemp("", "th-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	// Tests may run inside an agent the app launched; they must not count as one.
	os.Unsetenv("TRUEHAND_SESSION")
	core.AgentDetector = func() int { return 0 }
	humanauth.Authenticate = func(string) error { return nil }
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
