package cli

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate"
)

func init() {
	register("passkey bootstrap", "Register the first owner passkey of a server without a Mac (run with sudo, at the server's own terminal)", cmdPasskeyBootstrap)
	register("passkey revoke", "Revoke an owner passkey for good, where the user's processes cannot undo it (run with sudo)", cmdPasskeyRevoke)
}

// bootstrapEnv is everything the bootstrap touches outside itself, so tests can run it
// without root, a terminal or sudo.
type bootstrapEnv struct {
	euid      int
	sudoUID   string
	sudoUser  string
	tty       func() (io.WriteCloser, error)
	sudoNoPwd func(u *user.User) bool
	lookup    func(uid string) (*user.User, error)
	anchor    string
	writeFile func(path string, b []byte) error
	now       func() time.Time
	sleep     func(time.Duration)
	host      func() string
	ttl       time.Duration
	// dirOK checks the anchor directory as it exists (root's, not writable by others).
	dirOK func(os.FileInfo) error
	// confirm asks a question on the terminal and reports whether the answer was "yes";
	// yes skips it.
	confirm func(question string) (bool, error)
	yes     bool
}

func realBootstrapEnv() bootstrapEnv {
	return bootstrapEnv{
		euid: os.Geteuid(), sudoUID: os.Getenv("SUDO_UID"), sudoUser: os.Getenv("SUDO_USER"),
		tty:       func() (io.WriteCloser, error) { return os.OpenFile("/dev/tty", os.O_WRONLY, 0) },
		sudoNoPwd: sudoWithoutPassword, lookup: user.LookupId, anchor: appstate.AnchorPath,
		writeFile: writeRootFile, now: time.Now, sleep: time.Sleep, ttl: 10 * time.Minute, dirOK: appstate.AnchorFileOK, confirm: confirmOnTTY,
		host: func() string {
			if self, ok := tailscaleSelfInfo(); ok {
				return strings.TrimSuffix(self.DNSName, ".")
			}
			return ""
		},
	}
}

func passkeyEnv() (bootstrapEnv, error) {
	env := realBootstrapEnv()
	if e2eBootstrap != nil {
		if err := e2eBootstrap(&env); err != nil {
			return env, err
		}
	}
	return env, nil
}

func cmdPasskeyRevoke(c *ctx, args []string) error {
	fs := flag.NewFlagSet("passkey revoke", flag.ContinueOnError)
	repair := fs.Bool("repair", false, "rewrite a revocation list that does not parse, keeping every id it names (the old file is kept as .bak)")
	yes := fs.Bool("yes", false, "revoke without asking on the terminal (scripts)")
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *repair != (len(pos) == 0) || len(pos) > 1 {
		return errors.New("usage: sudo truehand passkey revoke <passkey id> | --repair")
	}
	env, err := passkeyEnv()
	if err != nil {
		return err
	}
	if *repair {
		return repairRevoked(c, env)
	}
	env.yes = *yes
	return revoke(c, env, pos[0])
}

func revokedListPath(env bootstrapEnv) string {
	return filepath.Join(filepath.Dir(env.anchor), "revoked.json")
}

// readRootList reads root's revocation list without following a link and only if it is a
// regular file: as root, a link's target could be anyone's file (or /etc/shadow). It
// returns os.ErrNotExist when there is none.
func readRootList(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(path)
		return nil, fmt.Errorf("%s is a link (to %s), not root's list: nothing is read through it. Check that file; if it is the real list, `sudo cp --remove-destination %s %s`, then run this again", path, target, target, path)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file: remove it by hand, then revoke lost passkeys again", path)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s changed while being read: run this again", path)
	}
	return io.ReadAll(io.LimitReader(f, 4<<20))
}

