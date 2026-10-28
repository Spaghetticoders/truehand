package gate

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/gitx"
)

func TestResolveCommits(t *testing.T) {
	dir := t.TempDir()
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.email=h@x.y", "-c", "user.name=H", "commit", "-q", "--allow-empty", "-m", "x"}, {"branch", "other"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
			t.Fatal(string(out))
		}
	}
	repo := &gitx.Repo{Root: dir}
	head, _ := repo.Git("rev-parse", "HEAD")
	head = strings.TrimSpace(head)
	refs := []string{"main", "", "-n", "nope", "main\nHEAD", head, gitx.EmptyTree, "other", "HEAD:.truehand"}
	got, err := resolveCommits(repo, refs)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{head, "", "", "", "", head, "", head, ""}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestApproversCacheIsNotShared(t *testing.T) {
	commit := strings.Repeat("c", 40)
	approversCache.Store(commit, []config.Approver{{ID: "mac", Name: "Mac"}})
	t.Cleanup(func() { approversCache.Delete(commit) })
	got, err := approversAtCommit(nil, commit)
	if err != nil {
		t.Fatal(err)
	}
	got[0].ID = "evil"
	again, _ := approversAtCommit(nil, commit)
	if again[0].ID != "mac" {
		t.Fatal("a caller's change reached the cache")
	}
}
