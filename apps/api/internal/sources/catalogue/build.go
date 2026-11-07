package catalogue

// build.go: the inventory -> source_routes + source_unmapped, the admin actions, and the agent's view.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Summary is one build's counts.
type Summary struct {
	New, Updated, Reopened, Removed, Stale int
	Routes                                 map[string]int // by status
	ServicesMapped, Services, Unmapped     int
	Inventory                              int
}

type existing struct {
	id                         int64
	origin, status, rule, hash string
	label                      string
	conf                       float64
	stale                      bool
}

type produced struct {
	label string
	m     Match
}

type rkey struct{ service, source, kind, locator string }

func srcType(source string) string { t, _, _ := strings.Cut(source, "/"); return t }

// Build matches every inventory item, upserts discovered routes and refreshes the unmapped list. db should be a
// transaction (the caller commits).
func Build(db DB, svcs *Services, at int64) (Summary, error) {
	var sum Summary
	if err := Migrate(db); err != nil {
		return sum, err
	}
	type inv struct {
		source, kind, key string
		attrs             map[string]any
	}
	var items []inv
	q, err := db.Query("SELECT source, kind, key, COALESCE(attrs,'{}') FROM source_inventory ORDER BY source, kind, key")
	if err != nil && !strings.Contains(err.Error(), "no such table") {
		return sum, err
	}
	if err == nil {
		for q.Next() {
			var it inv
			var a string
			if q.Scan(&it.source, &it.kind, &it.key, &a) == nil {
				json.Unmarshal([]byte(a), &it.attrs)
				items = append(items, it)
			}
		}
		q.Close()
	}
	sum.Inventory, sum.Services = len(items), len(svcs.Names)
	// a source with the exact repository of a service makes no name guesses for that service on that source
	exact := map[[2]string]bool{}
	for _, it := range items {
		if srcType(it.source) == "gitlab" && it.kind == "project" {
			if svc := svcs.byPath[strings.ToLower(it.key)]; svc != "" {
				exact[[2]string{it.source, svc}] = true
			}
		}
	}
	prod := map[rkey]produced{}
	type um struct{ source, kind, key, label, note, loc string }
	var unmapped []um
	for _, it := range items {
		st := srcType(it.source)
		ms, note := svcs.MatchItem(st, it.kind, it.key, it.attrs)
		if st == "gitlab" {
			var keep []Match
			for _, m := range ms {
				if m.Rule == "gitlab-path" || !exact[[2]string{it.source, m.Service}] {
					keep = append(keep, m)
				}
			}
			ms = keep
		}
		loc, label := Locator(it.kind, it.key, it.attrs)
		if len(ms) == 0 {
			unmapped = append(unmapped, um{it.source, it.kind, it.key, label, note, loc})
		}
		for _, m := range ms {
			prod[rkey{m.Service, it.source, it.kind, loc}] = produced{label, m}
		}
	}
	ex := map[rkey]existing{}
	rq, err := db.Query(`SELECT id, service, source, kind, locator, COALESCE(label,''), COALESCE(confidence,0), origin, status,
  COALESCE(rule,''), COALESCE(evidence_hash,''), COALESCE(stale,0) FROM source_routes`)
	if err != nil {
		return sum, err
	}
	for rq.Next() {
		var k rkey
		var e existing
		var stale int
		if rq.Scan(&e.id, &k.service, &k.source, &k.kind, &k.locator, &e.label, &e.conf, &e.origin, &e.status, &e.rule, &e.hash, &stale) == nil {
			e.stale = stale != 0
			ex[k] = e
		}
	}
	rq.Close()
	pk := make([]rkey, 0, len(prod))
	for k := range prod {
		pk = append(pk, k)
	}
	sort.Slice(pk, func(i, j int) bool {
		a, b := pk[i], pk[j]
		return a.service+"\x00"+a.source+"\x00"+a.kind+"\x00"+a.locator < b.service+"\x00"+b.source+"\x00"+b.kind+"\x00"+b.locator
	})
	for _, k := range pk {
		p := prod[k]
		h := EvidenceHash(p.m.Rule, p.m.Why)
		cur, ok := ex[k]
		switch {
		case !ok:
			if _, err := db.Exec(`INSERT INTO source_routes (service, source, kind, locator, label, confidence, origin, status, rule,
  evidence, evidence_hash, stale, created_at, updated_at, updated_by) VALUES (?,?,?,?,?,?,'discovered','proposed',?,?,?,0,?,?,'discovery')`,
				k.service, k.source, k.kind, k.locator, p.label, p.m.Confidence, p.m.Rule, p.m.Why, h, at, at); err != nil {
				return sum, err
			}
			sum.New++
		case cur.origin == "learned": // a fan-out of `argus sources find` proposed it (P3): a person decides, not a build
		case cur.origin == "human":
			if cur.stale {
				db.Exec("UPDATE source_routes SET stale=0 WHERE id=?", cur.id)
			}
		case cur.status == "rejected":
			if cur.hash != h { // new evidence: ask again
				db.Exec(`UPDATE source_routes SET status='proposed', confidence=?, rule=?, evidence=?, evidence_hash=?, label=?, stale=0,
  updated_at=?, updated_by='discovery' WHERE id=?`, p.m.Confidence, p.m.Rule, p.m.Why+" (evidence changed since it was rejected)",
					h, p.label, at, cur.id)
				sum.Reopened++
			}
		case cur.conf != p.m.Confidence || cur.rule != p.m.Rule || cur.hash != h || cur.label != p.label || cur.stale:
			db.Exec(`UPDATE source_routes SET confidence=?, rule=?, evidence=?, evidence_hash=?, label=?, stale=0, updated_at=? WHERE id=?`,
				p.m.Confidence, p.m.Rule, p.m.Why, h, p.label, at, cur.id)
			sum.Updated++
		}
	}
	for k, cur := range ex {
		if _, ok := prod[k]; ok || cur.origin != "discovered" {
			continue
		}
		if cur.status == "proposed" {
			db.Exec("DELETE FROM source_routes WHERE id=?", cur.id)
			sum.Removed++
		} else if cur.status == "confirmed" && !cur.stale {
			db.Exec("UPDATE source_routes SET stale=1, updated_at=? WHERE id=?", at, cur.id)
			sum.Stale++
		}
	}
	// an item a person routed by hand is not unmapped
	claimed := map[[3]string]bool{}
	cq, err := db.Query("SELECT source, kind, locator FROM source_routes")
	if err != nil {
		return sum, err
	}
	for cq.Next() {
		var a, b, c string
		if cq.Scan(&a, &b, &c) == nil {
			claimed[[3]string{a, b, c}] = true
		}
	}
	cq.Close()
	if _, err := db.Exec("DELETE FROM source_unmapped"); err != nil {
		return sum, err
	}
	for _, u := range unmapped {
		if claimed[[3]string{u.source, u.kind, u.loc}] {
			continue
		}
		db.Exec("INSERT OR REPLACE INTO source_unmapped (source, kind, key, label, note) VALUES (?,?,?,?,?)",
			u.source, u.kind, u.key, u.label, u.note)
		sum.Unmapped++
	}
	sum.Routes = map[string]int{}
	tq, err := db.Query("SELECT status, COUNT(*) FROM source_routes GROUP BY status")
	if err == nil {
		for tq.Next() {
			var s string
			var n int
			if tq.Scan(&s, &n) == nil {
				sum.Routes[s] = n
			}
		}
		tq.Close()
	}
	db.QueryRow("SELECT COUNT(DISTINCT service) FROM source_routes WHERE status IN ('proposed','confirmed')").Scan(&sum.ServicesMapped)
	b, _ := json.Marshal(sum)
	db.Exec("INSERT INTO source_catalogue_runs (at, services, counts, unmapped, note) VALUES (?,?,?,?,'')", at, sum.Services, string(b), sum.Unmapped)
	db.Exec("DELETE FROM source_catalogue_runs WHERE id NOT IN (SELECT id FROM source_catalogue_runs ORDER BY id DESC LIMIT 50)")
	return sum, nil
}

