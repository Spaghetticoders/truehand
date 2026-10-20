package config

import "fmt"

// Stacks known by `truehand init`.
var Stacks = []string{"flutter", "go", "node", "generic"}

type stackCommands struct {
	test, analyze, uiGlob string
}

var stackDefaults = map[string]stackCommands{
	"flutter": {"flutter test", "flutter analyze", "lib/**"},
	"go":      {"go test ./...", "go vet ./...", ""},
	"node":    {"npm test", "npm run lint", "src/**"},
	"generic": {"", "", ""},
}

// Template returns a commented starter config for a stack.
func Template(stack, base string) (string, error) {
	d, ok := stackDefaults[stack]
	if !ok {
		return "", fmt.Errorf("unknown stack %q (known: %v)", stack, Stacks)
	}
	eyes := ""
	if stack == "flutter" {
		eyes = `
# Eyes compare the running app with the design. Uncomment and fill to enable.
# eyes:
#   adapter: ios-simulator
#   design: figma
#   app: com.example.app        # bundle id
#   figma:
#     file_key: XXXXXXXXXXXX
#     token_env: FIGMA_TOKEN
`
	}
	analyzeRule := ""
	if d.analyze != "" {
		analyzeRule = `
    - check: hand_passed
      with: { hand: analyze }`
	}
	uiRule := ""
	if d.uiGlob != "" {
		uiRule = fmt.Sprintf(`
#   - check: eyes                  # enable once eyes are configured
#     when: { changed: ["%s"] }
#     with: { max_delta_ratio: 0.02 }`, d.uiGlob)
	}
	return fmt.Sprintf(`# Truehand config. Every option: https://github.com/spaghetticoders/truehand/blob/main/docs/config.md
version: 0

# Branch the task diff is measured against (merge-base with HEAD).
base: %s

commands:
  test: %q
  analyze: %q

# Task ids Truehand assigns when you create a task without one: T-1, T-2, ...
tasks:
  prefix: T

# Optional: link tasks to an external tracker (task new --source linear:SPA-123) and
# pull the ticket and its comments into the plan context.
# tickets:
#   adapter: linear        # linear (LINEAR_API_KEY) | github (gh CLI) | file
%s
gates:
  approved:
    - check: plan_valid
    - check: human_approval
  verified:
    - check: test_proof%s%s
  reviewed:
    - check: review_by_other_role
  delivered:
    - check: evidence_matches_head
    - check: conventional_commits
`, base, d.test, d.analyze, eyes, analyzeRule, uiRule), nil
}
