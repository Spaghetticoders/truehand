package serve

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/humanauth"
	"github.com/spaghetticoders/truehand/internal/sign"
)

const (
	viaToken  = "token"  // the Mac app's backend, with the token it keeps in the Keychain
	viaDevice = "device" // a browser holding a paired device's cookie
	viaSocket = "socket" // the unix socket: this user's terminal, or the Mac through ssh

	cookieName   = "th_session"
	shellHeader  = "X-Truehand-Shell"
	appDeviceID  = "d_app"
	pairTTL      = 2 * time.Minute
	pairMaxFails = 10
)

type identityKey struct{}

type identity struct {
	Via    string
	Device *appstate.Device
	// Shell is set when the request carries the Mac app's in-memory secret: the only caller
	// allowed to bring up the OS authentication dialog.
	Shell bool
	// ViewOnly may watch a terminal but not type into it.
	ViewOnly bool
}

func identityOf(r *http.Request) identity {
	id, _ := r.Context().Value(identityKey{}).(identity)
	return id
}

func (id identity) name() string {
	if id.Device != nil {
		return id.Device.Name
	}
	return id.Via
}

// authenticate accepts the bearer token or a paired device's cookie; never a query parameter.
func (s *Server) authenticate(r *http.Request) (identity, bool) {
	auth, err := s.Store.Auth()
	if err != nil {
		return identity{}, false
	}
	var id identity
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && appstate.MatchSecret(auth.TokenSHA256, tok) {
		id.Via = viaToken
	} else if c, err := r.Cookie(cookieName); err == nil {
		d, ok := auth.DeviceByCookie(c.Value)
		if !ok {
			return identity{}, false
		}
		dev := *d
		id.Via, id.Device = viaDevice, &dev
		if time.Since(d.LastSeen) > 5*time.Minute {
			_ = s.Store.UpdateAuth(func(a *appstate.Auth) error {
				for i := range a.Devices {
					if a.Devices[i].ID == dev.ID {
						a.Devices[i].LastSeen = time.Now().UTC().Truncate(time.Second)
					}
				}
				return nil
			})
		}
	} else {
		return identity{}, false
	}
	id.Shell = appstate.MatchSecret(s.ShellSecretHash, r.Header.Get(shellHeader))
	return id, true
}

func secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	// tailscale serve terminates TLS and forwards to loopback.
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return net.ParseIP(host).IsLoopback() && r.Header.Get("X-Forwarded-Proto") == "https"
}

func (s *Server) setDeviceCookie(w http.ResponseWriter, r *http.Request, value string) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: value, Path: "/",
		MaxAge: 400 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secureRequest(r),
	})
}

// addDevice stores a device under the hash of a new cookie and sets that cookie.
func (s *Server) addDevice(w http.ResponseWriter, r *http.Request, id, name string) (appstate.Device, error) {
	cookie := appstate.NewSecret()
	now := time.Now().UTC().Truncate(time.Second)
	d := appstate.Device{ID: id, Name: name, CookieSHA256: appstate.HashSecret(cookie), Created: now, LastSeen: now}
	err := s.Store.UpdateAuth(func(a *appstate.Auth) error {
		for i := range a.Devices {
			if a.Devices[i].ID == id {
				a.Devices = append(a.Devices[:i], a.Devices[i+1:]...)
				break
			}
		}
		a.Devices = append(a.Devices, d)
		return nil
	})
	if err != nil {
		return d, err
	}
	s.setDeviceCookie(w, r, cookie)
	return d, nil
}

// login turns the Mac app's token into a cookie for its web view, which cannot add headers
// to EventSource or WebSocket requests.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if identityOf(r).Via != viaToken {
		httpError(w, http.StatusForbidden, "sign in with the app token")
		return
	}
	if _, err := s.addDevice(w, r, appDeviceID, "Truehand app"); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

const pairAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func newPairCode() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = pairAlphabet[int(b[i])%len(pairAlphabet)]
	}
	return string(b)
}

func normalizeCode(c string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, strings.ToUpper(c))
}

