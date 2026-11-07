package app

import (
	"math"

	"github.com/sinaneysi/argus/internal/py"
)

// cicd_summary.py (the app side; bin/brief.py keeps the Python module): green per environment.

var (
	CSEnvs    = []string{"develop", "pre-release", "release"}
	CSStates  = []string{"success", "failed", "waiting", "canceled", "nopipeline", "nobranch"}
	CSWaiting = []string{"manual", "created", "pending", "preparing", "running", "waiting_for_resource", "scheduled"}
	CSKinds   = []string{"app", "package"}
	CSLabel   = map[string]string{"success": "Success", "failed": "Failed", "waiting": "Waiting", "canceled": "Canceled or skipped",
		"nopipeline": "No pipeline", "nobranch": "No branch"}
)

const CSDays = 30

// CSLabelOf is label(state, env).
func CSLabelOf(state, env string) string {
	if state == "waiting" && env == "release" {
		return "Waiting for manual deploy"
	}
	if state == "nopipeline" {
		return "No pipeline in " + Itoa(CSDays) + " d"
	}
	if state == "nobranch" {
		if env != "" {
			return "No " + env + " branch"
		}
		return CSLabel[state]
	}
	if l, ok := CSLabel[state]; ok {
		return l
	}
	return state
}

// CSBucket is bucket(status).
func CSBucket(status any) string {
	if !py.Truthy(status) {
		return "nopipeline"
	}
	s := py.Str(status)
	switch {
	case s == "success":
		return "success"
	case s == "failed":
		return "failed"
	case In(s, CSWaiting...):
		return "waiting"
	}
	return "canceled"
}

// CSSummary is summary(con, kind): nil when ops.db has no cicd tables.
type CSSummary struct {
	Kind  string
	Days  int64
	Since int64
	At    int64
	Envs  map[string]map[string]int64
	State map[string]map[int64]string
	IDs   []int64 // the projects, in query order (out["paths"])
	Paths map[int64]string
}

type csKey struct {
	pid int64
	ref string
}

// CSSummarize is cicd_summary.summary(ui.db(), kind) over ops.db; nil on any SQLite error.
func CSSummarize(kind string, now float64) *CSSummary {
	if !In(kind, CSKinds...) {
		kind = "app"
	}
	if now == 0 {
		now = Now()
	}
	n := int64(math.Floor(now))
	since := n - CSDays*86400
	projs, err := QueryPath(DB, "SELECT id, path FROM cicd_projects WHERE kind=? AND consumer=1 AND COALESCE(archived,0)=0", kind)
	if err != nil {
		return nil
	}
	s := &CSSummary{Kind: kind, Days: CSDays, Since: since, At: n, Envs: map[string]map[string]int64{},
		State: map[string]map[int64]string{}, Paths: map[int64]string{}}
	for _, r := range projs {
		id := r.I("id")
		if _, dup := s.Paths[id]; !dup {
			s.IDs = append(s.IDs, id)
		}
		s.Paths[id] = r.Str("path")
	}
	br, err := QueryPath(DB, "SELECT project_id, branch FROM cicd_branches WHERE present=1")
	if err != nil {
		return nil
	}
	present := map[csKey]bool{}
	for _, r := range br {
		present[csKey{r.I("project_id"), r.Str("branch")}] = true
	}
	pl, err := QueryPath(DB, "SELECT project_id, ref, status FROM cicd_pipelines WHERE ref IN (?,?,?) AND created_at >= ? ORDER BY id DESC",
		"develop", "pre-release", "release", since)
	if err != nil {
		return nil
	}
	ran, skipped := map[csKey]any{}, map[csKey]bool{}
	for _, r := range pl {
		pid := r.I("project_id")
		if _, ok := s.Paths[pid]; !ok {
			continue
		}
		k := csKey{pid, r.Str("ref")}
		if r.Get("status") == "skipped" {
			skipped[k] = true
		} else if _, ok := ran[k]; !ok {
			ran[k] = r.Get("status")
		}
	}
	for _, env := range CSEnvs {
		per := map[int64]string{}
		counts := map[string]int64{"skipped_only": 0}
		for _, st := range CSStates {
			counts[st] = 0
		}
		for _, pid := range s.IDs {
			k := csKey{pid, env}
			var st string
			if !present[k] {
				st = "nobranch"
			} else if v, ok := ran[k]; ok {
				st = CSBucket(v)
			} else if skipped[k] {
				st = "canceled"
				counts["skipped_only"]++
			} else {
				st = "nopipeline"
			}
			per[pid] = st
			counts[st]++
		}
		counts["total"] = int64(len(s.IDs))
		counts["branches"] = int64(len(s.IDs)) - counts["nobranch"]
		s.Envs[env] = counts
		s.State[env] = per
	}
	return s
}

// CSAsJSON is as_json(s): the public shape of /cicd/summary.json.
func CSAsJSON(s *CSSummary) *py.OMap {
	envs := py.NewOMap()
	for _, env := range CSEnvs {
		m := py.NewOMap()
		for _, k := range append(append([]string{"branches", "total"}, CSStates...), "skipped_only") {
			m.Set(k, s.Envs[env][k])
		}
		envs.Set(env, m)
	}
	states := py.NewOMap()
	for _, k := range CSStates {
		states.Set(k, CSLabel[k])
	}
	return py.NewOMap("kind", s.Kind, "window_days", s.Days, "since", s.Since, "generated_at", s.At, "envs", envs, "states", states)
}
