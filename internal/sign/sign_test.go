package sign_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/spaghetticoders/truehand/internal/sign"
	"github.com/spaghetticoders/truehand/internal/sign/signtest"
)

var plan = strings.Repeat("ab", 32)

func intent() sign.Intent {
	return sign.Intent{
		Action: sign.ActionApprovePlan, Repo: "github.com/spaghetticoders/truehand", Task: "T-12",
		PlanSHA256: plan, Nonce: strings.Repeat("0f", 16), Time: "2026-10-27T22:10:00Z", Approver: "emanuel-mac",
	}
}

func TestMessageBytes(t *testing.T) {
	want := "truehand-intent/1\n" +
		"action: approve-plan\n" +
		"repo: github.com/spaghetticoders/truehand\n" +
		"task: T-12\n" +
		"plan_sha256: " + plan + "\n" +
		"head: \n" +
		"nonce: " + strings.Repeat("0f", 16) + "\n" +
		"time: 2026-10-27T22:10:00Z\n" +
		"approver: emanuel-mac\n"
	if got := string(intent().Message()); got != want {
		t.Fatalf("message\n%q\nwant\n%q", got, want)
	}
}

func TestES256(t *testing.T) {
	k, pemKey := signtest.Key(t)
	pub, err := sign.ParsePublicKey(pemKey)
	if err != nil {
		t.Fatal(err)
	}
	s := signtest.Sign(t, k, intent())
	if err := s.Verify(pub, ""); err != nil {
		t.Fatal(err)
	}

	for name, change := range map[string]func(*sign.Intent){
		"action":   func(i *sign.Intent) { i.Action = sign.ActionPush; i.Head = strings.Repeat("1", 40) },
		"repo":     func(i *sign.Intent) { i.Repo = "github.com/evil/truehand" },
		"task":     func(i *sign.Intent) { i.Task = "T-13" },
		"plan":     func(i *sign.Intent) { i.PlanSHA256 = strings.Repeat("cd", 32) },
		"nonce":    func(i *sign.Intent) { i.Nonce = strings.Repeat("1f", 16) },
		"time":     func(i *sign.Intent) { i.Time = "2026-10-27T22:11:00Z" },
		"approver": func(i *sign.Intent) { i.Approver = "someone" },
	} {
		forged := s
		change(&forged.Intent)
		if err := forged.Verify(pub, ""); err == nil {
			t.Errorf("changed %s still verifies", name)
		}
	}

	other, _ := signtest.Key(t)
	if err := s.Verify(&other.PublicKey, ""); err == nil {
		t.Error("verifies with another key")
	}
	raw := s
	h := sha256.Sum256(s.Intent.Message())
	r, ss, _ := ecdsa.Sign(rand.Reader, k, h[:])
	rs := append(r.FillBytes(make([]byte, 32)), ss.FillBytes(make([]byte, 32))...)
	raw.Sig = base64.StdEncoding.EncodeToString(rs)
	if err := raw.Verify(pub, ""); err == nil {
		t.Error("raw r||s accepted: the format is DER")
	}
	bad := s
	bad.Format = "ed25519"
	if err := bad.Verify(pub, ""); err == nil {
		t.Error("unknown format accepted")
	}
	bad = s
	bad.ClientDataJSON = "e30"
	if err := bad.Verify(pub, ""); err == nil {
		t.Error("es256 with WebAuthn fields accepted")
	}
}

