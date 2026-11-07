package app

import (
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// vm_search.py: Find a VM, shared by /inventory, /capacity and the sidebar box.

const VMSearchMaxQ = 100

// VMSearchField is one (label, value) a VM is matched on; Val is a JSON or SQLite value.
type VMSearchField struct {
	Label string
	Val   any
}

func vmsearchE(v any) string {
	if v == nil {
		return ""
	}
	return E(py.Str(v))
}

// VMSearchQuery is query(qs).
func VMSearchQuery(qs py.QS) string {
	s := strings.Join(py.Split(qs.Get("q")), " ")
	return py.Strip(py.Head(s, VMSearchMaxQ))
}

// VMSearchWords is words(q).
func VMSearchWords(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range py.Split(q) {
		w = strings.ToLower(w)
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// VMSearchMatches is matches(fields, ws).
func VMSearchMatches(fields []VMSearchField, ws []string) bool {
	var texts []string
	for _, f := range fields {
		if py.Truthy(f.Val) {
			texts = append(texts, strings.ToLower(py.Str(f.Val)))
		}
	}
	for _, w := range ws {
		ok := false
		for _, t := range texts {
			if strings.Contains(t, w) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// VMSearchRank is rank(ids, q).
func VMSearchRank(ids []any, q string) int {
	set := map[string]bool{}
	for _, i := range ids {
		if py.Truthy(i) {
			set[strings.ToLower(py.Str(i))] = true
		}
	}
	if set[strings.ToLower(q)] {
		return 0
	}
	for _, w := range VMSearchWords(q) {
		if set[w] {
			return 1
		}
	}
	return 2
}

// VMSearchSearch is search(items, q, fields_of, ids_of) over indexes: the matching indexes, exact first, stable.
func VMSearchSearch(n int, q string, fieldsOf func(int) []VMSearchField, idsOf func(int) []any) []int {
	ws := VMSearchWords(q)
	var hits []int
	for i := 0; i < n; i++ {
		if VMSearchMatches(fieldsOf(i), ws) {
			hits = append(hits, i)
		}
	}
	rank := map[int]int{}
	for _, i := range hits {
		rank[i] = VMSearchRank(idsOf(i), q)
	}
	sort.SliceStable(hits, func(a, b int) bool { return rank[hits[a]] < rank[hits[b]] })
	return hits
}

func vmsearchRunes(s []rune, a, b int) string {
	if a < 0 {
		a = 0
	}
	if b > len(s) {
		b = len(s)
	}
	if a >= b {
		return ""
	}
	return string(s[a:b])
}

// VMSearchMark is mark(text, ws): escaped, every occurrence of a word in <mark>.
func VMSearchMark(v any, ws []string) string {
	text := ""
	if v != nil {
		text = py.Str(v)
	}
	tr := []rune(text)
	low := []rune(strings.ToLower(text))
	type span struct{ a, b int }
	var spans []span
	for _, w := range ws {
		wr := []rune(w)
		if len(wr) == 0 {
			continue
		}
		for i := 0; i+len(wr) <= len(low); {
			if string(low[i:i+len(wr)]) == w {
				spans = append(spans, span{i, i + len(wr)})
				i += len(wr)
			} else {
				i++
			}
		}
	}
	if len(spans) == 0 {
		return E(text)
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].a != spans[j].a {
			return spans[i].a < spans[j].a
		}
		return spans[i].b < spans[j].b
	})
	merged := []span{spans[0]}
	for _, s := range spans[1:] {
		last := &merged[len(merged)-1]
		if s.a <= last.b {
			if s.b > last.b {
				last.b = s.b
			}
		} else {
			merged = append(merged, s)
		}
	}
	var b strings.Builder
	pos := 0
	for _, s := range merged {
		b.WriteString(E(vmsearchRunes(tr, pos, s.a)) + "<mark>" + E(vmsearchRunes(tr, s.a, s.b)) + "</mark>")
		pos = s.b
	}
	b.WriteString(E(vmsearchRunes(tr, pos, len(tr))))
	return b.String()
}

// VMSearchWhy is why(fields, ws, shown).
func VMSearchWhy(fields []VMSearchField, ws []string, shown map[string]bool) string {
	var vis []string
	for _, f := range fields {
		if shown[f.Label] && py.Truthy(f.Val) {
			vis = append(vis, strings.ToLower(py.Str(f.Val)))
		}
	}
	v := strings.Join(vis, " ")
	var out []VMSearchField
	for _, w := range ws {
		if strings.Contains(v, w) {
			continue
		}
		for _, f := range fields {
			if !shown[f.Label] && py.Truthy(f.Val) && strings.Contains(strings.ToLower(py.Str(f.Val)), w) {
				dup := false
				for _, o := range out {
					if o.Label == f.Label && vmsearchEq(o.Val, f.Val) {
						dup = true
					}
				}
				if !dup {
					out = append(out, f)
				}
				break
			}
		}
	}
	var parts []string
	for i, f := range out {
		if i >= 2 {
			break
		}
		parts = append(parts, vmsearchE(f.Label)+" "+VMSearchMark(f.Val, ws))
	}
	return strings.Join(parts, " · ")
}

func vmsearchEq(a, b any) bool {
	if py.IsNumber(a) && py.IsNumber(b) {
		return py.Num(a) == py.Num(b)
	}
	return a == b
}
