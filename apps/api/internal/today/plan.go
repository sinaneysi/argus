package today

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// --- estimate and score ----------------------------------------------------------------------------------------------

type learnRow struct {
	n     int64
	ratio float64
}

func learned(ctx context.Context, d DB) map[string]learnRow {
	out := map[string]learnRow{}
	for _, r := range soft(ctx, d, "SELECT kind, n, ratio FROM today_learn") {
		out[py.Str(r.At(0))] = learnRow{i64(r.At(1)), num(r.At(2))}
	}
	return out
}

func roundMin(m float64) int {
	n := max(1, int(math.RoundToEven(m)))
	if n <= 10 {
		return n
	}
	return int(5 * math.RoundToEven(float64(n)/5.0))
}

// estimate is (minutes, base minutes, basis text): config minutes + per_unit x units (capped), x size, x learned ratio.
func estimate(c Cand, cfg *Config, learn map[string]learnRow) (int, int, string) {
	k := cfg.kind(c.Kind)
	base := num(k.GetD("minutes", int64(20)))
	if truthy(k.Get("per_unit")) {
		base += num(k.Get("per_unit")) * float64(max(1, c.Units))
	}
	if truthy(k.Get("max")) {
		base = min(base, num(k.Get("max")))
	}
	basis := py.Str(k.GetD("label", c.Kind))
	if c.Size != "" && cfg.Map("size").Has(c.Size) {
		base *= num(cfg.Map("size").Get(c.Size))
		basis += ", size " + c.Size
	}
	base = max(1.0, base)
	est := base
	if l, ok := learn[c.Kind]; ok && l.n != 0 {
		est = base * l.ratio
		basis += fmt.Sprintf(", learned x%s from %s", py.F(l.ratio, 2), plural(int(l.n), "actual"))
	}
	return roundMin(est), roundMin(base), basis
}

func score(c Cand, est int, carried int64) float64 {
	u := min(10.0, c.Urgency+0.5*float64(min(carried, 3)))
	return pyRound(u*c.Impact*c.Confidence/(1+float64(est)/60.0), 3)
}

type flags struct{ approval, window, own int }

func kindFlags(c Cand, cfg *Config) flags {
	k := cfg.Map("kinds").Map(c.Kind)
	if k == nil {
		k = py.NewOMap()
	}
	prod := c.Env == "release" || c.Env == "production"
	b := func(x bool) int {
		if x {
			return 1
		}
		return 0
	}
	return flags{approval: b(truthy(k.Get("approval")) && prod || c.Kind == "cicd_security"), window: b(truthy(k.Get("risky")) && prod),
		own: b(truthy(k.Get("own")))}
}

func sigOfCand(c Cand) string {
	if c.Sig != "" {
		return c.Sig
	}
	ps := make([]any, len(c.Members))
	for i, m := range c.Members {
		ps[i] = m
	}
	return sigOf(ps...)
}

// --- the plan ----------------------------------------------------------------------------------------------------------

func (o *Options) event(ctx context.Context, d DB, planID, itemID any, kind, actor, text string, at int64) error {
	_, err := d.ExecContext(ctx, "INSERT INTO today_events (plan_id, item_id, at, kind, actor, text) VALUES (?,?,?,?,?,?)",
		planID, itemID, at, kind, actor, clipRunes(text, 300))
	return err
}

func currentPlan(ctx context.Context, d DB, day string) (*Row, error) {
	if day != "" {
		return one1(ctx, d, "SELECT * FROM today_plans WHERE day=?", day)
	}
	return one1(ctx, d, "SELECT * FROM today_plans ORDER BY day DESC LIMIT 1")
}

func membersOf(v any) []string {
	s, _ := v.(string)
	if s == "" {
		return nil
	}
	x, err := py.ParseJSON([]byte(s))
	if err != nil {
		return nil
	}
	l, _ := x.([]any)
	out := make([]string, 0, len(l))
	for _, e := range l {
		out = append(out, py.Str(e))
	}
	return out
}

type state struct {
	status           string
	carried, pinned  int64
	deferUntil, note any
	slot             any // string or nil
}

