// Package plan defines the plan.md template and validates its structure.
// The content is the agent's job; Truehand only checks that nothing required is missing.
package plan

import (
	"fmt"
	"regexp"
	"strings"
)

// Sections required in every plan, in order.
var Sections = []string{"Goal", "Scope", "Out of scope", "Steps", "Verification", "Risks"}

const placeholder = "TODO"

func Template(id, title, context string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — %s\n\n", id, title)
	if context != "" {
		b.WriteString("<!-- Context collected by truehand: context.md -->\n\n")
	}
	hints := map[string]string{
		"Goal":         "One or two sentences: what is true when this task is done.",
		"Scope":        "What changes, and where (files, modules, screens).",
		"Out of scope": "What this task deliberately does not do.",
		"Steps":        "Numbered steps. Each step ends with `— verify:` and how it is verified.",
		"Verification": "Tests, hands and screens that prove the task works.",
		"Risks":        "What could break, and open questions.",
	}
	for _, s := range Sections {
		fmt.Fprintf(&b, "## %s\n\n<!-- %s -->\n%s\n\n", s, hints[s], placeholder)
	}
	return b.String()
}

type Problem struct {
	Section string `json:"section,omitempty"`
	Message string `json:"message"`
}

var (
	headingRe = regexp.MustCompile(`(?m)^##\s+(.+?)\s*$`)
	commentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	stepRe    = regexp.MustCompile(`^\s*(\d+[.)]|[-*])\s+`)
)

func hasPlaceholderLine(text string) bool {
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == placeholder {
			return true
		}
	}
	return false
}

// Check validates the plan structure. An empty result means the plan is valid.
func Check(md string) []Problem {
	body := map[string]string{}
	var order []string
	locs := headingRe.FindAllStringSubmatchIndex(md, -1)
	for i, loc := range locs {
		name := md[loc[2]:loc[3]]
		end := len(md)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		key := strings.ToLower(name)
		body[key] = strings.TrimSpace(commentRe.ReplaceAllString(md[loc[1]:end], ""))
		order = append(order, key)
	}
	var probs []Problem
	for _, s := range Sections {
		text, ok := body[strings.ToLower(s)]
		switch {
		case !ok:
			probs = append(probs, Problem{s, "missing section"})
		case text == "" || text == placeholder:
			probs = append(probs, Problem{s, "section is empty"})
		case hasPlaceholderLine(text):
			probs = append(probs, Problem{s, "section still contains the " + placeholder + " placeholder"})
		}
	}
	if steps, ok := body["steps"]; ok && steps != "" && steps != placeholder {
		n := 0
		for _, line := range strings.Split(steps, "\n") {
			if !stepRe.MatchString(line) {
				continue
			}
			n++
			if !strings.Contains(strings.ToLower(line), "verify:") {
				probs = append(probs, Problem{"Steps", fmt.Sprintf("step %d has no `verify:` line: %s", n, strings.TrimSpace(line))})
			}
		}
		if n == 0 {
			probs = append(probs, Problem{"Steps", "no numbered or bulleted steps"})
		}
	}
	return probs
}
