package serve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/machines"
	"github.com/spaghetticoders/truehand/internal/sign"
)

// Remote ids: a remote machine's project and session ids travel prefixed with "<mid>~", so
// every route can tell where to send a request without asking the UI.
const idSep = "~"

func splitRemote(id string) (mid, rest string, ok bool) {
	mid, rest, ok = strings.Cut(id, idSep)
	return mid, rest, ok && strings.HasPrefix(mid, "m_") && rest != ""
}

const (
	machineOnline      = "online"
	machineOffline     = "offline"
	machineNeedsUpdate = "needs-update"
	machineNeedsAuth   = "needs-auth"
	machineDisabled    = "disabled"
)

// Remotes are the machines this Mac reaches over ssh, with what was last seen of each.
type Remotes struct {
	SSH *machines.SSH
	// BinaryFor finds the truehand to install for os/arch; tests replace it.
	BinaryFor func(goos, arch string) (string, error)

	mu    sync.Mutex
	peers map[string]*remote
}

type remote struct {
	mu       sync.Mutex
	peer     *machines.Peer
	target   string
	status   string
	version  string
	info     machines.Info
	lastErr  string
	snap     *snapshotView
	checked  time.Time
	relaying bool
}

func (rs *Remotes) get(m appstate.Machine) *remote {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.peers == nil {
		rs.peers = map[string]*remote{}
	}
	r, ok := rs.peers[m.ID]
	if !ok || r.target != m.SSHTarget {
		r = &remote{peer: machines.NewPeer(rs.SSH, m.SSHTarget), target: m.SSHTarget, status: machineOffline}
		rs.peers[m.ID] = r
	}
	return r
}

func (rs *Remotes) forget(id string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if r, ok := rs.peers[id]; ok {
		r.peer.HTTP.CloseIdleConnections()
		delete(rs.peers, id)
	}
}

type machineView struct {
	ID         string          `json:"id"`
	Label      string          `json:"label"`
	SSHTarget  string          `json:"sshTarget,omitempty"`
	OS         string          `json:"os,omitempty"`
	Arch       string          `json:"arch,omitempty"`
	Status     string          `json:"status"`
	Version    string          `json:"version,omitempty"`
	Tools      map[string]bool `json:"tools,omitempty"`
	CanApprove bool            `json:"canApprove"`
	Error      string          `json:"error,omitempty"`
}

func (s *Server) machineView(m appstate.Machine) machineView {
	v := machineView{ID: m.ID, Label: m.Label, SSHTarget: m.SSHTarget, OS: m.OS, Arch: m.Arch, Status: machineOffline}
	if m.Disabled {
		v.Status = machineDisabled
		return v
	}
	if s.Remotes == nil {
		return v
	}
	r := s.Remotes.get(m)
	r.mu.Lock()
	defer r.mu.Unlock()
	v.Status, v.Version, v.Error = r.status, r.version, r.lastErr
	if r.info.Tools != nil {
		v.Tools = r.info.Tools
	}
	return v
}

func (s *Server) localMachine(ctx context.Context) machineView {
	h := s.tools(ctx)
	host, _ := os.Hostname()
	_, git := s.LookPath("git")
	return machineView{ID: appstate.LocalMachine, Label: strings.TrimSuffix(host, ".local"), Status: machineOnline, Version: s.Version,
		Tools: map[string]bool{"claude": h.claude.Found, "gh": h.gh.Found, "git": git == nil}, CanApprove: true}
}

