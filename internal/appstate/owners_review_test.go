package appstate

import (
	"errors"
	"testing"

	"github.com/spaghetticoders/truehand/internal/appstate/webauthntest"
)

// A lost phone revoked by another owner: the file is the user's, so whoever writes it can put
// the revoked entry back with the grant it had. Revoked durably (root's list, the Mac's
// Keychain), it is not an owner again, whatever the file says.
func TestReviewRevokedOwnerStaysRevoked(t *testing.T) {
	a1, lost := webauthntest.NewES256(rp), webauthntest.NewES256(rp)
	p1, p2 := passkeyOf(a1, true), passkeyOf(lost, true)
	p1.Grant = &Grant{Kind: GrantAnchor}
	p2.Grant = addOwnerGrant(a1, &p2)
	trust := Trust{Anchor: anchorFor(&p1), Revoked: []Revocation{RevocationOf(&p2, now())}}
	// The file restored to before the revocation, soft record and all gone.
	ps := &Passkeys{Passkeys: []Passkey{p1, p2}}
	if got := ps.Owners(trust); got[p2.ID] || !got[p1.ID] {
		t.Fatalf("owners with p2 revoked in root's list = %v", got)
	}
	// Under another id, with the same key: still revoked.
	p3 := p2
	p3.ID = "renamed"
	p3.Grant = addOwnerGrant(a1, &p3)
	ps.Passkeys = append(ps.Passkeys, p3)
	if ps.Owners(trust)["renamed"] {
		t.Fatal("the revoked key came back under a new id")
	}
	// A list that cannot be read trusts nobody rather than forget a revocation.
	if got := ps.Owners(Trust{Anchor: anchorFor(&p1), Broken: errors.New("bad")}); len(got) != 0 {
		t.Fatalf("owners with an unreadable list = %v", got)
	}
}

// The limit, documented: a revocation made only from a passkey lives in the user's file. It
// holds while the file keeps it, and is undone by restoring the file.
func TestSoftRevocationIsOnlyAsGoodAsTheFile(t *testing.T) {
	a1, lost := webauthntest.NewES256(rp), webauthntest.NewES256(rp)
	p1, p2 := passkeyOf(a1, true), passkeyOf(lost, true)
	p1.Grant = &Grant{Kind: GrantAnchor}
	p2.Grant = addOwnerGrant(a1, &p2)
	trust := Trust{Anchor: anchorFor(&p1)}
	soft := &Passkeys{Passkeys: []Passkey{p1, p2}, Soft: []Revocation{RevocationOf(&p2, now())}}
	if soft.Owners(trust)[p2.ID] {
		t.Fatal("soft revocation ignored")
	}
	restored := &Passkeys{Passkeys: []Passkey{p1, p2}}
	if !restored.Owners(trust)[p2.ID] {
		t.Fatal("the documented limit changed: update the docs and this test")
	}
}
