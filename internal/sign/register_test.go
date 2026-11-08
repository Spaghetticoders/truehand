package sign

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"math/big"
	"strings"
	"testing"
	"time"
)

// A CBOR encoder for building registrations by hand.
type cb []byte

func cHead(major byte, n uint64) cb {
	switch {
	case n < 24:
		return cb{major<<5 | byte(n)}
	case n < 1<<8:
		return cb{major<<5 | 24, byte(n)}
	case n < 1<<16:
		return binary.BigEndian.AppendUint16(cb{major<<5 | 25}, uint16(n))
	default:
		return binary.BigEndian.AppendUint32(cb{major<<5 | 26}, uint32(n))
	}
}

func cInt(i int64) cb {
	if i >= 0 {
		return cHead(0, uint64(i))
	}
	return cHead(1, uint64(-1-i))
}
func cBytes(b []byte) cb { return append(cHead(2, uint64(len(b))), b...) }
func cText(s string) cb  { return append(cHead(3, uint64(len(s))), s...) }
func cMap(kv ...cb) cb {
	out := cHead(5, uint64(len(kv)/2))
	for _, x := range kv {
		out = append(out, x...)
	}
	return out
}

type reg struct {
	rpID      string
	flags     byte
	credID    []byte
	key       cb
	ext       cb
	trailing  []byte
	typ       string
	challenge []byte
	origin    string
	cross     string
	fmt       cb
	attStmt   cb
}

func ec2Key(t *testing.T) (cb, *ecdsa.PrivateKey) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := k.PublicKey.Bytes()
	return cMap(cInt(1), cInt(2), cInt(3), cInt(-7), cInt(-1), cInt(1), cInt(-2), cBytes(raw[1:33]), cInt(-3), cBytes(raw[33:])), k
}

func rsaKey(t *testing.T, bits int, e int) (cb, *rsa.PrivateKey) {
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	return cMap(cInt(1), cInt(3), cInt(3), cInt(-257), cInt(-1), cBytes(k.N.Bytes()), cInt(-2), cBytes(big.NewInt(int64(e)).Bytes())), k
}

var testChallenge = bytes.Repeat([]byte{7}, 32)

func goodReg(key cb) reg {
	return reg{
		rpID: "host.ts.net", flags: flagUP | flagUV | flagAT | flagBE | flagBS, credID: []byte("credential-1"),
		key: key, typ: "webauthn.create", challenge: testChallenge, origin: "https://host.ts.net",
		fmt: cText("none"), attStmt: cMap(),
	}
}

func (r reg) build() (cdj, att []byte) {
	rp := sha256.Sum256([]byte(r.rpID))
	ad := append([]byte{}, rp[:]...)
	ad = append(ad, r.flags, 0, 0, 0, 9)
	ad = append(ad, bytes.Repeat([]byte{0xaa}, 16)...)
	ad = binary.BigEndian.AppendUint16(ad, uint16(len(r.credID)))
	ad = append(ad, r.credID...)
	ad = append(ad, r.key...)
	ad = append(ad, r.ext...)
	ad = append(ad, r.trailing...)
	cdj = []byte(`{"type":"` + r.typ + `","challenge":"` + base64.RawURLEncoding.EncodeToString(r.challenge) + `","origin":"` + r.origin + `"` + r.cross + `}`)
	att = cMap(cText("fmt"), r.fmt, cText("attStmt"), r.attStmt, cText("authData"), cBytes(ad))
	return cdj, att
}

func TestRegistrationHandMade(t *testing.T) {
	ec, _ := ec2Key(t)
	cdj, att := goodReg(ec).build()
	c, err := VerifyRegistration("host.ts.net", testChallenge, cdj, att)
	if err != nil {
		t.Fatal(err)
	}
	if string(c.ID) != "credential-1" || c.SignCount != 9 || !c.BackupEligible || !c.BackedUp || c.AAGUID[0] != 0xaa || c.Alg != AlgES256 {
		t.Fatalf("%+v", c)
	}
	withPort := goodReg(ec)
	withPort.origin = "https://host.ts.net:8443"
	cdj, att = withPort.build()
	if _, err := VerifyRegistration("host.ts.net", testChallenge, cdj, att); err != nil {
		t.Fatalf("origin on another https port: %v", err)
	}
	if _, err := VerifyRegistration("host.ts.net:8443", testChallenge, cdj, att); err == nil {
		t.Fatal("rp_id with a port accepted")
	}
	rk, _ := rsaKey(t, 2048, 65537)
	cdj, att = goodReg(rk).build()
	if c, err := VerifyRegistration("host.ts.net", testChallenge, cdj, att); err != nil || c.Alg != AlgRS256 {
		t.Fatalf("RS256: %v", err)
	}
	ed := goodReg(ec)
	ed.flags |= flagED
	ed.ext = cMap(cText("credProtect"), cInt(2))
	cdj, att = ed.build()
	if _, err := VerifyRegistration("host.ts.net", testChallenge, cdj, att); err != nil {
		t.Fatalf("extensions: %v", err)
	}
}

