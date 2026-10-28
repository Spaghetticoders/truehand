// Package signtest makes approver keys and signed intents for tests.
package signtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"

	"github.com/spaghetticoders/truehand/internal/sign"
)

// Key returns a new P-256 key and its public half as the PEM a config lists.
func Key(t testing.TB) (*ecdsa.PrivateKey, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return k, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// Sign signs an intent the way the Secure Enclave does: ES256, DER, standard base64.
func Sign(t testing.TB, k *ecdsa.PrivateKey, in sign.Intent) sign.Signed {
	t.Helper()
	h := sha256.Sum256(in.Message())
	sig, err := ecdsa.SignASN1(rand.Reader, k, h[:])
	if err != nil {
		t.Fatal(err)
	}
	return sign.Signed{Intent: in, Format: sign.FormatES256, Sig: base64.StdEncoding.EncodeToString(sig)}
}
