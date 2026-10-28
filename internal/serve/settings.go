package serve

import (
	"context"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/claudeplugin"
)

var tailnetRange = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

var hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)

// ValidRemote checks remote access: a tailnet address (100.64.0.0/10) or a MagicDNS name,
// never all interfaces.
func ValidRemote(rm appstate.Remote) error {
	switch rm.Mode {
	case "off":
		return nil
	case "tailnet":
		ip := net.ParseIP(rm.Addr)
		if ip == nil || !tailnetRange.Contains(ip) {
			return errBadRequest("remote.address must be this Mac's Tailscale IP (100.x.y.z)")
		}
	case "tailscale-serve":
		if !hostnameRe.MatchString(rm.Addr) || !strings.HasSuffix(strings.ToLower(rm.Addr), ".ts.net") {
			return errBadRequest("remote.address must be this Mac's MagicDNS name (….ts.net)")
		}
	}
	return nil
}

type toolView struct {
	Found   bool   `json:"found"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
}

type ghView struct {
	Found  bool `json:"found"`
	Authed bool `json:"authed"`
}

type deviceRef struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	LastSeen time.Time `json:"lastSeen"`
}

type remoteView struct {
	Enabled bool        `json:"enabled"`
	Address string      `json:"address"`
	Mode    string      `json:"mode"` // tailnet | tailscale-serve
	Devices []deviceRef `json:"devices"`
	// Status is what is really running: off | listening | error (with Error).
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type settingsView struct {
	Language              string     `json:"language"`
	Remote                remoteView `json:"remote"`
	Claude                toolView   `json:"claude"`
	GH                    ghView     `json:"gh"`
	DefaultPermissionMode string     `json:"defaultPermissionMode"`
	GlobalPluginInstalled bool       `json:"globalPluginInstalled"`
	Version               string     `json:"version"`
	SessionsAvailable     bool       `json:"sessionsAvailable"`
}

type healthCache struct {
	at     time.Time
	claude toolView
	gh     ghView
	plugin bool
}

// GlobalPlugin reports whether Truehand's plugin is installed in the user's Claude Code;
// replaceable in tests.
var GlobalPlugin = claudeplugin.Status

// tools reports claude and gh as last probed. Probing runs in the background, at most every
// 30 seconds: gh asks GitHub and `claude plugin list` can be slow, and the board must never
// wait for them. Only the very first answer waits, briefly, for the first probe.
func (s *Server) tools(ctx context.Context) healthCache {
	s.mu.Lock()
	hc, first, waited := s.health, s.probed, s.waitedProbe
	s.waitedProbe = true
	if first == nil {
		first = make(chan struct{})
		s.probed = first
	}
	if !s.probing && (hc == nil || time.Since(hc.at) > 30*time.Second) {
		s.probing = true
		go s.probeTools()
	}
	s.mu.Unlock()
	if hc != nil || waited {
		if hc == nil {
			return healthCache{}
		}
		return *hc
	}
	select {
	case <-first:
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.health != nil {
		return *s.health
	}
	return healthCache{}
}

// probeTools runs each probe under its own short limit and publishes the result.
func (s *Server) probeTools() {
	run := func(name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.Run(ctx, "", gitEnv(""), name, args...)
	}
	h := healthCache{at: time.Now()}
	if p, err := s.LookPath("claude"); err == nil {
		h.claude = toolView{Found: true, Path: p}
		if b, err := run(p, "--version"); err == nil {
			h.claude.Version = firstLine(string(b))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if installed, err := GlobalPlugin(ctx); err == nil {
			h.plugin = installed
		}
		cancel()
	}
	if p, err := s.LookPath("gh"); err == nil {
		_, err := run(p, "auth", "status")
		h.gh = ghView{Found: true, Authed: err == nil}
	}
	s.mu.Lock()
	s.health, s.probing = &h, false
	first := s.probed
	if first == nil {
		first = make(chan struct{})
		s.probed = first
	}
	select {
	case <-first:
	default:
		close(first)
	}
	s.mu.Unlock()
}

func (s *Server) settingsView(ctx context.Context) (settingsView, error) {
	st, err := s.Store.State()
	if err != nil {
		return settingsView{}, err
	}
	a, err := s.Store.Auth()
	if err != nil {
		return settingsView{}, err
	}
	h := s.tools(ctx)
	rm := st.Settings.Remote
	v := settingsView{
		Language: st.Settings.Lang, DefaultPermissionMode: st.Settings.DefaultPermissionMode,
		Claude: h.claude, GH: h.gh, GlobalPluginInstalled: h.plugin, Version: s.Version, SessionsAvailable: s.Agents != nil,
		Remote: remoteView{Enabled: rm.Mode != "off", Address: rm.Addr, Mode: rm.Mode, Devices: []deviceRef{}},
	}
	if !v.Remote.Enabled {
		v.Remote.Mode = "tailnet"
	}
	v.Remote.Status = "off"
	if s.RemoteStatus != nil {
		v.Remote.Status, v.Remote.Error = s.RemoteStatus()
	}
	for _, d := range a.Devices {
		if d.ID != appDeviceID {
			v.Remote.Devices = append(v.Remote.Devices, deviceRef{ID: d.ID, Name: d.Name, LastSeen: d.LastSeen})
		}
	}
	return v, nil
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.settingsView(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, v)
}

// putSettings takes a partial Settings. Opening or changing remote access needs the person
// at the Mac; the theme is the client's and is ignored.
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Language              *string `json:"language"`
		Theme                 *string `json:"theme"`
		DefaultPermissionMode *string `json:"defaultPermissionMode"`
		Remote                *struct {
			Enabled *bool   `json:"enabled"`
			Address *string `json:"address"`
			Mode    *string `json:"mode"`
		} `json:"remote"`
	}
	if !decode(w, r, &body) {
		return
	}
	st, err := s.Store.State()
	if err != nil {
		fail(w, err)
		return
	}
	next := st.Settings
	if body.Language != nil {
		next.Lang = *body.Language
	}
	if body.DefaultPermissionMode != nil {
		next.DefaultPermissionMode = *body.DefaultPermissionMode
	}
	if rb := body.Remote; rb != nil {
		enabled := next.Remote.Mode != "off"
		mode := next.Remote.Mode
		if mode == "off" {
			mode = "tailnet"
		}
		if rb.Enabled != nil {
			enabled = *rb.Enabled
		}
		if rb.Mode != nil {
			mode = *rb.Mode
		}
		if rb.Address != nil {
			next.Remote.Addr = strings.TrimSpace(*rb.Address)
		}
		next.Remote.Mode = "off"
		if enabled {
			next.Remote.Mode = mode
		}
		if enabled && mode == "tailnet" && next.Remote.Addr == "" && s.TailnetIP != nil {
			next.Remote.Addr = s.TailnetIP()
			if next.Remote.Addr == "" {
				httpError(w, http.StatusBadRequest, "no Tailscale address found on this Mac: is Tailscale running?")
				return
			}
		}
		if enabled && mode != "tailnet" && mode != "tailscale-serve" {
			httpError(w, http.StatusBadRequest, "remote.mode must be tailnet or tailscale-serve")
			return
		}
	}
	if err := appstate.ValidateSettings(next); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := ValidRemote(next.Remote); err != nil {
		fail(w, err)
		return
	}
	if next.Remote != st.Settings.Remote && !s.human(w, r, "Truehand: change who can reach the Truehand app from other devices", nil) {
		return
	}
	if err := s.Store.UpdateState(func(st *appstate.State) error {
		st.Settings = next
		return nil
	}); err != nil {
		fail(w, err)
		return
	}
	if next.Remote != st.Settings.Remote && s.ApplyRemote != nil {
		hosts, err := s.ApplyRemote(next.Remote)
		if err != nil {
			writeJSONStatus(w, http.StatusBadGateway, map[string]string{"error": "saved, but remote access could not start: " + err.Error(), "code": "remote-unavailable"})
			return
		}
		s.SetHosts(hosts)
	}
	s.getSettings(w, r)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}
