package sign_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/spaghetticoders/truehand/internal/sign"
)

func TestReviewOriginMatchesHostForms(t *testing.T) {
	const rp = "xn--hst-sna.ts.net" // punycode, as browsers send an IDN origin
	for origin, want := range map[string]bool{
		"https://xn--hst-sna.ts.net":         true,
		"https://xn--hst-sna.ts.net:8443":    true,
		"https://höst.ts.net":                false,
		"https://XN--HST-SNA.ts.net":         false,
		"https://xn--hst-sna.ts.net.":        false,
		"https://xn--hst-sna.ts.net.:443":    false,
		"https://xn--hst-sna.ts.net%2e":      false,
		"https://a.xn--hst-sna.ts.net":       false,
		"https://xn--hst-sna.ts.net.a":       false,
		"https://u:p@xn--hst-sna.ts.net":     false,
		"https://xn--hst-sna.ts.net:443/":    false,
		"https://xn--hst-sna.ts.net:1e3":     false,
		"https://xn--hst-sna.ts.net:443 ":    false,
		"https://xn--hst-sna.ts.net:443\x00": false,
		"https://xn--hst-sna.ts.net?x":       false,
		"https://xn--hst-sna.ts.net#x":       false,
	} {
		if got := sign.OriginMatches(origin, rp); got != want {
			t.Errorf("OriginMatches(%q) = %v, want %v", origin, got, want)
		}
	}
	if sign.CheckRPID(rp) != nil {
		t.Error("punycode rp_id refused")
	}
	for _, bad := range []string{"höst.ts.net", "[::1]", "::1", "fe80::1", "host.ts.net.", "u@host.ts.net", "host ts.net", "*.ts.net"} {
		if sign.CheckRPID(bad) == nil {
			t.Errorf("rp_id %q accepted", bad)
		}
	}
}

func serverIntent() sign.ServerIntent {
	return sign.ServerIntent{
		Action: "pair-code", Server: "host.ts.net", Subject: strings.Repeat("a", 64),
		Nonce: strings.Repeat("d", 32), Time: time.Now().UTC().Format("2006-01-02T15:04:05Z"), Credential: "mac",
	}
}

func es256(t *testing.T, k *ecdsa.PrivateKey, msg []byte) string {
	h := sha256.Sum256(msg)
	s, err := ecdsa.SignASN1(rand.Reader, k, h[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(s)
}

// The Mac's Secure Enclave key anchors the first owner with raw ES256 signatures.
func TestReviewServerVerifyES256(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	in := serverIntent()
	s := sign.SignedServer{Intent: in, Format: sign.FormatES256, Sig: es256(t, k, in.Message())}
	if err := s.VerifyES256(&k.PublicKey, "host.ts.net"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.SignCount(); err != nil || n != 0 {
		t.Fatalf("es256 sign count %d %v", n, err)
	}
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	repo := sign.Intent{Action: sign.ActionApprovePlan, Repo: "github.com/x/y", Task: "T-1", PlanSHA256: strings.Repeat("a", 64),
		Nonce: strings.Repeat("d", 32), Time: in.Time, Approver: "mac"}
	for name, bad := range map[string]sign.SignedServer{
		"other server":            {Intent: in, Format: sign.FormatES256, Sig: s.Sig},
		"other key":               {Intent: in, Format: sign.FormatES256, Sig: es256(t, other, in.Message())},
		"webauthn":                {Intent: in, Format: sign.FormatWebAuthn, Sig: s.Sig},
		"with webauthn fields":    {Intent: in, Format: sign.FormatES256, Sig: s.Sig, AuthenticatorData: "AA", ClientDataJSON: "e30"},
		"repo intent's signature": {Intent: in, Format: sign.FormatES256, Sig: es256(t, k, repo.Message())},
	} {
		rp := "host.ts.net"
		if name == "other server" {
			rp = "other.ts.net"
		}
		if bad.VerifyES256(&k.PublicKey, rp) == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// And the other way: the server intent's raw signature never approves a plan.
	if (sign.Signed{Intent: repo, Format: sign.FormatES256, Sig: s.Sig}).Verify(&k.PublicKey, "") == nil {
		t.Error("server intent signature approved a plan")
	}
}

// es256 is P-256 only, whatever key the caller hands in.
func TestReviewES256OnlyP256(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	in := serverIntent()
	s := sign.SignedServer{Intent: in, Format: sign.FormatES256, Sig: es256(t, k, in.Message())}
	if s.VerifyES256(&k.PublicKey, "host.ts.net") == nil {
		t.Error("server intent signed by a P-384 key accepted")
	}
	repo := sign.Intent{Action: sign.ActionApprovePlan, Repo: "github.com/x/y", Task: "T-1", PlanSHA256: strings.Repeat("a", 64),
		Nonce: strings.Repeat("d", 32), Time: in.Time, Approver: "mac"}
	if (sign.Signed{Intent: repo, Format: sign.FormatES256, Sig: es256(t, k, repo.Message())}).Verify(&k.PublicKey, "") == nil {
		t.Error("intent signed by a P-384 key accepted")
	}
}

func TestReviewSignCount(t *testing.T) {
	ad := append(make([]byte, 33), 0, 0, 1, 2)
	s := sign.Signed{Format: sign.FormatWebAuthn, AuthenticatorData: base64.RawURLEncoding.EncodeToString(ad)}
	if n, err := s.SignCount(); err != nil || n != 258 {
		t.Fatalf("%d %v", n, err)
	}
	s.AuthenticatorData = base64.RawURLEncoding.EncodeToString(ad[:36])
	if _, err := s.SignCount(); err == nil {
		t.Fatal("short authenticator data accepted")
	}
}
