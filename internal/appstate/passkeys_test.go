package appstate

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate/webauthntest"
	"github.com/spaghetticoders/truehand/internal/sign"
)

const rp = "box.tail1234.ts.net"

func passkeyOf(a *webauthntest.Authenticator, owner bool) Passkey {
	return Passkey{ID: a.CredentialID(), PublicKey: a.PublicKeyPEM(), Alg: -7, RPID: a.RPID, Owner: owner, Label: "phone"}
}

func intentFor(action string, params map[string]string) sign.ServerIntent {
	return sign.ServerIntent{Action: action, Server: rp, Subject: sign.Subject(params), Nonce: sign.NewNonce(), Time: time.Now().UTC().Format(time.RFC3339)}
}

func addOwnerGrant(by *webauthntest.Authenticator, child *Passkey) *Grant {
	params := child.Binding()
	s := by.SignServer(intentFor(ActionAddOwner, params))
	return &Grant{Kind: GrantOwner, By: by.CredentialID(), Params: params, Signed: &s}
}

func anchorFor(p *Passkey) *Anchor {
	return &Anchor{Version: 1, Owner: &AnchorOwner{Credential: p.ID, PublicKeySHA256: PublicKeySHA256(p.PublicKey), RPID: p.RPID}}
}

func TestOwnerChain(t *testing.T) {
	a1, a2, a3 := webauthntest.NewES256(rp), webauthntest.NewRS256(rp), webauthntest.NewES256(rp)
	p1, p2, p3 := passkeyOf(a1, true), passkeyOf(a2, true), passkeyOf(a3, true)
	p1.Grant = &Grant{Kind: GrantAnchor}
	p2.Grant = addOwnerGrant(a1, &p2)
	p3.Grant = addOwnerGrant(a2, &p3)
	ps := &Passkeys{Passkeys: []Passkey{p3, p2, p1}}
	trust := Trust{Anchor: anchorFor(&p1)}
	got := ps.Owners(trust)
	if len(got) != 3 {
		t.Fatalf("owners = %v, want all three", got)
	}

	// No anchor: nothing is an owner, whatever the file says.
	if got := ps.Owners(Trust{}); len(got) != 0 {
		t.Fatalf("owners without an anchor = %v", got)
	}
	// The anchor names another key for the same id.
	bad := anchorFor(&p1)
	bad.Owner.PublicKeySHA256 = PublicKeySHA256(p2.PublicKey)
	if got := ps.Owners(Trust{Anchor: bad}); len(got) != 0 {
		t.Fatalf("owners with a mismatched anchor = %v", got)
	}

	// Removing the middle owner orphans the one it granted (a deletion is only a DoS).
	cut := &Passkeys{Passkeys: []Passkey{p1, p3}}
	if got := cut.Owners(trust); !got[p1.ID] || got[p3.ID] {
		t.Fatalf("owners after removing p2 = %v", got)
	}
}

func TestForgedOwnersAreRefused(t *testing.T) {
	a1, evil := webauthntest.NewES256(rp), webauthntest.NewES256(rp)
	p1 := passkeyOf(a1, true)
	p1.Grant = &Grant{Kind: GrantAnchor}
	trust := Trust{Anchor: anchorFor(&p1)}

	cases := map[string]func() Passkey{
		"self-granted": func() Passkey {
			p := passkeyOf(evil, true)
			p.Grant = addOwnerGrant(evil, &p)
			return p
		},
		"anchor claimed": func() Passkey {
			p := passkeyOf(evil, true)
			p.Grant = &Grant{Kind: GrantAnchor}
			return p
		},
		"real grant for another passkey": func() Passkey {
			other := passkeyOf(webauthntest.NewES256(rp), true)
			p := passkeyOf(evil, true)
			p.Grant = addOwnerGrant(a1, &other)
			return p
		},
		"real grant, params swapped": func() Passkey {
			other := passkeyOf(webauthntest.NewES256(rp), true)
			p := passkeyOf(evil, true)
			g := addOwnerGrant(a1, &other)
			g.Params = p.Binding()
			p.Grant = g
			return p
		},
		"grant signed by the evil key in the owner's name": func() Passkey {
			p := passkeyOf(evil, true)
			s := evil.SignServer(intentFor(ActionAddOwner, p.Binding()))
			s.Intent.Credential = a1.CredentialID()
			p.Grant = &Grant{Kind: GrantOwner, By: a1.CredentialID(), Params: p.Binding(), Signed: &s}
			return p
		},
		"wrong action": func() Passkey {
			p := passkeyOf(evil, true)
			s := a1.SignServer(intentFor(ActionOwner, p.Binding()))
			p.Grant = &Grant{Kind: GrantOwner, By: a1.CredentialID(), Params: p.Binding(), Signed: &s}
			return p
		},
		"mac grant without a mac key": func() Passkey {
			p := passkeyOf(evil, true)
			k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			in := intentFor(ActionAnchorOwner, p.Binding())
			in.Credential = p.ID
			s := webauthntest.SignES256(k, in)
			p.Grant = &Grant{Kind: GrantMac, Signed: &s}
			return p
		},
	}
	for name, mk := range cases {
		p := mk()
		ps := &Passkeys{Passkeys: []Passkey{p1, p}}
		if got := ps.Owners(trust); got[p.ID] || !got[p1.ID] {
			t.Errorf("%s: owners = %v", name, got)
		}
		if err := ps.GrantError(p.ID, trust); err == nil {
			t.Errorf("%s: GrantError = nil", name)
		}
	}
}

