package app

import (
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// views_certs.py: /certs grouped by certificate (table cert_groups), else the per-name page (PageCerts).

type certsGroup struct {
	r                 Row
	names, sans, mism []string
}

func certsList(v any) []string {
	x, err := py.ParseJSON([]byte(py.OrS(v, "[]")))
	if err != nil {
		return nil
	}
	var out []string
	switch l := x.(type) {
	case []any:
		for _, s := range l {
			out = append(out, py.Str(s))
		}
	case *py.OMap:
		out = append(out, l.Keys()...)
	case string:
		for _, ch := range l {
			out = append(out, string(ch))
		}
	}
	return out
}

func certsLoad(runID any) map[string][]*certsGroup {
	rows := Q("SELECT * FROM cert_groups WHERE run_id=? ORDER BY status='EXPIRED', days_left IS NULL, "+
		"days_left, n_names DESC", runID)
	if len(rows) == 0 {
		return nil
	}
	out := map[string][]*certsGroup{}
	for _, r := range rows {
		g := &certsGroup{r: r, names: certsList(r.Get("names")), sans: certsList(r.Get("sans")), mism: certsList(r.Get("san_mismatch"))}
		k := r.Str("kind")
		out[k] = append(out[k], g)
	}
	return out
}

func certsLevel(days float64) string {
	if days >= 30 {
		return "ok"
	} else if days >= 7 {
		return "warn"
	}
	return "fail"
}

// certsHead sorts a run's groups: certificates, live ones, expired ones, unreachable names; the names counted, the
// overall status, and the banners of pinned and expected names.
func certsHead(g map[string][]*certsGroup) (certs, live, stale, unreach []*certsGroup, names int64, status, banners string) {
	pins := g["pin"]
	certs, unreach = g["cert"], g["unreachable"]
	for _, x := range certs {
		if x.r.Get("status") != "EXPIRED" {
			live = append(live, x)
		} else {
			stale = append(stale, x)
		}
	}
	for _, x := range certs {
		names += x.r.I("n_names")
	}
	names += int64(len(unreach))
	var worst *float64
	for _, x := range live {
		if x.r.Get("days_left") == nil {
			continue
		}
		v := x.r.F("days_left")
		if worst == nil || v < *worst {
			worst = &v
		}
	}
	status = "FAIL"
	if worst == nil || *worst >= 30 {
		status = "OK"
	} else if *worst >= 7 {
		status = "WARN"
	}
	first := func(l []string, d string) string {
		if len(l) > 0 {
			return l[0]
		}
		return d
	}
	for _, p := range pins {
		host := first(p.names, "?")
		na := py.Head(p.r.S("not_after"), 10)
		switch p.r.Get("status") {
		case "open":
			kind := "warn"
			if py.Num(py.Or(p.r.Get("days_left"), int64(0))) < 7 {
				kind = "fail"
			}
			banners += Banner(p.r.Str("label")+" · "+host+": "+p.r.Str("days_left")+" days left (expires "+na+"), not renewed. "+p.r.S("note"), kind)
		case "renewed":
			banners += Banner(p.r.Str("label")+" · "+host+": renewed, now expires "+na+".", "ok")
		default:
			banners += Banner(p.r.Str("label")+" · "+host+": "+p.r.Str("status")+" (pinned expiry "+p.r.Str("issuer")+").", "warn")
		}
	}
	for _, x := range g["expect"] {
		host := first(x.names, x.r.Str("label"))
		if x.r.Get("status") == "WARN(EXPECTED)" {
			s := x.sans
			if len(s) > 3 {
				s = s[:3]
			}
			cov := strings.Join(s, ", ")
			if cov == "" {
				cov = "?"
			}
			banners += Banner("Expected name mismatch · "+host+": the certificate covers "+cov+", "+
				"not this name. Known: "+py.OrS(x.r.Get("note"), "-"), "warn")
		} else {
			banners += Banner(host+" now matches its certificate: remove it from config/cert-expect.tsv.", "ok")
		}
	}
	return
}
