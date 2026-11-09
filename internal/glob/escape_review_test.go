package glob

import "testing"

// Whatever a file is named, its escaped form matches it and nothing else.
func TestReviewEscapeMatchesOnlyItself(t *testing.T) {
	names := []string{"docs/**", "**", "a*", "a?b", `a\b`, `a\*`, "[x].png", "a{b,c}", "a.b", "a+b", "(x)", "a$b", "^a", "a|b", "a/b/", "x y", "ü/ä"}
	others := []string{"docs/x", "docs/secret/deploy.yml", "abc", "a", "aXb", "ab", "x.png", "ab.c", "aab", "x", "a/b", "b"}
	for _, n := range names {
		p := Escape(n)
		if !Match(p, n) {
			t.Errorf("Escape(%q) = %q does not match itself", n, p)
		}
		for _, o := range append(others, names...) {
			if o != n && Match(p, o) {
				t.Errorf("Escape(%q) = %q also matches %q", n, p, o)
			}
		}
	}
}
