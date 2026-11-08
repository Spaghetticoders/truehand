package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/appstate/webauthntest"
)

// The bootstrap runs as root and reads the user's passkeys file: only that user's own
// regular file counts, never a symlink nor a file someone else owns.
func TestReviewBootstrapReadsOnlyTheUsersFile(t *testing.T) {
	const code = "ABCDEFGHJKLMNPQR"
	a := webauthntest.NewES256("box.tail1234.ts.net")
	p := appstate.Passkey{ID: a.CredentialID(), PublicKey: a.PublicKeyPEM(), Alg: -7, RPID: a.RPID, Owner: true, Label: "S25"}
	p.Grant = &appstate.Grant{Kind: appstate.GrantAnchor, CodeProof: appstate.CodeProof(code, &p)}
	b, _ := json.Marshal(appstate.Passkeys{Passkeys: []appstate.Passkey{p}})
	dir := t.TempDir()
	path := filepath.Join(dir, "passkeys.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	uid := os.Getuid()
	if got := findAnchoredPasskey(path, uid, code); got == nil || got.ID != p.ID {
		t.Fatalf("the user's own file: %v", got)
	}
	if got := findAnchoredPasskey(path, uid, "ABCDEFGHJKLMNPQS"); got != nil {
		t.Fatal("passkey found with another code")
	}
	if got := findAnchoredPasskey(path, uid+1, code); got != nil {
		t.Fatal("a file owned by another user was read as this user's passkeys")
	}
	link := filepath.Join(dir, "link.json")
	os.Symlink(path, link)
	if got := findAnchoredPasskey(link, uid, code); got != nil {
		t.Fatal("a symlinked passkeys file was followed")
	}
	fifo := filepath.Join(dir, "fifo.json")
	if err := mkfifo(fifo); err == nil {
		if got := findAnchoredPasskey(fifo, uid, code); got != nil {
			t.Fatal("a fifo was read")
		}
	}
}