type built struct {
	c         Cand
	est, base int
	basis     string
	sc        float64
	st        state
	slot      string
	why       any
	fl        flags
}

// Build creates or refreshes the plan of today and returns it.
func (o *Options) Build(ctx context.Context, d DB, cfg *Config, now int64, replan bool, trigger, by string) (*Row, error) {
	day := o.dayOf(now)
	st := o.settings(ctx, d, cfg)
	cands := o.Collect(ctx, d, cfg, now)
	learn := learned(ctx, d)
	plan, err := currentPlan(ctx, d, day)
	if err != nil {
		return nil, err
	}
	fresh := plan == nil
	if fresh {
		if _, err := d.ExecContext(ctx, "INSERT INTO today_plans (day, created_at, updated_at, budget_min, max_items, max_risky, trigger, by) "+
			"VALUES (?,?,?,?,?,?,?,?)", day, now, now, st.FocusMin, st.MaxItems, st.MaxRisky, trigger, by); err != nil {
			return nil, err
		}
		if plan, err = currentPlan(ctx, d, day); err != nil {
			return nil, err
		}
		if err := o.event(ctx, d, plan.Get("id"), nil, "plan", by, "planned ("+trigger+")", now); err != nil {
			return nil, err
		}
	} else if replan {
		if _, err := d.ExecContext(ctx, "UPDATE today_plans SET version=version+1, budget_min=?, max_items=?, max_risky=?, trigger=?, by=?, "+
			"updated_at=? WHERE id=?", st.FocusMin, st.MaxItems, st.MaxRisky, trigger, by, now, plan.Get("id")); err != nil {
			return nil, err
		}
		if err := o.event(ctx, d, plan.Get("id"), nil, "replan", by, "re-planned", now); err != nil {
			return nil, err
		}
	}
	pid := plan.Get("id")
	loadCur := func() (map[string]Row, []string, error) {
		rows, err := query(ctx, d, "SELECT * FROM today_items WHERE plan_id=?", pid)
		m := map[string]Row{}
		var order []string
		for _, r := range rows {
			m[py.Str(r.Get("ref"))] = r
			order = append(order, py.Str(r.Get("ref")))
		}
		return m, order, err
	}
	cur, curOrder, err := loadCur()
	if err != nil {
		return nil, err
	}
	prev := map[string]Row{}
	if fresh || replan {
		rows, err := query(ctx, d, "SELECT i.* FROM today_items i JOIN today_plans p ON p.id=i.plan_id WHERE p.day < ? ORDER BY p.day", day)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			prev[py.Str(r.Get("ref"))] = r
		}
	}
	quiet := int64(num(cfg.def("done_quiet_days", int64(7)))) * 86400
	byRef := map[string]Cand{}
	for _, c := range cands {
		byRef[c.Ref] = c
	}
	popped := map[string]bool{}

	// sources that closed: today's open items are done (auto), later rows go away
	for _, ref := range curOrder {
		r := cur[ref]
		c, ok := byRef[ref]
		closed := !ok
		if ok && truthy(r.Get("members")) {
			inter := false
			have := map[string]bool{}
			for _, m := range c.Members {
				have[m] = true
			}
			for _, m := range membersOf(r.Get("members")) {
				if have[m] {
					inter = true
				}
			}
			closed = !inter
		}
		if !closed {
			continue
		}
		rs, slot := py.Str(r.Get("status")), py.Str(r.Get("slot"))
		if (rs == "planned" || rs == "doing") && slot == "today" {
			if _, err := d.ExecContext(ctx, "UPDATE today_items SET status='done', auto_done=1, done_at=?, note=?, updated_at=? WHERE id=?",
				now, "closed at the source", now, r.Get("id")); err != nil {
				return nil, err
			}
			if err := o.event(ctx, d, pid, r.Get("id"), "auto-done", o.actor(), "closed at the source", now); err != nil {
				return nil, err
			}
			if ok {
				delete(byRef, ref)
				popped[ref] = true
			}
		} else if (rs == "planned" || rs == "skipped" || rs == "deferred") && slot == "later" && !ok {
			if _, err := d.ExecContext(ctx, "DELETE FROM today_items WHERE id=?", r.Get("id")); err != nil {
				return nil, err
			}
		}
	}
	if cur, _, err = loadCur(); err != nil {
		return nil, err
	}

	var rows []built
	for _, c0 := range cands {
		c, ok := byRef[c0.Ref]
		if !ok || popped[c0.Ref] {
			continue
		}
		stt := state{status: "planned"}
		if r, ok := cur[c.Ref]; ok {
			if py.Str(r.Get("status")) == "done" {
				continue
			}
			stt = state{status: py.Str(r.Get("status")), carried: i64(r.Get("carried")), pinned: i64(r.Get("pinned")),
				deferUntil: r.Get("defer_until"), note: r.Get("note"), slot: r.Get("slot")}
		} else if p, ok := prev[c.Ref]; ok {
			ps := py.Str(p.Get("status"))
			if ps == "done" && truthy(p.Get("done_at")) && now-i64(p.Get("done_at")) < quiet && strOr(p.Get("sig")) == sigOfCand(c) {
				continue
			}
			if ps == "deferred" && truthy(p.Get("defer_until")) && py.Str(p.Get("defer_until")) > day {
				stt.status, stt.deferUntil, stt.note = "deferred", p.Get("defer_until"), p.Get("note")
			} else if (ps == "planned" || ps == "doing") && py.Str(p.Get("slot")) == "today" {
				stt.carried = i64(p.Get("carried")) + 1
			}
		}
		est, base, basis := estimate(c, cfg, learn)
		rows = append(rows, built{c: c, est: est, base: base, basis: basis, sc: score(c, est, stt.carried), st: stt})
	}

	decide := fresh || replan
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.st.pinned != b.st.pinned {
			return a.st.pinned > b.st.pinned
		}
		ad, bd := a.st.status != "doing", b.st.status != "doing"
		if ad != bd {
			return !ad
		}
		return a.sc > b.sc
	})
	used, risky, count := 0, 0, 0
	slotIs := func(s state, v string) bool { x, ok := s.slot.(string); return ok && x == v }
	if !decide {
		for _, b := range rows {
			if slotIs(b.st, "today") && (b.st.status == "planned" || b.st.status == "doing") {
				used += b.est
				risky += kindFlags(b.c, cfg).window
			}
		}
	}
	dt, err := one1(ctx, d, "SELECT count(*), coalesce(sum(coalesce(actual_min, est_min)),0), coalesce(sum(needs_window),0) "+
		"FROM today_items WHERE plan_id=? AND slot='today' AND status='done'", pid)
	if err != nil {
		return nil, err
	}
	count = int(i64(dt.At(0)))
	if !decide {
		for _, b := range rows {
			if slotIs(b.st, "today") && (b.st.status == "planned" || b.st.status == "doing") {
				count++
			}
		}
	}
	used += int(i64(dt.At(1)))
	risky += int(i64(dt.At(2)))
	for i := range rows {
		b := &rows[i]
		fl := kindFlags(b.c, cfg)
		slot := "later"
		var why any
		s := b.st
		switch {
		case s.status == "deferred":
			w := "deferred until " + py.Str(s.deferUntil)
			if truthy(s.note) {
				w += ": " + py.Str(s.note)
			}
			why = w
		case s.status == "skipped":
			w := "skipped today"
			if truthy(s.note) {
				w += ": " + py.Str(s.note)
			}
			why = w
		case !decide && s.slot != nil:
			slot = py.Str(s.slot)
			if slot == "later" {
				why = cur[b.c.Ref].Get("later_reason")
			}
		case !decide && b.c.Urgency < 9:
			why = "arrived after this morning's plan; Re-plan to fit it in"
		case s.pinned != 0 || s.status == "doing":
			slot = "today"
		case !decide:
			slot = "today"
		case count >= st.MaxItems:
			why = fmt.Sprintf("the day is full (%d tasks)", st.MaxItems)
		case fl.window != 0 && risky >= st.MaxRisky:
			if st.MaxRisky != 0 {
				why = "one production change with a window is already planned today"
			} else {
				why = "no production changes planned (setting)"
			}
		case b.est > st.FocusMin:
			why = fmt.Sprintf("longer than your whole focus time (%d min); split it", b.est)
		case used+b.est > st.FocusMin:
			why = fmt.Sprintf("does not fit: needs %d min, %d min left", b.est, max(0, st.FocusMin-used))
		default:
			slot = "today"
		}
		if slot == "today" && (decide || !slotIs(s, "today")) {
			used += b.est
			count++
			risky += fl.window
		}
		b.slot, b.why, b.fl = slot, why, fl
	}

	for i, b := range rows {
		c := b.c
		det := make([]any, len(c.Details))
		for j, x := range c.Details {
			det[j] = py.NewOMap("text", x.Text, "link", x.Link)
		}
		mem := make([]any, len(c.Members))
		for j, m := range c.Members {
			mem[j] = m
		}
		var later any
		if b.slot == "later" {
			later = b.why
		}
		cols := []string{"kind", "area", "env", "title", "do_this", "why_now", "details", "link", "link_label", "close_link", "prompt_url",
			"members", "sig", "needs_approval", "needs_window", "own_action", "est_min", "est_base", "est_basis", "score", "urgency", "impact",
			"confidence", "slot", "rank", "later_reason", "status", "carried", "pinned", "defer_until", "note", "updated_at"}
		vals := []any{c.Kind, c.Area, c.Env, c.Title, c.Do, c.Why, py.Dumps(det, -1), c.Link, c.LinkLabel, c.Close, c.Prompt,
			py.Dumps(mem, -1), sigOfCand(c), b.fl.approval, b.fl.window, b.fl.own, b.est, b.base, b.basis, b.sc, c.Urgency, c.Impact,
			c.Confidence, b.slot, i + 1, later, b.st.status, b.st.carried, b.st.pinned, b.st.deferUntil, b.st.note, now}
		if r, ok := cur[c.Ref]; ok {
			_, err = d.ExecContext(ctx, "UPDATE today_items SET "+strings.Join(cols, "=?, ")+"=? WHERE id=?", append(vals, r.Get("id"))...)
		} else {
			cols = append(cols, "plan_id", "ref", "created_at")
			vals = append(vals, pid, c.Ref, now)
			_, err = d.ExecContext(ctx, "INSERT INTO today_items ("+strings.Join(cols, ", ")+") VALUES ("+
				strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")+")", vals...)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := totals(ctx, d, pid, now); err != nil {
		return nil, err
	}
	return currentPlan(ctx, d, day)
}