// Line is the build as one log line.
func (s Summary) Line() string {
	return fmt.Sprintf("catalogue: %d services, %d mapped, routes %v, new %d, updated %d, reopened %d, removed %d, stale %d, unmapped %d (inventory %d)",
		s.Services, s.ServicesMapped, s.Routes, s.New, s.Updated, s.Reopened, s.Removed, s.Stale, s.Unmapped, s.Inventory)
}

// --- the admin actions --------------------------------------------------------------------------------------------

// DiscoverCooldown: Discover now for one source at most this often.
const DiscoverCooldown = 5 * time.Minute

var (
	reqRe    = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._@-]{0,63})-([0-9a-f]{16})\.json$`)
	sourceRe = regexp.MustCompile(`^[a-z]{3,20}/[a-z0-9][a-z0-9-]{0,30}$`)
	ctrlRe   = regexp.MustCompile(`[\x00-\x1f\x7f]`)
)

// Request is one admin action the web app wrote (settings-in/source-actions/<user>-<16 hex>.json).
type Request struct {
	Action, User, Locator, Note, Source, Reason string
	ID                                          int64
}

func refusal(s string) error { return errors.New(s) }

// ParseRequest checks one request's fields.
func ParseRequest(raw []byte, fileUser string) (Request, error) {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return Request{}, refusal("request is not a JSON object")
	}
	r := Request{}
	r.Action, _ = m["action"].(string)
	r.User, _ = m["user"].(string)
	role, _ := m["role"].(string)
	if r.User != fileUser {
		return r, refusal("request user does not match its file name")
	}
	if role != "admin" {
		return r, refusal("only an admin can do this")
	}
	switch r.Action {
	case "discover":
		r.Source, _ = m["source"].(string)
		if !sourceRe.MatchString(r.Source) {
			return r, refusal("bad source")
		}
		return r, nil
	case "confirm", "reject", "edit":
	default:
		return r, refusal("unknown action")
	}
	id, ok := m["id"].(float64)
	if !ok || id < 1 || id > 1e9 || id != math.Trunc(id) {
		return r, refusal("bad route id")
	}
	r.ID = int64(id)
	if r.Action == "reject" {
		reason, _ := m["reason"].(string)
		r.Reason = strings.Join(strings.Fields(ctrlRe.ReplaceAllString(reason, " ")), " ")
		if len([]rune(r.Reason)) > 300 || strings.Contains(r.Reason, "|") {
			return r, refusal("the reason is up to 300 characters, without |")
		}
	}
	if r.Action == "edit" {
		loc, _ := m["locator"].(string)
		note, _ := m["note"].(string)
		r.Locator = strings.Join(strings.Fields(loc), " ")
		r.Note = strings.Join(strings.Fields(ctrlRe.ReplaceAllString(note, " ")), " ")
		if n := len([]rune(r.Locator)); n < 1 || n > 250 || strings.ContainsAny(r.Locator, "`|") || ctrlRe.MatchString(r.Locator) {
			return r, refusal("Where must be 1 to 250 characters, without ` or |")
		}
		if len([]rune(r.Note)) > 300 || strings.Contains(r.Note, "|") {
			return r, refusal("the note is up to 300 characters, without |")
		}
	}
	return r, nil
}

