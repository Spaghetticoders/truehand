package sign

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Real registrations and assertions made by Chromium's virtual authenticator (CTAP2, internal,
// user verification), on http://localhost: a secure context, so the origin is not https.
// Regenerate with testdata/gen-webauthn-vectors.js (Playwright, CDP WebAuthn).
type vector struct {
	Name         string
	RPID         string
	Origin       string
	Challenge    string
	Registration struct {
		ID, ClientDataJSON, AttestationObject string
		Alg                                   int
	}
	Intent       Intent
	ServerIntent ServerIntent
	Assertions   map[string]struct{ AuthenticatorData, ClientDataJSON, Signature string }
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	files, _ := filepath.Glob("testdata/webauthn-*.json")
	if len(files) < 3 {
		t.Fatalf("vectors missing: %v", files)
	}
	var vs []vector
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var v vector
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		vs = append(vs, v)
	}
	return vs
}

func unb64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (v vector) register(t *testing.T) (*Credential, error) {
	return verifyRegistration(v.RPID, v.Origin, unb64(t, v.Challenge), unb64(t, v.Registration.ClientDataJSON), unb64(t, v.Registration.AttestationObject))
}

// signed is the vector's assertion as the browser sends it: the signature re-encoded from
// base64url to the standard base64 Signed carries.
func (v vector) signed(t *testing.T, which string) (string, string, string) {
	a := v.Assertions[which]
	return base64.StdEncoding.EncodeToString(unb64(t, a.Signature)), a.AuthenticatorData, a.ClientDataJSON
}

func TestRealRegistrations(t *testing.T) {
	for _, v := range loadVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			c, err := v.register(t)
			if err != nil {
				t.Fatal(err)
			}
			if CredentialID(c.ID) != v.Registration.ID || c.Alg != v.Registration.Alg {
				t.Fatalf("id %s alg %d", CredentialID(c.ID), c.Alg)
			}
			pub, err := ParsePublicKey(c.PublicKey)
			if err != nil {
				t.Fatal(err)
			}
			switch c.Alg {
			case AlgES256:
				if _, ok := pub.(*ecdsa.PublicKey); !ok {
					t.Fatalf("%T", pub)
				}
			case AlgRS256:
				if k, ok := pub.(*rsa.PublicKey); !ok || k.N.BitLen() < 2048 {
					t.Fatalf("%T", pub)
				}
			}

			sig, ad, cd := v.signed(t, "intent")
			s := Signed{Intent: v.Intent, Format: FormatWebAuthn, Sig: sig, AuthenticatorData: ad, ClientDataJSON: cd}
			if err := s.Intent.Check(); err != nil {
				t.Fatal(err)
			}
			if err := verifyMessage(s.Intent.Message(), s.Format, s.Sig, ad, cd, pub, v.RPID, v.Origin); err != nil {
				t.Fatalf("real assertion: %v", err)
			}
			// The same assertion through the public path wants https://localhost: refused.
			if err := s.Verify(pub, v.RPID); err == nil || !strings.Contains(err.Error(), "signed on") {
				t.Fatalf("http origin accepted for an https rp_id: %v", err)
			}
			tampered := s
			tampered.Intent.Task = "T-2"
			if err := verifyMessage(tampered.Intent.Message(), s.Format, s.Sig, ad, cd, pub, v.RPID, v.Origin); err == nil {
				t.Fatal("assertion verified for another intent")
			}
			if err := verifyMessage(s.Intent.Message(), s.Format, s.Sig, ad, cd, pub, v.RPID, "http://localhost:1"); err == nil {
				t.Fatal("assertion verified for another origin")
			}

			sig, ad, cd = v.signed(t, "server")
			ss := SignedServer{Intent: v.ServerIntent, Format: FormatWebAuthn, Sig: sig, AuthenticatorData: ad, ClientDataJSON: cd}
			if err := ss.verify(pub, v.RPID, v.Origin); err != nil {
				t.Fatalf("real server assertion: %v", err)
			}
			ss.Intent.Subject = Subject(map[string]string{"x": "y"})
			if err := ss.verify(pub, v.RPID, v.Origin); err == nil {
				t.Fatal("server assertion verified for another subject")
			}
		})
	}
}

func TestRealRegistrationRefusals(t *testing.T) {
	v := loadVectors(t)[0]
	ch := unb64(t, v.Challenge)
	cd := unb64(t, v.Registration.ClientDataJSON)
	att := unb64(t, v.Registration.AttestationObject)
	other := append([]byte{}, ch...)
	other[0] ^= 1
	for name, err := range map[string]error{
		"other challenge": func() error { _, err := verifyRegistration(v.RPID, v.Origin, other, cd, att); return err }(),
		"other origin":    func() error { _, err := verifyRegistration(v.RPID, "http://localhost:1", ch, cd, att); return err }(),
		"https expected":  func() error { _, err := VerifyRegistration(v.RPID, ch, cd, att); return err }(),
		"other rp": func() error {
			_, err := verifyRegistration("example.com", v.Origin, ch, cd, att)
			return err
		}(),
		"truncated": func() error { _, err := verifyRegistration(v.RPID, v.Origin, ch, cd, att[:len(att)-1]); return err }(),
		"trailing": func() error {
			_, err := verifyRegistration(v.RPID, v.Origin, ch, cd, append(append([]byte{}, att...), 0))
			return err
		}(),
	} {
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
