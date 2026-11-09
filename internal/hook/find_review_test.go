package hook

import "testing"

func TestReviewFindOnTruehand(t *testing.T) {
	ws := workspace(t, cfg)
	root := ws.Repo.Root
	allow := []string{
		"find .truehand -name '*.yaml'",
		"find .truehand -type f -print",
		"find . -path ./.truehand -prune -o -name '*.go' -print",
		"find src -name '*.tmp' -delete",
	}
	deny := []string{
		"find .truehand -name approvals.jsonl -delete",
		"find .truehand/tasks -exec rm {} +",
		"find .truehand -execdir sh -c 'echo x > {}' ;",
		"find .truehand -ok rm {} ;",
		"find .truehand -fprint .truehand/x",
		"find . -name config.yaml -delete",
		"find ./.truehand/ -delete",
		"find .TRUEHAND -delete",
		"find $HOME/.truehand -delete",
		"find ~/.truehand/app -name passkeys.json -delete",
		"find / -path '*truehand*' -delete",
		"find . -newer x -exec sed -i s/a/b/ {} ;",
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
