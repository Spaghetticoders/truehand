package cli

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/appstate"
)

func TestMagicDNSNames(t *testing.T) {
	old := tailscaleStatus
	defer func() { tailscaleStatus = old }()
	tailscaleStatus = func(context.Context) ([]byte, error) {
		return []byte(`{"Self":{"DNSName":"mac.example-tail.ts.net.","HostName":"Dev-MBP","TailscaleIPs":["fd7a::1","100.64.0.1"]}}`), nil
	}
	if got := strings.Join(magicDNSNames(), " "); got != "mac.example-tail.ts.net mac" {
		t.Fatalf("names: %q", got)
	}
	if ip := tailnetIP(); ip != "100.64.0.1" {
		t.Fatalf("tailnet IPv4: %q", ip)
	}
	tailscaleStatus = func(context.Context) ([]byte, error) { return nil, errors.New("not running") }
	if len(magicDNSNames()) != 0 {
		t.Fatal("no tailscale, no names")
	}
	ra := &remoteAccess{port: "7878"}
	if hosts, err := ra.apply(appstate.Remote{Mode: "tailscale-serve", Addr: "mac.example-tail.ts.net"}); err != nil || strings.Join(hosts, " ") != "mac.example-tail.ts.net" {
		t.Fatalf("tailscale serve: %v %v", hosts, err)
	}
	if _, err := ra.apply(appstate.Remote{Mode: "tailnet", Addr: "0.0.0.0"}); err == nil {
		t.Fatal("all interfaces")
	}
	if hosts, err := ra.apply(appstate.Remote{Mode: "off"}); err != nil || len(hosts) != 0 {
		t.Fatalf("off: %v %v", hosts, err)
	}
}

func TestTailnetIPFromInterfaces(t *testing.T) {
	addrs := func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
			&net.IPNet{IP: net.ParseIP("192.168.1.20"), Mask: net.CIDRMask(24, 32)},
			&net.IPNet{IP: net.ParseIP("100.64.0.1"), Mask: net.CIDRMask(32, 32)},
		}, nil
	}
	if ip := interfaceTailnetIP(addrs); ip != "100.64.0.1" {
		t.Fatalf("got %q", ip)
	}
}

func TestCompletePath(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	completePath()
	p := os.Getenv("PATH")
	if !strings.HasPrefix(p, "/usr/bin:/bin") {
		t.Fatalf("the existing PATH comes first: %q", p)
	}
	for _, d := range toolDirs() {
		if _, err := os.Stat(d); err == nil && !strings.Contains(p, d) {
			t.Errorf("%s missing from %q", d, p)
		}
	}
	completePath()
	if os.Getenv("PATH") != p {
		t.Fatal("adding twice")
	}
}
