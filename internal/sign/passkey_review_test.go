package sign_test

import (
	"crypto/ecdsa"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/sign"
)

// Signatures made with main's code (f3ad7de) by a Secure Enclave-style ES256 key: what Macs
// already signed must keep verifying.
const mainPub = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEpJ3sH2GH3BQnCOHvGg49/mkpronY
cYHChEO5wQRFzGgKTrscdZv6NWjsc1mc1F3Vi+01zmfqX/6+MRIYuwI4dw==
-----END PUBLIC KEY-----`

var mainSigs = map[string]string{
	sign.ActionApprovePlan: "MEUCIFEP7LsLw82K92NCtY8/x7XqaAKfD9CpDlXHzDhVr3EvAiEA27CSTxYpx6QDal1BzsPzZ3Bq6Q8szQQnKEt5Q2XWYsw=",
	sign.ActionPush:        "MEUCIH4H8GkhcR0ir+TEXACEB4BWacd7DA8kJ59njgjm+9PXAiEAshzvyHCr9bkexIcGO4h6Xtb7oVWcEzwqHPynPqSvVPE=",
}

func mainIntent(action string) sign.Intent {
	in := sign.Intent{
		Action: action, Repo: "github.com/spaghetticoders/demo", Task: "T-1", PlanSHA256: strings.Repeat("a", 64),
		Nonce: strings.Repeat("b", 32), Time: "2026-11-01T10:00:00Z", Approver: "mac",
	}
	if action == sign.ActionPush {
		in.Head = strings.Repeat("c", 40)
	}
	return in
}

func TestReviewMainES256SignaturesStillVerify(t *testing.T) {
	pub, err := sign.ParsePublicKey(mainPub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sign.ParseP256PublicKey(mainPub); err != nil {
		t.Fatal(err)
	}
	for action, sig := range mainSigs {
		s := sign.Signed{Intent: mainIntent(action), Format: sign.FormatES256, Sig: sig}
		if err := s.Verify(pub, ""); err != nil {
			t.Errorf("%s signed on main: %v", action, err)
		}
		if err := s.Verify(pub.(*ecdsa.PublicKey), "host.ts.net"); err != nil {
			t.Errorf("%s signed on main, approver with rp_id: %v", action, err)
		}
		other := s
		other.Intent.Nonce = strings.Repeat("d", 32)
		if other.Verify(pub, "") == nil {
			t.Errorf("%s: main signature verified for another nonce", action)
		}
	}
}

func TestReviewOriginMatches(t *testing.T) {
	for origin, want := range map[string]bool{
		"https://host.ts.net":          true,
		"https://host.ts.net:8443":     true,
		"https://host.ts.net:443":      true,
		"https://host.ts.net:10000":    true,
		"https://host.ts.net:":         false,
		"https://host.ts.net:0443":     false,
		"https://host.ts.net:0":        false,
		"https://host.ts.net:65536":    false,
		"https://host.ts.net:+443":     false,
		"https://host.ts.net/":         false,
		"https://host.ts.net.evil.com": false,
		"https://host.ts.net@evil.com": false,
		"https://host.ts.net:443@evil": false,
		"https://evil.host.ts.net":     false,
		"https://HOST.ts.net":          false,
		"http://host.ts.net":           false,
		"host.ts.net":                  false,
		"":                             false,
	} {
		if got := sign.OriginMatches(origin, "host.ts.net"); got != want {
			t.Errorf("OriginMatches(%q) = %v, want %v", origin, got, want)
		}
	}
	for _, bad := range []string{"", "host.ts.net:8443", "https://host.ts.net", "Host.ts.net", "host.ts.net.", "-host.ts.net", "host..ts.net", "host.ts.net/x"} {
		if sign.CheckRPID(bad) == nil {
			t.Errorf("rp_id %q accepted", bad)
		}
	}
}
