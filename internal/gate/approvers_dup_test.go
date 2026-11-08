package gate

import (
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/sign"
	"github.com/spaghetticoders/truehand/internal/sign/signtest"
)

// Ids are unique in a config; a list that still carries one twice is refused, never resolved
// by picking one of the keys.
func TestVerifySignedRefusesDuplicateID(t *testing.T) {
	k, pub := signtest.Key(t)
	_, other := signtest.Key(t)
	in := sign.Intent{
		Action: sign.ActionApprovePlan, Repo: "github.com/x/y", Task: "T-1", PlanSHA256: strings.Repeat("a", 64),
		Nonce: strings.Repeat("b", 32), Time: "2026-11-08T00:00:00Z", Approver: "mac",
	}
	s := signtest.Sign(t, k, in)
	one := []config.Approver{{ID: "mac", Name: "M", PublicKey: pub}}
	if _, err := VerifySigned("github.com/x/y", one, sign.ActionApprovePlan, "T-1", in.PlanSHA256, "", &s); err != nil {
		t.Fatal(err)
	}
	two := append(one, config.Approver{ID: "mac", Name: "M", PublicKey: other})
	if _, err := VerifySigned("github.com/x/y", two, sign.ActionApprovePlan, "T-1", in.PlanSHA256, "", &s); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("duplicate id: %v", err)
	}
}
