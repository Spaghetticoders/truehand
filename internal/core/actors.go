package core

import (
	"sort"
	"strings"
	"unicode"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/gate"
)

// Workers returns everyone who built the task, as the review gate counts them: a reviewer
// among them is refused.
func (w *Workspace) Workers(t *board.Task) (map[string]bool, error) {
	events, err := w.Store.Events(t.ID)
	if err != nil {
		return nil, err
	}
	recs, err := evidence.Log{Store: w.Store, Task: t.ID}.Records()
	if err != nil {
		return nil, err
	}
	return gate.Workers(t, events, recs), nil
}

// Actor kinds. An unbound agent acted before its SessionStart hook named it (claude:pid-N).
const (
	ActorHuman        = "human"
	ActorAgent        = "agent"
	ActorAgentUnbound = "agent_unbound"
	ActorUnknown      = "unknown"
)

// SessionRef ties a Claude Code session id to the role and task it was launched for.
type SessionRef struct {
	ClaudeSessionID string
	Role            string
	Task            string
}

// ActorInfo is what a UI needs to label an actor; the label itself is the UI's, in its language.
type ActorInfo struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Role string `json:"role,omitempty"`
	Task string `json:"task,omitempty"`
}

// Identity is the git user of the clone, which names the human:<email> actor that matches it.
type Identity struct {
	Name  string
	Email string
}

func (w *Workspace) Identity() Identity {
	return Identity{Name: w.Repo.ConfigValue("user.name"), Email: w.Repo.ConfigValue("user.email")}
}

// ResolveActors labels each actor: humans by git user.name when it is the clone's user, agents
// by the session that launched them, anything else abbreviated.
func ResolveActors(actors []string, sessions []SessionRef, me Identity) map[string]ActorInfo {
	byID := map[string]SessionRef{}
	for _, s := range sessions {
		if s.ClaudeSessionID != "" {
			byID[s.ClaudeSessionID] = s
		}
	}
	out := map[string]ActorInfo{}
	for _, a := range actors {
		if a == "" {
			continue
		}
		kind, rest, _ := strings.Cut(Printable(a), ":")
		switch {
		case kind == "human":
			name := rest
			if me.Email != "" && strings.EqualFold(rest, me.Email) && me.Name != "" {
				name = me.Name
			}
			out[a] = ActorInfo{Name: name, Kind: ActorHuman}
		case kind == "approver" && rest != "":
			out[a] = ActorInfo{Name: rest, Kind: ActorHuman}
		case kind == "claude" && strings.HasPrefix(rest, "pid-"):
			out[a] = ActorInfo{Name: rest, Kind: ActorAgentUnbound}
		case kind == "claude" && rest != "":
			s := byID[rest]
			out[a] = ActorInfo{Name: abbreviate(rest), Kind: ActorAgent, Role: s.Role, Task: s.Task}
		default:
			out[a] = ActorInfo{Name: abbreviate(Printable(a)), Kind: ActorUnknown}
		}
	}
	return out
}

// Printable drops control and format characters (ESC, bidi overrides): actor ids and
// summaries come from files in the repo and end up on terminals.
func Printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.In(r, unicode.Cc, unicode.Cf) {
			return -1
		}
		return r
	}, s)
}

// PrintableText is Printable for text with lines: newlines and tabs stay.
func PrintableText(s string) string {
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.In(r, unicode.Cc, unicode.Cf) {
			return -1
		}
		return r
	}, s)
}

func abbreviate(s string) string {
	if r := []rune(s); len(r) > 12 {
		return string(r[:8]) + "…"
	}
	return s
}

// ActorsOf lists, sorted and once each, the actors that appear in a task's history.
func ActorsOf(d *TaskDetail) []string {
	set := map[string]bool{}
	for _, e := range d.Events {
		set[e.Actor] = true
	}
	for _, e := range d.Comments {
		set[e.Actor] = true
	}
	for _, ev := range d.Evidence {
		set[ev.Actor] = true
	}
	for _, r := range d.Task.Roles {
		set[r] = true
	}
	delete(set, "")
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}
