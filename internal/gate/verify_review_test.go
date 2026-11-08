package gate

import (
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/sign"
	"github.com/spaghetticoders/truehand/internal/sign/signtest"
)

const reviewRepo = "github.com/spaghetticoders/demo"

func reviewIntentFor(action string) sign.Intent {
	in := sign.Intent{
		Action: action, Repo: reviewRepo, Task: "T-1", PlanSHA256: strings.Repeat("a", 64),
		Nonce: strings.Repeat("b", 32), Time: "2026-11-08T10:00:00Z", Approver: "phone",
	}
	switch action {
	case sign.ActionCommitConfig, sign.ActionPushBase:
		in.Task = sign.ConfigTask
		in.Head = strings.Repeat("c", 40)
	case sign.ActionPush, sign.ActionOpenPR:
		in.Head = strings.Repeat("c", 40)
	}
	return in
}

// A signature for one action never authorises another, with every other field equal.
func TestReviewActionsNeverStandInForEachOther(t *testing.T) {
	k, pub := signtest.Key(t)
	aps := []config.Approver{{ID: "phone", Name: "Phone", PublicKey: pub}}
	actions := []string{sign.ActionApprovePlan, sign.ActionPush, sign.ActionOpenPR, sign.ActionCommitConfig, sign.ActionPushBase}
	for _, signed := range actions {
		s := signtest.Sign(t, k, reviewIntentFor(signed))
		for _, asked := range actions {
			want := reviewIntentFor(asked)
			_, err := VerifySigned(reviewRepo, aps, asked, want.Task, want.PlanSHA256, want.Head, &s)
			if (err == nil) != (signed == asked) {
				t.Errorf("signed %s, verified as %s: %v", signed, asked, err)
			}
			// Relabelling the intent to the asked action keeps the signature of the old one.
			if signed != asked {
				relabel := s
				relabel.Intent = want
				if _, err := VerifySigned(reviewRepo, aps, asked, want.Task, want.PlanSHA256, want.Head, &relabel); err == nil {
					t.Errorf("signature of %s relabelled as %s accepted", signed, asked)
				}
			}
		}
	}
}

// A passkey approver (RSA, rp_id) signs only through WebAuthn: its key never verifies a raw
// signature, and the rp_id it is listed with is the one its assertion must name.
func TestReviewRSAApproverOnlyThroughWebAuthn(t *testing.T) {
	rk, pub := signtest.RSAKey(t, 2048)
	aps := []config.Approver{{ID: "phone", Name: "Phone", PublicKey: pub, RPID: "host.ts.net"}}
	in := reviewIntentFor(sign.ActionApprovePlan)
	h := sha256.Sum256(in.Message())
	raw, _ := rk.Sign(rand.Reader, h[:], crypto.SHA256)
	s := sign.Signed{Intent: in, Format: sign.FormatES256, Sig: base64.StdEncoding.EncodeToString(raw)}
	if _, err := VerifySignedApproval(reviewRepo, aps, "T-1", in.PlanSHA256, &s); err == nil {
		t.Fatal("RSA approver accepted a raw signature")
	}
	assert := func(rpHost, origin string) *sign.Signed {
		rp := sha256.Sum256([]byte(rpHost))
		ad := append(rp[:], 0x05, 0, 0, 0, 1)
		ch := sha256.Sum256(in.Message())
		cdj := []byte(`{"type":"webauthn.get","challenge":"` + base64.RawURLEncoding.EncodeToString(ch[:]) + `","origin":"` + origin + `","crossOrigin":false}`)
		cdh := sha256.Sum256(cdj)
		d := sha256.Sum256(append(append([]byte{}, ad...), cdh[:]...))
		sig, _ := rk.Sign(rand.Reader, d[:], crypto.SHA256)
		return &sign.Signed{Intent: in, Format: sign.FormatWebAuthn, Sig: base64.StdEncoding.EncodeToString(sig),
			AuthenticatorData: base64.RawURLEncoding.EncodeToString(ad), ClientDataJSON: base64.RawURLEncoding.EncodeToString(cdj)}
	}
	if _, err := VerifySignedApproval(reviewRepo, aps, "T-1", in.PlanSHA256, assert("host.ts.net", "https://host.ts.net")); err != nil {
		t.Fatalf("good RS256 assertion: %v", err)
	}
	if _, err := VerifySignedApproval(reviewRepo, aps, "T-1", in.PlanSHA256, assert("other.ts.net", "https://host.ts.net")); err == nil {
		t.Fatal("assertion for another relying party accepted")
	}
	noRP := []config.Approver{{ID: "phone", Name: "Phone", PublicKey: pub}}
	if _, err := VerifySignedApproval(reviewRepo, noRP, "T-1", in.PlanSHA256, assert("host.ts.net", "https://host.ts.net")); err == nil {
		t.Fatal("WebAuthn assertion accepted for an approver listed without rp_id")
	}
}
