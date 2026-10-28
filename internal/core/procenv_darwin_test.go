package core

import (
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
)

func procArgs(argc int, path string, pad int, args []string, env []string) []byte {
	b := binary.LittleEndian.AppendUint32(nil, uint32(argc))
	b = append(b, path...)
	b = append(b, make([]byte, 1+pad)...)
	for _, a := range append(append(args, env...), "") {
		b = append(append(b, a...), 0)
	}
	return append(b, make([]byte, 16)...)
}

func TestParseProcArgs(t *testing.T) {
	path := "/var/folders/x/core.test" // 24 bytes: +1 NUL, padded with 7 to 32
	env := []string{"IDLE=1", SessionEnv + "=s:x@127.0.0.1:1"}
	for name, c := range map[string]struct {
		b    []byte
		want []string
		ok   bool
	}{
		"plain":                  {procArgs(2, path, 7, []string{path, "-x"}, env), env, true},
		"empty argv0":            {procArgs(2, path, 7, []string{"", "-x"}, env), env, true},
		"empty arguments":        {procArgs(4, path, 7, []string{path, "", "", "-x"}, env), env, true},
		"argument A=b":           {procArgs(2, path, 7, []string{path, "A=b"}, env), env, true},
		"no padding":             {procArgs(1, strings.Repeat("p", 15), 0, []string{"p"}, env), env, true},
		"session in an argument": {procArgs(2, path, 7, []string{path, SessionEnv + "=x"}, []string{"A=1"}), []string{SessionEnv + "=x", "A=1"}, true},
		"hidden env":             {procArgs(1, path, 7, []string{path}, nil), nil, true},
		"short padding":          {procArgs(1, path, 2, []string{path}, env), nil, false},
		"truncated":              {procArgs(3, path, 7, []string{path}, nil)[:40], nil, false},
		"no path end":            {append(binary.LittleEndian.AppendUint32(nil, 1), "abc"...), nil, false},
		"too short":              {[]byte{1, 0}, nil, false},
	} {
		got, ok := parseProcArgs(c.b)
		if ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q %v, want %q %v", name, got, ok, c.want, c.ok)
		}
	}
}
