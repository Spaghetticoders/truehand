// Package board stores tasks, their state and their event log under .truehand/tasks.
package board

import (
	"fmt"
	"regexp"
	"time"
)

type State string

const (
	Planned   State = "planned"
	Approved  State = "approved"
	Building  State = "building"
	Verified  State = "verified"
	Reviewed  State = "reviewed"
	Delivered State = "delivered"
)

// States in lifecycle order.
var States = []State{Planned, Approved, Building, Verified, Reviewed, Delivered}

func ParseState(s string) (State, error) {
	for _, st := range States {
		if string(st) == s {
			return st, nil
		}
	}
	return "", fmt.Errorf("unknown state %q (states: planned, approved, building, verified, reviewed, delivered)", s)
}

func (s State) Index() int {
	for i, st := range States {
		if st == s {
			return i
		}
	}
	return -1
}

// Next returns the state after s, or "" if s is the last one.
func (s State) Next() State {
	i := s.Index()
	if i < 0 || i+1 >= len(States) {
		return ""
	}
	return States[i+1]
}

// CheckMove reports whether a task may move from -> to. Forward moves go one step at a
// time and are gated; backward moves (rework) are always allowed and never gated.
func CheckMove(from, to State) (forward bool, err error) {
	fi, ti := from.Index(), to.Index()
	switch {
	case fi < 0 || ti < 0:
		return false, fmt.Errorf("invalid move %s → %s", from, to)
	case ti == fi:
		return false, fmt.Errorf("task is already %s", to)
	case ti == fi+1:
		return true, nil
	case ti > fi+1:
		return false, fmt.Errorf("cannot skip states: %s → %s (next is %s)", from, to, from.Next())
	default:
		return false, nil
	}
}

type Task struct {
	ID     string `yaml:"id" json:"id"`
	Title  string `yaml:"title" json:"title"`
	Source string `yaml:"source,omitempty" json:"source,omitempty"`
	State  State  `yaml:"state" json:"state"`
	// Base is the commit the task started from; BaseRef the branch its diff is measured
	// against. The effective base is recomputed from BaseRef on every check (see core).
	Base    string `yaml:"base" json:"base"`
	BaseRef string `yaml:"base_ref,omitempty" json:"base_ref,omitempty"`
	// Scope lists the globs of the files this task owns; empty means every changed file.
	Scope   []string          `yaml:"scope,omitempty" json:"scope,omitempty"`
	Roles   map[string]string `yaml:"roles,omitempty" json:"roles,omitempty"`
	Created time.Time         `yaml:"created" json:"created"`
	Updated time.Time         `yaml:"updated" json:"updated"`
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func ValidID(id string) error {
	if !idRe.MatchString(id) {
		return fmt.Errorf("invalid task id %q: use letters, digits, '.', '_' or '-' (max 64)", id)
	}
	return nil
}