func TestMacGrant(t *testing.T) {
	mac, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	a := webauthntest.NewES256(rp)
	p := passkeyOf(a, true)
	in := intentFor(ActionAnchorOwner, p.Binding())
	in.Credential = p.ID
	s := webauthntest.SignES256(mac, in)
	p.Grant = &Grant{Kind: GrantMac, Signed: &s}
	ps := &Passkeys{Passkeys: []Passkey{p}}
	if !ps.Owners(Trust{MacKey: &mac.PublicKey})[p.ID] {
		t.Fatalf("mac-signed owner refused: %v", ps.GrantError(p.ID, Trust{MacKey: &mac.PublicKey}))
	}
	if ps.Owners(Trust{MacKey: &other.PublicKey})[p.ID] {
		t.Fatal("owner accepted with another Mac's key")
	}
}

func TestRevocationRegrants(t *testing.T) {
	a1, a2, a3 := webauthntest.NewES256(rp), webauthntest.NewES256(rp), webauthntest.NewES256(rp)
	p1, p2, p3 := passkeyOf(a1, true), passkeyOf(a2, true), passkeyOf(a3, true)
	p1.Grant = &Grant{Kind: GrantAnchor}
	p2.Grant = addOwnerGrant(a1, &p2)
	p3.Grant = addOwnerGrant(a2, &p3)
	// p1 revokes p2 and keeps p3 with one signature.
	params := map[string]string{"credential": p2.ID, "keep": KeepList([]*Passkey{&p3})}
	s := a1.SignServer(intentFor(ActionRevokePasskey, params))
	p3.Grant = &Grant{Kind: GrantOwner, By: p1.ID, Params: params, Signed: &s}
	ps := &Passkeys{Passkeys: []Passkey{p1, p3}}
	if got := ps.Owners(Trust{Anchor: anchorFor(&p1)}); !got[p3.ID] {
		t.Fatalf("kept owner lost: %v", ps.GrantError(p3.ID, Trust{Anchor: anchorFor(&p1)}))
	}
	// The same revocation does not make an owner of a passkey it does not keep.
	evil := passkeyOf(webauthntest.NewES256(rp), true)
	evil.Grant = &Grant{Kind: GrantOwner, By: p1.ID, Params: params, Signed: &s}
	ps.Passkeys = append(ps.Passkeys, evil)
	if ps.Owners(Trust{Anchor: anchorFor(&p1)})[evil.ID] {
		t.Fatal("revocation reused for a passkey it does not keep")
	}
}

func TestCodeProof(t *testing.T) {
	p := passkeyOf(webauthntest.NewES256(rp), true)
	p.Grant = &Grant{Kind: GrantAnchor, CodeProof: CodeProof("ABCD", &p)}
	if !CodeProofOK("ABCD", &p) || CodeProofOK("ABCE", &p) {
		t.Fatal("code proof does not tell the code")
	}
	q := passkeyOf(webauthntest.NewES256(rp), true)
	q.Grant = &Grant{Kind: GrantAnchor, CodeProof: p.Grant.CodeProof}
	if CodeProofOK("ABCD", &q) {
		t.Fatal("code proof copied to another passkey")
	}
}

