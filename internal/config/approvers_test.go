package config_test

import (
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/sign/signtest"
)

func indent(s string) string {
	return "      " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n      ")
}

func withApprovers(entries ...string) string {
	return "version: 0\ngates: {}\napprovers:\n" + strings.Join(entries, "")
}

func approver(id, name, key string) string {
	return "  - id: " + id + "\n    name: " + name + "\n    public_key: |\n" + indent(key) + "\n"
}

func TestApprovers(t *testing.T) {
	_, k1 := signtest.Key(t)
	_, k2 := signtest.Key(t)
	cfg, err := config.Parse([]byte(withApprovers(approver("mac", "Emanuel", k1), approver("phone", "Emanuel phone", k2))))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Approvers) != 2 || cfg.Approver("phone").Name != "Emanuel phone" || cfg.Approver("nope") != nil {
		t.Fatalf("%+v", cfg.Approvers)
	}
	for name, yaml := range map[string]string{
		"duplicate id":   withApprovers(approver("mac", "A", k1), approver("mac", "B", k2)),
		"bad id":         withApprovers(approver("../x", "A", k1)),
		"missing name":   withApprovers(approver("mac", "\"\"", k1)),
		"not a key":      withApprovers(approver("mac", "A", "-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----")),
		"missing key":    withApprovers("  - id: mac\n    name: A\n"),
		"misspelt field": withApprovers("  - id: mac\n    name: A\n    publicKey: x\n"),
	} {
		if _, err := config.Parse([]byte(yaml)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
