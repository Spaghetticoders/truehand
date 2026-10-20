# Truehand — Spec v0

## 1. What it is

Truehand turns a way of working with AI agents into something that is hard not to follow.
The agent (Claude Code, OpenClaw, Hermes, …) is the brain. Truehand provides:

1. **Hands** — deterministic tools that perform one job the same way every time and return
   a verdict with evidence.
2. **The board** — a per-repo state machine. Each task moves through fixed states, and each
   transition is guarded by a gate that checks evidence on disk.
3. **Locks** — agent hooks (soft), managed settings (firm) and a CI check (hard) that all run
   the same gate logic.

### Non-goals

- Truehand never calls an LLM and never handles the user's Claude credentials. Anything that
  needs judgement is done by the user's agent, on the user's subscription.
- No chat, no terminal, no agent runner in the GUI.
- Not a full project-management tool (no sprints, estimates, assignments). A task *is* a
  ticket — title, description, comments — so a repo needs nothing else; a task can also be
  linked to a Linear/GitHub ticket, whose content then joins the plan context.

## 2. Concepts

| Term | Meaning |
|---|---|
| Task | One unit of work, and its ticket: title, `description.md`, comments. Lives in `.truehand/tasks/<id>/` |
| State | `planned → approved → building → verified → reviewed → delivered` |
| Gate | The rule set that must pass to enter a state. Defined in `.truehand/config.yaml` |
| Evidence | Files produced by hands, recorded with hashes and the fingerprint of the content they ran on |
| Hand | A deterministic operation: input JSON → `{verdict, evidence[], summary}` |
| Role | Who does a step (analyst, developer, reviewer, tester, approver). Writer ≠ checker |
| Adapter | Stack-specific implementation behind a hand (mobile/web eyes, Linear/Jira tickets…) |

## 3. On-disk layout (in the user's repo)

The files are the source of truth. Everything else (SQLite index, GUI, cloud) is derived.

```
.truehand/
  config.yaml                 # gates, adapters, commands (test, build, analyze)
  tasks/
    SPA-123/
      task.yaml               # id, title, optional external ticket, state, roles, timestamps
      description.md          # what has to be done: the ticket (comments live in events.jsonl)
      plan.md                 # written by the agent, validated by the plan hand
      events.jsonl            # append-only log of transitions and hand runs
      evidence/
        index.jsonl           # append-only: {id, hand, verdict, actor, files+sha256, inputs fingerprint}
        test-proof-1-with-change.log
        test-proof-1-without-change.log
        eyes-1-actual.png  eyes-1-design.png  eyes-1-diff.png  eyes-1-report.json
```

### config.yaml (v0)

Each gate is a list of rules `{check, when?, with?}`; see [config.md](config.md) for every
option and check.

```yaml
version: 0
base: origin/main
commands: { test: flutter test, analyze: flutter analyze }
gates:
  approved:  [ {check: plan_valid}, {check: human_approval} ]
  verified:
    - check: test_proof
    - check: hand_passed
      with: { hand: analyze }
    - check: eyes
      when: { changed: ["lib/**/ui/**"] }
  reviewed:  [ {check: review_by_other_role} ]
  delivered: [ {check: evidence_fresh}, {check: conventional_commits} ]
```

`when.changed` globs are matched against the task's diff (base..working tree, untracked
files included, `.truehand/` excluded).

### Evidence and freshness

`evidence/index.jsonl` is append-only. Each record stores the hand, verdict, actor, the
sha256 of every file it wrote, and the **inputs fingerprint**: the git blob hash of every
file the task changes, at the moment the hand ran. Freshness is recomputed from the checkout
(no local git objects needed), so CI sees exactly what the laptop saw. A hand whose run
changed the content (a formatter, a generator) records `error`, never `pass`.

## 4. State machine

```
planned ──(human_approval)──▶ approved ──▶ building ──(verified gates)──▶ verified
   ▲                                          ▲                              │
   └──────────── reject ──────────────────────┴──── review fails ◀── reviewed
                                                                             │
                                                         delivered ◀─────────┘
```

- Transitions happen only through `truehand task move <id> <state>`, which runs the gate.
- A failed gate returns the list of missing or failing evidence; the task does not move.
- Moving backwards (rework) is always allowed and never gated.
- Content changed after a hand ran makes its evidence stale: later gates and CI fail until
  the hand runs again. Committing does not change content, so it keeps evidence fresh.

## 5. Hands (v0)

All hands share one contract, exposed as `truehand hand <name> --json` and as MCP tools.

