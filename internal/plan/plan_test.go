package plan

import (
	"strings"
	"testing"
)

const good = `# T-1 — Login

## Goal
Users can log in with email.

## Scope
lib/auth.

## Out of scope
Social login.

## Steps
1. Add form — verify: widget test
2. Wire API — verify: unit test with fake client

## Verification
flutter test, eyes on login screen.

## Risks
Token refresh.
`

func TestCheckValid(t *testing.T) {
	if p := Check(good); len(p) != 0 {
		t.Fatalf("expected valid plan, got %+v", p)
	}
}

func TestTemplateIsInvalidUntilFilled(t *testing.T) {
	p := Check(Template("T-1", "Login", ""))
	if len(p) != len(Sections) {
		t.Fatalf("expected one problem per section, got %d: %+v", len(p), p)
	}
}

func TestCheckProblems(t *testing.T) {
	cases := map[string]struct {
		md      string
		section string
	}{
		"missing risks":     {strings.Replace(good, "## Risks\nToken refresh.\n", "", 1), "Risks"},
		"step w/o verify":   {strings.Replace(good, "1. Add form — verify: widget test", "1. Add form", 1), "Steps"},
		"todo left":         {strings.Replace(good, "Social login.", "Social login.\nTODO", 1), "Out of scope"},
		"comment only goal": {strings.Replace(good, "Users can log in with email.", "<!-- hint -->", 1), "Goal"},
		"no steps":          {strings.Replace(good, "1. Add form — verify: widget test\n2. Wire API — verify: unit test with fake client", "We do things.", 1), "Steps"},
	}
	for name, c := range cases {
		probs := Check(c.md)
		found := false
		for _, p := range probs {
			if p.Section == c.section {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: expected a problem in %s, got %+v", name, c.section, probs)
		}
	}
}

func TestTodoWordIsFine(t *testing.T) {
	md := strings.Replace(good, "Social login.", "Remove the TODO comments left in auth.", 1)
	if p := Check(md); len(p) != 0 {
		t.Fatalf("the word TODO in prose must be accepted: %+v", p)
	}
}
