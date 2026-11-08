package app

// Settings -> Writing style as JSON: admins only, the route's policy and limit, a save writes the request the worker
// applies, an oversize or binary text is refused with the field named, the page shows the default or the live copy.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/style"
)

func TestAPIv1WritingStylePolicy(t *testing.T) {
	settingsTwinChecks(t, map[string]string{"/api/v1/admin/writing-style": "/admin/writing-style"}, map[string]any{"action": "reset"}, false)
}

func TestAPIv1WritingStyle(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	if r := e.req("GET", "/api/v1/settings/writing-style", nil, nil, nil); r.status != 401 {
		t.Fatal(r.status)
	}
	if r := e.req("GET", "/api/v1/settings/writing-style", map[string]string{auth.COOKIE: vw}, nil, nil); r.status != 403 {
		t.Fatal(r.status)
	}
	ck := map[string]string{auth.COOKIE: adm}
	d := v1JSON(t, e.req("GET", "/api/v1/settings/writing-style", ck, nil, nil))
	if d["text"] != style.Default() || d["custom"] != false || d["max"] != float64(style.MaxBytes) || !strings.Contains(d["line"].(string), "default") {
		t.Fatal(d)
	}
	for _, b := range []map[string]any{
		{"action": "save", "text": strings.Repeat("a", style.MaxBytes+1)},
		{"action": "save", "text": "bad\x01text"},
		{"action": "save", "text": "  \n "},
	} {
		r := e.v1Post("/api/v1/admin/writing-style", ck, b, nil)
		if r.status != 400 || v1JSON(t, r)["field"] != "text" {
			t.Fatal(r.status, r.body)
		}
	}
	if _, err := os.Stat(styleRequestPath()); err == nil {
		t.Fatal("a refused save wrote a request")
	}
	r := e.v1Post("/api/v1/admin/writing-style", ck, map[string]any{"action": "save", "text": "Write short.\r\nOne idea per sentence."}, nil)
	if r.status != 200 || v1JSON(t, r)["ok"] != true {
		t.Fatal(r.status, r.body)
	}
	var req map[string]any
	b, _ := os.ReadFile(styleRequestPath())
	if json.Unmarshal(b, &req) != nil || req["text"] != "Write short.\nOne idea per sentence.\n" || req["reset"] != false || req["updated_by"] != "admin" {
		t.Fatal(string(b))
	}
	// waiting for the applier
	d = v1JSON(t, e.req("GET", "/api/v1/settings/writing-style", ck, nil, nil))
	if n := d["notices"].([]any); len(n) != 1 || !strings.Contains(n[0].(map[string]any)["text"].(string), "Sent.") || d["refresh"] == nil {
		t.Fatal(d)
	}
	// applied: the live copy is what the page shows
	os.WriteFile(filepath.Join(SET_OUT, style.FileName), []byte(req["text"].(string)), 0o644)
	os.WriteFile(filepath.Join(SET_OUT, "writing-style-applied.json"), []byte(`{"ok":true,"by":"admin","custom":true,"at":1800000000,"request_at":`+
		I2S(int64(req["updated_at"].(float64)))+`}`), 0o644)
	d = v1JSON(t, e.req("GET", "/api/v1/settings/writing-style", ck, nil, nil))
	if d["text"] != "Write short.\nOne idea per sentence.\n" || d["custom"] != true || len(d["notices"].([]any)) != 0 || !strings.Contains(d["line"].(string), "saved by admin") {
		t.Fatal(d)
	}
	// saving the default text is a reset
	if r := e.v1Post("/api/v1/admin/writing-style", ck, map[string]any{"action": "save", "text": style.Default()}, nil); r.status != 200 {
		t.Fatal(r.body)
	}
	b, _ = os.ReadFile(styleRequestPath())
	if json.Unmarshal(b, &req) != nil || req["reset"] != true || req["text"] != "" {
		t.Fatal(string(b))
	}
}
