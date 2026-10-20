---
name: truehand
description: The workflow for any coding task in a repo that has a .truehand/ directory — task, plan, human approval, build, evidence from the hands, review by someone else, delivery. Use it before touching code in such a repo, and whenever a Truehand gate, hook or "Truehand:" message blocks you.
---

# Working with Truehand

Truehand does not trust claims, only evidence. A task moves through
`planned → approved → building → verified → reviewed → delivered`, and every forward move
is a gate that checks files the hands wrote. You cannot talk your way through a gate; you
can only produce what it asks for. `truehand_status` (or `truehand task show`) always says
what is missing.

Use the MCP tools (`truehand_*`) or the CLI (`truehand …`); they are the same operations.

## 1. Task and plan

- One task per ticket or slice: `truehand_task_new {id, title, source}` (source like
  `linear:SPA-123`). It becomes the active task.
- `truehand_plan_fetch` writes `context.md` with the ticket **and all its comments** —
  comments carry the course corrections the description lacks. Read it all.
- Check whether the work is already done before planning: a grep is cheaper than a plan.
- Fill `.truehand/tasks/<id>/plan.md`: Goal, Scope, Out of scope, Steps (each step ends with
  `— verify:` and how), Verification, Risks. `truehand_plan_check` until it is valid.
- **Stop and ask the user to approve.** Approval is human-only: they run
  `truehand approve <id>` in a terminal. There is no tool for it, and trying to fake it is
  blocked. While you wait, you may only read.

## 2. Build

- After approval: `truehand_task_move {state: approved}`, then `building`.
- Write the code and the tests together. If a design looks wrong, stop and ask; never invent
  copy or measures, and never silently implement the version you prefer.

## 3. Verify with the hands

The verified gate lists exactly what is required. Typically:

- **`truehand_test_proof`** — runs the tests with your change (must pass) and with the
  non-test files reverted to the task base (must fail). If the tests still pass without your
  change, they do not test it: write one that does. If they only fail because they no longer
  compile, that proves nothing either: narrow `commands.test_proof` to tests that compile on
  the base.
- **`truehand_hand_run {name: analyze}`** (or lint, build…) — truehand runs the command and
  records the exit code. Do not paste outputs; let the hand run it.
- **`truehand_eyes {screen}`** — captures the real screen and compares it with its Figma
  node: pixel diff and every visible Figma text on screen. On fail, open `diff.png` next to
  `design.png` and `actual.png`, fix, run again. If the difference is intended, it is a
  conversation with the user and the designer, not a silent override.

Any edit after a hand ran makes that evidence stale; run the hand again. When the gate is
open: `truehand_task_move {state: verified}`.

## 4. Review by someone else

The developer cannot review their own task — the gate checks the actor, and everything in
one Claude Code process (subagents, in-process teammates) counts as the same actor. A review
comes from a separate Claude Code process (a teammate in its own pane, another session) or a
human: they read the diff and the
evidence, run what they need, and record `truehand_review {verdict, notes}` with what they
read, ran and found. Then `reviewed`.

## 5. Deliver

- Commits are allowed only from `reviewed` on, with conventional commit subjects
  (`type(scope): summary`). Never `--no-verify`.
- **Never push.** The user pushes.
- `truehand_task_move {state: delivered}`. Commit `.truehand/tasks/<id>/` with the work: CI
  runs `truehand ci verify` and refuses changes not covered by a delivered task.

## When blocked

A `Truehand:` denial says why and what to do. Do not work around it (other commands,
other paths, editing truehand's files): the CI lock repeats every check on the pushed
content, so a workaround only moves the failure later. If you cannot proceed, say so to the
user plainly instead of declaring the work done.