// check reaches a machine's peer, starting it if needed, and records its status.
func (s *Server) check(ctx context.Context, m appstate.Machine) string {
	r := s.Remotes.get(m)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	v, err := r.peer.Ensure(ctx)
	status, msg := machineOnline, ""
	switch {
	case errors.Is(err, machines.ErrAuth):
		status, msg = machineNeedsAuth, err.Error()
	case err != nil:
		status, msg = machineOffline, err.Error()
	case v.Version != s.Version:
		status, msg = machineNeedsUpdate, fmt.Sprintf("truehand %s there, %s here", v.Version, s.Version)
	}
	r.mu.Lock()
	changed := r.status != status
	r.status, r.lastErr, r.checked = status, msg, time.Now()
	if err == nil {
		r.version = v.Version
	}
	r.mu.Unlock()
	if changed {
		s.broadcast(sseEvent{"machine", map[string]string{"id": m.ID, "status": status}})
		s.waitingDirty.Store(true)
	}
	if status == machineOnline {
		r.mu.Lock()
		probed := r.info.Tools != nil
		r.mu.Unlock()
		if !probed {
			if info, err := s.Remotes.SSH.Probe(ctx, m.SSHTarget); err == nil {
				r.mu.Lock()
				r.info = info
				r.mu.Unlock()
			}
		}
		s.relay(m, r)
	}
	return status
}

// WatchMachines keeps every enabled machine's status fresh.
func (s *Server) WatchMachines(stop <-chan struct{}, every time.Duration) {
	if s.Remotes == nil {
		return
	}
	for {
		if st, err := s.Store.State(); err == nil {
			var wg sync.WaitGroup
			for _, m := range st.Machines {
				if !m.Disabled {
					wg.Add(1)
					go func(m appstate.Machine) { defer wg.Done(); s.check(context.Background(), m) }(m)
				}
			}
			wg.Wait()
		}
		select {
		case <-stop:
			return
		case <-time.After(every):
		}
	}
}

// relay forwards a peer's change stream with its ids prefixed, while the peer answers.
func (s *Server) relay(m appstate.Machine, r *remote) {
	r.mu.Lock()
	if r.relaying {
		r.mu.Unlock()
		return
	}
	r.relaying = true
	r.mu.Unlock()
	go func() {
		defer func() {
			r.mu.Lock()
			r.relaying = false
			r.mu.Unlock()
		}()
		req, _ := http.NewRequest(http.MethodGet, machines.URL("/api/events"), nil)
		client := &http.Client{Transport: r.peer.HTTP.Transport}
		resp, err := client.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		var event string
		for sc.Scan() {
			line := sc.Text()
			if e, ok := strings.CutPrefix(line, "event: "); ok {
				event = e
				continue
			}
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			switch event {
			case "changed":
				s.broadcast(sseEvent{"changed", m.ID + idSep + data})
			case "session":
				var v map[string]string
				if json.Unmarshal([]byte(data), &v) == nil {
					v["id"] = m.ID + idSep + v["id"]
					s.broadcast(sseEvent{"session", v})
				}
			case "waiting":
				s.waitingDirty.Store(true)
			}
		}
	}()
}

// remoteSnapshot is a machine's snapshot with its ids prefixed; the last one seen when the
// machine does not answer in time.
func (s *Server) remoteSnapshot(ctx context.Context, m appstate.Machine) *snapshotView {
	r := s.Remotes.get(m)
	r.mu.Lock()
	status, cached := r.status, r.snap
	r.mu.Unlock()
	if status != machineOnline {
		return cached
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, machines.URL("/api/snapshot"), nil)
	resp, err := r.peer.HTTP.Do(req)
	if err != nil {
		return cached
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return cached
	}
	var snap snapshotView
	if json.Unmarshal(prefixIDs(b, m.ID), &snap) != nil {
		return cached
	}
	r.mu.Lock()
	r.snap = &snap
	r.mu.Unlock()
	return &snap
}

