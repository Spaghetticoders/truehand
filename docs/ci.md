# Truehand in CI

`truehand ci verify` is the hard lock. Agent hooks can be disabled on a laptop; CI cannot.
It passes only when:

- every file changed on the branch (outside `.truehand/`) belongs to a task whose
  `.truehand/tasks/<id>/` was changed on the branch, and that task is `delivered`;
- every gate of those tasks still passes on the checked-out content — evidence intact
  (hashes match) and fresh (the files it ran on are byte-identical to the commit).

It needs the full history to compute merge-bases:

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
