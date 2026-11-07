package app

// Shared pieces of the queue pages (DESIGN.md "Queue page", static/queue.css): the search form, the sort links, the
// result line, the pager and the SQL search clause. Every page keeps its own GET state type; these helpers only take
// the hrefs it builds, so a control is rendered only where the page's server code implements it end to end.

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

const queuePageSize = 25 // rows per page on every queue page

// queueSortOpt is one sort link: key in the query, label, SQL ORDER BY (or "" when sorted in memory), title.
type queueSortOpt struct{ key, label, order, title string }

// queueWords trims the search box to max runes and returns it with at most maxWords words (ANDed by queueLike).
func queueWords(raw string, max, maxWords int) (string, []string) {
	q := strings.Join(strings.Fields(raw), " ")
	if utf8.RuneCountInString(q) > max {
		q = strings.TrimSpace(string([]rune(q)[:max]))
	}
	var words []string
	for _, w := range strings.Fields(q) {
		if len(words) < maxWords {
			words = append(words, w)
		}
	}
	return q, words
}

// queueLike is the SQL condition "every word is in one of cols" (LIKE with % and _ escaped), with its arguments.
func queueLike(words, cols []string) ([]string, []any) {
	var conds []string
	var args []any
	for _, w := range words {
		var or []string
		for _, col := range cols {
			or = append(or, "coalesce("+col+",'') LIKE ? ESCAPE '\\'")
			args = append(args, "%"+likeEsc(w)+"%")
		}
		conds = append(conds, "("+strings.Join(or, " OR ")+")")
	}
	return conds, args
}

// queuePageNum reads ?page= (1 when missing or out of range).
func queuePageNum(raw string) int {
	if n, err := strconv.Atoi(raw); err == nil && n > 1 && n < 100000 {
		return n
	}
	return 1
}

// queueClamp: a page past the end shows the last page.
func queueClamp(page int, total int64) int {
	if pages := int((total + queuePageSize - 1) / queuePageSize); page > pages && pages > 0 {
		return pages
	}
	return page
}

// queueSortKey returns key when it names an option, else the first (the documented default).
func queueSortKey(opts []queueSortOpt, key string) queueSortOpt {
	for _, o := range opts {
		if o.key == key {
			return o
		}
	}
	return opts[0]
}

// queueHidden renders pairs as hidden inputs, leaving out the names in skip.
func queueHidden(pairs [][2]string, skip ...string) string {
	var b strings.Builder
	for _, p := range pairs {
		if !In(p[0], skip...) {
			b.WriteString(`<input type="hidden" name="` + p[0] + `" value="` + E(p[1]) + `">`)
		}
	}
	return b.String()
}
