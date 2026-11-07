package app

// Queue-page state for in-memory list pages (Incidents, Alerts): GET parameters in a fixed order, defaults left out of
// URLs, every filter change back to page 1, and the toolbar parts of DESIGN.md "Queue page": the search form and hidden
// inputs from queue.go, facet rows from facetRow/proposalChip, the result line with sort, the pager with a noun and a
// per-list page size. No JavaScript; the server filters, sorts and pages.

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sinaneysi/argus/internal/py"
)

const (
	listPageSize = 25
	listMaxQ     = 100 // search box, characters
	listMaxWords = 6
)

// listQ is one list page's GET state. keys fixes the URL order; def holds the values left out of URLs.
type listQ struct {
	path   string
	keys   []string
	def    map[string]string
	v      map[string]string
	page   int
	size   int      // rows per page; 0 is listPageSize
	anchor string   // the list's id on a page with other sections ("vms"): every link and the search form land on it
	tabs   []string // keys that pick a view tab ("view"): not a filter, kept by "Clear all filters"
}

func (l listQ) per() int {
	if l.size > 0 {
		return l.size
	}
	return listPageSize
}

// newListQ reads keys from qs; allowed(key, value) validates each one (an invalid value falls back to the default).
func newListQ(path string, qs py.QS, keys []string, def map[string]string, allowed func(k, v string) bool) listQ {
	l := listQ{path: path, keys: keys, def: def, v: map[string]string{}, page: 1}
	for _, k := range keys {
		val := qs.Get(k)
		if k == "q" {
			val = strings.Join(strings.Fields(val), " ")
			if utf8.RuneCountInString(val) > listMaxQ {
				val = string([]rune(val)[:listMaxQ])
			}
		} else if val != "" && !allowed(k, val) {
			val = ""
		}
		if val == "" {
			val = def[k]
		}
		l.v[k] = val
	}
	if n, err := strconv.Atoi(qs.Get("page")); err == nil && n > 1 && n < 100000 {
		l.page = n
	}
	return l
}

func (l listQ) get(k string) string { return l.v[k] }

// words is the search split into at most listMaxWords lower-case words (ANDed).
func (l listQ) words() []string {
	var out []string
	for _, w := range strings.Fields(strings.ToLower(l.v["q"])) {
		if len(out) < listMaxWords {
			out = append(out, w)
		}
	}
	return out
}

// filtered: any key away from its default (the sort is not a filter).
func (l listQ) filtered() bool {
	for _, k := range l.keys {
		if k != "sort" && !In(k, l.tabs...) && l.v[k] != l.def[k] {
			return true
		}
	}
	return false
}

func (l listQ) pairs() [][2]string {
	var p [][2]string
	for _, k := range l.keys {
		if l.v[k] != l.def[k] {
			p = append(p, [2]string{k, l.v[k]})
		}
	}
	if l.page > 1 {
		p = append(p, [2]string{"page", strconv.Itoa(l.page)})
	}
	return p
}

func (l listQ) href() string {
	h := l.path
	if p := l.pairs(); len(p) > 0 {
		h += "?" + py.Urlencode(p)
	}
	if l.anchor != "" {
		h += "#" + l.anchor
	}
	return h
}

// with returns a copy with k set to v (the default when v is ""), back on page 1.
func (l listQ) with(k, v string) listQ {
	g := l.clone()
	if v == "" {
		v = l.def[k]
	}
	g.v[k] = v
	g.page = 1
	return g
}

func (l listQ) clone() listQ {
	g := l
	g.v = make(map[string]string, len(l.v))
	for k, v := range l.v {
		g.v[k] = v
	}
	return g
}

func (l listQ) atPage(p int) listQ {
	g := l.clone()
	g.page = p
	return g
}

// cleared drops every filter and keeps the sort and the view tab.
func (l listQ) cleared() listQ {
	g := l.clone()
	g.page = 1
	for _, k := range l.keys {
		if k != "sort" && !In(k, l.tabs...) {
			g.v[k] = l.def[k]
		}
	}
	return g
}

func (l listQ) hidden(skip ...string) string { return queueHidden(l.pairs(), skip...) }

// listPage clamps the page (a page past the end shows the last page) and returns the [from, to) slice bounds.
func listPage(l *listQ, total int) (int, int) {
	n := l.per()
	pages := max(1, (total+n-1)/n)
	if l.page > pages {
		l.page = pages
	}
	from := (l.page - 1) * n
	return from, min(from+n, total)
}

// listMatch: every search word appears in one of the fields (case-insensitive substring).
func listMatch(words []string, fields ...string) bool {
	if len(words) == 0 {
		return true
	}
	hay := strings.ToLower(strings.Join(fields, "\x00"))
	for _, w := range words {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}
