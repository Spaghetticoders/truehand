# Truehand

Reliable hands for AI coding agents.

Claude, OpenClaw, Hermes and the rest are general-purpose robots: they decide *what* to do.
Truehand gives them hands that do it *the same way every time*, and a board with gates so
work cannot move forward without evidence:

```
planned ──▶ approved ──▶ building ──▶ verified ──▶ reviewed ──▶ delivered
   plan +      human          code      hands'       someone      conventional
   approval    only                    evidence      else         commits, CI lock
```

| Hand | What it proves |
|---|---|
| `plan fetch` | The plan starts from the whole task, comments included — and from the linked Linear/GitHub ticket, if any |
| `hand test-proof` | The tests pass with the change **and fail without it** |
| `hand eyes` | The real screen matches its Figma node: pixel diff + every visible text |
| `hand run <name>` | Any command (analyze, lint, build), run by Truehand, exit code recorded |
| `hand attach` | Screenshots or files the agent looked at, kept on the task for the humans |
| `review` | A review exists, by someone who did not work on the task |

- **Agent-agnostic.** CLI + MCP server; a Claude Code plugin adds the locks.
- **No LLM inside.** Hands are deterministic code. Judgement stays with the user's agent,
  on the user's own subscription. Truehand never touches Claude credentials.
- **Evidence, not claims.** Each hand writes files plus a record of the exact content it ran
  on. Change a file afterwards and the evidence goes stale; edit the evidence and it reads
  as tampered.
- **Human approval means a human.** `truehand approve` asks the operating system to
  authenticate you (password or Touch ID); an agent running as your user can run commands
  but cannot answer that dialog.
- **Three locks.** Agent hooks keep agents on the path (no push, no commit before review, no
  edits to Truehand's files, no "done" with unverified work) — a soft lock that stops
  mistakes and shortcuts; managed settings for companies; and `truehand ci verify`, the
  merge lock, which re-checks everything on the pushed content with the base branch's gates.

## Install

```sh
go install github.com/spaghetticoders/truehand/cmd/truehand@latest
```

Claude Code plugin (hooks, MCP tools, skills):

```
/plugin marketplace add spaghetticoders/truehand
/plugin install truehand@truehand
```

## Quick start

```sh
cd your-repo
truehand init                      # .truehand/config.yaml for your stack
truehand hook install              # commit-msg + pre-push git hooks
truehand task new --title "Login screen" --description "Email + password, remember me."
                                   # → T-1; the task is the ticket. Linked tracker optional:
                                   #   --source linear:SPA-123
truehand plan fetch                # context.md with description and comments + plan.md
# …the agent fills plan.md…
truehand approve                   # you: in the app or in a terminal → approved
truehand task move building
# …the agent builds, then runs the hands…
truehand hand test-proof
truehand hand run analyze
truehand hand eyes login
truehand task move verified
# …someone else reviews…
truehand review --verdict pass --notes review.md
truehand task move reviewed
git commit -m "feat(auth): new login screen"
truehand task move delivered
```

`truehand task show` always says what the next gate is missing.

## The app

```sh
truehand serve --repo ~/code/app-a --repo ~/code/app-b --web app/build/web
```

Opens a board across repos with gates, plans, evidence (screenshots, diffs) and history.
Every API call needs the token the server prints. Starting the server asks for OS
authentication, after which plans can be approved from the app — handy from a phone
(`--addr 0.0.0.0:7878`, through a tunnel such as Tailscale: the server speaks plain HTTP);
`--read-only` skips it. The app is Flutter: web, macOS and iOS from
one codebase (`app/`).

## Docs

- [docs/SPEC-v0.md](docs/SPEC-v0.md) — concepts, files, gates, hands, locks
- [docs/config.md](docs/config.md) — every config option
- [docs/ci.md](docs/ci.md) — the merge lock in GitHub Actions

## Build

```sh
go build -o bin/truehand ./cmd/truehand
go test ./...
cd app && flutter test
```