```json
{ "verdict": "pass" | "fail" | "error",
  "summary": "one line",
  "record": "test-proof-1",
  "evidence": [{ "path": "evidence/…", "sha256": "…" }],
  "next": "optional hint for the agent" }
```

### 5.1 `plan` — ticket → plan

- `plan fetch <ticket>`: pulls ticket, comments, attachments and links via the ticket adapter,
  writes `context.md` and an empty `plan.md` template with required sections
  (goal, scope, out of scope, steps, verification, risks).
- `plan check`: validates `plan.md` structure (sections present, every step has a
  verification line). The *content* is written by the agent.

### 5.2 `test-proof` — the test really catches the bug

1. Snapshot the working tree (no `git stash`: copy changed non-test files aside).
2. Revert the non-test changes of the task to the merge-base version.
3. Run the configured test command filtered to the task's tests; it **must fail**.
4. Restore the changes; run again; it **must pass**.
5. Always restore, even on crash (restore manifest written before step 2).

Evidence: both runs' logs, exit codes, list of reverted files.

### 5.3 `eyes` — look at the real screen (adapter: `ios-simulator` first)

1. Boot/locate the simulator, launch the app (`xcrun simctl`), navigate via `idb`
   accessibility labels following a short route file (`.truehand/routes/<screen>.yaml`).
2. Capture the screenshot.
3. Fetch the Figma node render and node metadata (REST API, token from env/keychain).
4. Compare: scale to the same logical size, pixel diff, plus structural checks
   (text content, colors of text layers, bounding boxes) → `diff.json`.
5. Verdict vs `max_delta_px` and mismatch list.

Known hard cases to handle explicitly: expired Figma token (one call, clear error), multiple
Figma files per project, safe areas / status bar masking, font rendering differences.

### 5.4 `deliver` — clean commits (v0.2)

Groups the working tree into disjoint conventional commits from the task's plan and diff.
Never pushes. Gate `conventional_commits` validates messages.

## 6. Squadra (orchestration)

Squadra is the protocol on top of the board, shipped as a skill plus board rules:

- Roles per task are recorded in `task.yaml`; the gate `review_by_other_role` checks that the
  reviewer session/agent id differs from the developer's.
- The skill tells the agent which role to play next based on the task state; the board, not
  the agent, decides whether the step is complete.

## 7. Locks

| Level | Mechanism | v0 |
|---|---|---|
| Agent | Claude Code hooks: `PreToolUse` on `git commit` / `git push` → `truehand hook pre-commit`; `Stop` → `truehand hook stop` blocks "done" while the active task has unmet gates | yes |
| Company | Same hooks via Claude Code managed settings, not user-overridable | docs only |
| Repo | `truehand ci verify` + GitHub Action: every task touched by the PR must be `delivered` with evidence matching the PR head | yes |

Hooks must answer in < 50 ms when there is nothing to check (single Go binary, no network).

**Human approval.** `truehand approve` and app approvals (enabled when `truehand serve`
starts) require OS authentication — the macOS authorization dialog (password / Touch ID) or
polkit on Linux. An agent running as the same user can spawn processes, escape any
process-tree detection and fake a TTY, but it cannot answer that dialog. The process-tree
and TTY checks remain as early, friendly refusals.

**What the locks close (each has a regression test in `internal/cli/bypass_test.go`,
`internal/hook/policy_test.go`, `internal/serve/serve_test.go`, `internal/hands/eyes_test.go`)**
- Hands are bound to their configured command (`hand run`, and `test-proof` to
  `commands.test_proof`/`test`); `test-proof`, `eyes`, `review` are reserved names. A
  `hand_passed` gate on an unconfigured hand is a config error.
- `eyes --screenshot` only with `eyes.adapter: file`; the record keeps its source and the
  gate rejects images from another source. A simulator capture with no texts is not a pass.
- The reviewer must differ from everyone who worked on the task: whoever moved it to
  building, every actor that ran a hand, and every session that edited files (recorded by
  the PreToolUse hook).
- Agents cannot write under `.truehand/` except `plan.md` and `context.md`; the hooks follow
  `cd`, `sh -c`, git subcommands that rewrite paths, `-c core.hooksPath`, links.
- If the config does not load, the hooks stay closed for commits, pushes and `.truehand/`.
- The config is parsed strictly (unknown keys are errors; `gates:` is required).
- CI evaluates every gate whatever `task.yaml` says, with the gates of the base branch.
- `evidence_matches_head`: evidence must pass on exactly what is committed; only hands that
  some gate uses count, so a deliberately failing diagnostic run does not block delivery.