// checkRootDir refuses an anchor directory that is a link, not a directory, or not root's
// alone: bootstrap and revoke would otherwise make, chown or write through it. A missing one
// is made later.
func checkRootDir(env bootstrapEnv) error {
	dir := filepath.Dir(env.anchor)
	fi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s is not a directory (a link?): fix it by hand, nothing was changed", dir)
	}
	if env.dirOK != nil {
		if err := env.dirOK(fi); err != nil {
			return fmt.Errorf("%s: %w: fix it by hand (root's, 0755), nothing was changed", dir, err)
		}
	}
	return nil
}

// keyIsOnlyThis refuses a key that, in the user's file, also belongs to another passkey, or
// that is the anchor owner's under another id.
func keyIsOnlyThis(env bootstrapEnv, ps *appstate.Passkeys, p *appstate.Passkey, uid int) error {
	sha := appstate.PublicKeySHA256(p.PublicKey)
	for _, q := range ps.Passkeys {
		if q.ID != p.ID && appstate.PublicKeySHA256(q.PublicKey) == sha {
			return fmt.Errorf("the key of %s also belongs to %s: the passkeys file was tampered with. Nothing revoked; revoke by id only after checking which is which", p.ID, q.ID)
		}
	}
	if b, err := readRootList(env.anchor); err == nil {
		var a appstate.Anchor
		if json.Unmarshal(b, &a) == nil && a.UID == uid && a.Owner != nil && a.Owner.PublicKeySHA256 == sha && a.Owner.Credential != p.ID {
			return fmt.Errorf("the key of %s is the anchor owner's (%s): the passkeys file was tampered with. Nothing revoked", p.ID, a.Owner.Credential)
		}
	}
	return nil
}

// groupKey shows a fingerprint as people compare it: "abcd ef01 2345".
func groupKey(k string) string {
	var parts []string
	for i := 0; i < len(k); i += 4 {
		parts = append(parts, k[i:min(i+4, len(k))])
	}
	return strings.Join(parts, " ")
}

// confirmOnTTY asks on the controlling terminal, never on stdin a script could fill.
func confirmOnTTY(question string) (bool, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	fmt.Fprint(f, question)
	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && line == "" {
		return false, err
	}
	return strings.TrimSpace(line) == "yes", nil
}

func brokenListError(path, user string) error {
	return fmt.Errorf("%s does not parse as a revocation list for %s: run `sudo truehand passkey revoke --repair` (it keeps every id it can read and the old file as .bak). By hand: `sudo mv %s %s.bak`, then `sudo truehand passkey revoke <id>` for each id in the .bak", path, user, path, path)
}

var revocationBlockRe = regexp.MustCompile(`\{[^{}]*\}`)
var keyHashRe = regexp.MustCompile(`"public_key_sha256"\s*:\s*"([0-9a-f]{64})"`)

