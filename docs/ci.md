# Truehand in CI

`truehand ci verify` is the hard lock. Agent hooks can be disabled on a laptop; CI cannot.
It passes only when:

- every file changed on the branch (outside `.truehand/`) belongs to a task whose
  `.truehand/tasks/<id>/` was changed on the branch, and that task is `delivered`;
- every gate of those tasks still passes on the checked-out content — evidence intact
  (hashes match) and fresh (the files it ran on are byte-identical to the commit).

It needs the full history to compute merge-bases, and a shallow clone is refused as soon as
the base lists approvers:

```yaml
# .github/workflows/truehand.yml
name: truehand
on: pull_request
jobs:
  verify:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: spaghetticoders/truehand@main
```

Make the `truehand / verify` check required in the branch protection rules. That is what
turns the workflow from a habit into a rule.

Locally, `truehand hook install` adds a `pre-push` hook that runs the same check, and a
`commit-msg` hook that enforces conventional commits.

## What the branch cannot change

The gates come from the config at the merge-base with the base branch: a branch cannot relax
the rules it is judged by. The approvers come from the config at the tip of the base branch
the pull request goes into: a branch cannot move that tip, and an approver removed or a key
rotated on the base stops counting at once, also for branches made before. An approval
signed with a revoked key fails and must be signed again. A branch's own config, task.yaml
`base_ref` or working tree never adds a key nor turns signing off.

Git runs with replace refs and grafts switched off, so the commits checked are the real ones;
with approvers on the base, a shallow clone or an `info/grafts` file fails the check.

Config changes reach the base like any other change (a pull request reviewed by a human), or
from the app: `commit-config` commits the config onto the base and `push-base` pushes it,
each signed by an approver the base already lists (see [config](config.md#the-signed-message)).
`push-base` carries only commits that change `.truehand/config.yaml`, so a signature for a
config never brings code or a workflow with it. On a headless server this replaces the
terminal for config changes. The push itself is guarded by a lease on the remote's tip.

## Protect the workflow itself

A `pull_request` workflow runs the version of `.github/workflows/truehand.yml` in the pull
request, not the base's. A branch can therefore rewrite it (another base, a step that exits
0) and the required check still turns green. The lock holds only when `.github/` cannot
change without a human. Set up both:

- **A ruleset that requires the workflow from the base** (the organisation's Settings →
  Rules → Rulesets, targeting the repo's base branch: "Require workflows to pass before
  merging", pointing at `.github/workflows/truehand.yml` on the base branch, or at a
  separate repo that holds it).
  GitHub then runs the base's copy, whatever the pull request changes. This is an
  organisation ruleset (GitHub Team or Enterprise); a personal repo on the free plan does not
  have it and relies on CODEOWNERS alone.
- **CODEOWNERS with required review** for the workflow and the rules:

  ```
  # .github/CODEOWNERS
  /.github/            @owner
  /.truehand/config.yaml @owner
  ```

  with "Require review from Code Owners" on the base branch, so a change to them needs a
  human's approval besides the check.

Without the ruleset, CODEOWNERS alone still stops a rewritten workflow from merging, as long
as the reviewer reads `.github/` changes in the pull request. Do not switch to
`pull_request_target` with a checkout of the pull request's head to get the base's workflow:
it runs the pull request's code with the base's secrets and write token. Truehand's own
`push-base` never carries `.github/` changes (only config commits).

## Limits

CI is the lock only where the `truehand / verify` check is required on the base and the
workflow is protected as above. A pull request that edits `.github/` without either is not
stopped by Truehand. A commit that removes every approver from the base takes effect in CI
(signing stops for the next pull requests) but does not revoke the old lists locally; the app
refuses to push such a config. Locally all refs can be rewritten by an agent; only what CI
checks is binding.
