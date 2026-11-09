package today

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/sinaneysi/argus/internal/py"
)

var (
	actions    = []string{"start", "done", "skip", "defer", "undo", "today", "replan", "settings"}
	userRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,39}$`)
	actionName = regexp.MustCompile(`^[0-9]{14}-[0-9a-f]{8}\.json$`)
	allowed    = map[string][]string{"start": {"planned"}, "done": {"planned", "doing"}, "skip": {"planned", "doing"},
		"defer": {"planned", "doing", "skipped"}, "undo": {"done", "skipped", "deferred", "doing"}, "today": {"planned", "skipped", "deferred"}}
)

// Request is one validated request file of the app.
type Request struct {
	Action, By, Reason string
	Item               any // int64 or nil
	Minutes            any // int64 or nil
	Days               int64
	Settings           *py.OMap
}

func in(s string, xs []string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func isInt(v any) (int64, bool) {
	n, ok := v.(int64)
	return n, ok
}

// ReadRequest reads and checks one request file (admins only, at most 4 KiB, no symlink).
func ReadRequest(path string) (*Request, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 4097)
	n, err := io.ReadFull(f, raw)
	f.Close()
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	raw = raw[:n]
	if n > 4096 {
		return nil, errors.New("too large")
	}
	if !utf8.Valid(raw) {
		return nil, errors.New("not UTF-8")
	}
	v, err := py.ParseJSON(raw)
	if err != nil {
		return nil, err
	}
	req, ok := v.(*py.OMap)
	if !ok {
		return nil, errors.New("not an object")
	}
	act, _ := req.Get("action").(string)
	by := strOr(req.Get("by"))
	role, _ := req.Get("role").(string)
	if !in(act, actions) || !userRe.MatchString(by) || role != "admin" {
		return nil, errors.New("bad fields (admins only)")
	}
	out := &Request{Action: act, By: by, Days: 3, Settings: py.NewOMap()}
	if act != "replan" && act != "settings" {
		iid, ok := isInt(req.Get("item"))
		if !ok || iid <= 0 || iid >= 1e9 {
			return nil, errors.New("bad item")
		}
		out.Item = iid
	}
	out.Reason = clipRunes(strings.Join(py.Split(ctrl.ReplaceAllString(strOr(req.Get("reason")), " ")), " "), 200)
	if m := req.Get("minutes"); m != nil && m != "" {
		n, ok := isInt(m)
		if !ok || n < 1 || n > 600 {
			return nil, errors.New("minutes must be 1 to 600")
		}
		out.Minutes = n
	}
	dd, ok := isInt(req.GetD("days", int64(3)))
	if !ok || dd < 1 || dd > 30 {
		return nil, errors.New("days must be 1 to 30")
	}
	out.Days = dd
	if act == "settings" {
		for _, l := range settingLimits {
			v := req.Get(l.k)
			if v == nil {
				continue
			}
			n, ok := isInt(v)
			if !ok || n < int64(l.lo) || n > int64(l.hi) {
				return nil, fmt.Errorf("%s must be %d to %d", l.k, l.lo, l.hi)
			}
			out.Settings.Set(l.k, n)
		}
		if out.Settings.Len() == 0 {
			return nil, errors.New("no setting")
		}
	}
	return out, nil
}

// ApplyRequest applies one request and returns what happened.
func (o *Options) ApplyRequest(ctx context.Context, d DB, req *Request, cfg *Config, now int64) (string, error) {
	a, by := req.Action, req.By
	plan, err := currentPlan(ctx, d, "")
	if err != nil {
		return "", err
	}
	switch a {
	case "settings":
		for _, k := range req.Settings.Keys() {
			if _, err := d.ExecContext(ctx, "INSERT OR REPLACE INTO today_settings (k, v, by, at) VALUES (?,?,?,?)", k, py.Str(req.Settings.Get(k)), by, now); err != nil {
				return "", err
			}
		}
		if plan != nil {
			if err := o.event(ctx, d, plan.Get("id"), nil, "settings", by, py.Dumps(req.Settings, -1), now); err != nil {
				return "", err
			}
		}
		if _, err := o.Build(ctx, d, cfg, now, true, "settings", by); err != nil {
			return "", err
		}
		return "settings saved, re-planned", nil
	case "replan":
		if _, err := o.Build(ctx, d, cfg, now, true, "manual", by); err != nil {
			return "", err
		}
		return "re-planned", nil
	}
	if plan == nil {
		return "no plan", nil
	}
	r, err := one1(ctx, d, "SELECT * FROM today_items WHERE id=? AND plan_id=?", req.Item, plan.Get("id"))
	if err != nil {
		return "", err
	}
	if r == nil {
		return "not in today's plan", nil
	}
	if st := py.Str(r.Get("status")); !in(st, allowed[a]) {
		return "not applied: the item is " + st, nil
	}
	var note any
	if req.Reason != "" {
		note = req.Reason
	}
	id := r.Get("id")
	switch a {
	case "start":
		_, err = d.ExecContext(ctx, "UPDATE today_items SET status='doing', started_at=?, updated_at=? WHERE id=?", now, now, id)
	case "done":
		actual := req.Minutes
		if actual == nil && truthy(r.Get("started_at")) {
			actual = min(max(int64(1), int64(math.RoundToEven(float64(now-i64(r.Get("started_at")))/60))), 480)
		}
		_, err = d.ExecContext(ctx, "UPDATE today_items SET status='done', done_at=?, actual_min=?, auto_done=0, note=coalesce(?, note), "+
			"updated_at=? WHERE id=?", now, actual, note, now, id)
		if err == nil && truthy(actual) {
			err = learnFrom(ctx, d, py.Str(r.Get("kind")), i64(actual), i64(or(r.Get("est_base"), r.Get("est_min"))), cfg, now)
		}
	case "skip":
		_, err = d.ExecContext(ctx, "UPDATE today_items SET status='skipped', note=?, updated_at=? WHERE id=?", note, now, id)
	case "defer":
		until := dayAdd(o.dayOf(now), int(req.Days))
		why := "deferred until " + until
		if note != nil {
			why += ": " + req.Reason
		}
		_, err = d.ExecContext(ctx, "UPDATE today_items SET status='deferred', defer_until=?, note=?, slot='later', later_reason=?, updated_at=? "+
			"WHERE id=?", until, note, why, now, id)
	case "undo":
		_, err = d.ExecContext(ctx, "UPDATE today_items SET status='planned', slot='today', later_reason=NULL, done_at=NULL, actual_min=NULL, "+
			"started_at=NULL, defer_until=NULL, auto_done=0, updated_at=? WHERE id=?", now, id)
	case "today":
		_, err = d.ExecContext(ctx, "UPDATE today_items SET status='planned', slot='today', pinned=1, later_reason=NULL, defer_until=NULL, "+
			"updated_at=? WHERE id=?", now, id)
	}
	if err != nil {
		return "", err
	}
	if err := o.event(ctx, d, plan.Get("id"), id, a, by, req.Reason, now); err != nil {
		return "", err
	}
	if err := totals(ctx, d, plan.Get("id"), now); err != nil {
		return "", err
	}
	return a, nil
}

// TxDB is the database the commands open transactions on.
type TxDB interface {
	DB
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

func (o *Options) tx(ctx context.Context, fn func(DB) error) error {
	db, ok := o.DB.(TxDB)
	if !ok {
		return fn(o.DB)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Apply applies the app's request files (the oldest 200), removes each one and rewrites the status file.
func (o *Options) Apply(ctx context.Context, cfg *Config) error {
	ents, _ := os.ReadDir(o.ActionsDir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) > 200 {
		names = names[:200]
	}
	for _, name := range names {
		path := filepath.Join(o.ActionsDir, name)
		if !actionName.MatchString(name) {
			if !strings.HasPrefix(name, ".") {
				os.Remove(path)
			}
			continue
		}
		req, err := ReadRequest(path)
		if err != nil {
			fmt.Fprintf(o.log(), "today request %s rejected: %v\n", name, err)
			os.Remove(path)
			continue
		}
		var res string
		if err := o.tx(ctx, func(d DB) error {
			var e error
			res, e = o.ApplyRequest(ctx, d, req, cfg, o.now())
			return e
		}); err != nil {
			return fmt.Errorf("today request %s: %w", name, err)
		}
		item := ""
		if req.Item != nil {
			item = py.Str(req.Item)
		}
		fmt.Fprintf(o.out(), "today request %s: %s %s by %s -> %s\n", name, req.Action, item, req.By, res)
		os.Remove(path)
	}
	_, err := o.WriteStatus(ctx, cfg)
	return err
}

// WriteStatus rewrites today-status.json (0644), the file the app reads.
func (o *Options) WriteStatus(ctx context.Context, cfg *Config) (*py.OMap, error) {
	now := o.now()
	st := o.settings(ctx, o.DB, cfg)
	plan, err := currentPlan(ctx, o.DB, "")
	if err != nil {
		return nil, err
	}
	dec, updated := o.decisionsList(ctx)
	var day, pid any
	if plan != nil {
		day, pid = plan.Get("day"), plan.Get("id")
	}
	doc := py.NewOMap("at", now, "enabled", o.Enabled, "day", day, "plan_id", pid, "focus_min", int64(st.FocusMin), "max_items", int64(st.MaxItems),
		"max_risky", int64(st.MaxRisky), "plan_at", o.planAt(cfg), "refine", false, "decisions", dec, "decisions_updated", updated)
	tmp := o.StatusFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(py.Dumps(doc, 1)), 0o644); err != nil {
		return nil, err
	}
	os.Chmod(tmp, 0o644)
	return doc, os.Rename(tmp, o.StatusFile)
}

func (o *Options) planAt(cfg *Config) string {
	if o.PlanAt != "" {
		return o.PlanAt
	}
	return py.Str(cfg.def("plan_at", "07:30"))
}

var hhmm = regexp.MustCompile(`^(\d{1,2}):(\d\d)$`)

func (o *Options) planDue(now int64, cfg *Config) bool {
	h, mi := 7, 30
	if m := hhmm.FindStringSubmatch(o.planAt(cfg)); m != nil {
		h, _ = strconv.Atoi(m[1])
		mi, _ = strconv.Atoi(m[2])
	}
	t := time.Unix(now, 0).In(o.loc())
	return t.Hour() > h || t.Hour() == h && t.Minute() >= mi
}

func (o *Options) planLine(p *Row) string {
	return fmt.Sprintf("Today %s: %s tasks, ~%s min of %s, %s later", py.Str(p.Get("day")), py.Str(p.Get("n_today")), py.Str(p.Get("planned_min")),
		py.Str(p.Get("budget_min")), py.Str(p.Get("n_later")))
}

// Tick builds the day's plan once it is due, and refreshes it on every later tick.
func (o *Options) Tick(ctx context.Context, cfg *Config, force bool) error {
	now := o.now()
	if !o.Enabled && !force {
		if _, err := o.WriteStatus(ctx, cfg); err != nil {
			return err
		}
		fmt.Fprintln(o.out(), "Today is off (FEATURE_TODAY=0)")
		return nil
	}
	day := o.dayOf(now)
	var p *Row
	err := o.tx(ctx, func(d DB) error {
		var err error
		if p, err = currentPlan(ctx, d, day); err != nil {
			return err
		}
		if p == nil && (o.planDue(now, cfg) || force) {
			p, err = o.Build(ctx, d, cfg, now, false, "timer", o.actor())
		} else if p != nil {
			p, err = o.Build(ctx, d, cfg, now, false, "refresh", o.actor())
		}
		return err
	})
	if err != nil {
		return err
	}
	if _, err := o.WriteStatus(ctx, cfg); err != nil {
		return err
	}
	if p != nil {
		fmt.Fprintln(o.out(), o.planLine(p))
	}
	return nil
}

// Plan builds (or with replan rebuilds) today's plan now and prints it.
func (o *Options) Plan(ctx context.Context, cfg *Config, replan bool) error {
	if err := o.tx(ctx, func(d DB) error {
		_, err := o.Build(ctx, d, cfg, o.now(), replan, "manual", o.actor())
		return err
	}); err != nil {
		return err
	}
	if _, err := o.WriteStatus(ctx, cfg); err != nil {
		return err
	}
	t, err := o.Text(ctx)
	fmt.Fprintln(o.out(), t)
	return err
}

// Text is today's plan as text.
func (o *Options) Text(ctx context.Context) (string, error) {
	p, err := currentPlan(ctx, o.DB, "")
	if err != nil || p == nil {
		return "no plan yet", err
	}
	rows, err := query(ctx, o.DB, "SELECT * FROM today_items WHERE plan_id=? ORDER BY slot='later', rank", p.Get("id"))
	if err != nil {
		return "", err
	}
	out := []string{fmt.Sprintf("Today %s: %s tasks, ~%s min of %s min · %s later", py.Str(p.Get("day")), py.Str(p.Get("n_today")),
		py.Str(p.Get("planned_min")), py.Str(p.Get("budget_min")), py.Str(p.Get("n_later")))}
	n := 0
	var later []Row
	for _, r := range rows {
		if py.Str(r.Get("slot")) == "later" {
			later = append(later, r)
		}
		if py.Str(r.Get("slot")) != "today" {
			continue
		}
		n++
		fl := ""
		for _, f := range []struct {
			name string
			on   bool
		}{{"needs window", truthy(r.Get("needs_window"))}, {"needs approval", truthy(r.Get("needs_approval"))},
			{"carried " + py.Str(r.Get("carried")) + "d", truthy(r.Get("carried"))}, {py.Str(r.Get("status")), py.Str(r.Get("status")) != "planned"}} {
			if f.on {
				fl += " [" + f.name + "]"
			}
		}
		env := py.Str(r.Get("env"))
		es, ok := envShort[env]
		if !ok {
			es = orS(strOr(r.Get("env")), "-")
		}
		out = append(out, fmt.Sprintf("%2d. %s — %s min · %s%s", n, py.Str(r.Get("title")), py.Str(r.Get("est_min")), es, fl))
		out = append(out, "    Do this: "+py.Str(r.Get("do_this")))
		out = append(out, "    Why now: "+py.Str(r.Get("why_now")))
	}
	if len(later) > 0 {
		out = append(out, fmt.Sprintf("Later (%d):", len(later)))
		for _, r := range later {
			out = append(out, fmt.Sprintf("  - %s (%s min, score %s): %s", py.Str(r.Get("title")), py.Str(r.Get("est_min")), py.F(num(r.Get("score")), 1),
				py.Str(r.Get("later_reason"))))
		}
	}
	return strings.Join(out, "\n"), nil
}
