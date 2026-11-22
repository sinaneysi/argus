package cicd

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"testing"
)

// testdata/parity.json is the earlier installation's output (its Python triage, CI parser, inventory parser and lock
// rule) on synthetic inputs. The Go port must give the same classes, rules, key lines, signatures, excerpts and
// proposal actions. Listed difference: the "why" and proposal texts are neutral (no installation names).
type parity struct {
	Triage []struct {
		Name  string
		Job   map[string]any
		Trace string
		Want  map[string]any
	}
	CI struct {
		Text    string
		Incs    [][]string
		Target  string
		KindPkg string `json:"kind_pkg"`
	}
	Inventory struct {
		Text   string
		Groups map[string][]string
		IPs    map[string]string
		Hosts  []string
		Group  string
	}
	Locks struct {
		Waiting map[string]any
		Cases   map[string][]map[string]any
		Want    map[string]struct {
			Holder int64
			Ahead  []int64
			Props  [][]any
		}
	}
}

func loadParity(t *testing.T) parity {
	t.Helper()
	b, err := os.ReadFile("testdata/parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var p parity
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func testWatcher() *Watcher { return &Watcher{Opt: Options{DeployBridge: "trigger_deploy"}} }

func propsOf(ps []Proposal) [][]any {
	out := [][]any{}
	for _, p := range ps {
		var tg any = p.Target
		switch x := tg.(type) {
		case int64:
			tg = float64(x)
		}
		out = append(out, []any{p.Action, p.Allowed, p.Path, tg})
	}
	return out
}

func TestTriageParity(t *testing.T) {
	p := loadParity(t)
	w := testWatcher()
	for _, c := range p.Triage {
		t.Run(c.Name, func(t *testing.T) {
			j := jobOf(c.Job)
			got := Classify(c.Trace, j, "grp/app")
			want := c.Want
			if got.Klass != want["klass"] || got.Rule != want["rule"] || got.Line != want["line"] || got.Signature != want["signature"] {
				t.Fatalf("got %+v\nwant %v", got, want)
			}
			if c.Trace != "" && Excerpt(c.Trace, got.Line) != want["excerpt"] {
				t.Fatalf("excerpt %q, want %q", Excerpt(c.Trace, got.Line), want["excerpt"])
			}
			for _, env := range []string{"develop", "release"} {
				props := w.Proposals(got, F{"env": env, "project_id": int64(5), "job_id": int64(9), "job_name": j.Name, "ref": env, "author": "Dev"})
				g, _ := json.Marshal(propsOf(props))
				wb, _ := json.Marshal(want["props_"+env])
				if string(g) != string(wb) {
					t.Fatalf("%s proposals %s, want %s", env, g, wb)
				}
			}
		})
	}
}

func TestParseParity(t *testing.T) {
	p := loadParity(t)
	incs, target := ParseCI(p.CI.Text, regexp.MustCompile(`project:\s*['"]?grp/engine['"]?`))
	var got [][]string
	for _, i := range incs {
		got = append(got, []string{i.Ref, i.File})
	}
	if !reflect.DeepEqual(got, p.CI.Incs) || target != p.CI.Target || KindOf([]string{"cicd/package.gitlab-ci.yml"}) != p.CI.KindPkg {
		t.Fatalf("ParseCI %v %q", got, target)
	}
	if inc, _ := ParseCI("include:\n  project: other/template\n", regexp.MustCompile(`project:\s*['"]?grp/engine['"]?`)); len(inc) != 0 {
		t.Fatal("an include of another project counted")
	}
	inv := ParseInventory(p.Inventory.Text)
	if !reflect.DeepEqual(inv.Groups, p.Inventory.Groups) || !reflect.DeepEqual(inv.IPs, p.Inventory.IPs) {
		t.Fatalf("inventory %v %v", inv.Groups, inv.IPs)
	}
	hosts := RecapHosts("PLAY RECAP ****\napp-01 : ok=3 changed=1\napp-02             : ok=3\n")
	if !reflect.DeepEqual(hosts, p.Inventory.Hosts) || TargetGroup(hosts, inv) != p.Inventory.Group {
		t.Fatalf("recap %v group %q", hosts, TargetGroup(hosts, inv))
	}
}

func TestLockParity(t *testing.T) {
	p := loadParity(t)
	w := testWatcher()
	wt := Pipe{ID: toI(p.Locks.Waiting["id"]), Ref: str(p.Locks.Waiting["ref"]), Status: str(p.Locks.Waiting["status"]), ProjectID: toI(p.Locks.Waiting["project_id"])}
	for name, others := range p.Locks.Cases {
		var os []Pipe
		for _, o := range others {
			os = append(os, Pipe{ID: toI(o["id"]), Ref: str(o["ref"]), Status: str(o["status"]), BridgeStatus: str(o["bridge_status"]), ProdDeploy: str(o["prod_deploy"])})
		}
		plan := w.PlanLock(wt, os, "g/p")
		want := p.Locks.Want[name]
		var holder int64
		if plan.Holder != nil {
			holder = plan.Holder.ID
		}
		var ahead []int64
		for _, a := range plan.Ahead {
			ahead = append(ahead, a.ID)
		}
		var props [][]any
		for _, x := range plan.Proposals {
			props = append(props, []any{x.Action, x.Allowed, x.Path})
		}
		g, _ := json.Marshal(props)
		wb, _ := json.Marshal(want.Props)
		if holder != want.Holder || fmt.Sprint(ahead) != fmt.Sprint(append([]int64(nil), want.Ahead...)) || string(g) != string(wb) {
			t.Fatalf("%s: holder %d ahead %v props %s; want %d %v %s", name, holder, ahead, g, want.Holder, want.Ahead, wb)
		}
	}
}

func TestTS(t *testing.T) {
	if TS("2026-10-20T10:00:00.000Z") != TS("2026-10-20T13:30:00.000+03:30") || TS("garbage") != 0 {
		t.Fatal("TS")
	}
}

func TestRedact(t *testing.T) {
	in := "\x1b[31mexport API_SECRET=abc123def\x1b[0m\nTARGET_HOSTS=main-shop\nsection_start:1:build\nkey Ab3dEf9hIj2kLm5nOp8qRs1tUv4wXy7zAb0cDe6f\n"
	got := Redact(in)
	for _, bad := range []string{"abc123def", "\x1b[", "section_start", "Ab3dEf9hIj2kLm5nOp8qRs1tUv4wXy7z"} {
		if regexp.MustCompile(regexp.QuoteMeta(bad)).MatchString(got) {
			t.Fatalf("%q left in %q", bad, got)
		}
	}
	if !regexp.MustCompile(`TARGET_HOSTS=main-shop`).MatchString(got) {
		t.Fatalf("routing key dropped: %q", got)
	}
}
