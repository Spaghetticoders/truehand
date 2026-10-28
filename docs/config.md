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

tasks:
  prefix: T                        # ids for tasks created without one: T-1, T-2, …

tickets:                           # optional: external tracker for --source
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

approvers:                         # optional: plan approvals must be signed by one of these keys
  - id: emanuel-mac                # letters, digits, '.', '_', '-'; unique
    name: Emanuel (MacBook)
    public_key: |                  # ECDSA P-256, PEM SubjectPublicKeyInfo
      -----BEGIN PUBLIC KEY-----
      MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE...
      -----END PUBLIC KEY-----
    rp_id: truehand.example        # only for a key registered with WebAuthn

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
| `human_approval` | a human approved this exact `plan.md` after OS authentication (`truehand approve`, or the app); with `approvers`, the approval must be signed by one of their keys (see below) |
| `test_proof` | latest `test-proof` evidence passed, intact, on the current content |
| `eyes` | latest `eyes` evidence of every screen checked passed, intact, fresh |
| `hand_passed` | latest evidence of `with.hand` passed, intact, fresh |
| `review_by_other_role` | latest review passed and its actor did not work on the task (moved it to building, ran a hand, or edited files) |
| `evidence_fresh` | the latest record of every gated hand passed, is intact and fresh |
| `evidence_matches_head` | `evidence_fresh`, and no task file has uncommitted changes (the default for `delivered`) |
| `conventional_commits` | every commit since the task base is `type(scope): summary` |

## Signed approvals

With `approvers`, `human_approval` passes only for an approval of the current `plan.md`
signed by one of the listed keys, for this repo and this task, with a nonce no earlier
approval of the task used (a copy of an approval counts as nothing). `truehand approve`
then refuses: approvals are made in the Truehand app, which signs with a key held in
hardware (the Mac's Secure Enclave, behind Touch ID). In every repo that lists approvers,
local or on a remote machine, pushing and opening a pull request from the app need a signed
intent the same way (`action: push` / `open-pr`); the OS dialog alone is not accepted.

Only the list on the base branch counts: a branch cannot add its own key, and dropping the
list on a branch does not turn signing off. CI reads the list at the merge-base with the
base branch, whatever the branch's own files say.

Locally signed approvals are required as soon as any of these lists approvers: the task's
base ref, the configured `base`, the commit the task started from, origin's default branch,
local `main` or `master`, or the config in the working tree. The keys of all but the last
count together; a key only in the working tree's config counts while no base lists any
(approvers being introduced on a branch). A base that does not resolve is skipped; when
none resolves, the check fails. Locally every one of these can still be rewritten by a
determined agent (a local commit with its own key named as the task's start commit or base,
an edited task.yaml, `git update-ref`): CI is the lock. A change to `approvers` takes effect
once merged.

`repo` is the `origin` URL reduced to host/path (`github.com/owner/repo`), or `root:<sha>` of
the root commit in a repo without `origin`.

The signed message is UTF-8 text, one `key: value` line per field, in this order:

```
truehand-intent/1
action: approve-plan
repo: github.com/owner/repo
task: T-12
plan_sha256: <sha256 of plan.md, lowercase hex>
head: 
nonce: <32-64 lowercase hex>
time: 2026-10-27T22:10:00Z
approver: emanuel-mac
```

`action` is `approve-plan`, `push` or `open-pr`; `head` is the commit for the last two and
empty for `approve-plan` (the line is still there, as `head: ` with its space). The
signature is ECDSA P-256 over SHA-256 of these bytes, ASN.1 DER, standard base64. The app
accepts an intent only within five minutes of its `time`, and each nonce once.

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