// repairRevoked replaces a revocation list that does not parse with one revoking every
// credential it names, in one rename: there is no moment without the list. A list that
// parses is only rewritten as root's.
func repairRevoked(c *ctx, env bootstrapEnv) error {
	if env.euid != 0 || env.sudoUID == "" || env.sudoUID == "0" {
		return errors.New("run it as `sudo truehand passkey revoke --repair` from your own account")
	}
	u, err := env.lookup(env.sudoUID)
	if err != nil {
		return fmt.Errorf("user %s: %w", env.sudoUID, err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	if err := checkRootDir(env); err != nil {
		return err
	}
	path := revokedListPath(env)
	b, err := readRootList(path)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(c.stdout, "There is no revocation list: nothing to repair.")
		return nil
	}
	if err != nil {
		return err
	}
	l := appstate.RevokedList{Version: 1, UID: uid}
	var parsed appstate.RevokedList
	if json.Unmarshal(b, &parsed) == nil && parsed.Version == 1 {
		if parsed.UID != uid {
			return fmt.Errorf("%s is the revocation list of uid %d, not %s: leave it", path, parsed.UID, u.Username)
		}
		l = parsed
	} else {
		if err := env.writeFile(path+".bak", b); err != nil {
			return err
		}
		// Root's data only: not for other users to read.
		if err := os.Chmod(path+".bak", 0o600); err != nil {
			return err
		}
		now := env.now().UTC().Truncate(time.Second)
		seen := map[string]bool{}
		add := func(id, sha string) {
			if !seen[id] {
				seen[id] = true
				l.Revoked = append(l.Revoked, appstate.Revocation{Credential: id, PublicKeySHA256: sha, Label: "recovered", Time: now})
			}
		}
		for _, blk := range revocationBlockRe.FindAllString(string(b), -1) {
			if m := credentialRe.FindStringSubmatch(blk); m != nil {
				sha := ""
				if k := keyHashRe.FindStringSubmatch(blk); k != nil {
					sha = k[1]
				}
				add(m[1], sha)
			}
		}
		// The last entry of a cut-off file has no closing brace: its key is taken only with
		// the credential of that same fragment, never paired with another id.
		if i := strings.LastIndexByte(string(b), '{'); i >= 0 && !strings.ContainsRune(string(b[i:]), '}') {
			frag := string(b[i:])
			if m := credentialRe.FindStringSubmatch(frag); m != nil {
				if k := keyHashRe.FindStringSubmatch(frag); k != nil {
					add(m[1], k[1])
				}
			}
		}
		for _, m := range credentialRe.FindAllStringSubmatch(string(b), -1) {
			add(m[1], "")
		}
	}
	if l.Revoked == nil {
		l.Revoked = []appstate.Revocation{}
	}
	out, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := env.writeFile(path, append(out, '\n')); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "%s rewritten as root's, revoking %d passkey(s):\n", path, len(l.Revoked))
	for _, x := range l.Revoked {
		fmt.Fprintf(c.stdout, "  %s %s\n", x.Credential, x.Label)
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		fmt.Fprintf(c.stdout, "The old file is %s.bak: check it, then remove it.\n", path)
	}
	return nil
}

