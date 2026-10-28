package cli

import (
	"context"
	"testing"
)

// The OS host name is only this Mac's name in the tailnet when MagicDNS gave it that name.
// When the tailnet renamed this Mac (mac-1), "mac" belongs to another
// node, and accepting it as a Host would let that node's name reach this server.
func TestReviewMagicDNSNamesOnlyOwnNames(t *testing.T) {
	orig := tailscaleStatus
	t.Cleanup(func() { tailscaleStatus = orig })
	tailscaleStatus = func(context.Context) ([]byte, error) {
		return []byte(`{"Self":{"DNSName":"mac-1.example-tail.ts.net.","HostName":"Mac","TailscaleIPs":["100.101.102.103"]}}`), nil
	}
	for _, n := range magicDNSNames() {
		if n == "mac" {
			t.Fatalf("accepted the OS host name, which in this tailnet is another node's: %v", magicDNSNames())
		}
	}
}
