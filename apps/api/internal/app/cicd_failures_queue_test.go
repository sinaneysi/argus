package app

// CI/CD failures as a queue page: Open / Resolved tabs with counts, GET search / env / class facets, sort and 25-row
// pagination in SQL, the old env/class links, severity and class as separate dimensions, queue.css only on this
// CI/CD page. Fictional fixture data only.

import (
	"regexp"
)

var cicdFailRowRe = regexp.MustCompile(`<li id="f-(\d+)">`)