func TestReadAnchor(t *testing.T) {
	dir := t.TempDir()
	old, oldOK := AnchorPath, AnchorFileOK
	t.Cleanup(func() { AnchorPath, AnchorFileOK = old, oldOK })
	AnchorPath = filepath.Join(dir, "truehand", "anchor.json")
	AnchorFileOK = func(os.FileInfo) error { return nil }
	if a, err := ReadAnchor(501); a != nil || err != nil {
		t.Fatalf("missing anchor = %v, %v", a, err)
	}
	os.MkdirAll(filepath.Dir(AnchorPath), 0o755)
	b, _ := json.Marshal(Anchor{Version: 1, UID: 501, CodeSHA256: HashSecret("CODE"), Expires: time.Now().Add(time.Minute)})
	os.WriteFile(AnchorPath, b, 0o644)
	a, err := ReadAnchor(501)
	if err != nil || a == nil || !a.CodeMatches("CODE", time.Now()) || a.CodeMatches("CODE", time.Now().Add(2*time.Minute)) || a.CodeMatches("COD", time.Now()) {
		t.Fatalf("anchor = %+v, %v", a, err)
	}
	if a, _ := ReadAnchor(502); a != nil {
		t.Fatal("anchor of another user accepted")
	}
	// The real check refuses files the user owns.
	AnchorFileOK = rootOwned
	if os.Getuid() != 0 {
		if _, err := ReadAnchor(501); err == nil {
			t.Fatal("user-owned anchor accepted")
		}
	}
	// A symlink is refused even when its target would pass.
	AnchorFileOK = func(os.FileInfo) error { return nil }
	os.Rename(AnchorPath, AnchorPath+".real")
	os.Symlink(AnchorPath+".real", AnchorPath)
	if _, err := ReadAnchor(501); err == nil {
		t.Fatal("symlinked anchor accepted")
	}
}

func TestPasskeysFileIsPrivate(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := webauthntest.NewES256(rp)
	if err := s.UpdatePasskeys(func(ps *Passkeys) error {
		ps.Passkeys = append(ps.Passkeys, passkeyOf(a, false))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(s.Dir, passkeysFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("passkeys.json mode = %v, %v", fi.Mode(), err)
	}
	ps, _ := s.Passkeys()
	if _, ok := ps.Get(a.CredentialID()); !ok {
		t.Fatal("passkey not stored")
	}
}

// Two stores on one directory (as two processes would be) never both spend a nonce.
func TestSpendIsAtomicAcrossStores(t *testing.T) {
	dir := t.TempDir()
	a, _ := Open(dir)
	b, _ := Open(dir)
	var mu sync.Mutex
	won := 0
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		s := a
		if i%2 == 1 {
			s = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.UpdateAuth(func(x *Auth) error {
				if x.SpentIntents == nil {
					x.SpentIntents = map[string]time.Time{}
				}
				if _, ok := x.SpentIntents["n"]; ok {
					return ErrExists
				}
				x.SpentIntents["n"] = time.Now()
				return nil
			})
			if err == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("nonce spent %d times", won)
	}
}

// A revocation list that is there but cannot be trusted fails closed; an absent one revokes
// nothing.
func TestReadRevokedFailsClosed(t *testing.T) {
	dir := t.TempDir()
	old, oldOK := AnchorPath, AnchorFileOK
	t.Cleanup(func() { AnchorPath, AnchorFileOK = old, oldOK })
	AnchorPath = filepath.Join(dir, "truehand", "anchor.json")
	AnchorFileOK = func(os.FileInfo) error { return nil }
	os.MkdirAll(filepath.Dir(AnchorPath), 0o755)
	if l, err := ReadRevoked(501); l != nil || err != nil {
		t.Fatalf("absent list = %v %v", l, err)
	}
	os.WriteFile(RevokedPath(), []byte("{not json"), 0o644)
	if _, err := ReadRevoked(501); err == nil {
		t.Fatal("unparsable list accepted")
	}
	os.WriteFile(RevokedPath()+".real", []byte(`{"version":1,"uid":501,"revoked":[]}`), 0o644)
	os.Remove(RevokedPath())
	os.Symlink(RevokedPath()+".real", RevokedPath())
	if _, err := ReadRevoked(501); err == nil {
		t.Fatal("symlinked list accepted")
	}
	os.Remove(RevokedPath())
	os.WriteFile(RevokedPath(), []byte(`{"version":1,"uid":501,"revoked":[{"credential":"x"}]}`), 0o666)
	AnchorFileOK = rootOwned
	if os.Getuid() != 0 {
		if _, err := ReadRevoked(501); err == nil {
			t.Fatal("user-owned list accepted")
		}
	}
	AnchorFileOK = func(os.FileInfo) error { return nil }
	if l, err := ReadRevoked(501); err != nil || len(l) != 1 {
		t.Fatalf("good list = %v %v", l, err)
	}
	// Another user's list: refused, not ignored.
	if _, err := ReadRevoked(502); err == nil {
		t.Fatal("a list for another uid was taken as no revocations")
	}
}
