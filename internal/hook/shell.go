package hook

import "strings"

// segments splits a shell command line into simple commands at ;, &&, ||, |, & and newlines,
// and each simple command into words. It understands quotes and backslashes but not
// subshells or expansions: hooks are a soft lock, CI is the hard one.
func segments(line string) [][]string {
	var segs [][]string
	var words []string
	var cur strings.Builder
	inWord := false
	flushWord := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	flushSeg := func() {
		flushWord()
		if len(words) > 0 {
			segs = append(segs, words)
			words = nil
		}
	}
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' && i+1 < len(line) {
				i++
				cur.WriteByte(line[i])
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
			inWord = true
		case c == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
			inWord = true
		case c == ';' || c == '\n' || c == '|' || c == '&' || c == '(' || c == ')':
			flushSeg()
		case c == ' ' || c == '\t':
			flushWord()
		case c == '>' || c == '<':
			flushWord()
			words = append(words, string(c))
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	flushSeg()
	return segs
}

// stripEnv removes leading VAR=value assignments and common wrappers.
func stripEnv(words []string) []string {
	for len(words) > 0 {
		w := words[0]
		switch {
		case strings.Contains(w, "=") && !strings.HasPrefix(w, "-") && !strings.Contains(w, "/"):
			words = words[1:]
		case w == "env" || w == "command" || w == "exec" || w == "nohup" || w == "time" || w == "sudo":
			words = words[1:]
		default:
			return words
		}
	}
	return words
}

// gitCommand returns the git subcommand, its arguments and the global options before it.
func gitCommand(words []string) (sub string, args, global []string) {
	if len(words) == 0 || (words[0] != "git" && !strings.HasSuffix(words[0], "/git")) {
		return "", nil, nil
	}
	for i := 1; i < len(words); i++ {
		w := words[i]
		switch {
		case w == "-C" || w == "-c" || w == "--git-dir" || w == "--work-tree" || w == "--namespace":
			global = append(global, w)
			if i+1 < len(words) {
				global = append(global, words[i+1])
			}
			i++
		case strings.HasPrefix(w, "-"):
			global = append(global, w)
		default:
			return w, words[i+1:], global
		}
	}
	return "", nil, global
}

func isTruehand(word string) bool {
	return word == "truehand" || strings.HasSuffix(word, "/truehand")
}
