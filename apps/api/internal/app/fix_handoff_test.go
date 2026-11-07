package app

import (
	"os"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/py"
)

func fixHandoffAsOMap(h Handoff) *py.OMap {
	opts := []any{}
	for _, o := range h.Options {
		opts = append(opts, o)
	}
	flags := []any{}
	for _, f := range h.Flags {
		flags = append(flags, f)
	}
	return py.NewOMap("options", opts, "prompt", h.Prompt, "state", h.State, "flags", flags, "body", h.Body)
}

// Every case was parsed by app/fix_handoff.py (testdata/fix_handoff_golden.json): the Go parse must give the
// same options (keys, order, values), prompt, state, flags and body.
func TestFixHandoffGolden(t *testing.T) {
	data, err := os.ReadFile("testdata/fix_handoff_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	v, err := py.ParseJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	for i, cv := range py.AsList(v) {
		c := py.AsMap(cv)
		got := py.Dumps(fixHandoffAsOMap(FHParse(c.S("md"), c.S("env"), c.S("fallback"))), 1)
		want := py.Dumps(c.Get("want"), 1)
		if got != want {
			t.Errorf("case %d (%s):\n got %.600s\nwant %.600s", i, c.S("env"), got, want)
		}
	}
}

func TestFixHandoffContract(t *testing.T) {
	data, _ := os.ReadFile("testdata/fix_handoff_golden.json")
	v, _ := py.ParseJSON(data)
	md := py.AsMap(py.AsList(v)[0]).S("md")
	h := FHParse(md, "release", "")
	if h.State != "ok" || len(h.Options) != 3 || h.Options[0].Get("recommended") != true {
		t.Fatalf("contract parse: %+v", h)
	}
	if !strings.HasPrefix(h.Prompt, FHProdLine) || !strings.Contains(h.Prompt, "```sh") {
		t.Fatal("release prompt line / inner fence")
	}
	for _, o := range h.Options {
		if o.Get("approval_kind") != "production" {
			t.Fatal("release options must be production")
		}
	}
	if strings.Contains(h.Body, "How to fix it") || !strings.Contains(h.Body, "## Root cause") {
		t.Fatal("body split")
	}
	if FHParse("", "develop", "").State != "missing" {
		t.Fatal("missing")
	}
}

func TestFixHandoffProductionLine(t *testing.T) {
	p, flags := FHCheckPrompt("# Fix\nstep", "release")
	if !strings.HasPrefix(p, FHProdLine+"\n# Fix") || len(flags) != 1 || flags[0] != "production line added" {
		t.Fatal(p, flags)
	}
	p, flags = FHCheckPrompt("**PRODUCTION CHANGE** old wording\n# Fix", "production")
	if strings.Count(p, "PRODUCTION CHANGE") != 1 || len(flags) != 1 {
		t.Fatal("replaced line", p)
	}
	p, flags = FHCheckPrompt(FHProdLine+"\n# Fix", "release")
	if len(flags) != 0 || strings.Count(p, "PRODUCTION") != 1 {
		t.Fatal("already there")
	}
	if p, _ := FHCheckPrompt("# Fix", "develop"); strings.Contains(p, "PRODUCTION") {
		t.Fatal("develop prompt got the production line")
	}
	p, flags = FHCheckPrompt(strings.Repeat("x", 40000), "develop")
	if py.Len(p) > FHMaxPrompt || flags[len(flags)-1] != "prompt cut" {
		t.Fatal("cap")
	}
}
