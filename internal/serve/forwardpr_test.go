package serve

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"context"

	"github.com/spaghetticoders/truehand/internal/sign"
)

// signedBody is an actions body with an open-pr intent signed by k.
func signedBody(t *testing.T, k *ecdsa.PrivateKey, in sign.Intent) string {
	t.Helper()
	h := sha256.Sum256(in.Message())
	sig, err := ecdsa.SignASN1(rand.Reader, k, h[:])
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"signedIntent": sign.Signed{Intent: in, Format: sign.FormatES256, Sig: base64.StdEncoding.EncodeToString(sig)}})
	return string(b)
}

func openPRIntent(head string, at time.Time) sign.Intent {
	return sign.Intent{Action: sign.ActionOpenPR, Repo: "github.com/acme/app", Task: "T-1", PlanSHA256: strings.Repeat("c", 64), Head: head, Nonce: sign.NewNonce(), Time: at.UTC().Format("2006-01-02T15:04:05Z"), Approver: "mac"}
}

// A PR a remote machine hands back is opened only for an intent this Mac's own key signed,
// recently, once: the machine's own check does not count, since it is the one distrusted.
func TestForwardedPRNeedsThisMacsSignature(t *testing.T) {
	e := newEnv(t, false)
	head := strings.Repeat("a", 40)
	pr := `{"ok":false,"prRequest":{"repo":"github.com/acme/app","head":"th/T-1","title":"x","body":"y"}}`
	mid := hostilePeer(t, e, fakePeerRT{status: 200, header: http.Header{"Content-Type": {"application/json"}}, body: pr})
	created := 0
	e.s.Run = func(ctx context.Context, d string, env []string, name string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "api" {
			return []byte(head), nil
		}
		created++
		return []byte("https://github.com/acme/app/pull/1\n"), nil
	}
	mac, _ := newKey(t)
	other, _ := newKey(t)
	path := "/api/projects/" + mid + "~p_1/tasks/T-1/actions/open-pr"
	try := func(body string) actResult {
		var res actResult
		e.json("POST", path, body, asToken, &res)
		return res
	}
	if res := try(signedBody(t, mac, openPRIntent(head, time.Now()))); res.Code != "pr-not-signed" {
		t.Fatalf("no key registered: %+v", res)
	}
	e.s.MacKey = &mac.PublicKey
	if res := try(signedBody(t, other, openPRIntent(head, time.Now()))); res.Code != "pr-not-signed" {
		t.Fatalf("signed by another key (made up on a phone): %+v", res)
	}
	if res := try(signedBody(t, mac, openPRIntent(head, time.Now().Add(-time.Hour)))); res.Code != "pr-not-signed" {
		t.Fatalf("an old signature: %+v", res)
	}
	if created != 0 {
		t.Fatal("gh pr create ran for a refused intent")
	}
	good := signedBody(t, mac, openPRIntent(head, time.Now()))
	// A request refused for the branch does not use up the signature.
	moved := head
	head = strings.Repeat("b", 40)
	if res := try(good); res.Code != "pr-mismatch" {
		t.Fatalf("branch moved: %+v", res)
	}
	head = moved
	if res := try(good); !res.OK || res.URL == "" || created != 1 {
		t.Fatalf("this Mac's signature: %+v", res)
	}
	if res := try(good); res.Code != "pr-not-signed" || created != 1 {
		t.Fatalf("the same signature twice: %+v", res)
	}
}

func TestShellRegistersItsKey(t *testing.T) {
	e := newEnv(t, false)
	_, pub := newKey(t)
	body, _ := json.Marshal(map[string]string{"publicKey": pub})
	if code := e.code("POST", "/api/shell/key", string(body), asToken); code != 403 {
		t.Fatalf("token alone: %d", code)
	}
	var got map[string]string
	if code := e.json("POST", "/api/shell/key", string(body), asShell, &got); code != 200 || got["key"] != keyFingerprint(pub) || e.s.MacKey == nil {
		t.Fatalf("the app registering its key: %d %v", code, got)
	}
}

// upgradePeer answers a WebSocket upgrade with 101 and headers a hostile peer would add.
type upgradePeer struct{}

func (upgradePeer) RoundTrip(r *http.Request) (*http.Response, error) {
	a, b := net.Pipe()
	go func() { io.Copy(io.Discard, b); b.Close() }()
	return &http.Response{StatusCode: http.StatusSwitchingProtocols, Request: r, Body: a, Header: http.Header{
		"Upgrade":              {"websocket"},
		"Connection":           {"Upgrade"},
		"Sec-Websocket-Accept": {"s3pPLMBiTxaQ9kYGzzhZRbK+xOo="},
		"Set-Cookie":           {cookieName + "=stolen; Path=/"},
		"Clear-Site-Data":      {`"cookies"`},
	}}, nil
}

func TestPeerUpgradeCarriesOnlyWebSocketHeaders(t *testing.T) {
	e := newEnv(t, false)
	mid := hostilePeer(t, e, upgradePeer{})
	conn, err := net.Dial("tcp", strings.TrimPrefix(e.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /api/sessions/%s~s_01/pty HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", mid, strings.TrimPrefix(e.srv.URL, "http://"), testToken)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("Clear-Site-Data") != "" {
		t.Fatalf("a peer's upgrade set headers on the Mac's origin: %v", resp.Header)
	}
	if resp.Header.Get("Sec-Websocket-Accept") == "" {
		t.Fatal("the upgrade itself must still pass")
	}
}