func totals(ctx context.Context, d DB, pid any, now int64) error {
	r, err := one1(ctx, d, "SELECT coalesce(sum(CASE WHEN slot='today' AND status IN ('planned','doing','done') THEN est_min END),0) m, "+
		"sum(slot='today' AND status IN ('planned','doing','done')) t, sum(slot='later') l FROM today_items WHERE plan_id=?", pid)
	if err != nil || r == nil {
		return err
	}
	_, err = d.ExecContext(ctx, "UPDATE today_plans SET planned_min=?, n_today=?, n_later=?, updated_at=? WHERE id=?",
		i64(r.At(0)), i64(r.At(1)), i64(r.At(2)), now, pid)
	return err
}

func learnFrom(ctx context.Context, d DB, kind string, actual int64, base int64, cfg *Config, now int64) error {
	if actual == 0 || base == 0 {
		return nil
	}
	a, lo, hi := num(cfg.def("learn_alpha", 0.3)), num(cfg.def("learn_min", 0.5)), num(cfg.def("learn_max", 3.0))
	sample := min(max(float64(actual)/float64(base), lo), hi)
	r, err := one1(ctx, d, "SELECT n, ratio FROM today_learn WHERE kind=?", kind)
	if err != nil {
		return err
	}
	if r == nil {
		_, err = d.ExecContext(ctx, "INSERT INTO today_learn (kind, n, ratio, updated_at) VALUES (?,?,?,?)", kind, 1, sample, now)
		return err
	}
	ratio := min(max((1-a)*num(r.At(1))+a*sample, lo), hi)
	_, err = d.ExecContext(ctx, "UPDATE today_learn SET n=n+1, ratio=?, updated_at=? WHERE kind=?", ratio, now, kind)
	return err
}
