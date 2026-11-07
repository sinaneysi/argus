package app

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// testdata/lazy_vectors.json is written by app/lazy.py (parse and strip of the same inputs): the Go port must agree.
func TestLazyVectorsMatchPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/lazy_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases map[string]struct {
		In    string             `json:"in"`
		Parse *map[string]string `json:"parse"`
		Strip string             `json:"strip"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for name, c := range cases {
		l, ok := LazyParse(c.In)
		if (c.Parse != nil) != ok {
			t.Errorf("%s: parse ok=%v, python %v", name, ok, c.Parse != nil)
			continue
		}
		if ok {
			want := *c.Parse
			got := map[string]string{"what": l.What, "why": l.Why, "impact": l.Impact, "fix": l.Fix, "status": l.Status}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("%s: %s = %q, python %q", name, k, got[k], v)
				}
			}
		}
		if s := LazyStrip(c.In); s != c.Strip {
			t.Errorf("%s: strip = %q, python %q", name, s, c.Strip)
		}
	}
}

func TestLazyLoad(t *testing.T) {
	l, ok := LazyLoad(`{"what": "a", "fix": "b", "status": "Mitigated", "source": "model", "ref": "rca:7"}`)
	if !ok || l.Status != "mitigated" || l.Source != "model" || l.Ref != "rca:7" {
		t.Fatalf("%+v %v", l, ok)
	}
	if l, _ := LazyLoad(`{"what": "a", "fix": "b", "source": "evil"}`); l.Source != "derived" {
		t.Fatal(l.Source)
	}
	for _, bad := range []any{nil, "", "[]", "{", `{"what": "a"}`, `{"what": 1, "fix": "b"}`, int64(3)} {
		if _, ok := LazyLoad(bad); ok {
			t.Errorf("loaded %v", bad)
		}
	}
}

// testdata/lazy_trim_vectors.json is written by app/lazy.py trim() (tests/test_lazy.py checks it is current): the
// word and character caps, the sentence / clause / word cut and the kept grade must agree to the rune.
func TestLazyTrimVectorsMatchPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/lazy_trim_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ In, Out, How string }
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 10 {
		t.Fatalf("only %d vectors", len(cases))
	}
	for _, c := range cases {
		out, how := lzTrim(c.In)
		if out != c.Out || how != c.How {
			t.Errorf("trim(%.40q) = %q/%s, python %q/%s", c.In, out, how, c.Out, c.How)
		}
		if n := len(strings.Fields(out)); n > lzMaxWords {
			t.Errorf("%d words: %q", n, out)
		}
		if n := len([]rune(out)); n > lzMaxField {
			t.Errorf("%d characters: %q", n, out)
		}
	}
}

func TestLazyLoadTrimsAnOldLongValue(t *testing.T) {
	long := strings.Repeat("word ", 40)
	l, ok := LazyLoad(`{"what": "` + long + `", "fix": "f", "source": "model"}`)
	if !ok || len(strings.Fields(l.What)) > lzMaxWords || !strings.HasSuffix(l.What, "…") {
		t.Fatalf("%q", l.What)
	}
}
