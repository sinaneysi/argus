package app

// General settings and Users as settings/form pages: labelled fields, a rejected save keeps the entries and marks the
// field, saved-and-applying feedback, discard, in-place confirm for lock-out actions, form.css budget.

import (
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestSettingsGeneralForm(t *testing.T) {
	e := srvSetup(t)
	tok, csrf := e.login("admin")
	ck := map[string]string{"opsr_session": tok}

	form := url.Values{"csrf": {csrf}, "rd:present": {"1"}, "rd:event_name": {"Big sale"}, "rd:event_date": {"2026-11-27"},
		"rd:freeze_days": {"7"}, "rd:before_days": {"30"}, "rd:after_days": {"2"}}
	for _, at := range ADMIN_TASKS {
		form.Set("sched:"+at[0], "1h")
	}
	for _, l := range LIMITS {
		form.Set("th:"+l.k, "")
	}
	form.Set("th:kuma_min_down_minutes", "5")
	form.Set("th:kuma_flap_count", "99") // out of range
	form.Set("th:cert_warn_days", "30")
	form.Set("th:cert_fail_days", "14")
	for _, s := range RCA_AUTO {
		if _, ok := s.def.(bool); !ok {
			form.Set("ra:"+s.k, "3")
		}
	}
	r := e.req("POST", "/api/v1/admin/save", ck, form, nil)
	if r.status != 400 || !strings.Contains(r.body, "Kuma: downs in one batch that count as flapping must be between 2 and 20.") ||
		!strings.Contains(r.body, `"field":"th:kuma_flap_count"`) {
		t.Fatal("rejected save feedback", r.status, r.body)
	}
	if _, err := os.Stat(settingsPath()); err == nil {
		t.Fatal("a rejected save wrote settings.json")
	}
	form.Set("th:kuma_flap_count", "3")
	if r = e.req("POST", "/api/v1/admin/save", ck, form, nil); r.status != 200 || !strings.Contains(r.body, "Saved. Applying now") {
		t.Fatal("saved feedback")
	}
}