- `serve`: token on every API call, no CORS, loopback Host only (unless bound to all
  interfaces), no symlinks served, app approvals only for the plan version on screen.

**Known v0 weaknesses**
- Identity is not proof against an adversarial agent: an agent can escape its session's
  process tree and set `TRUEHAND_ACTOR`, so `review_by_other_role` stops honest mistakes and
  shortcuts, not deliberate impersonation. The real separation for review is a human PR
  review (branch protection) on top of `truehand ci verify`.
- Approval and evidence files are plain files: an agent determined to forge them through an
  unparsed shell form can. CI re-checks evidence against the content, but cannot yet tell a
  forged approval from a real one. v0.2: approvals signed with a key held in the OS keychain,
  public keys committed in the repo, signatures verified in CI.
- Identity is per Claude Code process: subagents and in-process agent-team teammates share
  it, so they cannot review each other's work. Reviewers run as separate processes or are
  humans.
- `test-proof` cannot prove behaviour for code that does not exist on the base (new APIs):
  without the change the tests do not compile. That case is reported as a failed proof
  unless `tests.accept_build_failure` is set (then an assertion-free test would pass).
- `serve` speaks plain HTTP. On `--addr 0.0.0.0` the token crosses the network in clear;
  reach the board from a phone through an encrypted tunnel (Tailscale, WireGuard, SSH) rather
  than an open Wi-Fi.
- On macOS the authentication dialog is the administrator one: a standard (non-admin)
  account cannot approve. A LocalAuthentication-based helper is planned.
- The OS dialog appears whenever something asks for it, so an agent can raise it at a moment
  of its choosing and hope for a distracted click. The prompt names the task and its title;
  read it before authenticating.
- The desktop app (truehand-desktop) talks to a local backend with no login. Its
  `/api/truehand/*` routes require a per-launch key that only the app's own window receives,
  and no process the backend spawns (agents, their terminals, the CLI) inherits it, so web
  pages and casual scripts are refused. A same-user agent can still read the key from the
  backend's environment (`ps eww`); the OS dialog remains the barrier, as above.
- File edits made through Bash (`sed -i`, redirects) are not recorded as "edited"; only the
  Write/Edit tools are. Hands and the move to building still mark their actors as workers.
- Tasks are not moved back automatically when evidence goes stale (reads never write): the
  board shows `(stale)` and CI refuses them.

## 8. Packaging

- One static Go binary (`truehand`), built with GoReleaser for macOS/Linux/Windows.
- `truehand mcp` — stdio MCP server exposing the hands (official Go MCP SDK).
- Claude Code plugin (`plugin/`, marketplace in `.claude-plugin/`): skills `truehand` (the
  workflow; absorbs start-work, gui-test and figma-check, now done by the plan and eyes
  hands), `squadra`, `verify-for-real`; `hooks/hooks.json` (SessionStart, PreToolUse, Stop →
  `truehand hook claude`); `.mcp.json` → `truehand mcp`; `bin/truehand` shim that finds the
  real binary and never breaks a session when it is missing.
- `truehand serve`: local HTTP API over the files, a server-sent-events change stream
  (`/api/events`; the web build polls because browsers buffer streamed fetches), and app
  approvals guarded by a per-run token bound to the sha256 of the plan shown.

## 9. Tech stack

Go (core, hooks, hands, MCP via the official go-sdk, CI, HTTP server), YAML/JSON/Markdown
files (state), Flutter (app: web, macOS, iOS). Not yet: SQLite index (the files are fast
enough at this scale), Go + Postgres cloud tier.

## 10. Milestones

| # | Deliverable | Done when |
|---|---|---|
| M1 | `init`, `task new/show/move`, config parsing, gates engine, events log | A task moves through all states on a sample repo with fake evidence, and gates block correctly |
| M2 | `test-proof` hand + `hook pre-commit` + `hook stop` | On a real Spaghetticoders repo, a commit is blocked without test proof and allowed with it |
| M3 | `plan` hand with Linear adapter | `plan fetch SPA-xxx` produces context + template; `plan check` enforces structure |
| M4 | `eyes` hand, iOS simulator + Figma | Verdict with diff on a real screen of a client app |
| M5 | `mcp` server + Claude Code plugin + `ci verify` GitHub Action | Installed on one internal project, used for 4–6 weeks as customer zero |
| M6 | `serve` + Flutter board | Board readable from desktop and phone, plans approvable from the app |