// pairCode makes a one-time code shown as a QR on the Mac.
func (s *Server) pairCode(w http.ResponseWriter, r *http.Request) {
	if !s.human(w, r, "Truehand: pair a new device with your Truehand app", nil) {
		return
	}
	code := newPairCode()
	exp := time.Now().Add(pairTTL)
	s.mu.Lock()
	if s.pairs == nil {
		s.pairs = map[string]time.Time{}
	}
	for h, e := range s.pairs {
		if time.Now().After(e) {
			delete(s.pairs, h)
		}
	}
	s.pairs[appstate.HashSecret(code)] = exp
	s.mu.Unlock()
	writeJSON(w, map[string]any{"code": code, "expires": exp.UTC()})
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code       string `json:"code"`
		DeviceName string `json:"deviceName"`
	}
	if !decode(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.DeviceName)
	if name == "" || len(name) > 60 {
		httpError(w, http.StatusBadRequest, "deviceName must be 1-60 characters")
		return
	}
	h := appstate.HashSecret(normalizeCode(body.Code))
	s.mu.Lock()
	now := time.Now()
	recent := s.pairFail[:0]
	for _, t := range s.pairFail {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	s.pairFail = recent
	if len(s.pairFail) >= pairMaxFails {
		s.mu.Unlock()
		httpError(w, http.StatusTooManyRequests, "too many wrong codes: wait a minute and make a new one")
		return
	}
	exp, ok := s.pairs[h]
	if ok {
		delete(s.pairs, h)
	}
	if !ok || now.After(exp) {
		s.pairFail = append(s.pairFail, now)
		s.mu.Unlock()
		httpError(w, http.StatusUnauthorized, "wrong or expired code")
		return
	}
	s.mu.Unlock()
	d, err := s.addDevice(w, r, appstate.NewID("d_"), name)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"id": d.ID, "name": d.Name})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.meOf(r)) }

type deviceView struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"lastSeen"`
	Current  bool      `json:"current"`
}

func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	a, err := s.Store.Auth()
	if err != nil {
		fail(w, err)
		return
	}
	me := identityOf(r).Device
	out := []deviceView{}
	for _, d := range a.Devices {
		out = append(out, deviceView{ID: d.ID, Name: d.Name, Created: d.Created, LastSeen: d.LastSeen, Current: me != nil && me.ID == d.ID})
	}
	writeJSON(w, out)
}

// deleteDevice revokes a paired device: a phone may sign itself out, only the Mac app may
// revoke the others.
func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if me := identityOf(r); !me.Shell && (me.Device == nil || me.Device.ID != id) {
		httpError(w, http.StatusForbidden, "revoke other devices from the Truehand app on the Mac")
		return
	}
	err := s.Store.UpdateAuth(func(a *appstate.Auth) error {
		for i := range a.Devices {
			if a.Devices[i].ID == id {
				a.Devices = append(a.Devices[:i], a.Devices[i+1:]...)
				return nil
			}
		}
		return appstate.ErrNotFound
	})
	if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// shutdown lets the Mac app stop a server it did not start itself (it has no safe pid): only
// its backend, with the token and its secret. No dialog: stopping is not a human-only act.
func (s *Server) shutdown(w http.ResponseWriter, r *http.Request) {
	id := identityOf(r)
	if id.Via != viaSocket && (id.Via != viaToken || !id.Shell) {
		httpError(w, http.StatusForbidden, "only the Truehand Mac app can stop the server")
		return
	}
	if s.Shutdown == nil {
		httpError(w, http.StatusNotImplemented, "this server cannot be stopped remotely")
		return
	}
	w.WriteHeader(http.StatusNoContent)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go s.Shutdown()
}

var challengeNonceRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

const challengeDomain = "truehand-shell-challenge/1\n"

// shellChallenge proves to the Mac app, before it sends anything secret, that this server
// was started with its secret: it answers HMAC-SHA256 keyed by SHA-256(shell secret). It
// needs no credential, and the domain prefix keeps it from signing anything else.
func (s *Server) shellChallenge(w http.ResponseWriter, r *http.Request) {
	key, err := hex.DecodeString(s.ShellSecretHash)
	if s.ShellSecretHash == "" || err != nil || len(key) != sha256.Size {
		httpError(w, http.StatusNotFound, "this server was not started by the Truehand app")
		return
	}
	var body struct {
		Nonce string `json:"nonce"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !challengeNonceRe.MatchString(body.Nonce) {
		httpError(w, http.StatusBadRequest, "nonce must be 64 lowercase hex characters")
		return
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(challengeDomain + body.Nonce))
	writeJSON(w, map[string]string{"mac": hex.EncodeToString(m.Sum(nil))})
}

