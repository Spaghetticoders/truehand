package hook

import "testing"

// Reading truehand's files is allowed in every form an agent uses; writing them is not,
// whatever the flags look like.
func TestReviewReadsAllowedWritesDenied(t *testing.T) {
	ws := workspace(t, cfg)
	root := ws.Repo.Root
	allow := []string{
		"ls .truehand",
		"ls -la .truehand/tasks",
		"sed -n 1,40p .truehand/config.yaml",
		"sed -n -e '/gates/,$p' .truehand/config.yaml",
		"sed 's/a/b/' .truehand/config.yaml > /tmp/cfg.yaml",
		"sed -E -n '/approvers/p' .truehand/config.yaml",
		"cp .truehand/config.yaml /tmp/cfg.yaml",
		"cp -r .truehand/tasks /tmp/backup",
		"install -m 644 .truehand/config.yaml /tmp/cfg.yaml",
		`echo "see .truehand/config.yaml for the gates" >> /tmp/notes.md`,
		`printf '%s\n' .truehand/config.yaml >> /tmp/list`,
		"grep -n approvers .truehand/config.yaml",
		"cat .truehand/config.yaml | head -5",
		"python3 -m pytest",
	}
	deny := []string{
		"sed -i 's/a/b/' .truehand/config.yaml",
		"sed -ibak 's/a/b/' .truehand/config.yaml",
		"sed -ni 's/a/b/' .truehand/config.yaml",
		"sed --in-place=.bak 's/a/b/' .truehand/config.yaml",
		"sed -n 's/a/b/' -i .truehand/config.yaml",
		"sed -n 'w .truehand/config.yaml' /tmp/evil",
		"sed -f /tmp/script.sed .truehand/config.yaml",
		"cp /tmp/evil .truehand/config.yaml",
		"cp -t .truehand/tasks/T-1 /tmp/approvals.jsonl",
		"cp -rt .truehand/tasks/T-1 /tmp/approvals.jsonl",
		"cp -vt .truehand/tasks/T-1 /tmp/approvals.jsonl",
		"cp --target-directory=.truehand/tasks/T-1 /tmp/approvals.jsonl",
		"cp --target-directory .truehand/tasks/T-1 /tmp/approvals.jsonl",
		"cp /tmp/a /tmp/b .truehand/tasks/T-1/",
		"install -Dm644 /tmp/evil .truehand/config.yaml",
		"echo x > .truehand/config.yaml",
		`printf 'x' >> .truehand/tasks/T-1/approvals.jsonl`,
		`python3 -c "open('.truehand/config.yaml','w').write('x')"`,
		"python3 - <<'EOF'\nopen('.truehand/config.yaml','w')\nEOF",
		`node -e "require('fs').writeFileSync('.truehand/config.yaml','')"`,
		"cat /tmp/x | tee .truehand/config.yaml",
	}
	for _, c := range allow {
		if d := CheckBash(ws, root, c); d.Deny {
			t.Errorf("must allow %q: %s", c, d.Reason)
		}
	}
	for _, c := range deny {
		if d := CheckBash(ws, root, c); !d.Deny {
			t.Errorf("must deny %q", c)
		}
	}
}
