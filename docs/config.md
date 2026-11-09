# `.truehand/config.yaml`

```yaml
version: 0

# Branch the task diff is measured against. A new task's base is the merge-base of HEAD
# with this ref, or HEAD itself if the ref is missing or HEAD is on it, or HEAD when the
# commits before it close reviewed tasks (stacked tasks, see below).
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
  - id: alice-mac                  # letters, digits, '.', '_', '-'; unique in the list
    name: Alice (MacBook)
    public_key: |                  # PEM SubjectPublicKeyInfo: ECDSA P-256, or RSA >= 2048 bits, e = 65537
      -----BEGIN PUBLIC KEY-----
      MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE...
      -----END PUBLIC KEY-----
  - id: alice-phone                # a passkey, registered in the app from the phone's browser
    name: Alice (phone)
    public_key: |
      -----BEGIN PUBLIC KEY-----
      MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE...
      -----END PUBLIC KEY-----
    rp_id: mac.tail1234.ts.net     # passkeys only: the host the passkey was registered on

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
| `in_scope` | a task with a `scope` changed no file outside it, except files another task's scope claims; it lists the others, so the agent learns at `verified`, not in CI, that a human must widen the scope (the app does it, recorded as that human's decision) or the change be undone. Passes for tasks without a scope |

## Signed approvals

With `approvers`, `human_approval` passes only for an approval of the current `plan.md`
signed by one of the listed keys, for this repo and this task, with a nonce no earlier
approval of the task used (a copy of an approval counts as nothing). `truehand approve`
then refuses: approvals are made in the Truehand app, which signs with a key held in
hardware: the Mac's Secure Enclave behind Touch ID, or a passkey (WebAuthn) on a phone,
a laptop or a security key, behind the device's own fingerprint, face or PIN. In every repo
that lists approvers, local or on a remote machine, pushing and opening a pull request from
the app need a signed intent the same way (`action: push` / `open-pr`), and so do committing
the config to the base and pushing the base (`commit-config` / `push-base`, below); the OS
dialog alone is not accepted.

### Approvers

Each entry has exactly these fields; any other field is a config error.

| field | |
|---|---|
| `id` | letters, digits, `.`, `_`, `-`, at most 64; unique in the list (a duplicate id is a config error) |
| `name` | who holds the key, as shown in the app; not empty |
| `public_key` | one PEM `PUBLIC KEY` block: ECDSA P-256 (ES256), or RSA of at least 2048 bits with exponent 65537 (RS256, as Windows Hello makes) |
| `rp_id` | passkeys only: the WebAuthn relying party, the host the passkey was registered on |

`rp_id` is a lowercase host name and nothing else: no scheme, no port, never an IP address
(WebAuthn does not allow one). A passkey signs for the origin `https://<rp_id>` on any port,
so `tailscale serve` on 443, 8443 or 10000 makes no difference; the domain itself is bound by
the signature. Moving the app to another host means registering the passkeys again. A config
committed with an `rp_id` in capitals or with `:port` does not load: fix it on the base.

An RSA key signs only through WebAuthn. Passkeys synced by their platform (Google Password
Manager, iCloud Keychain) are accepted and are the default on Android and iPhone; the app
labels them "synced". Their key lives in that account's cloud too, so whoever controls the
account can sign: prefer a device-bound passkey or a security key for an approver who must
not depend on a cloud account.

### Which list counts

Approvers come only from committed configs, never from the working tree: a key an agent
writes there is ignored, and a branch cannot add its own key nor turn signing off by
dropping the list. A change to `approvers` takes effect once committed to the base.

In CI the list is the one at the tip of the base branch the change goes into (see
[CI](ci.md)). Locally the sources are the task's base ref, the configured `base`, the commit
the task started from, origin's default branch, local `main` and `master`. Of the sources
that list approvers only the newest count: one another source descends from is an older
state of the same history and is ignored, so after a key is rotated or an approver removed
on the base, the old key no longer signs, whichever ref still lists it. Sources on diverged
histories count together; an id listed by more than one takes its key from the first in the
order above. A source with no approvers never hides the others: a commit that removes every
approver does not revoke the earlier lists locally (the app refuses to push such a config,
see below). A source that does not resolve is skipped; when none resolves, the check fails.