// addRemotes merges every machine's snapshot into the Mac's.
func (s *Server) addRemotes(ctx context.Context, snap *snapshotView) {
	st, err := s.Store.State()
	if err != nil {
		return
	}
	snap.Machines = []machineView{s.localMachine(ctx)}
	results := make([]*snapshotView, len(st.Machines))
	var wg sync.WaitGroup
	for i, m := range st.Machines {
		if m.Disabled || s.Remotes == nil {
			continue
		}
		wg.Add(1)
		go func(i int, m appstate.Machine) { defer wg.Done(); results[i] = s.remoteSnapshot(ctx, m) }(i, m)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, c := range snap.Clients {
		seen[c.ID] = true
	}
	for i, m := range st.Machines {
		snap.Machines = append(snap.Machines, s.machineView(m))
		rs := results[i]
		if rs == nil {
			continue
		}
		for _, c := range rs.Clients {
			if !seen[c.ID] {
				seen[c.ID] = true
				snap.Clients = append(snap.Clients, c)
			}
		}
		snap.Projects = append(snap.Projects, rs.Projects...)
		snap.Tasks = append(snap.Tasks, rs.Tasks...)
		snap.Sessions = append(snap.Sessions, rs.Sessions...)
	}
}

// peerKeep are the response headers a peer may set: the content and how to cache it.
// Anything that acts on the Mac's origin (cookies, CSP, CORS, HSTS, Clear-Site-Data) is the
// Mac's alone.
var peerKeep = map[string]bool{"Content-Type": true, "Content-Length": true, "Cache-Control": true, "Last-Modified": true, "Etag": true, "Content-Range": true, "Accept-Ranges": true, "Date": true}

// peerHeaders reduces a peer's response headers to peerKeep, and makes anything but JSON
// inert: a hostile peer's HTML or SVG never runs in the Mac's origin.
func peerHeaders(h http.Header) {
	for k := range h {
		if !peerKeep[http.CanonicalHeaderKey(k)] {
			h.Del(k)
		}
	}
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
}

// upgradeHeaders keeps only what a WebSocket upgrade needs from a peer's 101.
func upgradeHeaders(h http.Header) {
	for k := range h {
		ck := http.CanonicalHeaderKey(k)
		if ck != "Upgrade" && ck != "Connection" && !strings.HasPrefix(ck, "Sec-Websocket-") {
			h.Del(k)
		}
	}
}

// openRemotePR opens on this Mac, with its gh, the PR a machine without gh asked for after
// it verified the signed intent and pushed. The machine says which repo and branch; the Mac
// opens it only for the repo the person signed and only if that branch's head on GitHub is
// the commit they signed.
func (s *Server) openRemotePR(ctx context.Context, b, request []byte) []byte {
	var res actResult
	if json.Unmarshal(b, &res) != nil || res.PRRequest == nil {
		return b
	}
	refuse := func(code, msg string) []byte {
		nb, _ := json.Marshal(actResult{Error: msg, Code: code})
		return nb
	}
	var asked struct {
		SignedIntent *sign.Signed `json:"signedIntent"`
	}
	if json.Unmarshal(request, &asked) != nil || asked.SignedIntent == nil || asked.SignedIntent.Intent.Action != sign.ActionOpenPR {
		return refuse("pr-not-signed", "the machine asked to open a PR that was not signed here")
	}
	in := asked.SignedIntent.Intent
	// The machine checked the signature too, but it is the one being distrusted here.
	s.mu.Lock()
	key := s.MacKey
	s.mu.Unlock()
	if key == nil {
		return refuse("pr-not-signed", "this Mac has no registered signing key: open the Truehand app")
	}
	if err := asked.SignedIntent.Verify(key, ""); err != nil {
		return refuse("pr-not-signed", "the signature is not this Mac's: "+err.Error())
	}
	if err := in.Fresh(time.Now(), core.SignedIntentMaxAge); err != nil {
		return refuse("pr-not-signed", err.Error())
	}
	if res.PRRequest.Repo != in.Repo {
		return refuse("pr-mismatch", fmt.Sprintf("the machine asked for a PR on %s, the signature is for %s", res.PRRequest.Repo, in.Repo))
	}
	if head, err := s.remoteBranchHead(ctx, res.PRRequest.Repo, res.PRRequest.Head); err != nil || head != in.Head {
		return refuse("pr-mismatch", fmt.Sprintf("branch %s on %s is not at the signed commit %.12s", res.PRRequest.Head, in.Repo, in.Head))
	}
	// Spent only now: a refused request does not use up a good signature.
	if err := s.spendIntent(in.Nonce); err != nil {
		return refuse("pr-not-signed", err.Error())
	}
	url, out, err := s.openPRHere(ctx, *res.PRRequest)
	if err != nil {
		reason := ghReason(out)
		res = actResult{Error: "gh pr create failed on the Mac: " + reason, Code: "gh-failed", Reason: reason, Output: string(out)}
	} else {
		res = actResult{OK: true, URL: url}
	}
	nb, err := json.Marshal(res)
	if err != nil {
		return b
	}
	return nb
}

// spendIntent records a nonce this Mac acts on, and refuses one already used.
func (s *Server) spendIntent(nonce string) error {
	return s.Store.UpdateAuth(func(a *appstate.Auth) error {
		if a.SpentIntents == nil {
			a.SpentIntents = map[string]time.Time{}
		}
		for n, t := range a.SpentIntents {
			// Older than twice the freshness window: Fresh refuses them anyway.
			if time.Since(t) > 2*core.SignedIntentMaxAge {
				delete(a.SpentIntents, n)
			}
		}
		if _, ok := a.SpentIntents[nonce]; ok {
			return errors.New("this signature was already used")
		}
		a.SpentIntents[nonce] = time.Now().UTC()
		return nil
	})
}

// remoteBranchHead asks GitHub, through this Mac's gh, for the commit a branch points at.
func (s *Server) remoteBranchHead(ctx context.Context, repo, branch string) (string, error) {
	host, ownerRepo, ok := strings.Cut(repo, "/")
	if !ok || !refName.MatchString(branch) {
		return "", errors.New("invalid repo or branch")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := s.Run(ctx, "", append(gitEnv(""), "GH_HOST="+host), "gh", "api", "repos/"+ownerRepo+"/commits/"+url.PathEscape(branch), "--jq", ".sha")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

var refName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,200}$`)

// prefixIDs rewrites a peer's JSON for the Mac: project and session ids, and every
// reference to them, get the machine's prefix, and projects and sessions get its id.
func prefixIDs(b []byte, mid string) []byte {
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return b
	}
	p := func(x any) any {
		if s, ok := x.(string); ok && s != "" {
			return mid + idSep + s
		}
		return x
	}
	var walk func(x any)
	// A path into a project or a session of the peer becomes a path into the Mac's route.
	prefixURL := func(x any) any {
		s, ok := x.(string)
		if !ok {
			return x
		}
		for _, base := range []string{"/api/projects/", "/api/sessions/"} {
			if rest, ok := strings.CutPrefix(s, base); ok && rest != "" {
				return base + mid + idSep + rest
			}
		}
		return x
	}
	walk = func(x any) {
		switch t := x.(type) {
		case []any:
			for i, e := range t {
				t[i] = prefixURL(e)
				walk(e)
			}
		case map[string]any:
			_, isProject := t["clientId"]
			_, isSession := t["startedAt"]
			if isProject || isSession {
				t["id"] = p(t["id"])
				t["machineId"] = mid
			}
			for _, k := range []string{"projectId", "sessionId"} {
				if _, ok := t[k]; ok {
					t[k] = p(t[k])
				}
			}
			if ids, ok := t["sessionIds"].([]any); ok {
				for i := range ids {
					ids[i] = p(ids[i])
				}
			}
			for k, e := range t {
				if k != "id" {
					t[k] = prefixURL(e)
					walk(e)
				}
			}
		}
	}
	walk(v)
	out, err := json.Marshal(v)
	if err != nil {
		return b
	}
	return out
}

// unprefixBody strips the machine's prefix from the session ids a request names.
func unprefixBody(b []byte, mid string) []byte {
	var v map[string]any
	if len(b) == 0 || json.Unmarshal(b, &v) != nil {
		return b
	}
	for _, k := range []string{"sessionId", "sendToSession"} {
		if s, ok := v[k].(string); ok {
			v[k] = strings.TrimPrefix(s, mid+idSep)
		}
	}
	delete(v, "machineId")
	out, err := json.Marshal(v)
	if err != nil {
		return b
	}
	return out
}

// routeRemote sends requests about a remote machine's projects and sessions to its peer.
func (s *Server) routeRemote(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, isProject := strings.CutPrefix(r.URL.Path, "/api/projects/")
		if !isProject {
			rest, _ = strings.CutPrefix(r.URL.Path, "/api/sessions/")
		}
		if rest == r.URL.Path || s.Remotes == nil {
			next.ServeHTTP(w, r)
			return
		}
		id, tail, _ := strings.Cut(rest, "/")
		mid, local, ok := splitRemote(id)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		prefix := "/api/sessions/"
		if isProject {
			prefix = "/api/projects/"
		}
		path := prefix + local
		if tail != "" {
			path += "/" + tail
		}
		s.forward(w, r, mid, path)
	})
}

func (s *Server) machineByID(mid string) (appstate.Machine, bool) {
	st, err := s.Store.State()
	if err != nil {
		return appstate.Machine{}, false
	}
	m, ok := st.Machine(mid)
	if !ok {
		return appstate.Machine{}, false
	}
	return *m, true
}

// forward proxies r to the machine's peer at path, WebSockets included. Credentials,
// cookies and Origin stay here: the peer trusts its socket, the Mac already checked them.
func (s *Server) forward(w http.ResponseWriter, r *http.Request, mid, path string) {
	m, ok := s.machineByID(mid)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown machine")
		return
	}
	if m.Disabled {
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": m.Label + " is disabled", "code": "machine-disabled"})
		return
	}
	rm := s.Remotes.get(m)
	ws := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	var body []byte
	if !ws && r.Body != nil {
		b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		body = unprefixBody(b, mid)
	}
	proxy := &httputil.ReverseProxy{
		Transport: rm.peer.HTTP.Transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			u, _ := url.Parse(machines.URL(path))
			u.RawQuery = r.URL.RawQuery
			pr.Out.URL, pr.Out.Host = u, "peer"
			for _, h := range []string{"Cookie", "Authorization", "Origin", shellHeader, viewOnlyHeader, "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
				pr.Out.Header.Del(h)
			}
			// The peer trusts the Mac's connection; a caller the Mac only lets watch stays so.
			if identityOf(r).ViewOnly {
				pr.Out.Header.Set(viewOnlyHeader, "1")
			}
			if !ws {
				pr.Out.Body = io.NopCloser(bytes.NewReader(body))
				pr.Out.ContentLength = int64(len(body))
				pr.Out.Header.Set("Content-Length", strconv.Itoa(len(body)))
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode == http.StatusSwitchingProtocols {
				upgradeHeaders(resp.Header)
				return nil
			}
			peerHeaders(resp.Header)
			if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
				return nil
			}
			b, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				return err
			}
			b = prefixIDs(b, mid)
			if strings.HasSuffix(path, "/actions/open-pr") {
				b = s.openRemotePR(resp.Request.Context(), b, body)
			}
			resp.Body = io.NopCloser(bytes.NewReader(b))
			resp.ContentLength = int64(len(b))
			resp.Header.Set("Content-Length", strconv.Itoa(len(b)))
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			go s.check(context.Background(), m)
			writeJSONStatus(w, http.StatusBadGateway, map[string]string{"error": m.Label + " does not answer: " + err.Error(), "code": "machine-offline"})
		},
		FlushInterval: -1,
	}
	proxy.ServeHTTP(w, r)
}
