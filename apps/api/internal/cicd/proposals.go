package cicd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Proposal is one suggested fix: the action, the GitLab call it would make, whether the operator's rules would allow
// it, and the text for people. Argus never executes one: it is shown on the finding and copied into a task.
type Proposal struct {
	Action     string            `json:"action"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Body       map[string]string `json:"body"`
	Project    string            `json:"project"`
	Env        string            `json:"env"`
	Target     any               `json:"target"`
	Reason     string            `json:"reason"`
	JobName    string            `json:"job_name,omitempty"`
	HoldsLock  bool              `json:"holds_lock,omitempty"`
	ProdDeploy string            `json:"prod_deploy,omitempty"`
	Allowed    bool              `json:"allowed"`
	Rule       string            `json:"rule"`
	Curl       string            `json:"curl"`
	Text       string            `json:"text"`
	FixOptions []string          `json:"fix_options,omitempty"`
	FixPrompt  string            `json:"fix_prompt,omitempty"`
}

// DeployJobs are job names that deploy (never retried: a new pipeline instead).
var DeployJobs = []string{"deploy_systemd", "trigger_deploy", "deploy", "systemd_ci", "rollback_systemd"}

func (w *Watcher) proposal(action string, project any, env string, target any, reason string, mod func(*Proposal)) Proposal {
	p := url.PathEscape(fmt.Sprint(project))
	pr := Proposal{Action: action, Project: fmt.Sprint(project), Env: env, Target: target, Reason: runeHead(reason, 400)}
	switch action {
	case "cancel_pipeline":
		pr.Method, pr.Path = "POST", fmt.Sprintf("projects/%s/pipelines/%v/cancel", p, target)
	case "retry_job":
		pr.Method, pr.Path = "POST", fmt.Sprintf("projects/%s/jobs/%v/retry", p, target)
	case "new_pipeline":
		pr.Method, pr.Path, pr.Body = "POST", fmt.Sprintf("projects/%s/pipeline", p), map[string]string{"ref": fmt.Sprint(target)}
	case "set_variable":
		pr.Method, pr.Path = "PUT", fmt.Sprintf("projects/%s/variables/%v", p, target)
		pr.Body = map[string]string{"filter[environment_scope]": env, "value": "<the operator's value>"}
	case "play_job":
		pr.Method, pr.Path = "POST", fmt.Sprintf("projects/%s/jobs/%v/play", p, target)
	case "pin_lockfile":
		pr.Method, pr.Path = "MR", fmt.Sprintf("projects/%s/merge_requests", p)
		pr.Body = map[string]string{"source_branch": "chore/lockfile-" + env, "target_branch": env}
	}
	if mod != nil {
		mod(&pr)
	}
	pr.Allowed, pr.Rule = w.policy(pr)
	pr.Curl = curlText(pr)
	return pr
}

func (w *Watcher) isEngine(project string) bool {
	if w.Opt.EngineID == 0 && w.Opt.EnginePath == "" {
		return false
	}
	return project == fmt.Sprint(w.Opt.EngineID) || (w.Opt.EnginePath != "" &&
		(project == w.Opt.EnginePath || project == url.PathEscape(w.Opt.EnginePath)))
}

// policy is the operator's rule for an action: allowed once writes exist (they do not in Argus), and why.
func (w *Watcher) policy(pr Proposal) (bool, string) {
	a, env := pr.Action, pr.Env
	if w.isEngine(pr.Project) && a != "none" {
		return false, "the CI engine project: never played, run, retried or cancelled by Argus (operator only)"
	}
	switch a {
	case "play_job":
		return false, "playing a job is the operator's (production never)"
	case "set_variable":
		return false, "variables: the operator sets the value (Argus never handles values)"
	case "pin_lockfile":
		return false, "package-only MR on the branch: opened by a person or the fixer after review"
	case "cancel_pipeline":
		switch env {
		case "develop":
			return true, "develop pipelines holding or queued ahead of the lock may be cancelled"
		case "release":
			if pr.HoldsLock && pr.ProdDeploy == "manual" {
				return true, "a release pipeline holding the lock may be cancelled while its prod deploy is still manual"
			}
			return false, "release pipeline: cancel only while it holds the lock and its prod deploy is manual; otherwise the operator"
		}
		return false, orS(env, "unknown env") + ": cancelling needs the operator's OK"
	case "retry_job":
		if inList(pr.JobName, DeployJobs) {
			return false, "never retry a deploy job (a failed deploy deletes its candidate; start a new pipeline)"
		}
		if env == "develop" {
			return true, "develop: retry of a non-deploy job is allowed"
		}
		why := "operator OK"
		if env == "release" {
			why = "never on release"
		}
		return false, orS(env, "unknown env") + ": retry needs the operator (" + why + ")"
	case "new_pipeline":
		if env == "develop" {
			return true, "develop: a new pipeline is allowed"
		}
		return false, env + ": a new pipeline needs the operator"
	}
	return false, "no action"
}

func curlText(pr Proposal) string {
	if pr.Method == "" || pr.Method == "MR" {
		return ""
	}
	body := ""
	if len(pr.Body) > 0 {
		var ks []string
		for k := range pr.Body {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		var parts []string
		for _, k := range ks {
			parts = append(parts, fmt.Sprintf("--data-urlencode '%s=%s'", k, pr.Body[k]))
		}
		body = " " + strings.Join(parts, " ")
	}
	return fmt.Sprintf(`curl -X %s -H "PRIVATE-TOKEN: $CICD_WRITE_TOKEN" "$GITLAB_URL/api/v4/%s"%s`, pr.Method, pr.Path, body)
}

func pyDumps(v any) (string, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func inList(x string, xs []string) bool {
	for _, y := range xs {
		if x == y {
			return true
		}
	}
	return false
}

func orS(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