As soon as a source lists approvers, a shallow clone or a `.git/info/grafts` file is an error
(`git fetch --unshallow`): which source is newer depends on the real history. Truehand runs
git with replace refs and grafts switched off, so `git replace` cannot show it another commit
under an id. Without approvers none of this applies.

Approvals signed with a key since revoked on the base no longer count and must be signed
again. Locally every source can still be rewritten by a determined agent (a local commit on
`main` with its own key, an edited task.yaml, `git update-ref`, a stale `origin/main` that
still lists a removed approver): CI is the lock.

### The signed message

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
approver: alice-mac
```

| action | task | plan_sha256 | head |
|---|---|---|---|
| `approve-plan` | the task | of its `plan.md` | empty (the line is still there, as `head: ` with its space) |
| `push`, `open-pr` | the task | of its `plan.md` | the commit pushed |
| `commit-config` | `config` | of the config YAML to commit | the base commit it is committed onto |
| `push-base` | `config` | of `.truehand/config.yaml` at `head` | the base commit pushed |

`commit-config` and `push-base` are checked against the approvers of the config committed at
the base they change (for `push-base`, the remote's current tip), never against the config
being committed. A `push-base` carries only config commits: every commit between the
remote's tip and `head` must be a non-merge commit changing nothing but
`.truehand/config.yaml`, the config at `head` must load, and it must not drop every approver
while the base lists some. This is how a headless server gets a config change onto the base
without a terminal: the approver signs both steps from the app.

The signature is ES256 (ECDSA P-256 over SHA-256 of these bytes, ASN.1 DER, standard base64)
for a Secure Enclave key. For a passkey it is a WebAuthn assertion whose challenge is the
SHA-256 of these bytes: the signature (DER for ES256, PKCS#1 v1.5 for RS256, standard
base64) comes with `authenticator_data` and `client_data_json` (base64url). It counts only
with user presence and user verification, `type` `webauthn.get`, an origin `https://<rp_id>`
on any port, not in a cross-origin frame, and the relying party hash of `rp_id`. The app
accepts an intent only within five minutes of its `time`, and each nonce once.

## Task scope

`truehand task new <id> --scope "lib/auth/**,test/auth/**"` declares the files a task owns.
Without a scope a task owns every file changed since its base, which is right for one branch
per task. When several tasks share a branch (trunk-based work), give each a scope: its
fingerprint, `test-proof` reverts, `when.changed` rules and CI coverage then consider only
its files, and the other tasks' work never makes its evidence stale.

## Stacked tasks


When a branch carries a reviewed task not yet merged (COACH-1) and a new task continues on top
of it (COACH-2), the new task is stacked: its `task.yaml` gets `stacked_on: COACH-1` and a
`base` at the commit that closes COACH-1, so its diff, `test-proof` reverts and scope hold only
its own work. `task new` does this by itself when every commit since the base branch is
covered by reviewed or delivered tasks whose gates pass at HEAD. A task created before that
is moved with `truehand task restack COACH-2`, from your own terminal: it finds the latest
commit that closes a reviewed task, shows it, and asks to confirm and authenticate; commits
after that point stay COACH-2's. If COACH-1's evidence saw files its commits do not have
(another task's changes in the folder, untracked files), they are listed and ignored only if
you accept that list. COACH-1 is then judged at that commit and its hands no longer
run; CI accepts the pair when both are delivered, each covering its own files.

A task whose `base` lies past the point where its branch leaves `base:` without being stacked
has no base (an error asking to restack): the commits in between would otherwise belong to no
task. From this version on, fields of `task.yaml` that a Truehand binary does not know are
kept when it saves the task; older binaries drop `stacked_on` when they move a task, so run
every agent with the same Truehand (the app's plugin) and restack after an older one touched
the task.

## Identity

The actor of a command is the Claude Code session the process descends from
(`claude:<session-id>`, recorded by the SessionStart hook), which `TRUEHAND_ACTOR` cannot
override; outside a Claude Code session, `TRUEHAND_ACTOR` when set (for other agent runners);
otherwise `human:<git user.email>`. The developer of a task is whoever moved it to `building`.
Everything inside one Claude Code process shares its identity: subagents, and agent-team
teammates running in-process. A review must come from a separate Claude Code process (a
teammate in its own tmux/split pane, or another `claude` session) or from a human.
