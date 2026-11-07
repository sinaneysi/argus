package app

import (
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/access"
	"github.com/sinaneysi/argus/internal/config"
)

// Site is the company-specific display configuration (docs/architecture/CONFIG.md §3): every value is an
// environment key, and its default comes from the build. The normal build (site_defaults.go, tag !demo) keeps
// today's values, so an unchanged deployment renders exactly as before; the demo build (site_defaults_demo.go,
// tag demo) has neutral example values and carries no company data in the binary at all.
var (
	// COMPANY_NAME (alias ORG_NAME): the brand subtitle, the sign-in hint and the help pages.
	COMPANY_NAME = env("COMPANY_NAME", defCompanyName)
	// ARGUS_HOSTNAME: the server Argus runs on, in operator hints ("check the timers on <host>").
	ARGUS_HOSTNAME = env("ARGUS_HOSTNAME", defArgusHostname)
	// ARGUS_SSH_ALIAS: the operator's SSH alias of that server, in copy-paste commands.
	ARGUS_SSH_ALIAS = env("ARGUS_SSH_ALIAS", defArgusSSHAlias)
	// ACCESS: the install's access broker, for the Connect commands (ADR-0019).
	ACCESS = access.Config{Broker: env("ARGUS_ACCESS_BROKER", ""), TeleportProxy: env("ARGUS_TELEPORT_PROXY", ""), SSHUser: env("ARGUS_ACCESS_SSH_USER", ""),
		BoundaryAddr: env("ARGUS_BOUNDARY_ADDR", ""), BoundaryScope: env("ARGUS_BOUNDARY_SCOPE", "")}
	// MATRIX_QA_ROOM_LABEL: the QA chat room's name on /qa, the incidents page and the settings.
	MATRIX_QA_ROOM_LABEL = env("MATRIX_QA_ROOM_LABEL", defQARoomLabel)
	// MATRIX_SERVER_NAME: the server part of a Matrix ID, in examples and validation hints.
	MATRIX_SERVER_NAME = env("MATRIX_SERVER_NAME", defMatrixServerName)
	// GITLAB_URL: links to projects, findings and the backup-templates repository.
	GITLAB_URL = strings.TrimRight(env("GITLAB_URL", defGitlabURL), "/")
	// CICD_GROUP: the GitLab group of the company's projects (the first of CICD_GROUPS).
	CICD_GROUP = firstOf(env("CICD_GROUPS", defCicdGroup))
	// CICD_ENGINE_PROJECT / _ID: the shared CI engine every deploying project includes.
	CICD_ENGINE_PROJECT    = env("CICD_ENGINE_PROJECT", defCicdEngineProject)
	CICD_ENGINE_PROJECT_ID = env("CICD_ENGINE_PROJECT_ID", defCicdEngineProjectID)
	// BACKUP_TEMPLATES_REPO: group/project of the backup templates (merge requests for backup plans).
	BACKUP_TEMPLATES_REPO = env("BACKUP_TEMPLATES_REPO", defBackupTemplatesRepo)
	// OUTLINE_URL: the default Outline server on the Outline settings page.
	OUTLINE_URL  = strings.TrimRight(env("OUTLINE_URL", defOutlineURL), "/")
	OUTLINE_HOST = strings.TrimPrefix(strings.TrimPrefix(OUTLINE_URL, "https://"), "http://")
	// ARGUS_REPO: group/project of Argus's own repository (operator instructions).
	ARGUS_REPO = env("ARGUS_REPO", defArgusRepo)
	// PUBLIC_DOMAIN: the shop's public domain (readiness landing pages are shown as paths below it).
	PUBLIC_DOMAIN = env("PUBLIC_DOMAIN", defPublicDomain)
	// JIRA_PROJECT: the Jira project key in examples.
	JIRA_PROJECT = env("JIRA_PROJECT", defJiraProject)
	// ARGUS_TZ_LABEL: the name of the time zone the schedules are shown in.
	ARGUS_TZ_LABEL = env("ARGUS_TZ_LABEL", defTZLabel)
	// OBS_SITES and OBS_SITE_<ID>_LABEL: the monitoring sites (datacenters), as filter values and labels.
	OBS_SITES = splitList(env("OBS_SITES", defObsSites))
	// ENV_LABELS: display names of the environments, e.g. "release=production,pre-release=staging".
	// The environment keys stay develop / pre-release / release (ADR-0006 makes them data); only labels change.
	ENV_LABELS = parseMap(env("ENV_LABELS", ""))
)

// SiteTZ is the zone of the schedules (backup plans, the readiness window). ARGUS_TZ wins when the zone database
// has it; the build's default otherwise.
var SiteTZ = func() *time.Location {
	if v := env("ARGUS_TZ", ""); v != "" {
		if loc, err := time.LoadLocation(v); err == nil {
			return loc
		}
	}
	return defTZ
}()

// ObsSiteLabel is OBS_SITE_<ID>_LABEL (default: the id with a capital first letter).
func ObsSiteLabel(id string) string {
	if k := "OBS_SITE_" + strings.ToUpper(strings.ReplaceAll(id, "-", "_")) + "_LABEL"; config.Known(k) {
		if v := env(k, ""); v != "" {
			return v
		}
	}
	if id == "" {
		return id
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

// ObsSiteLabels is "A, B" of every site.
func ObsSiteLabels() string {
	out := make([]string, len(OBS_SITES))
	for i, s := range OBS_SITES {
		out[i] = ObsSiteLabel(s)
	}
	return strings.Join(out, ", ")
}

// IsObsSite is whether s (any case) is one of the monitoring sites.
func IsObsSite(s string) bool {
	for _, o := range OBS_SITES {
		if strings.EqualFold(o, s) {
			return true
		}
	}
	return false
}

// EnvLabel is the display name of an environment key (ENV_LABELS), the key itself when none is set.
func EnvLabel(envv string) string {
	if v, ok := ENV_LABELS[envv]; ok && v != "" {
		return v
	}
	return envv
}

// BrandSubtitle is the line under "Argus" in the sidebar.
func BrandSubtitle() string { return strings.TrimSpace(COMPANY_NAME + " DevOps") }

func firstOf(s string) string {
	if l := splitList(s); len(l) > 0 {
		return l[0]
	}
	return ""
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseMap(s string) map[string]string {
	m := map[string]string{}
	for _, p := range splitList(s) {
		if k, v, ok := strings.Cut(p, "="); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

// helpVars are the {{name}} placeholders of the help pages (helpdocs/*.md).
func helpVars() map[string]string {
	return map[string]string{
		"company": COMPANY_NAME, "host": ARGUS_HOSTNAME, "qa_room": MATRIX_QA_ROOM_LABEL, "tz": ARGUS_TZ_LABEL,
		"jira": JIRA_PROJECT, "sites": ObsSiteLabels(), "site_tags": defHelpSiteTags, "chat_from_alt": defHelpChatFrom,
		"chat_yes_alt": defHelpChatYes, "chat_no_alt": defHelpChatNo,
	}
}

// HelpExpand replaces the {{name}} placeholders of a help page.
func HelpExpand(md string) string {
	if !strings.Contains(md, "{{") {
		return md
	}
	for k, v := range helpVars() {
		md = strings.ReplaceAll(md, "{{"+k+"}}", v)
	}
	return md
}

// defIssueRef is " (<id>)" for a tracker reference in a help sentence, "" when the build has none.
func defIssueRef(id string) string {
	if id == "" || id == "unencrypted" {
		return ""
	}
	return " (" + id + ")"
}