// shellKey registers the Mac app's signing key (a public key: nothing secret), which this
// server needs to check signed intents itself. Only the app may set it.
func (s *Server) shellKey(w http.ResponseWriter, r *http.Request) {
	if id := identityOf(r); id.Via != viaToken || !id.Shell {
		httpError(w, http.StatusForbidden, "only the Truehand Mac app registers its key")
		return
	}
	var body struct {
		PublicKey string `json:"publicKey"`
	}
	if !decode(w, r, &body) {
		return
	}
	pub, err := sign.ParsePublicKey(body.PublicKey)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	s.MacKey = pub
	s.mu.Unlock()
	writeJSON(w, map[string]string{"key": keyFingerprint(body.PublicKey)})
}

func (s *Server) rotateToken(w http.ResponseWriter, r *http.Request) {
	if !s.human(w, r, "Truehand: replace the Truehand app's access token", nil) {
		return
	}
	tok := appstate.NewSecret()
	if err := s.Store.SetToken(tok); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]string{"token": tok})
}

// human lets a request through only with a person at the Mac: it must carry the app's
// secret, and then the OS authenticates the user. Without the secret, a request that can
// wait (q set) is queued for the Mac (202) and anything else is refused; the OS dialog is
// never shown for it.
func (s *Server) human(w http.ResponseWriter, r *http.Request, reason string, q *appstate.QueuedAction) bool {
	id := identityOf(r)
	if id.Via == viaSocket {
		if s.Peer {
			writeJSONStatus(w, http.StatusConflict, map[string]string{"error": "on a remote machine, approve, push and open PRs with a signed intent: configure approvers in the repo's .truehand/config.yaml on its base branch", "code": "remote-approval-unavailable"})
		} else {
			httpError(w, http.StatusForbidden, "this needs you at the Mac, in the Truehand app: not available from the terminal")
		}
		return false
	}
	if !id.Shell {
		if q == nil {
			httpError(w, http.StatusForbidden, "this needs you at the Mac, in the Truehand app")
			return false
		}
		qid, err := s.enqueue(*q, id)
		if err != nil {
			fail(w, err)
			return false
		}
		writeJSONStatus(w, http.StatusAccepted, actResult{OK: true, Queued: true, QueuedID: qid})
		return false
	}
	if err := s.HumanAuth(reason); err != nil {
		httpError(w, http.StatusForbidden, err.Error())
		return false
	}
	return true
}

func (s *Server) enqueue(q appstate.QueuedAction, id identity) (string, error) {
	q.ID, q.Device, q.Time = appstate.NewID("q_"), id.name(), time.Now().UTC().Truncate(time.Second)
	err := s.Store.UpdateAuth(func(a *appstate.Auth) error {
		for i, x := range a.Queued {
			if x.Kind == q.Kind && x.Project == q.Project && x.Task == q.Task && x.PlanSHA256 == q.PlanSHA256 {
				if q.Kind == "config" {
					// The latest edit of a project's config replaces the one still waiting.
					a.Queued[i].Params, a.Queued[i].Device, a.Queued[i].Time = q.Params, q.Device, q.Time
				}
				q.ID = x.ID
				return nil
			}
		}
		a.Queued = append(a.Queued, q)
		return nil
	})
	if err == nil {
		s.waitingDirty.Store(true)
	}
	return q.ID, err
}

// AuthMethod names how the person was authenticated, recorded with each approval; the E2E
// build replaces it so its fake approvals can never pass for real ones.
var AuthMethod = humanauth.Method

func humanMethod(id identity) string {
	if id.Shell {
		return "app+" + AuthMethod()
	}
	return "app"
}
