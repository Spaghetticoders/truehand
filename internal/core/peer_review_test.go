package core

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
	"testing"
)

// The session env must be found whatever the peer's argv looks like: an empty argv[0] or
// empty arguments must not shift the parser into the environment.
func TestReviewPeerEnvWithOddArgv(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no process environment reader")
	}
	orig := psLine
	t.Cleanup(func() { psLine = orig })
	psLine = func(int) (string, error) { return "1 sleep", nil }
	for name, args := range map[string][]string{
		"empty argv0":     {"", "-test.run=^$"},
		"empty arguments": {os.Args[0], "", "", "-test.run=^$"},
		"looks like env":  {os.Args[0], "A=b", "-test.run=^$"},
	} {
		cmd := exec.Command(os.Args[0])
		cmd.Args = args
		cmd.Env = []string{SessionEnv + "=s:x@127.0.0.1:1", "TRUEHAND_TEST_IDLE=1", "PATH=" + os.Getenv("PATH")}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if line, _ := bufio.NewReader(out).ReadString('\n'); line != "ready\n" {
			t.Fatalf("%s: child not ready: %q", name, line)
		}
		pid := cmd.Process.Pid
		agent := PeerIsAgent(pid)
		cmd.Process.Kill()
		cmd.Wait()
		if !agent {
			t.Errorf("%s: a peer carrying the session env was trusted", name)
		}
	}
}
