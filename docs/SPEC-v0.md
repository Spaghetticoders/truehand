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
- No chat of its own. The app runs the user's Claude Code in real terminals (§8), with the
  arguments the server chooses; it never talks to a model itself.
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

**Human approval.** Every approval asks for the human factor at the moment it is made, never
once for a whole run: `truehand approve` and an approval clicked in the Mac app each raise
the OS authentication dialog (macOS authorization dialog with password / Touch ID, polkit on
Linux) naming the task and its title. An agent running as the same user can spawn processes,
escape any process-tree detection and fake a TTY, but it cannot answer that dialog. The
process-tree and TTY checks remain as early, friendly refusals. `serve` itself never asks
for authentication when it starts.

In a repo whose base branch lists `approvers` in `.truehand/config.yaml`, the dialog is not
enough: approving a plan, pushing and opening a PR need an intent signed with one of those
keys (ECDSA P-256; the Mac app keeps its key in the Secure Enclave and every signature asks
for Touch ID). The approvers are read from the base branch, never from the task's branch;
CI reads them at the merge-base. `truehand approve` then refuses and points to the app.
Format and anchors: [config.md](config.md#signed-approvals). The same factor guards pushes
and PRs opened from the app, and every action that could remove it (`PUT …/config`,
commit-config, pairing, remote access, revoking devices).

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
- `serve`: a credential on every API call (the Mac app's token, or a paired device's
  cookie), no CORS, Origin = Host on every write and WebSocket, Host allowlist (loopback, and
  with tailnet access only this Mac's own MagicDNS names and tailnet IPs), no symlinks
  served, markdown sanitized under a strict CSP, app approvals only for the plan version on
  screen. The token alone never approves, pushes, opens a PR or pairs a device (each test in
  `internal/serve/*_review_test.go`).
- Sessions started by the app never run with a permission mode that skips prompts
  (`bypassPermissions` is not offered); the choice is `default`, `acceptEdits` or `plan`.
- On the unix socket, a peer process that descends from Claude Code or carries
  `TRUEHAND_SESSION` (in its environment or an ancestor's), or whose environment cannot be
  read, is an agent: read-only, its terminals view-only, also when forwarded to remote
  machines. Inside an agent session `serve` starts read-only, and `serve --app` refuses.

**Known v0 weaknesses**
- Identity is not proof against an adversarial agent: an agent can escape its session's
  process tree and set `TRUEHAND_ACTOR`, so `review_by_other_role` stops honest mistakes and
  shortcuts, not deliberate impersonation. The real separation for review is a human PR
  review (branch protection) on top of `truehand ci verify`.
- Approval and evidence files are plain files: an agent determined to forge them through an
  unparsed shell form can. CI re-checks evidence against the content; it tells a forged
  approval from a real one only in repos that list `approvers` (signed approvals). Without
  approvers, an approval is a record, not a proof.
- Signed approvals, locally: the approvers' anchors (the task's base ref, the configured
  base, the start commit, origin's default branch, local `main`/`master`) can all be
  rewritten by a determined agent with a local commit, an edited `task.yaml` or
  `git update-ref`. CI, which reads approvers at the merge-base, is the barrier. A CI run on
  a push without a PR takes its base from the branch itself.
- Identity is per Claude Code process: subagents and in-process agent-team teammates share
  it, so they cannot review each other's work. Reviewers run as separate processes or are
  humans.
- `test-proof` cannot prove behaviour for code that does not exist on the base (new APIs):
  without the change the tests do not compile. That case is reported as a failed proof
  unless `tests.accept_build_failure` is set (then an assertion-free test would pass).
- `serve` speaks plain HTTP. It binds 127.0.0.1 only; with tailnet access on it also listens
  on this Mac's tailnet IP, which Tailscale encrypts. "Tailscale serve" adds HTTPS.
- On macOS the authentication dialog is the administrator one: a standard (non-admin)
  account cannot approve unless the repo uses signed approvals (Touch ID on the app's key).
- The OS dialog appears whenever something asks for it, so an agent can raise it at a moment
  of its choosing and hope for a distracted click. The prompt names the action, the task and
  its title (and, for signatures, repo, branch and sha); read it before authenticating.

**Known limits of the app (§8)**
- A paired device is a shell. A terminal in the app runs Claude Code, and its `!` commands
  and "yes" answers to permission prompts execute commands as the user. The boundary is
  pairing (one-time code, confirmed at the Mac) and revocation in Settings.
- R7, escaping the agent check: a process started through launchd or LaunchServices
  (`open -a Terminal`, `launchctl`, `at`), or that drops the variable with `env -u
  TRUEHAND_SESSION` and runs a binary of its own, has a clean environment and is trusted on
  the socket. Approve, push and PR stay behind the human factor regardless.
- Agent locks (the plugin's hooks) hold only in sessions launched by the app, which load the
  plugin with `--plugin-dir`. Claude Code started elsewhere has only the git hooks and CI.
- An older global install of the plugin (`claude plugin install truehand@truehand`) makes
  its hooks run twice in app sessions; uninstall it (`claude plugin uninstall
  truehand@truehand`). Settings warns when it is present.
- The Mac app keeps the token and shell secret in one Keychain item readable only by
  Truehand.app. An agent trying to read it (`security find-generic-password`) raises a
  visible macOS dialog instead of getting the secret; deny it. An ad-hoc signed build asks
  once per new build ("Always Allow").
- "Review by someone else" guarantees a different identity (a new session, never a
  `--resume` of the developer's), not that a human started it.
- Commit-config is bound to the sha256 of the config shown (in the dialog and in the
  queue), but the commit itself runs the repo's git hooks (`pre-commit`, `commit-msg`),
  which any same-user process can edit: a hook can block the commit or change what it
  records. Check the base branch's config after committing; CI reads approvers from there.
- A PR for a task on a remote machine is opened from the Mac (`gh` on the Mac) only with an
  intent signed by this Mac's key, checked by the Mac itself as well as by the peer.
- Windows is out of scope (PTY, peer credentials, Keychain); Linux is best effort.
- File edits made through Bash (`sed -i`, redirects) are not recorded as "edited"; only the
  Write/Edit tools are. Hands and the move to building still mark their actors as workers.
- Tasks are not moved back automatically when evidence goes stale (reads never write): the
  board shows `(stale)` and CI refuses them.

## 8. The app

One binary, `truehand serve --app`, is the whole app; the Mac app is a thin shell around it.

| Part | What it does |
|---|---|
| Server | `serve --app` serves the board of every registered project (Client → Project), the tasks with plan, evidence and diff, and the agents' terminals. It runs as a daemon in its own session: closing the window leaves agents running. State lives in `~/.truehand/app/` (0700; files 0600, written atomically): `state.json` (projects, machines, settings), `sessions.json`, `auth.json` (hashes of the token and of device cookies, the approval queue), `scrollback/`, `hooks/settings.json`, `serve.lock`. Nothing in browser storage except the theme. |
| Web UI | Svelte, embedded in the binary (`make app`, `-tags webui`). Board with "Waiting for you", Task (Description and comments, Agent, Plan, Evidence, Changes) with one contextual button per state, Sessions, Settings. Italian and English. The server computes what waits for whom and the button; the UI only draws it. |
| Agents | Each session is a real Claude Code in a PTY, started by the server with `--plugin-dir ~/.truehand/plugin/<version>` (a versioned copy of the plugin, never installed in the user's Claude config), `--settings ~/.truehand/app/hooks/settings.json` (status hooks only: async, never deciding), `--session-id` chosen by the server (the actor `claude:<id>`) and a permission mode from `default`, `acceptEdits`, `plan`, never one that skips prompts. A clean environment: no token, only `TRUEHAND_SESSION`. |
| Worktrees | A task gets its own branch `th/<id>` and worktree `~/.truehand/worktrees/<project>/<id>`, so several agents work on one repo at once. The config must be committed on the base branch first (the app offers "Commit config"). |
| Review | "Start review" opens a second session with a new id, never a `--resume` of the developer's; it is refused if that session worked on the task. |
| Mac shell | Tauri, no domain logic. Starts or reuses the server, passes token and shell secret on stdin, keeps them in one Keychain item readable only by Truehand.app. Before sending any credential it makes the listener prove it knows the secret (HMAC challenge, `POST /api/shell/challenge`). The human-factor actions go through the shell, which adds the shell secret: the server then raises the OS dialog. Holds the approver key (P-256 in the Secure Enclave, Touch ID on every signature). |
| Phone | Pairs with a one-time code (QR or typed, 2 minutes) shown on the Mac, gets its own revocable cookie, reaches the server over the tailnet (Settings → tailnet access, off by default). It sees everything and can type into terminals after tapping "Write"; approve, push, PR and commit-config from a phone go into a queue that is confirmed at the Mac. |
| Remote machines | `truehand machine add <ssh-target>` (or Settings → Machines) installs the same binary in the remote `~/.truehand`, and starts `serve --peer` there: only a unix socket, no TCP port, no cookies. The Mac reaches it with `ssh <target> ~/.truehand/bin/truehand peer-proxy` (ControlMaster) and routes every project, session and WebSocket of that machine through it; ids are prefixed, the Mac's credentials are never forwarded. On a remote, approve, push and PR need a signed intent checked against the repo's approvers. |
| Terminal | The Mac's server also listens on `~/.truehand/app/serve.sock` (0600). `truehand sessions` lists sessions on this Mac and its machines; `truehand attach <session>` opens one in the current terminal (Ctrl-] detaches, `--view` watches only); works in any terminal or multiplexer pane (herdr included). No human-factor action is available on the socket, and a caller that is an agent is read-only. |

Approvals from the app: a click at the Mac raises the OS dialog for that approval, bound to
the sha256 of the plan on screen (or, with approvers, Touch ID signs an intent naming
action, repo, task, plan sha or head, nonce and time). Commit-config is bound in the same
way to the sha256 of the config text shown, and its dialog lists the approvers it adds or
removes with the fingerprint of each key ("this Mac" or "foreign key").

## 9. Packaging

- One Go binary (`truehand`) for macOS and Linux; `make dist` cross-builds the binaries the
  Mac installs on remote machines (darwin/linux × arm64/amd64, no web app).
- `make app` embeds the web UI; `make shell` builds the Mac app (Truehand.app + DMG, ad-hoc
  signed); `make e2e` builds `bin/truehand-e2e` for browser tests driven from agent sessions
  (no agent guard, fake OS authentication, temporary HOME only, never shipped).
- `truehand mcp` — stdio MCP server exposing the hands (official Go MCP SDK).
- Claude Code plugin (`plugin/`, marketplace in `.claude-plugin/`): skills `truehand` (the
  workflow; absorbs start-work, gui-test and figma-check, now done by the plan and eyes
  hands), `squadra`, `verify-for-real`; `hooks/hooks.json` (SessionStart, PreToolUse, Stop →
  `truehand hook claude`); `.mcp.json` → `truehand mcp`; `bin/truehand` shim that finds the
  real binary and never breaks a session when it is missing. The app loads it per session
  with `--plugin-dir`; installing it globally is only for Claude Code used outside the app.
- `truehand serve`: HTTP API over the files (`/api/snapshot`, tasks, actions, sessions with a
  WebSocket per terminal), a server-sent-events change stream (`/api/events`), the unix
  socket for the CLI, and `--peer` for remote machines.
- The Flutter app in `app/` is legacy: it spoke the old `serve` API and no longer works with
  this one.

## 10. Tech stack

Go (core, hooks, hands, MCP via the official go-sdk, CI, HTTP server, PTYs via creack/pty,
WebSockets via coder/websocket), Svelte 5 + Vite + xterm.js (web UI), Tauri 2 + Rust +
CryptoKit (Mac shell, Secure Enclave key), YAML/JSON/Markdown files (state). Not yet:
SQLite index (the files are fast enough at this scale), Go + Postgres cloud tier, WebAuthn
approvals from the phone.

## 11. Milestones

| # | Deliverable | Done when |
|---|---|---|
| M1 | `init`, `task new/show/move`, config parsing, gates engine, events log | A task moves through all states on a sample repo with fake evidence, and gates block correctly |
| M2 | `test-proof` hand + `hook pre-commit` + `hook stop` | On a real Spaghetticoders repo, a commit is blocked without test proof and allowed with it |
| M3 | `plan` hand with Linear adapter | `plan fetch SPA-xxx` produces context + template; `plan check` enforces structure |
| M4 | `eyes` hand, iOS simulator + Figma | Verdict with diff on a real screen of a client app |
| M5 | `mcp` server + Claude Code plugin + `ci verify` GitHub Action | Installed on one internal project, used for 4–6 weeks as customer zero |
| M6 | `serve` + Flutter board (legacy) | Board readable from desktop and phone, plans approvable from the app |
| M7 | `serve --app` + Svelte UI + Tauri shell, agents in PTYs, worktrees, phone pairing, remote machines, signed approvals | A task goes from plan to PR with real Claude Code sessions, from the Mac and from a phone |
