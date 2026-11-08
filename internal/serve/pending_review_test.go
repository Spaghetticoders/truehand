package serve

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/appstate/webauthntest"
)

// The passkeys file is the user's: an agent can swap the key of a passkey that waits for the
// Mac, keeping its id, label and device. What the Mac is then asked to confirm must be the
// key the phone registered, never the swapped one.
func TestReviewSwappedPendingKeyIsNotOffered(t *testing.T) {
	e := newEnv(t, false)
	e.s.SetHosts([]string{tsHost})
	e.s.ReadAnchor = func() (*appstate.Anchor, error) { return nil, nil }
	var code struct {
		Code string `json:"code"`
	}
	e.json("POST", "/api/passkeys/enroll-code", `{"owner":true}`, asShell, &code)
	p := e.phone(false)
	if c, v := p.register(code.Code); c != 200 || v.Pending != "mac" {
		t.Fatalf("register = %d %+v", c, v)
	}
	agent := webauthntest.NewES256(tsHost)
	e.s.Store.UpdatePasskeys(func(ps *appstate.Passkeys) error {
		q, _ := ps.Get(p.auth.CredentialID())
		q.PublicKey = agent.PublicKeyPEM()
		return nil
	})
	resp, _ := e.req("GET", "/api/passkeys/"+p.auth.CredentialID()+"/anchor-intent", "", asShell)
	if resp.StatusCode == http.StatusOK {
		t.Fatal("the Mac is offered to confirm a passkey whose key was swapped in the file")
	}
}

// Nor may an entry the server never registered (written straight into the file) wait for
// the Mac or for an owner.
func TestReviewForgedPendingEntryIsNotOffered(t *testing.T) {
	e := newEnv(t, false)
	e.s.SetHosts([]string{tsHost})
	e.s.ReadAnchor = func() (*appstate.Anchor, error) { return nil, nil }
	agent := webauthntest.NewES256(tsHost)
	e.s.Store.UpdatePasskeys(func(ps *appstate.Passkeys) error {
		ps.Passkeys = append(ps.Passkeys, appstate.Passkey{ID: agent.CredentialID(), PublicKey: agent.PublicKeyPEM(), Alg: -7, RPID: tsHost, Owner: true, Label: "S25 Ultra"})
		return nil
	})
	resp, b := e.req("GET", "/api/passkeys/"+agent.CredentialID()+"/anchor-intent", "", asShell)
	if resp.StatusCode == http.StatusOK {
		var v map[string]any
		json.Unmarshal(b, &v)
		t.Fatalf("the Mac is offered a passkey no registration made: label %v", v["label"])
	}
}

// The same swap against an owner confirming from the phone: add-owner is never offered for a
// key the server did not register.
func TestReviewOwnerIsNotOfferedASwappedKey(t *testing.T) {
	e := newEnv(t, false)
	a := e.headless()
	s25 := e.phone(false)
	e.bootstrap(a, s25)
	var code struct {
		Code string `json:"code"`
	}
	if c := s25.asOwner("POST", "/api/passkeys/enroll-code", `{"owner":true}`, &code); c != 200 {
		t.Fatalf("enroll-code by the owner = %d", c)
	}
	iphone := e.phone(false)
	if c, v := iphone.register(code.Code); c != 200 || !v.Owner {
		t.Fatalf("register = %d %+v", c, v)
	}
	agent := webauthntest.NewES256(tsHost)
	e.s.Store.UpdatePasskeys(func(ps *appstate.Passkeys) error {
		q, _ := ps.Get(iphone.auth.CredentialID())
		q.PublicKey = agent.PublicKeyPEM()
		return nil
	})
	if c, out := s25.signed(appstate.ActionAddOwner, map[string]string{"passkey": iphone.auth.CredentialID()}); c == 200 {
		t.Fatalf("the owner is offered to confirm a swapped key: %s", out)
	}
}
