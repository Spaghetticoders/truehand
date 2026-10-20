package glob

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"lib/**", "lib/a.dart", true},
		{"lib/**", "lib/ui/home/page.dart", true},
		{"lib/**", "test/a.dart", false},
		{"lib/**/ui/**", "lib/feature/ui/page.dart", true},
		{"lib/**/ui/**", "lib/ui/page.dart", true},
		{"lib/**/ui/**", "lib/feature/data/repo.dart", false},
		{"*.go", "main.go", true},
		{"*.go", "cmd/main.go", false},
		{"**/*.go", "cmd/main.go", true},
		{"**/*.go", "main.go", true},
		{"src/?.ts", "src/a.ts", true},
		{"src/?.ts", "src/ab.ts", false},
		{"a.b", "axb", false},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.path); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}
