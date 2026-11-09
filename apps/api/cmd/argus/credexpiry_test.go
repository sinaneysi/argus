package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/credexpiry"
	"github.com/sinaneysi/argus/internal/migrate"
)

// "Check now" of Settings -> Credentials in an empty volume: the worker consumes the request, writes credentials.json
// (Argus's own credentials, none set) and the run state; the hourly credcheck writes cred-status.json.
func TestVolumeCredJobs(t *testing.T) {
	vol := t.TempDir()
	env := filepath.Join(vol, "argus.env")
	os.WriteFile(env, []byte("COMPANY_NAME=Example\nARGUS_PUBLIC_URL=https://argus.example.com\nARGUS_LAYOUT=volume\nARGUS_STATE_DIR="+vol+"\n"), 0o600)
	c, err := config.Load(config.Options{File: env, Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.EnsureDirs(c); err != nil {
		t.Fatal(err)
	}
	if err := migrate.OnStart(context.Background(), c, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	req := filepath.Join(c.SettingsIn, "requests", "cred-expiry.req")
	os.MkdirAll(filepath.Dir(req), 0o755)
	os.WriteFile(req, nil, 0o644)
	if err := credRequestJob(c, io.Discard)(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(req); err == nil {
		t.Error("request not consumed")
	}
	d, err := credexpiry.Load(credExpiryFile(c))
	if err != nil || d.CheckedAt == 0 || len(d.Items) == 0 || d.Worst != "OK" {
		t.Fatalf("credentials.json: %+v %v", d, err)
	}
	for _, it := range d.Items {
		if it.Status != "absent" {
			t.Errorf("%s: %s", it.ID, it.Status)
		}
	}
	var rr map[string]map[string]map[string]any
	b, _ := os.ReadFile(filepath.Join(c.SettingsOut, "run-requests.json"))
	if json.Unmarshal(b, &rr) != nil || rr["tasks"]["cred-expiry"]["result"] != "ok" {
		t.Errorf("run-requests.json: %s", b)
	}
	if err := credcheckRun(c); err != nil {
		t.Fatal(err)
	}
	var cs struct {
		Items []struct {
			Name string
			OK   bool
		}
	}
	b, _ = os.ReadFile(c.Values["ARGUS_CRED_STATUS_FILE"].Raw)
	if json.Unmarshal(b, &cs) != nil || len(cs.Items) != 2 || cs.Items[0].Name != "Claude login" || cs.Items[0].OK {
		t.Errorf("cred-status.json: %s", b)
	}
}