func TestRegistrationRefusals(t *testing.T) {
	ec, _ := ec2Key(t)
	weak, _ := rsaKey(t, 1024, 65537)
	e3, _ := rsaKey(t, 2048, 3)
	raw := []byte(ec)
	offCurve := append(cb{}, raw...)
	offCurve[len(offCurve)-1] ^= 1
	cases := map[string]func(*reg){
		"type get":         func(r *reg) { r.typ = "webauthn.get" },
		"other challenge":  func(r *reg) { r.challenge = bytes.Repeat([]byte{8}, 32) },
		"other origin":     func(r *reg) { r.origin = "https://evil.ts.net" },
		"http origin":      func(r *reg) { r.origin = "http://host.ts.net" },
		"cross origin":     func(r *reg) { r.cross = `,"crossOrigin":true` },
		"other rp":         func(r *reg) { r.rpID = "evil.ts.net" },
		"no UP":            func(r *reg) { r.flags &^= flagUP },
		"no UV":            func(r *reg) { r.flags &^= flagUV },
		"no AT":            func(r *reg) { r.flags &^= flagAT },
		"BS without BE":    func(r *reg) { r.flags &^= flagBE },
		"empty cred id":    func(r *reg) { r.credID = nil },
		"cred id too long": func(r *reg) { r.credID = make([]byte, 1024) },
		"trailing bytes":   func(r *reg) { r.trailing = []byte{0} },
		"ED without ext":   func(r *reg) { r.flags |= flagED },
		"ext without ED":   func(r *reg) { r.ext = cMap() },
		"RSA-1024":         func(r *reg) { r.key = weak },
		"RSA e=3":          func(r *reg) { r.key = e3 },
		"point off curve":  func(r *reg) { r.key = offCurve },
		"EdDSA": func(r *reg) {
			r.key = cMap(cInt(1), cInt(1), cInt(3), cInt(-8), cInt(-1), cInt(6), cInt(-2), cBytes(make([]byte, 32)))
		},
		"EC2 with RS256": func(r *reg) { r.key = cMap(cInt(1), cInt(2), cInt(3), cInt(-257)) },
		"P-384 curve":    func(r *reg) { r.key = bytes.Replace(ec, append(cInt(-1), cInt(1)...), append(cInt(-1), cInt(2)...), 1) },
		"duplicate key label": func(r *reg) {
			r.key = append(cMap(cInt(1), cInt(2), cInt(1), cInt(3))[:0:0], cMap(cInt(1), cInt(2), cInt(1), cInt(2), cInt(3), cInt(-7))...)
		},
		"key not a map":   func(r *reg) { r.key = cBytes([]byte{1}) },
		"fmt not text":    func(r *reg) { r.fmt = cInt(1) },
		"attStmt not map": func(r *reg) { r.attStmt = cText("x") },
	}
	for name, change := range cases {
		r := goodReg(ec)
		change(&r)
		cdj, att := r.build()
		if _, err := VerifyRegistration("host.ts.net", testChallenge, cdj, att); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	cdj, att := goodReg(ec).build()
	for name, err := range map[string]error{
		"short challenge": func() error { _, err := VerifyRegistration("host.ts.net", testChallenge[:8], cdj, att); return err }(),
		"bad rp_id":       func() error { _, err := VerifyRegistration("https://host.ts.net", testChallenge, cdj, att); return err }(),
		"huge": func() error {
			_, err := VerifyRegistration("host.ts.net", testChallenge, cdj, make([]byte, 65<<10))
			return err
		}(),
		"no authData": func() error {
			_, err := VerifyRegistration("host.ts.net", testChallenge, cdj, cMap(cText("fmt"), cText("none"), cText("attStmt"), cMap()))
			return err
		}(),
		"duplicate authData": func() error {
			dup := append(append(cHead(5, 4), att[1:]...), append(cText("authData"), cBytes(nil)...)...)
			_, err := VerifyRegistration("host.ts.net", testChallenge, cdj, dup)
			return err
		}(),
	} {
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestCBORLimits(t *testing.T) {
	deep := bytes.Repeat([]byte{0x81}, cborMaxDepth+2)
	deep = append(deep, 0)
	for name, b := range map[string][]byte{
		"empty":             {},
		"too deep":          deep,
		"indefinite map":    {0xbf, 0xff},
		"indefinite bytes":  {0x5f, 0xff},
		"length past end":   {0x5a, 0xff, 0xff, 0xff, 0xff},
		"array count lies":  {0x9b, 0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		"map count lies":    {0xbb, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		"uint past int64":   {0x1b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		"reserved info":     {0x1c},
		"truncated head":    {0x19, 0x01},
		"bad simple":        {0xf8, 0x10},
		"tag without value": {0xc0},
	} {
		if _, _, err := decodeCBOR(b); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if it, n, err := decodeCBOR([]byte{0xa1, 0x01, 0x82, 0xf5, 0xfb, 0, 0, 0, 0, 0, 0, 0, 0, 0xff}); err != nil || n != 13 || it.Kind != cborMap {
		t.Fatalf("map with array of true and float: %v %d", err, n)
	}
}

func FuzzVerifyRegistration(f *testing.F) {
	ec, _ := ec2Key(&testing.T{})
	cdj, att := goodReg(ec).build()
	f.Add(att)
	f.Add([]byte{0xa0})
	f.Fuzz(func(t *testing.T, att []byte) {
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = VerifyRegistration("host.ts.net", testChallenge, cdj, att)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("registration parse took over 2s")
		}
	})
}

func FuzzCBOR(f *testing.F) {
	f.Add([]byte{0xa1, 0x01, 0x82, 0xf5, 0xf6})
	f.Add(bytes.Repeat([]byte{0x81}, 20))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, n, err := decodeCBOR(b)
		if err == nil && (n <= 0 || n > len(b)) {
			t.Fatalf("used %d of %d bytes", n, len(b))
		}
	})
}

// webauthnSig signs msg as a browser would with key k for origin, with these flags.
func webauthnSig(t *testing.T, k crypto.Signer, msg []byte, rpHost, origin, typ, extra string, flags byte) (sig, ad, cd string) {
	t.Helper()
	ch := sha256.Sum256(msg)
	rp := sha256.Sum256([]byte(rpHost))
	auth := append(rp[:], flags, 0, 0, 0, 1)
	cdj := []byte(`{"type":"` + typ + `","challenge":"` + base64.RawURLEncoding.EncodeToString(ch[:]) + `","origin":"` + origin + `"` + extra + `}`)
	cdh := sha256.Sum256(cdj)
	h := sha256.Sum256(append(append([]byte{}, auth...), cdh[:]...))
	var opts crypto.SignerOpts = crypto.SHA256
	s, err := k.Sign(rand.Reader, h[:], opts)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(s), base64.RawURLEncoding.EncodeToString(auth), base64.RawURLEncoding.EncodeToString(cdj)
}

func testIntent() Intent {
	return Intent{
		Action: ActionApprovePlan, Repo: "github.com/spaghetticoders/demo", Task: "T-1",
		PlanSHA256: strings.Repeat("a", 64), Nonce: strings.Repeat("b", 32),
		Time: "2026-11-07T10:00:00Z", Approver: "phone",
	}
}

func TestWebAuthnClientDataChecks(t *testing.T) {
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	in := testIntent()
	for _, k := range []crypto.Signer{ek, rk} {
		pub := k.Public()
		ok := func(rpID, origin string) Signed {
			host, _, _ := SplitRPID(rpID)
			sig, ad, cd := webauthnSig(t, k, in.Message(), host, origin, "webauthn.get", "", 0x05)
			return Signed{Intent: in, Format: FormatWebAuthn, Sig: sig, AuthenticatorData: ad, ClientDataJSON: cd}
		}
		if err := ok("host.ts.net", "https://host.ts.net").Verify(pub, "host.ts.net"); err != nil {
			t.Fatalf("%T: %v", pub, err)
		}
		if err := ok("host.ts.net", "https://host.ts.net:8443").Verify(pub, "host.ts.net"); err != nil {
			t.Fatalf("%T on port 8443: %v", pub, err)
		}
		if err := ok("host.ts.net", "https://host.ts.net:8443").Verify(pub, "host.ts.net:8443"); err == nil {
			t.Fatalf("%T rp_id with a port accepted", pub)
		}
		for name, s := range map[string]Signed{
			"http origin":      ok("host.ts.net", "http://host.ts.net"),
			"subdomain origin": ok("host.ts.net", "https://evil.host.ts.net"),
			"bad port":         ok("host.ts.net", "https://host.ts.net:99999"),
			"cross origin": func() Signed {
				sig, ad, cd := webauthnSig(t, k, in.Message(), "host.ts.net", "https://host.ts.net", "webauthn.get", `,"crossOrigin":true`, 0x05)
				return Signed{Intent: in, Format: FormatWebAuthn, Sig: sig, AuthenticatorData: ad, ClientDataJSON: cd}
			}(),
			"create type": func() Signed {
				sig, ad, cd := webauthnSig(t, k, in.Message(), "host.ts.net", "https://host.ts.net", "webauthn.create", "", 0x05)
				return Signed{Intent: in, Format: FormatWebAuthn, Sig: sig, AuthenticatorData: ad, ClientDataJSON: cd}
			}(),
		} {
			if err := s.Verify(pub, "host.ts.net"); err == nil {
				t.Errorf("%T %s accepted", pub, name)
			}
		}
		crossFalse := func() Signed {
			sig, ad, cd := webauthnSig(t, k, in.Message(), "host.ts.net", "https://host.ts.net", "webauthn.get", `,"crossOrigin":false`, 0x05)
			return Signed{Intent: in, Format: FormatWebAuthn, Sig: sig, AuthenticatorData: ad, ClientDataJSON: cd}
		}()
		if err := crossFalse.Verify(pub, "host.ts.net"); err != nil {
			t.Errorf("crossOrigin false refused: %v", err)
		}
	}
	// An RSA key never signs the raw ES256 format.
	h := sha256.Sum256(in.Message())
	raw, _ := rsa.SignPKCS1v15(rand.Reader, rk, crypto.SHA256, h[:])
	s := Signed{Intent: in, Format: FormatES256, Sig: base64.StdEncoding.EncodeToString(raw)}
	if err := s.Verify(&rk.PublicKey, ""); err == nil {
		t.Fatal("RSA es256 accepted")
	}
	// A weak RSA key handed in directly still does not verify.
	weak, _ := rsa.GenerateKey(rand.Reader, 1024)
	sig, ad, cd := webauthnSig(t, weak, in.Message(), "host.ts.net", "https://host.ts.net", "webauthn.get", "", 0x05)
	if err := (Signed{Intent: in, Format: FormatWebAuthn, Sig: sig, AuthenticatorData: ad, ClientDataJSON: cd}).Verify(&weak.PublicKey, "host.ts.net"); err == nil {
		t.Fatal("RSA-1024 assertion accepted")
	}
}

func TestConfigActions(t *testing.T) {
	head := strings.Repeat("c", 40)
	for _, a := range []string{ActionCommitConfig, ActionPushBase} {
		in := testIntent()
		in.Action, in.Task, in.Head = a, ConfigTask, head
		if err := in.Check(); err != nil {
			t.Fatalf("%s: %v", a, err)
		}
		if !bytes.Contains(in.Message(), []byte("action: "+a+"\nrepo: ")) || !bytes.Contains(in.Message(), []byte("task: config\n")) {
			t.Fatalf("%s", in.Message())
		}
		for name, change := range map[string]func(*Intent){
			"no head":    func(i *Intent) { i.Head = "" },
			"other task": func(i *Intent) { i.Task = "T-1" },
		} {
			bad := in
			change(&bad)
			if bad.Check() == nil {
				t.Errorf("%s %s accepted", a, name)
			}
		}
	}
	in := testIntent()
	in.Task = ConfigTask
	if in.Check() == nil {
		t.Fatal("approve-plan on task config accepted")
	}
}

func TestServerIntent(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	in := ServerIntent{
		Action: "pair-code", Server: "host.ts.net", Subject: Subject(map[string]string{"device": "s25"}),
		Nonce: strings.Repeat("d", 32), Time: time.Now().UTC().Format("2006-01-02T15:04:05Z"), Credential: "AbC-_9",
	}
	want := "truehand-server-intent/1\naction: pair-code\nserver: host.ts.net\nsubject: " + in.Subject +
		"\nnonce: " + in.Nonce + "\ntime: " + in.Time + "\ncredential: AbC-_9\n"
	if string(in.Message()) != want {
		t.Fatalf("%q", in.Message())
	}
	if Subject(map[string]string{"b": "2", "a": "1"}) != Subject(map[string]string{"a": "1", "b": "2"}) ||
		Subject(map[string]string{"a": "1\nb: 2"}) == Subject(map[string]string{"a": "1", "b": "2"}) || Subject(nil) != Subject(map[string]string{}) {
		t.Fatal("subject is not canonical")
	}
	sig, ad, cd := webauthnSig(t, k, in.Message(), "host.ts.net", "https://host.ts.net", "webauthn.get", "", 0x05)
	s := SignedServer{Intent: in, Format: FormatWebAuthn, Sig: sig, AuthenticatorData: ad, ClientDataJSON: cd}
	if err := s.Verify(&k.PublicKey, "host.ts.net"); err != nil {
		t.Fatal(err)
	}
	if err := in.Fresh(time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(&k.PublicKey, "other.ts.net"); err == nil {
		t.Fatal("verified for another server")
	}
	// Signed on this server's origin, but naming another server: refused.
	elsewhere := in
	elsewhere.Server = "other.ts.net"
	sig2, ad2, cd2 := webauthnSig(t, k, elsewhere.Message(), "host.ts.net", "https://host.ts.net", "webauthn.get", "", 0x05)
	if err := (SignedServer{Intent: elsewhere, Format: FormatWebAuthn, Sig: sig2, AuthenticatorData: ad2, ClientDataJSON: cd2}).Verify(&k.PublicKey, "host.ts.net"); err == nil {
		t.Fatal("intent naming another server accepted")
	}
	es := s
	h := sha256.Sum256(in.Message())
	raw, _ := ecdsa.SignASN1(rand.Reader, k, h[:])
	es.Format, es.Sig, es.AuthenticatorData, es.ClientDataJSON = FormatES256, base64.StdEncoding.EncodeToString(raw), "", ""
	if err := es.Verify(&k.PublicKey, "host.ts.net"); err == nil {
		t.Fatal("owner intent signed without a passkey")
	}
	if err := es.VerifyES256(&k.PublicKey, "host.ts.net"); err != nil {
		t.Fatalf("mac anchor: %v", err)
	}
	if err := es.VerifyES256(&k.PublicKey, "other.ts.net"); err == nil {
		t.Fatal("mac anchor for another server accepted")
	}
	if err := s.VerifyES256(&k.PublicKey, "host.ts.net"); err == nil {
		t.Fatal("webauthn assertion accepted as es256")
	}
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err := es.VerifyES256(&other.PublicKey, "host.ts.net"); err == nil {
		t.Fatal("mac anchor by another key accepted")
	}
	if n, err := s.SignCount(); err != nil || n != 1 {
		t.Fatalf("sign count %d %v", n, err)
	}
	if n, err := es.SignCount(); err != nil || n != 0 {
		t.Fatalf("es256 sign count %d %v", n, err)
	}
	// A repo intent's signature never passes for a server intent, nor the other way.
	repo := Signed{Intent: testIntent(), Format: FormatWebAuthn, Sig: sig, AuthenticatorData: ad, ClientDataJSON: cd}
	if err := repo.Verify(&k.PublicKey, "host.ts.net"); err == nil {
		t.Fatal("server signature passed for a repo intent")
	}
	for name, change := range map[string]func(*ServerIntent){
		"action":     func(i *ServerIntent) { i.Action = "Pair code" },
		"server":     func(i *ServerIntent) { i.Server = "https://host" },
		"subject":    func(i *ServerIntent) { i.Subject = "x" },
		"nonce":      func(i *ServerIntent) { i.Nonce = "short" },
		"time":       func(i *ServerIntent) { i.Time = "yesterday" },
		"credential": func(i *ServerIntent) { i.Credential = "a=\n" },
		"empty cred": func(i *ServerIntent) { i.Credential = "" },
		"long cred":  func(i *ServerIntent) { i.Credential = strings.Repeat("a", 1365) },
	} {
		bad := in
		change(&bad)
		if bad.Check() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestCheckRPID(t *testing.T) {
	for _, ok := range []string{"localhost", "host.tail1234.ts.net", "a-b.example", "xn--bcher-kva.example"} {
		if err := CheckRPID(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "100.64.0.1", "127.0.0.1", "1.2", "::1", "[::1]", "host-.ts.net", "-host.ts.net", "Host.ts.net", "host.ts.net:8443", "host.ts.net."} {
		if CheckRPID(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
