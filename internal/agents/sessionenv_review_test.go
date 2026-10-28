package agents

import "testing"

func TestReviewParseSessionEnvOnlyLoopback(t *testing.T) {
	for _, v := range []string{
		"s:x@127.0.0.1:80@evil.example",
		"s:x@127.0.0.1:80/../x",
		"s:x@127.0.0.1:abc",
		"s:x@127.0.0.1:",
		"s:x@127.0.0.1.evil.example:80",
		"s:x@100.64.0.1:80",
		":x@127.0.0.1:80",
		"s:@127.0.0.1:80",
	} {
		if _, _, addr, err := ParseSessionEnv(v); err == nil {
			t.Errorf("%q accepted (addr %q)", v, addr)
		}
	}
	if sid, sec, addr, err := ParseSessionEnv("s1:abc@127.0.0.1:4711"); err != nil || sid != "s1" || sec != "abc" || addr != "127.0.0.1:4711" {
		t.Fatalf("%q %q %q %v", sid, sec, addr, err)
	}
}

func TestReviewCleanEnvDropsCredentials(t *testing.T) {
	base := []string{"PATH=/bin", "TRUEHAND_ACTOR=human:spoof@x.y", "TRUEHAND_TOKEN=tok", "TRUEHAND_SESSION=old", "CLAUDECODE=1", "TERM=dumb", "HOME=/h"}
	got := CleanEnv(base, "s:x@127.0.0.1:1")
	sessions := 0
	for _, kv := range got {
		switch {
		case kv == "TRUEHAND_SESSION=s:x@127.0.0.1:1":
			sessions++
		case kv == "TRUEHAND_ACTOR=human:spoof@x.y", kv == "TRUEHAND_TOKEN=tok", kv == "TRUEHAND_SESSION=old", kv == "CLAUDECODE=1", kv == "TERM=dumb":
			t.Errorf("kept %s", kv)
		}
	}
	if sessions != 1 {
		t.Fatalf("TRUEHAND_SESSION set %d times: %v", sessions, got)
	}
}
