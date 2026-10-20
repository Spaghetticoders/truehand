// Package glob matches slash-separated paths against patterns with ** support.
package glob

import (
	"regexp"
	"strings"
	"sync"
)

var cache sync.Map

// Match reports whether path matches pattern. "**" matches any number of path segments,
// "*" anything except "/", "?" one non-"/" character.
func Match(pattern, path string) bool {
	re, ok := cache.Load(pattern)
	if !ok {
		re = regexp.MustCompile(toRegexp(pattern))
		cache.Store(pattern, re)
	}
	return re.(*regexp.Regexp).MatchString(path)
}

func MatchAny(patterns []string, path string) bool {
	for _, p := range patterns {
		if Match(p, path) {
			return true
		}
	}
	return false
}

func toRegexp(p string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '*' && i+1 < len(p) && p[i+1] == '*':
			i++
			if i+1 < len(p) && p[i+1] == '/' {
				i++
				b.WriteString("(?:.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return b.String()
}
