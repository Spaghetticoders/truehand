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
	cfg, err := config.Parse([]byte(withApprovers(approver("mac", "Alice", k1), approver("phone", "Alice phone", k2))))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Approvers) != 2 || cfg.Approver("phone").Name != "Alice phone" || cfg.Approver("nope") != nil {
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

func TestApproverRSAAndRPID(t *testing.T) {
	_, ec := signtest.Key(t)
	_, rsaKey := signtest.RSAKey(t, 2048)
	_, weak := signtest.RSAKey(t, 1024)
	withRP := func(key, rp string) string {
		return withApprovers(approver("phone", "Alice", key) + "    rp_id: " + rp + "\n")
	}
	cfg, err := config.Parse([]byte(withRP(rsaKey, "host.tail1234.ts.net")))
	if err != nil {
		t.Fatalf("RSA-2048 passkey rejected: %v", err)
	}
	if cfg.Approver("phone").RPID != "host.tail1234.ts.net" {
		t.Fatal(cfg.Approver("phone"))
	}
	for name, yaml := range map[string]string{
		"RSA-1024":       withRP(weak, "host.ts.net"),
		"rp_id 443":      withRP(ec, "host.ts.net:443"),
		"rp_id port":     withRP(ec, "host.ts.net:8443"),
		"rp_id scheme":   withRP(ec, "https://host.ts.net"),
		"rp_id upper":    withRP(ec, "Host.ts.net"),
		"rp_id path":     withRP(ec, "host.ts.net/x"),
		"rp_id bad port": withRP(ec, "host.ts.net:08443"),
	} {
		if _, err := config.Parse([]byte(yaml)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
