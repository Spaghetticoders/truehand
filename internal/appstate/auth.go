package appstate

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

type Device struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	CookieSHA256 string    `json:"cookie_sha256"`
	Created      time.Time `json:"created"`
	LastSeen     time.Time `json:"last_seen"`
}

// QueuedAction is an approval, push or PR asked for from a device that cannot prove a human
// is present; it waits for confirmation from the Mac.
type QueuedAction struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"` // approve | push | pr
	Project    string         `json:"project"`
	Task       string         `json:"task"`
	PlanSHA256 string         `json:"plan_sha256,omitempty"`
	Params     map[string]any `json:"params,omitempty"`
	Device     string         `json:"device"`
	Time       time.Time      `json:"time"`
}

type Auth struct {
	TokenSHA256 string         `json:"token_sha256"`
	Devices     []Device       `json:"devices"`
	Queued      []QueuedAction `json:"queued_approvals"`
	// SpentIntents are the nonces of signed intents this Mac already acted on (forwarded
	// PRs), with when: one signature never opens two PRs.
	SpentIntents map[string]time.Time `json:"spent_intents,omitempty"`
}

func initAuth(a *Auth) {
	if a.Devices == nil {
		a.Devices = []Device{}
	}
	if a.Queued == nil {
		a.Queued = []QueuedAction{}
	}
}

func (s *Store) Auth() (*Auth, error) { return load(s, authFile, initAuth) }

func (s *Store) UpdateAuth(fn func(*Auth) error) error { return update(s, authFile, initAuth, fn) }

// SetToken stores only the hash of the shell's token.
func (s *Store) SetToken(token string) error {
	return s.UpdateAuth(func(a *Auth) error {
		a.TokenSHA256 = HashSecret(token)
		return nil
	})
}

// DeviceByCookie finds the paired device holding cookie.
func (a *Auth) DeviceByCookie(cookie string) (*Device, bool) {
	for i := range a.Devices {
		if MatchSecret(a.Devices[i].CookieSHA256, cookie) {
			return &a.Devices[i], true
		}
	}
	return nil, false
}

// NewID returns a short random identifier with a prefix.
func NewID(prefix string) string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}