func TestIntentCheck(t *testing.T) {
	for name, change := range map[string]func(*sign.Intent){
		"repo with a line break":  func(i *sign.Intent) { i.Repo = "github.com/x\napprover: evil" },
		"no repo":                 func(i *sign.Intent) { i.Repo = "" },
		"repo with a CR":          func(i *sign.Intent) { i.Repo = "github.com/x\r" },
		"task with a line break":  func(i *sign.Intent) { i.Task = "T-1\nx" },
		"uppercase plan sha":      func(i *sign.Intent) { i.PlanSHA256 = strings.ToUpper(plan) },
		"short nonce":             func(i *sign.Intent) { i.Nonce = "abcd" },
		"head on approve-plan":    func(i *sign.Intent) { i.Head = strings.Repeat("1", 40) },
		"push without head":       func(i *sign.Intent) { i.Action = sign.ActionPush },
		"unknown action":          func(i *sign.Intent) { i.Action = "merge" },
		"time with offset":        func(i *sign.Intent) { i.Time = "2026-10-27T23:10:00+01:00" },
		"time with fraction":      func(i *sign.Intent) { i.Time = "2026-10-27T22:10:00.5Z" },
		"approver with a newline": func(i *sign.Intent) { i.Approver = "a\nb" },
		"empty approver":          func(i *sign.Intent) { i.Approver = "" },
	} {
		in := intent()
		change(&in)
		if err := in.Check(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	push := intent()
	push.Action, push.Head = sign.ActionOpenPR, strings.Repeat("a", 40)
	if err := push.Check(); err != nil {
		t.Error(err)
	}
}

func TestFresh(t *testing.T) {
	in := intent()
	at, _ := in.When()
	for d, ok := range map[time.Duration]bool{0: true, 4 * time.Minute: true, -4 * time.Minute: true, 6 * time.Minute: false, -6 * time.Minute: false} {
		if err := in.Fresh(at.Add(d), 5*time.Minute); (err == nil) != ok {
			t.Errorf("now %+v: %v", d, err)
		}
	}
}

func TestParsePublicKey(t *testing.T) {
	k, pemKey := signtest.Key(t)
	if _, err := sign.ParsePublicKey(pemKey); err != nil {
		t.Fatal(err)
	}
	// The Secure Enclave exports 04||X||Y; this fixed prefix makes it a SubjectPublicKeyInfo.
	prefix, _ := hex.DecodeString("3059301306072a8648ce3d020106082a8648ce3d030107034200")
	raw := elliptic.Marshal(elliptic.P256(), k.X, k.Y)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if string(append(prefix, raw...)) != string(der) {
		t.Fatal("SPKI prefix for a raw P-256 key is wrong")
	}

	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	d384, _ := x509.MarshalPKIXPublicKey(&p384.PublicKey)
	rk, _ := rsa.GenerateKey(rand.Reader, 1024)
	drsa, _ := x509.MarshalPKIXPublicKey(&rk.PublicKey)
	dpriv, _ := x509.MarshalECPrivateKey(k)
	block := func(typ string, b []byte) string { return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: b})) }
	for name, s := range map[string]string{
		"P-384":       block("PUBLIC KEY", d384),
		"RSA":         block("PUBLIC KEY", drsa),
		"private key": block("EC PRIVATE KEY", dpriv),
		"two blocks":  pemKey + pemKey,
		"not PEM":     "hello",
		"empty":       "",
		"garbage DER": block("PUBLIC KEY", []byte("nope")),
	} {
		if _, err := sign.ParsePublicKey(s); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func webauthn(t *testing.T, k *ecdsa.PrivateKey, in sign.Intent, rpID, typ string, flags byte) sign.Signed {
	t.Helper()
	rp := sha256.Sum256([]byte(rpID))
	auth := append(append(rp[:], flags), 0, 0, 0, 7)
	ch := sha256.Sum256(in.Message())
	cd := []byte(`{"type":"` + typ + `","challenge":"` + base64.RawURLEncoding.EncodeToString(ch[:]) + `","origin":"https://` + rpID + `"}`)
	cdh := sha256.Sum256(cd)
	h := sha256.Sum256(append(append([]byte{}, auth...), cdh[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, k, h[:])
	if err != nil {
		t.Fatal(err)
	}
	return sign.Signed{
		Intent: in, Format: sign.FormatWebAuthn, Sig: base64.StdEncoding.EncodeToString(sig),
		AuthenticatorData: base64.RawURLEncoding.EncodeToString(auth), ClientDataJSON: base64.RawURLEncoding.EncodeToString(cd),
	}
}

func TestWebAuthn(t *testing.T) {
	k, pemKey := signtest.Key(t)
	pub, _ := sign.ParsePublicKey(pemKey)
	const rp = "truehand.example"
	if err := webauthn(t, k, intent(), rp, "webauthn.get", 0x05).Verify(pub, rp); err != nil {
		t.Fatal(err)
	}
	other := intent()
	other.Task = "T-99"
	forged := webauthn(t, k, other, rp, "webauthn.get", 0x05)
	forged.Intent = intent()
	cases := map[string]struct {
		s  sign.Signed
		rp string
	}{
		"no user verification":  {webauthn(t, k, intent(), rp, "webauthn.get", 0x01), rp},
		"no user presence":      {webauthn(t, k, intent(), rp, "webauthn.get", 0x04), rp},
		"another relying party": {webauthn(t, k, intent(), "evil.example", "webauthn.get", 0x05), rp},
		"registration, not get": {webauthn(t, k, intent(), rp, "webauthn.create", 0x05), rp},
		"challenge of another":  {forged, rp},
		"no rp_id configured":   {webauthn(t, k, intent(), rp, "webauthn.get", 0x05), ""},
	}
	for name, c := range cases {
		if err := c.s.Verify(pub, c.rp); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestNormalizeRepoURL(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:Spaghetticoders/Truehand.git":               "github.com/spaghetticoders/truehand",
		"https://github.com/spaghetticoders/truehand":               "github.com/spaghetticoders/truehand",
		"https://user:tok@github.com/spaghetticoders/truehand.git/": "github.com/spaghetticoders/truehand",
		"ssh://git@github.com:22/spaghetticoders/truehand.git":      "github.com/spaghetticoders/truehand",
		"/srv/git/demo.git": "/srv/git/demo",
		"":                  "",
	} {
		if got := sign.NormalizeRepoURL(in); got != want {
			t.Errorf("NormalizeRepoURL(%q) = %q, want %q", in, got, want)
		}
	}
	if n := sign.NewNonce(); len(n) != 32 || n == sign.NewNonce() {
		t.Errorf("nonce %q", n)
	}
}