func audit(db DB, at int64, by, action string, rid int64, service string, detail map[string]any) {
	b, _ := json.Marshal(detail)
	db.Exec("INSERT INTO source_route_audit (at, by_user, action, route_id, service, detail) VALUES (?,?,?,?,?,?)",
		at, by, action, rid, service, string(b))
}

// Apply acts on one request; discover runs Discover now for one source (then the caller rebuilds).
func Apply(db DB, r Request, at int64, discover func(source string) (string, error)) (string, error) {
	if r.Action == "discover" {
		var last sql.NullInt64
		db.QueryRow("SELECT MAX(started_at) FROM source_discovery_runs WHERE source=? AND status != 'skipped'", r.Source).Scan(&last)
		if last.Valid && at-last.Int64 < int64(DiscoverCooldown/time.Second) {
			return "", refusal(fmt.Sprintf("%s was discovered less than %d min ago", r.Source, int(DiscoverCooldown/time.Minute)))
		}
		audit(db, at, r.User, "discover", 0, "", map[string]any{"source": r.Source})
		if discover == nil {
			return "", refusal("discovery is not available here")
		}
		res, err := discover(r.Source)
		if err != nil {
			return "", err
		}
		return res, nil
	}
	var svc, src, kind, loc, status, evidence string
	if db.QueryRow("SELECT service, source, kind, locator, status, COALESCE(evidence,'') FROM source_routes WHERE id=?", r.ID).
		Scan(&svc, &src, &kind, &loc, &status, &evidence) != nil {
		return "", refusal("no such route")
	}
	switch r.Action {
	case "confirm", "reject":
		st := map[string]string{"confirm": "confirmed", "reject": "rejected"}[r.Action]
		if _, err := db.Exec("UPDATE source_routes SET status=?, updated_at=?, updated_by=? WHERE id=?", st, at, r.User, r.ID); err != nil {
			return "", err
		}
		detail := map[string]any{"was": status}
		if r.Reason != "" {
			detail["reason"] = r.Reason
		}
		audit(db, at, r.User, r.Action, r.ID, svc, detail)
		return fmt.Sprintf("route %d %s: %s %s for %s", r.ID, st, src, loc, svc), nil
	}
	note := r.Note
	if note == "" {
		note = evidence
	}
	if note == "" {
		note = "edited in Argus"
	}
	var other int64
	if db.QueryRow("SELECT id FROM source_routes WHERE service=? AND source=? AND kind=? AND locator=? AND id != ?",
		svc, src, kind, r.Locator, r.ID).Scan(&other) == nil {
		db.Exec("DELETE FROM source_routes WHERE id=?", other)
	}
	if _, err := db.Exec(`UPDATE source_routes SET locator=?, evidence=?, origin='human', status='confirmed', confidence=1.0, stale=0,
  updated_at=?, updated_by=? WHERE id=?`, r.Locator, note, at, r.User, r.ID); err != nil {
		return "", err
	}
	audit(db, at, r.User, "edit", r.ID, svc, map[string]any{"was": loc, "now": r.Locator})
	return fmt.Sprintf("route %d edited and confirmed: %s %s for %s", r.ID, src, r.Locator, svc), nil
}

