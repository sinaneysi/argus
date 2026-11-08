package app

// modules.go: optional modules (ADR-0021 D6). A module groups pages, API routes and a sidebar group. A module that is
// off is hidden from the navigation and its API routes answer 404. The product default is off; ARGUS_MODULES names
// the modules that are on, and an admin may switch a module on or off in Settings -> General (auth_settings
// "module.<key>", "1" or "0"), which wins over the configuration. Read per request: a switch applies at once.

import (
	"strings"

	"github.com/sinaneysi/argus/internal/auth"
)

type module struct {
	Key, Name, Desc string
	Section         string   // the sidebar group (ia.go SECTIONS key)
	Routes          []string // API routes (GET and POST)
}

// MODULES are the optional modules, in Settings order.
var MODULES = []module{
	{Key: "marketing", Name: "Marketing", Section: "marketing", Routes: []string{"/api/v1/seo", "/api/v1/seo/send"},
		Desc: "Search health: crawler traffic and errors, search console, sitemaps and the CDN (the SEO page)."},
}

// ModulesEnv is ARGUS_MODULES: the modules on by configuration.
var ModulesEnv = env("ARGUS_MODULES", "")

func moduleByKey(key string) *module {
	for i := range MODULES {
		if MODULES[i].Key == key {
			return &MODULES[i]
		}
	}
	return nil
}

// ModuleOn says whether a module is on and why: "settings" (an admin switched it), "config" (ARGUS_MODULES) or
// "default" (off).
func ModuleOn(key string) (bool, string) {
	switch auth.Setting("module." + key) {
	case "1":
		return true, "settings"
	case "0":
		return false, "settings"
	}
	for _, k := range strings.Split(ModulesEnv, ",") {
		if strings.TrimSpace(k) == key {
			return true, "config"
		}
	}
	return false, "default"
}

// moduleOff is the module of a route or sidebar group that is switched off ("" when none is).
func routeOff(path string) bool {
	for _, m := range MODULES {
		for _, r := range m.Routes {
			if r == path {
				on, _ := ModuleOn(m.Key)
				return !on
			}
		}
	}
	return false
}

func sectionOff(key string) bool {
	for _, m := range MODULES {
		if m.Section == key {
			on, _ := ModuleOn(m.Key)
			return !on
		}
	}
	return false
}

// apiModules is the Settings -> General list.
func apiModules() []map[string]any {
	out := []map[string]any{}
	for _, m := range MODULES {
		on, src := ModuleOn(m.Key)
		out = append(out, map[string]any{"key": m.Key, "name": m.Name, "desc": m.Desc, "on": on, "source": src})
	}
	return out
}

// ModuleSave is POST /api/v1/admin/modules {module, on: "1"|"0"|"config"}: "config" drops the admin's choice.
func ModuleSave(key, on string, sess *Session) (string, *apiErr) {
	m := moduleByKey(key)
	if m == nil {
		return "", &apiErr{400, "No such module."}
	}
	var err error
	switch on {
	case "1", "0":
		err = auth.SetSetting("module."+key, on)
	case "config":
		_, err = auth.Con().Exec("DELETE FROM auth_settings WHERE key=?", "module."+key)
	default:
		return "", &apiErr{400, "Choose on, off or the configured default."}
	}
	if err != nil {
		panic(err)
	}
	now, _ := ModuleOn(key)
	logf("module %s %s by %s", key, If(now, "on", "off"), sess.Username)
	return m.Name + " is " + If(now, "on", "off") + ".", nil
}