// revoke adds a passkey to root's revocation list, by id and, when the user's file still
// has it, by key.
func revoke(c *ctx, env bootstrapEnv, id string) error {
	if env.euid != 0 || env.sudoUID == "" || env.sudoUID == "0" {
		return errors.New("run it as `sudo truehand passkey revoke <id>` from your own account")
	}
	u, err := env.lookup(env.sudoUID)
	if err != nil {
		return fmt.Errorf("user %s: %w", env.sudoUID, err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	if err := checkRootDir(env); err != nil {
		return err
	}
	path := revokedListPath(env)
	l := appstate.RevokedList{Version: 1, UID: uid}
	if b, err := readRootList(path); err == nil {
		if err := json.Unmarshal(b, &l); err != nil || l.Version != 1 || l.UID != uid {
			return brokenListError(path, u.Username)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	rv := appstate.Revocation{Credential: id, Time: env.now().UTC().Truncate(time.Second)}
	question := fmt.Sprintf("Revoke for good the passkey %s (id only: it is not in %s's passkeys)?", id, u.Username)
	ps := readUserPasskeys(filepath.Join(u.HomeDir, ".truehand", "app", "passkeys.json"), uid)
	var p *appstate.Passkey
	if ps != nil {
		p, _ = ps.Get(id)
	}
	if p != nil {
		// The user's file is the agents' to write too: a key that is also another passkey's,
		// or the anchor owner's, was put there to make root revoke the wrong key.
		if err := keyIsOnlyThis(env, ps, p, uid); err != nil {
			return err
		}
		rv = appstate.RevocationOf(p, env.now())
		question = fmt.Sprintf("Revoke for good the passkey %q (%s), key %s?", p.Label, id, groupKey(rv.Key))
	} else {
		fmt.Fprintf(c.stderr, "warning: %s is not in %s's passkeys: revoking the id only\n", id, u.Username)
	}
	if !env.yes {
		if env.confirm == nil {
			return errors.New("no terminal to confirm on: run it at a terminal, or pass --yes")
		}
		ok, err := env.confirm(question + " Compare it with the fingerprint shown for this passkey in Settings → Passkeys on a device you still have. Type yes: ")
		if err != nil {
			return fmt.Errorf("no terminal to confirm on (%v): run it at a terminal, or pass --yes", err)
		}
		if !ok {
			return errors.New("not revoked")
		}
	}
	done := "Revoked %s %q for good.\n"
	found := false
	for i, x := range l.Revoked {
		if x.Credential != id {
			continue
		}
		found = true
		// An entry without the key (recovered by --repair) is completed while the passkey
		// is still known, so the same key under a new id stays revoked too.
		if x.PublicKeySHA256 != "" || rv.PublicKeySHA256 == "" {
			fmt.Fprintf(c.stdout, "%s is already revoked.\n", id)
			return nil
		}
		rv.Time = x.Time
		l.Revoked[i] = rv
		done = "Key added to the revocation of %s %q.\n"
	}
	if !found {
		l.Revoked = append(l.Revoked, rv)
	}
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := env.writeFile(path, append(b, '\n')); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, done, id, rv.Label)
	if todo := pendingFromBackup(path+".bak", l.Revoked); len(todo) > 0 {
		fmt.Fprintf(c.stdout, "Still to revoke again from %s.bak:\n", path)
		for _, x := range todo {
			fmt.Fprintf(c.stdout, "  sudo truehand passkey revoke %s\n", x)
		}
	}
	return nil
}

var credentialRe = regexp.MustCompile(`"credential"\s*:\s*"([A-Za-z0-9_-]+)"`)

// pendingFromBackup lists the credentials named in a set-aside list (parsed, or read as
// best it can) that the current list does not revoke yet.
func pendingFromBackup(path string, have []appstate.Revocation) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	done := map[string]bool{}
	for _, x := range have {
		done[x.Credential] = true
	}
	var out []string
	for _, m := range credentialRe.FindAllStringSubmatch(string(b), -1) {
		if !done[m[1]] {
			done[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

func cmdPasskeyBootstrap(c *ctx, args []string) error {
	fs := flag.NewFlagSet("passkey bootstrap", flag.ContinueOnError)
	if _, _, err := parse(fs, args); err != nil {
		return err
	}
	env, err := passkeyEnv()
	if err != nil {
		return err
	}
	return bootstrap(c, env)
}

// sudoWithoutPassword reports whether u can use sudo without typing a password, cached
// credentials aside: then anything running as u, agents included, could write the anchor.
func sudoWithoutPassword(u *user.User) bool {
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	cmd := exec.Command("sudo", "-n", "-k", "true")
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + u.HomeDir}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}, Setsid: true}
	return cmd.Run() == nil
}

// writeRootFile writes b to path as root, 0644, in a 0755 directory, atomically.
func writeRootFile(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.Chown(dir, 0, 0); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".anchor.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func formatCode(code string) string {
	var parts []string
	for i := 0; i < len(code); i += 4 {
		parts = append(parts, code[i:min(i+4, len(code))])
	}
	return strings.Join(parts, "-")
}

func bootstrap(c *ctx, env bootstrapEnv) error {
	if env.euid != 0 || env.sudoUID == "" || env.sudoUID == "0" {
		return errors.New("run it as `sudo truehand passkey bootstrap` from your own account: root makes the anchor, your account runs the server")
	}
	u, err := env.lookup(env.sudoUID)
	if err != nil {
		return fmt.Errorf("user %s: %w", env.sudoUID, err)
	}
	if env.sudoNoPwd(u) {
		return fmt.Errorf("%s can use sudo without a password, so its agents could make an anchor too. Require a password for sudo (remove NOPASSWD for %s), then run this again", u.Username, u.Username)
	}
	if err := checkRootDir(env); err != nil {
		return err
	}
	tty, err := env.tty()
	if err != nil {
		return errors.New("no terminal: run it at the server's own terminal (or over ssh with a tty), never from a service or a script")
	}
	defer tty.Close()
	uid, _ := strconv.Atoi(u.Uid)

	prev, _ := readAnchorFile(env.anchor, uid)
	code := randomBootstrapCode()
	now := env.now().UTC()
	a := appstate.Anchor{Version: 1, UID: uid, CodeSHA256: appstate.HashSecret(code), Expires: now.Add(env.ttl).Truncate(time.Second)}
	if prev != nil {
		a.Owner = prev.Owner
	}
	if err := writeAnchor(env, a); err != nil {
		return err
	}
	host := env.host()
	url := "https://" + host + "/enroll"
	if host == "" {
		url = "https://<this machine>.ts.net/enroll (tailscale serve is not answering: run `truehand linux install` first)"
	}
	fmt.Fprintf(tty, "\nOn the phone that will approve, open:\n  %s\nand enter this one-time code (valid %d minutes):\n\n    %s\n\nKeep this terminal away from agents, and run `sudo -k` when done.\nWaiting for the passkey...\n", url, int(env.ttl/time.Minute), formatCode(code))
	fmt.Fprintln(c.stdout, "Code shown on the terminal; waiting for the passkey.")

	pkPath := filepath.Join(u.HomeDir, ".truehand", "app", "passkeys.json")
	for env.now().Before(a.Expires) {
		if p := findAnchoredPasskey(pkPath, uid, code); p != nil {
			done := appstate.Anchor{Version: 1, UID: uid, Owner: &appstate.AnchorOwner{
				Credential: p.ID, PublicKeySHA256: appstate.PublicKeySHA256(p.PublicKey), RPID: p.RPID, Label: p.Label, Created: env.now().UTC().Truncate(time.Second),
			}}
			if err := writeAnchor(env, done); err != nil {
				return err
			}
			fmt.Fprintf(tty, "Owner passkey %q (%s) registered for %s.\n", p.Label, p.RPID, u.Username)
			fmt.Fprintf(c.stdout, "Owner passkey %q registered.\n", p.Label)
			return nil
		}
		env.sleep(2 * time.Second)
	}
	a.CodeSHA256, a.Expires = "", time.Time{}
	if err := writeAnchor(env, a); err != nil {
		return err
	}
	return errors.New("no passkey registered with the code in time: run it again")
}

func writeAnchor(env bootstrapEnv, a appstate.Anchor) error {
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return env.writeFile(env.anchor, append(b, '\n'))
}

func randomBootstrapCode() string {
	// 16 characters of 32: 80 bits, since the anchor's hash of it is readable by everyone.
	return newCode(16)
}

func readAnchorFile(path string, uid int) (*appstate.Anchor, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var a appstate.Anchor
	if err := json.Unmarshal(b, &a); err != nil || a.UID != uid {
		return nil, errors.New("no anchor for this user")
	}
	return &a, nil
}

// readUserPasskeys reads the user's passkeys as root: no symlinks, only the user's own
// regular file.
func readUserPasskeys(path string, uid int) *appstate.Passkeys {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 4<<20 {
		return nil
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
		return nil
	}
	var ps appstate.Passkeys
	if err := json.NewDecoder(io.LimitReader(f, 4<<20)).Decode(&ps); err != nil {
		return nil
	}
	return &ps
}

func readUserPasskey(path string, uid int, id string) *appstate.Passkey {
	if ps := readUserPasskeys(path, uid); ps != nil {
		if p, ok := ps.Get(id); ok {
			return p
		}
	}
	return nil
}

// findAnchoredPasskey finds the owner passkey that proves it was registered with code.
func findAnchoredPasskey(path string, uid int, code string) *appstate.Passkey {
	ps := readUserPasskeys(path, uid)
	if ps == nil {
		return nil
	}
	for i := range ps.Passkeys {
		p := &ps.Passkeys[i]
		if p.Owner && p.Grant != nil && p.Grant.Kind == appstate.GrantAnchor && appstate.CodeProofOK(code, p) {
			return p
		}
	}
	return nil
}

const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func newCode(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(b)
}
