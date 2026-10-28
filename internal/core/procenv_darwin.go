package core

import (
	"bytes"
	"encoding/binary"

	"golang.org/x/sys/unix"
)

// processEnv is pid's environment from kern.procargs2: argc, the executable path padded with
// NULs to a multiple of 8 bytes, argc NUL-terminated arguments (possibly empty), then the
// environment up to an empty string. Arguments are counted, not found by skipping NULs: an
// empty argv[0] would otherwise shift the environment by one. Any surprise means unreadable.
func processEnv(pid int) ([]string, bool) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, false
	}
	return parseProcArgs(b)
}

func parseProcArgs(b []byte) ([]string, bool) {
	if len(b) < 4 {
		return nil, false
	}
	argc := int(binary.LittleEndian.Uint32(b[:4]))
	rest := b[4:]
	n := bytes.IndexByte(rest, 0)
	if n < 0 {
		return nil, false
	}
	start := (n + 1 + 7) &^ 7
	if start > len(rest) || len(bytes.Trim(rest[n:start], "\x00")) > 0 {
		return nil, false
	}
	rest = rest[start:]
	var env []string
	for i := 0; i < argc; i++ {
		j := bytes.IndexByte(rest, 0)
		if j < 0 {
			return nil, false
		}
		// An argument that looks like the session variable counts as one: the safe side.
		if bytes.HasPrefix(rest[:j], []byte(SessionEnv+"=")) {
			env = append(env, string(rest[:j]))
		}
		rest = rest[j+1:]
	}
	for len(rest) > 0 {
		j := bytes.IndexByte(rest, 0)
		if j <= 0 {
			break
		}
		env = append(env, string(rest[:j]))
		rest = rest[j+1:]
	}
	return env, true
}
