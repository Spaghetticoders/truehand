package core

import (
	"os"
	"strconv"
	"strings"
)

func processEnv(pid int) ([]string, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return nil, false
	}
	var env []string
	for _, kv := range strings.Split(string(b), "\x00") {
		if kv != "" {
			env = append(env, kv)
		}
	}
	return env, true
}