// ActionOptions say where the requests are and who may write them.
type ActionOptions struct {
	ReqDir   string
	AppUID   int
	Now      func() time.Time
	Discover func(source string) (string, error)
	Rebuild  func(db DB) error
	Log      io.Writer
}

const maxRequest = 4096

// readRequest is the request's bytes; the file is deleted whatever happens.
func (o ActionOptions) readRequest(p string) ([]byte, error) {
	defer os.Remove(p)
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, refusal("cannot open request")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, refusal("request is not a regular file")
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != o.AppUID && int(sys.Uid) != os.Geteuid() {
		return nil, refusal("request not written by the app")
	}
	b, _ := io.ReadAll(io.LimitReader(f, maxRequest+1))
	if len(b) > maxRequest {
		return nil, refusal("request file too large")
	}
	return b, nil
}

// Actions applies every waiting request (oldest name first) and records each result for the page.
func Actions(db *sql.DB, o ActionOptions) ([]string, error) {
	if err := Migrate(db); err != nil {
		return nil, err
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	des, _ := os.ReadDir(o.ReqDir)
	var done []string
	rebuild := false
	for _, de := range des {
		name := de.Name()
		p := filepath.Join(o.ReqDir, name)
		m := reqRe.FindStringSubmatch(name)
		if m == nil {
			if !strings.HasPrefix(name, ".") {
				os.Remove(p)
			}
			continue
		}
		usr, rid := m[1], m[2]
		var res string
		raw, err := o.readRequest(p)
		var r Request
		if err == nil {
			r, err = ParseRequest(raw, usr)
		}
		if err == nil {
			res, err = Apply(db, r, now().Unix(), o.Discover)
			rebuild = rebuild || (err == nil && r.Action == "discover")
		}
		if err != nil {
			res = "refused: " + err.Error()
		}
		if len(res) > 400 {
			res = res[:400]
		}
		db.Exec("INSERT OR REPLACE INTO source_action_results (rid, user, action, result, at) VALUES (?,?,?,?,?)",
			rid, usr, r.Action, res, now().Unix())
		done = append(done, rid+": "+res)
		if o.Log != nil {
			fmt.Fprintf(o.Log, "source action %s %s by %s: %s\n", rid, r.Action, usr, res)
		}
	}
	db.Exec("DELETE FROM source_action_results WHERE rid NOT IN (SELECT rid FROM source_action_results ORDER BY at DESC LIMIT 200)")
	if rebuild && o.Rebuild != nil {
		if err := o.Rebuild(db); err != nil {
			return done, err
		}
	}
	return done, nil
}

// --- for the agent ------------------------------------------------------------------------------------------------

// Route is one source_routes row.
type Route struct {
	ID                                                                  int64
	Service, Source, Kind, Locator, Label, Origin, Status, Rule, Reason string
	UpdatedBy                                                           string
	Confidence                                                          float64
	Stale                                                               bool
	UpdatedAt                                                           int64
}

// Routes reads routes (where is a SQL condition on source_routes with its arguments; "" for all).
func Routes(db DB, where string, args ...any) []Route {
	q := `SELECT id, service, source, kind, locator, COALESCE(label,''), origin, status, COALESCE(rule,''), COALESCE(evidence,''),
  COALESCE(updated_by,''), COALESCE(confidence,0), COALESCE(stale,0), COALESCE(updated_at,0) FROM source_routes`
	if where != "" {
		q += " WHERE " + where
	}
	q += " ORDER BY service, status='confirmed' DESC, confidence DESC, source, kind, locator"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Route
	for rows.Next() {
		var r Route
		var st int
		if rows.Scan(&r.ID, &r.Service, &r.Source, &r.Kind, &r.Locator, &r.Label, &r.Origin, &r.Status, &r.Rule, &r.Reason,
			&r.UpdatedBy, &r.Confidence, &st, &r.UpdatedAt) == nil {
			r.Stale = st != 0
			out = append(out, r)
		}
	}
	return out
}

// FindService is a service by its exact name or by an unambiguous normalised name; else the candidates.
func FindService(db DB, name string) (string, []string) {
	var names []string
	q, err := db.Query("SELECT DISTINCT service FROM source_routes ORDER BY service")
	if err != nil {
		return "", nil
	}
	for q.Next() {
		var s string
		if q.Scan(&s) == nil {
			names = append(names, s)
		}
	}
	q.Close()
	sv := NewServices(nil, nil)
	_, base := split(name)
	n := sv.Norm(base)
	var hits []string
	for _, s := range names {
		if s == name {
			return s, nil
		}
		_, r := split(s)
		if sv.Norm(r) == n || Simple(s) == Simple(name) {
			hits = append(hits, s)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	return "", hits
}

// PrintRoutes is `argus sources routes <service>`: confirmed first, then proposed with confidence and reason.
func PrintRoutes(db DB, service string, out io.Writer) {
	svc, alts := FindService(db, service)
	if svc == "" {
		if len(alts) > 0 {
			if len(alts) > 8 {
				alts = alts[:8]
			}
			fmt.Fprintf(out, "no routes for %q; did you mean: %s\n", service, strings.Join(alts, ", "))
		} else {
			fmt.Fprintf(out, "no routes for %q (not in the catalogue: see argus sources list and argus sources unmapped)\n", service)
		}
		return
	}
	rs := Routes(db, "service=? AND status != 'rejected'", svc)
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Status == "confirmed" && rs[j].Status != "confirmed" })
	var rej, conf, prop int
	db.QueryRow("SELECT COUNT(*) FROM source_routes WHERE service=? AND status='rejected'", svc).Scan(&rej)
	for _, r := range rs {
		if r.Status == "confirmed" {
			conf++
		} else {
			prop++
		}
	}
	fmt.Fprintf(out, "# %s: %d confirmed, %d proposed, %d rejected (never use rejected)\n", svc, conf, prop, rej)
	fmt.Fprintln(out, "status\tsource\tkind\twhere\tconfidence\torigin\twhy")
	for _, r := range rs {
		where := r.Locator
		if r.Label != "" {
			where += " (" + r.Label + ")"
		}
		why := r.Reason
		if r.Stale {
			why += "; not seen by the last discovery"
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%.2f\t%s\t%s\n", r.Status, r.Source, r.Kind, where, r.Confidence, r.Origin, why)
	}
	fmt.Fprintln(out, "\nUse confirmed routes first; a proposed route is a lead to check. Tools that take an instance: "+
		"argus tools kube <kubernetes name>. For a question, or when nothing is routed: argus sources find \"<question>\" "+
		"--type <type> (one confirmed -> use it; several -> the top 2-3, say which; none -> a capped fan-out: report "+
		"\"searched N of M instances\" as printed).")
}

// PrintUnmapped is `argus sources unmapped [type]`: what discovery found that no service claims.
func PrintUnmapped(db DB, typ string, out io.Writer, limit int) {
	q, err := db.Query("SELECT source, kind, key, COALESCE(label,''), COALESCE(note,'') FROM source_unmapped ORDER BY source, kind, key")
	if err != nil {
		fmt.Fprintln(out, "the catalogue is not built yet (argus sources catalogue)")
		return
	}
	defer q.Close()
	var rows [][5]string
	for q.Next() {
		var r [5]string
		if q.Scan(&r[0], &r[1], &r[2], &r[3], &r[4]) == nil && (typ == "" || srcType(r[0]) == typ) {
			rows = append(rows, r)
		}
	}
	head := fmt.Sprintf("# %d discovered items no service claims", len(rows))
	if typ != "" {
		head += " (" + typ + ")"
	}
	if len(rows) > limit {
		head += fmt.Sprintf("; first %d", limit)
		rows = rows[:limit]
	}
	fmt.Fprintln(out, head)
	for _, r := range rows {
		fmt.Fprintln(out, strings.Join(r[:], "\t"))
	}
}
