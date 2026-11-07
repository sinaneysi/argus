package access

import (
	"strings"
	"testing"
)

func TestCommands(t *testing.T) {
	tp := Config{Broker: "teleport", TeleportProxy: "teleport.example.com:443", SSHUser: "ops"}
	if got := tp.SSH("web-01"); len(got) != 2 || got[0].Cmd != "tsh login --proxy=teleport.example.com:443" || got[1].Cmd != "tsh ssh ops@web-01" {
		t.Fatal(got)
	}
	if got := tp.Kube("prod"); !strings.HasPrefix(got[1].Cmd, "tsh kube login prod") {
		t.Fatal(got)
	}
	bd := Config{Broker: "boundary", BoundaryAddr: "https://boundary.example.com", BoundaryScope: "ops"}
	if got := bd.SSH("web-01"); got[1].Cmd != "boundary connect ssh -target-name web-01 -target-scope-name ops" {
		t.Fatal(got)
	}
	if got := bd.Kube("prod"); !strings.Contains(got[1].Cmd, "connect kube -target-name prod") {
		t.Fatal(got)
	}
}

func TestShowsNothingWhenUnsafeOrUnset(t *testing.T) {
	tp := Config{Broker: "teleport", TeleportProxy: "teleport.example.com:443", SSHUser: "ops"}
	for _, bad := range []string{"", "a b", "x;rm -rf /", "$(id)", "-oProxyCommand=x", "a\nb"} {
		if got := tp.SSH(bad); got != nil {
			t.Fatalf("%q: %v", bad, got)
		}
	}
	for _, c := range []Config{{}, {Broker: "teleport"}, {Broker: "boundary", BoundaryAddr: "http://x", BoundaryScope: "s"}, {Broker: "other"}} {
		if c.SSH("web-01") != nil || c.Kube("prod") != nil {
			t.Fatalf("%+v shows commands", c)
		}
	}
	if (Config{Broker: "other"}).Label() != "" || tp.Label() != "Teleport" {
		t.Fatal("label")
	}
}
