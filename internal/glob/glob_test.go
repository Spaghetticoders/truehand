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

func TestEscape(t *testing.T) {
	for _, path := range []string{"docs/**", "a/*.go", "q?", `back\slash`, "plain/file.txt"} {
		pat := Escape(path)
		if !Match(pat, path) {
			t.Errorf("%q does not match its own path %q", pat, path)
		}
	}
	for pat, other := range map[string]string{Escape("docs/**"): "docs/secret/deploy.yml", Escape("a/*.go"): "a/b.go", Escape("q?"): "qx"} {
		if Match(pat, other) {
			t.Errorf("%q matched %q", pat, other)
		}
	}
}

func TestNonASCII(t *testing.T) {
	for pat, path := range map[string]string{"docs/città/**": "docs/città/note.md", "ü/ä": "ü/ä", "lib/?.txt": "lib/é.txt", Escape("città*.md"): "città*.md",
		"assets/🎉/*.png": "assets/🎉/festa.png", "lib/?.dart": "lib/🎉.dart", "lib/src/attività/**": "lib/src/attività/perché_così.dart"} {
		if !Match(pat, path) {
			t.Errorf("%q does not match %q", pat, path)
		}
	}
	if Match("lib/?.txt", "lib/ab.txt") || Match(Escape("città*.md"), "cittàX.md") || Match("lib/?.dart", "lib/🎉🎉.dart") {
		t.Error("non-ASCII pattern matched too much")
	}
}
