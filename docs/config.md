# `.truehand/config.yaml`

```yaml
version: 0

# Branch the task diff is measured against. A new task's base is the merge-base of HEAD
# with this ref, or HEAD itself if the ref is missing or HEAD is on it.
base: origin/main

commands:
  test: flutter test               # used by test-proof
  test_proof: flutter test test/unit   # optional, narrower command for test-proof
  analyze: flutter analyze         # any name works with `truehand hand run <name>`

tests:
  globs: ["test/**", "**/*_test.dart"]   # what counts as test code (default covers most stacks)
  accept_build_failure: false      # true: "does not compile without the change" counts as a proof (weaker)

tickets:
  adapter: linear                  # linear (LINEAR_API_KEY) | github (gh CLI) | file

locks:
  commit_from: reviewed            # earliest state in which agents may commit
  agent_push: false                # agents never push unless this is true

eyes:
  adapter: ios-simulator           # or file (pass --screenshot)
  device: booted                   # or a simulator UDID
  app: com.example.app             # bundle id, needed with launch
  launch: false                    # relaunch the app before every capture
  figma:
    file_key: XXXXXXXXXXXX
    token_env: FIGMA_TOKEN         # env var holding a personal access token
  max_delta_ratio: 0.03            # share of differing pixels allowed
  threshold: 40                    # per-channel difference that counts as a differing pixel
  mask: [{x: 0, y: 0, w: 10000, h: 54}]   # points; default masks the status bar; [] = none
  screens:
    login:
      node: "12:34"                # or "12-34" as in Figma URLs
      file_key: YYYY               # projects often have several Figma files
      route:                       # how to reach the screen, by accessibility label
        - tap: "Accedi"
        - wait: "Email"
        - text: "demo"             # letters, digits, spaces only
        - swipe: up
        - sleep: 500ms
      mask: [{x: 0, y: 700, w: 402, h: 100}]
      ignore_texts: ["9:41"]
      skip_texts: false            # true for canvas-drawn screens with no labels

gates:
  approved:
    - check: plan_valid
    - check: human_approval
  verified:
    - check: test_proof
    - check: hand_passed
      with: { hand: analyze }
    - check: eyes
      when: { changed: ["lib/**/ui/**"] }   # only when the task touches these files
  reviewed:
    - check: review_by_other_role
  delivered:
    - check: evidence_fresh
    - check: conventional_commits
```

## Checks

| check | passes when |
|---|---|
| `plan_valid` | `plan.md` has Goal, Scope, Out of scope, Steps (each with `verify:`), Verification, Risks |
| `human_approval` | a human approved this exact `plan.md` after OS authentication (`truehand approve`, or the app when `serve` was started with authentication) |
| `test_proof` | latest `test-proof` evidence passed, intact, on the current content |
| `eyes` | latest `eyes` evidence of every screen checked passed, intact, fresh |
| `hand_passed` | latest evidence of `with.hand` passed, intact, fresh |
| `review_by_other_role` | latest review passed and its actor did not work on the task (moved it to building, ran a hand, or edited files) |
| `evidence_fresh` | the latest record of every gated hand passed, is intact and fresh |
| `evidence_matches_head` | `evidence_fresh`, and no task file has uncommitted changes (the default for `delivered`) |
| `conventional_commits` | every commit since the task base is `type(scope): summary` |

## Task scope

`truehand task new <id> --scope "lib/auth/**,test/auth/**"` declares the files a task owns.
Without a scope a task owns every file changed since its base, which is right for one branch
per task. When several tasks share a branch (trunk-based work), give each a scope: its
fingerprint, `test-proof` reverts, `when.changed` rules and CI coverage then consider only
its files, and the other tasks' work never makes its evidence stale.

## Identity

The actor of a command is the Claude Code session the process descends from
(`claude:<session-id>`, recorded by the SessionStart hook), which `TRUEHAND_ACTOR` cannot
override; outside a Claude Code session, `TRUEHAND_ACTOR` when set (for other agent runners);
otherwise `human:<git user.email>`. The developer of a task is whoever moved it to `building`.
Everything inside one Claude Code process shares its identity: subagents, and agent-team
teammates running in-process. A review must come from a separate Claude Code process (a
teammate in its own tmux/split pane, or another `claude` session) or from a human.
