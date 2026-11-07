package app

import (
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestSourcesFindAPI(t *testing.T) {
	e, ck, _ := srcSetup(t)
	const api = "/api/v1/sources/find"
	if code, m := apiReq(t, e, "GET", api+"?q=cart", nil, "", nil); code != 401 || m["error"] != "login required" {
		t.Fatalf("anonymous: %d %v", code, m)
	}
	vtok, _ := e.login("view.er")
	if code, m := apiReq(t, e, "GET", api+"?q=cart", map[string]string{"opsr_session": vtok}, "", nil); code != 403 || m["error"] != "admins only" {
		t.Fatalf("viewer: %d %v", code, m)
	}
	for _, q := range []string{"", "?q=", "?q=%01x", "?q=cart&type=ftp", "?q=" + strings.Repeat("a", 501)} {
		if code, m := apiReq(t, e, "GET", api+q, ck, "", nil); code != 400 || m["error"] == "" {
			t.Fatalf("bad %q: %d %v", q, code, m)
		}
	}
	if code, _ := apiReq(t, e, "POST", api, ck, `{}`, nil); code != 405 {
		t.Fatalf("POST: %d", code)
	}
	// no catalogue yet: routing still answers, nothing is found
	if code, m := apiReq(t, e, "GET", api+"?q=cart", ck, "", nil); code != 200 || len(m["services"].([]any)) != 0 {
		t.Fatalf("empty: %d %v", code, m)
	}
	catDemoDB(t)
	srcStatusFile(t, `{"at": 1700000000, "sources": {
 "kubernetes/stage": {"type": "kubernetes", "name": "stage", "env": "develop", "purpose": "agent", "status": "readonly", "enabled": true},
 "gitlab/main": {"type": "gitlab", "name": "main", "env": "shared", "url": "https://git.northwind.example", "purpose": "agent",
   "status": "readonly", "enabled": true}}}`)
	q := url.QueryEscape("why is the cart service slow on production")
	code, m := apiReq(t, e, "GET", api+"?q="+q+"&type=kube&type=gitlab,loki", ck, "", nil)
	if code != 200 || m["release_question"] != true || m["note"] != "fan-out runs in the agent" {
		t.Fatalf("find: %d %v", code, m)
	}
	svc := m["services"].([]any)
	if len(svc) != 1 || svc[0].(map[string]any)["service"] != "shop/cart" {
		t.Fatalf("service: %v", svc)
	}
	types := svc[0].(map[string]any)["types"].(map[string]any)
	if len(types) != 3 {
		t.Fatalf("types: %v", types)
	}
	k8s := types["k8s"].(map[string]any)
	if k8s["decision"] != "several" || k8s["fanout"] != nil || !strings.Contains(mustJSONString(k8s), "argus tools kube stage") {
		t.Fatalf("k8s: %v", k8s)
	}
	for _, typ := range []string{"gitlab", "loki"} {
		d := types[typ].(map[string]any)
		if d["fanout"] != nil {
			t.Fatalf("%s fanned out in the web process: %v", typ, d)
		}
		if d["decision"] == "none" && !strings.Contains(d["note"].(string), "fan-out runs in the agent") {
			t.Fatalf("%s: %v", typ, d)
		}
	}
	if _, err := os.Stat(env("ARGUS_SOURCES_FIND_AUDIT_LOG", "")); err == nil {
		t.Fatal("the web process wrote the find audit log")
	}
}

func mustJSONString(v any) string { return string(mustJSON(v)) }
