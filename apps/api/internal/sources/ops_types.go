package sources

// ops_types.go: the data-source types that feed Argus's own jobs rather than the agent (ADR-0021 D5): Alertmanager
// or Prometheus (the alerts snapshot), Jira (the board's tracker) and Matrix (notices). Their checks only read: who
// the login is and whether the API answers.

import (
	"encoding/base64"
	"fmt"
)

// AllowHTTP lets plain http:// addresses through CleanURL (ARGUS_SOURCES_ALLOW_HTTP=1: test installations with fake
// services only). Set by the commands from the configuration.
var AllowHTTP = false

func checkAlertmanager(base, cred string, _ *Entry) (Check, error) {
	hdr := map[string]string{}
	if cred != "" {
		hdr["Authorization"] = "Bearer " + cred
	}
	st, d, err := call("GET", base, "/api/v2/alerts", hdr, nil)
	if err != nil {
		return Check{}, err
	}
	if st == 401 || st == 403 {
		return loginFailed(st), nil
	}
	if l, ok := d.([]any); st == 200 && ok {
		return result("ok", fmt.Sprintf("Alertmanager answers: %d alerts now", len(l)), "", nil, map[string]any{"api": "alertmanager", "alerts": len(l)}), nil
	}
	st2, d2, err := call("GET", base, "/api/v1/alerts", hdr, nil)
	if err != nil {
		return Check{}, err
	}
	if st2 == 401 || st2 == 403 {
		return loginFailed(st2), nil
	}
	if m := asMap(d2); st2 == 200 && m != nil && m["status"] == "success" {
		n := 0
		if l, ok := asMap(m["data"])["alerts"].([]any); ok {
			n = len(l)
		}
		return result("ok", fmt.Sprintf("Prometheus answers: %d alerts now", n), "", nil, map[string]any{"api": "prometheus", "alerts": n}), nil
	}
	return result("unreachable", fmt.Sprintf("neither an Alertmanager (/api/v2/alerts: HTTP %d) nor a Prometheus (/api/v1/alerts: HTTP %d) answered", st, st2), "", nil, nil), nil
}

// AlertsPath is the alerts endpoint of an alertmanager source, from its last check.
func AlertsPath(e *Entry) string {
	if e.Facts["api"] == "prometheus" {
		return "/api/v1/alerts"
	}
	return "/api/v2/alerts"
}

func jiraHeaders(e *Entry, cred string) map[string]string {
	if e.Username != "" {
		return map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(e.Username+":"+cred))}
	}
	return map[string]string{"Authorization": "Bearer " + cred}
}

func checkJira(base, cred string, e *Entry) (Check, error) {
	st, d, err := call("GET", base, "/rest/api/2/myself", jiraHeaders(e, cred), nil)
	if err != nil {
		return Check{}, err
	}
	if st == 401 || st == 403 {
		return loginFailed(st), nil
	}
	m := asMap(d)
	if st != 200 || m == nil {
		return result("unreachable", fmt.Sprintf("Jira answered HTTP %d for /rest/api/2/myself", st), "", nil, nil), nil
	}
	if m["active"] == false {
		return result("failed", "the Jira user is not active", str(m["name"]), nil, nil), nil
	}
	id := str(m["name"])
	if id == "" {
		id = str(m["displayName"])
	}
	return result("ok", "works as the board's tracker", id, nil, nil), nil
}

func checkMatrix(base, cred string, _ *Entry) (Check, error) {
	st, d, err := call("GET", base, "/_matrix/client/v3/account/whoami", map[string]string{"Authorization": "Bearer " + cred}, nil)
	if err != nil {
		return Check{}, err
	}
	if st == 401 || st == 403 {
		return loginFailed(st), nil
	}
	m := asMap(d)
	if st != 200 || m == nil || str(m["user_id"]) == "" {
		return result("unreachable", fmt.Sprintf("the homeserver answered HTTP %d for whoami", st), "", nil, nil), nil
	}
	return result("ok", "works for notices", str(m["user_id"]), nil, nil), nil
}

func init() {
	checks["alertmanager"], checks["jira"], checks["matrix"] = checkAlertmanager, checkJira, checkMatrix
}
