package sign_test

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/spaghetticoders/truehand/internal/sign"
	"github.com/spaghetticoders/truehand/internal/sign/signtest"
)

func pemOf(t *testing.T, pub any) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func reviewIntent() sign.Intent {
	return sign.Intent{
		Action: sign.ActionApprovePlan, Repo: "github.com/o/r", Task: "T-1", PlanSHA256: strings.Repeat("a", 64),
		Nonce: sign.NewNonce(), Time: time.Now().UTC().Format("2006-01-02T15:04:05Z"), Approver: "mac",
	}
}

func TestReviewOnlyP256Keys(t *testing.T) {
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	p224, _ := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	r, _ := rsa.GenerateKey(rand.Reader, 2048)
	ed, _, _ := ed25519.GenerateKey(rand.Reader)
	_, good := signtest.Key(t)
	for name, s := range map[string]string{
		"P-384":   pemOf(t, &p384.PublicKey),
		"P-224":   pemOf(t, &p224.PublicKey),
		"RSA":     pemOf(t, &r.PublicKey),
		"Ed25519": pemOf(t, ed),
		"two":     good + good,
		"private": strings.Replace(good, "PUBLIC KEY", "EC PRIVATE KEY", 2),
		"junk":    "not a key",
		"trailer": good + "garbage",
	} {
		if _, err := sign.ParsePublicKey(s); err == nil {
			t.Errorf("%s key accepted", name)
		}
	}
	if _, err := sign.ParsePublicKey(good); err != nil {
		t.Fatal(err)
	}
}

func TestReviewOnlyDERSignatures(t *testing.T) {
	k, _ := signtest.Key(t)
	in := intent()
	s := signtest.Sign(t, k, in)
	if err := s.Verify(&k.PublicKey, ""); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(in.Message())
	r, ss, _ := ecdsa.Sign(rand.Reader, k, h[:])
	raw := make([]byte, 64)
	r.FillBytes(raw[:32])
	ss.FillBytes(raw[32:])
	bad := s
	bad.Sig = base64.StdEncoding.EncodeToString(raw)
	if bad.Verify(&k.PublicKey, "") == nil {
		t.Error("raw r||s signature accepted")
	}
	bad.Sig = base64.RawURLEncoding.EncodeToString(raw)
	if bad.Verify(&k.PublicKey, "") == nil {
		t.Error("base64url raw signature accepted")
	}
	other, _ := signtest.Key(t)
	if s.Verify(&other.PublicKey, "") == nil {
		t.Error("verified with another key")
	}
	md := s
	md.Format = "ES256 "
	if md.Verify(&k.PublicKey, "") == nil {
		t.Error("unknown format accepted")
	}
	wa := s
	wa.AuthenticatorData = "AAAA"
	if wa.Verify(&k.PublicKey, "") == nil {
		t.Error("es256 with WebAuthn fields accepted")
	}
}

func TestReviewMessageCannotBeSmuggled(t *testing.T) {
	k, _ := signtest.Key(t)
	for name, mut := range map[string]func(*sign.Intent){
		"repo newline":     func(i *sign.Intent) { i.Repo = "github.com/o/r\ntask: T-2" },
		"task newline":     func(i *sign.Intent) { i.Task = "T-1\nx" },
		"approver newline": func(i *sign.Intent) { i.Approver = "mac\nhead: x" },
		"upper sha":        func(i *sign.Intent) { i.PlanSHA256 = strings.Repeat("A", 64) },
		"short nonce":      func(i *sign.Intent) { i.Nonce = "abc" },
		"head on approve":  func(i *sign.Intent) { i.Head = strings.Repeat("b", 40) },
		"local time":       func(i *sign.Intent) { i.Time = "2026-10-27T22:00:00+01:00" },
		"no seconds":       func(i *sign.Intent) { i.Time = "2026-10-27T22:00Z" },
		"push no head":     func(i *sign.Intent) { i.Action = sign.ActionPush },
	} {
		in := intent()
		mut(&in)
		if err := signtest.Sign(t, k, in).Verify(&k.PublicKey, ""); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestReviewNormalizeRepoURL(t *testing.T) {
	want := "github.com/spaghetticoders/demo"
	for _, u := range []string{
		"https://github.com/Spaghetticoders/demo.git",
		"git@github.com:spaghetticoders/demo.git",
		"ssh://git@github.com:22/spaghetticoders/demo",
		"https://user:tok@github.com/spaghetticoders/demo/",
	} {
		if got := sign.NormalizeRepoURL(u); got != want {
			t.Errorf("%s → %s", u, got)
		}
	}
	if sign.NormalizeRepoURL("https://github.com/spaghetticoders/demo-evil") == want {
		t.Error("a different repo normalizes the same")
	}
}
