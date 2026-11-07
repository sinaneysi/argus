package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sinaneysi/argus/internal/py"
)

func capacityMap(t *testing.T, s string) *py.OMap {
	t.Helper()
	v, err := py.ParseJSON([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return py.AsMap(v)
}

func TestCapacityGrowthAndDays(t *testing.T) {
	cases := map[string]string{
		`{"growth_per_day": null}`:           `<span class="dim" title="not growing over the last 7 days">—</span>`,
		`{"loss_per_day": null, "avail": 5}`: `<span class="dim" title="not growing over the last 7 days">—</span>`,
		`{"loss_per_day": null}`:             "-",
		`{"growth_per_day": -5000}`:          `<span class="dim">shrinking</span>`,
		`{"growth_per_day": 0.5}`:            `<span class="dim">—</span>`,
		`{"growth_per_day": 1000}`:           "+&lt; 1 MB",
		`{"growth_per_day": 5242880}`:        "+5 MB",
		`{"growth_per_day": 2147483648}`:     "+2.0 GB",
		`{"loss_per_day": 3221225472000}`:    "+3,000.0 GB",
	}
	for in, want := range cases {
		if got := CapacityGrowth(capacityMap(t, in)); got != want {
			t.Errorf("%s: %q != %q", in, got, want)
		}
	}
	for in, want := range map[any]string{nil: "—", 0.5: "< 1 d", 12.0: "12 d", 12.25: "12.25 d", 400.0: "> 1 year"} {
		if got := CapacityFmtDays(in); got != want {
			t.Errorf("%v: %q", in, got)
		}
	}
	// same id as bin/disk_explain.py: sha1(dc|host|mount)[:12]
	if id := CapacityFSID(capacityMap(t, `{"dc": "dc1", "host": "host1", "mount": "/var"}`)); id != "d72d55b2f098" {
		t.Fatal(id)
	}
}

func TestCapacityPageAndAnalyse(t *testing.T) {
	e := inventorySetup(t)
	e.exec(t, `CREATE TABLE disk_reports (id INTEGER PRIMARY KEY, fs_id TEXT, created INTEGER, requested_by TEXT, run_id INTEGER, status TEXT)`)
	e.exec(t, `CREATE TABLE triage_problems (run_id INTEGER, idx INTEGER, title TEXT)`)
	e.write(t, "capacity.json", `{"generated_at": 1, "summary": {"vms": 2, "filesystems": 1, "by_level": {"warn": 1, "ok": 1}},
	  "filesystems": [{"dc": "dc1", "host": "host1", "mount": "/var", "level": "warn", "free_pct": 20, "size": 1073741824, "days": 9,
	    "growth_per_day": 10485760, "vm_name": "<b>vm</b>"}],
	  "vms": [{"name": "web-1", "level": "warn", "monitored": true, "env": "release", "dc": "dc1", "cpu_p95": 95.5, "mem_p95": 10,
	    "disks": [{"mount": "/var", "used_pct": 80, "level": "warn", "days": 9}], "filesystems": [{"mount": "/var", "growth_per_day": 10485760, "days": 9}]},
	    {"name": "db-1", "level": "unmonitored", "why": ["no node_exporter"]}]}`)
	c := inventoryCtx(inventoryAdmin(), "/capacity")

	// filters: vlevel narrows All VMs

	viewer := inventoryCtx(inventoryViewer(), "/capacity")

	if code, _ := CapacityPostAnalyse(viewer, py.NewQS("fs", "d72d55b2f098"), inventoryViewer()); code != 403 {
		t.Fatal(code)
	}
	if code, _ := CapacityPostAnalyse(c, py.NewQS("fs", "../../x"), inventoryAdmin()); code != 400 {
		t.Fatal(code)
	}
	if code, _ := CapacityPostAnalyse(c, py.NewQS("fs", "000000000000"), inventoryAdmin()); code != 404 {
		t.Fatal(code)
	}
	code, loc := CapacityPostAnalyse(c, py.NewQS("fs", "d72d55b2f098"), inventoryAdmin())
	if code != 303 || loc != "/capacity?sent=analyse" {
		t.Fatal(code, loc)
	}
	if _, err := os.Stat(filepath.Join(SET_IN, "requests", "disk-explain-d72d55b2f098.req")); err != nil {
		t.Fatal(err)
	}
	if code, _ := CapacityPostAnalyse(c, py.NewQS("fs", "d72d55b2f098"), inventoryAdmin()); code != 429 {
		t.Fatal("queued twice", code)
	}
}
