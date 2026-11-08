package app

import (
	"os"
	"path/filepath"

	"github.com/sinaneysi/argus/internal/py"
)

// Claude usage switch (Settings > General > Claude usage). Argus's headless `claude -p` runs share the operator's
// Claude subscription; off stops every model call (bin/claude_gate.py). The app saves the choice in
// settings.json ("claude": {"enabled": bool}); root (`argus settings apply`) applies it to /etc/argus/claude.json and
// writes the live copy settings-out/claude.json, which `argus settings claude on|off` on the Argus host also changes.

// ClaudeOn is the switch as Argus sees it: the live copy root wrote, else the saved choice, else on.
func ClaudeOn(cfg any) bool {
	live := filepath.Join(SET_OUT, "claude.json")
	if _, err := os.Stat(live); err == nil {
		b, ok := ReadJSONMap(live).Get("enabled").(bool)
		return ok && b // present but unreadable: off, like bin/claude_gate.py
	}
	if cfg == nil {
		cfg = ReadJSON(settingsPath(), py.NewOMap())
	}
	if m := py.AsMap(cfg); m != nil {
		if b, ok := py.AsMap(m.Get("claude")).Get("enabled").(bool); ok {
			return b
		}
	}
	return true
}

// ClaudeOffBanner is the Overview / Today notice while Claude usage is off ("" while on).
func ClaudeOffBanner() string {
	if ClaudeOn(nil) {
		if stopped, why := claudeEgressStopped(); stopped {
			return `<div class="banner warn" role="status">` + Icon("alert") + `<span><b>AI stopped.</b> ` + E(why) +
				` (<a href="/admin/claude#claude-egress">Settings → Claude</a>). Checks still collect their facts; model runs ` +
				`are skipped, not failed.</span></div>`
		}
		return ""
	}
	return `<div class="banner warn" role="status">` + Icon("alert") + `<span><b>Claude paused.</b> Claude usage is off ` +
		`(<a href="/admin/claude#claude-usage">Settings</a>): Argus makes no model calls. Checks still collect their facts; ` +
		`triage, RCA, analyses and task runs are skipped, not failed.</span></div>`
}

// ClaudeSave is POST /admin/claude: store the switch in settings.json for root to apply. The message for the page.
func ClaudeSave(form py.QS, sess *Session) (string, error) {
	v := form.Get("claude")
	if v != "on" && v != "off" {
		return "Choose on or off.", nil
	}
	if _, err := os.Stat(settingsPath()); err != nil {
		// Root applies the whole file: without saved schedules a default file would turn the checks off.
		return "Save the general settings once first, then switch Claude usage.", nil
	}
	cfg := py.AsMap(ReadJSON(settingsPath(), py.NewOMap()))
	if cfg == nil {
		return "settings.json is not readable; save the general settings first.", nil
	}
	cfg.Set("claude", py.NewOMap("enabled", v == "on"))
	cfg.Set("updated_by", sess.Username)
	cfg.Set("updated_at", NowInt())
	if err := WriteFileAtomic(settingsPath(), filepath.Join(SET_IN, ".settings.json.tmp"), []byte(py.Dumps(cfg, 1))); err != nil {
		return "", err
	}
	logf("settings: claude usage %s by %s", v, sess.Username)
	return "Claude usage " + v + ". Applying now; reload in a few seconds to see the result.", nil
}
