// Package config is Argus's one configuration loader (ADR-0016 D9, docs/architecture/CONFIG.md).
//
// Sources, lowest to highest precedence:
//
//  1. the built-in default of each key (the Fields table below);
//  2. the file argus.env: KEY=value lines, "#" comments, no shell expansion. Its path is ARGUS_CONFIG, else
//     /etc/argus/argus.env (a missing default file is fine; a missing ARGUS_CONFIG file is an error);
//  3. the process environment (containers and systemd Environment= lines set keys this way).
//
// In ARGUS_LAYOUT=volume one more layer sits between the defaults and argus.env: ARGUS_ETC_DIR/imported.env, the
// site and integration settings `argus import` carried over from an earlier installation. Only the Importable keys
// are read from it (never a secret, a path or a security setting); argus.env and the environment still win.
//
// Every key has one canonical name. Older names (the web app's OPS_* keys, ORG_NAME) are read as aliases with a
// one-line deprecation warning, so an unchanged installation keeps working. Load never stops at the first
// problem: it returns every validation error at once. Secret values are never printed.
package config

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// DefaultFile is the configuration file read when ARGUS_CONFIG is not set.
const DefaultFile = "/etc/argus/argus.env"

// Kind is the type of a key's value; it decides the validation.
type Kind int

const (
	Str  Kind = iota // free text
	Path             // an absolute path, or empty when the key allows it
	URL              // an https URL
	Int              // a decimal integer
	Bool             // 0 or 1
	List             // comma-separated values
	Addr             // host:port to listen on
	Zone             // an IANA time zone
)

// Field describes one key.
type Field struct {
	Key      string   // canonical name
	Aliases  []string // deprecated names, read when the canonical one is not set
	Kind     Kind
	Default  string // may reference other keys as ${KEY}; expanded in defaults only, never in values
	Required bool
	Secret   bool
	Help     string
	Min, Max int      // Int only: the allowed range when Max > 0
	Pattern  bool     // Key is a pattern with one "*" (OBS_SITE_*_LABEL): every matching name is read
	Choices  []string // Str only: the allowed values when not empty
}

// Fields is the schema: every key the loader knows, in print order.
var Fields = []Field{
	{Key: "COMPANY_NAME", Aliases: []string{"ORG_NAME"}, Kind: Str, Required: true, Help: "the company name in titles and messages"},
	{Key: "ARGUS_DISPLAY_NAME", Kind: Str, Default: "Argus", Help: "the product name shown to users"},
	{Key: "ARGUS_PUBLIC_URL", Aliases: []string{"OPS_PUBLIC_URL"}, Kind: URL, Help: "the https address users open (required unless DEMO_MODE=1)"},
	{Key: "ARGUS_HOSTNAME", Kind: Str, Help: "the server Argus runs on, in operator hints"},
	{Key: "ARGUS_TZ", Kind: Zone, Default: "UTC", Help: "time zone of timers, quiet hours and day caps"},
	{Key: "ARGUS_LAYOUT", Kind: Str, Default: "legacy", Choices: []string{"legacy", "volume"}, Help: "legacy: today's paths; volume: every path default under ARGUS_STATE_DIR (one volume, ADR-0021 D1)"},
	{Key: "ARGUS_MIGRATE", Kind: Str, Default: "auto", Choices: []string{"auto", "off"}, Help: "auto: argus-api and argus worker apply the schema migrations at start (a read-only database is skipped); off: only argus migrate does"},
	{Key: "ARGUS_ETC_DIR", Kind: Path, Default: "/etc/argus", Help: "configuration directory"},
	{Key: "ARGUS_SECRETS_DIR", Kind: Path, Default: "${ARGUS_ETC_DIR}/secrets", Help: "one secret per file"},
	{Key: "ARGUS_STATE_DIR", Aliases: []string{"OPS_STATE_DIR", "OPS_DATA_DIR"}, Kind: Path, Default: "/var/lib/argus", Help: "collector state: databases and JSON files"},
	{Key: "ARGUS_DB", Aliases: []string{"OPS_DB"}, Kind: Path, Default: "${ARGUS_STATE_DIR}/ops.db", Help: "evidence database"},
	{Key: "ARGUS_BOARD_DB", Aliases: []string{"OPS_BOARD_DB", "BOARD_DB"}, Kind: Path, Help: "decisions database (empty: board/board.db next to ARGUS_DB)"},
	{Key: "ARGUS_AUTH_DB", Aliases: []string{"OPS_AUTH_DB"}, Kind: Path, Default: "/auth/auth.db", Help: "users and sessions"},
	{Key: "ARGUS_ALERTS_FILE", Aliases: []string{"OPS_ALERTS"}, Kind: Path, Default: "${ARGUS_STATE_DIR}/alerts-firing.json"},
	{Key: "ARGUS_CRED_STATUS_FILE", Aliases: []string{"OPS_CRED_STATUS"}, Kind: Path, Default: "${ARGUS_STATE_DIR}/cred-status.json"},
	{Key: "ARGUS_SELFCHECK_FILE", Aliases: []string{"OPS_SELFCHECK"}, Kind: Path, Default: "${ARGUS_STATE_DIR}/selfcheck.json"},
	{Key: "ARGUS_SELFCHECK_PAGE_AFTER", Aliases: []string{"SELF_PAGE_AFTER"}, Kind: Int, Default: "3600", Min: 300, Max: 604800, Help: "worker job selfcheck: seconds Argus must be failing before one notice is sent"},
	{Key: "ARGUS_SELFCHECK_API_URL", Kind: Str, Help: "worker job selfcheck: the API's health address it checks (empty: not checked; deploy/compose.yml sets it)"},
	{Key: "ARGUS_DEADMAN_PUSH_URL", Aliases: []string{"KUMA_PUSH_URL"}, Kind: URL, Secret: true, Help: "worker job selfcheck: a push monitor https://<host>/api/push/<token> pinged while Argus is not failing (external dead-man switch)"},
	{Key: "ARGUS_READINESS_FILE", Aliases: []string{"OPS_READINESS_DATA"}, Kind: Path, Default: "${ARGUS_STATE_DIR}/readiness.json"},
	{Key: "ARGUS_CRED_EXPIRY_FILE", Aliases: []string{"OPS_CRED_EXPIRY"}, Kind: Path, Help: "credential expiry file (empty: the app's default)"},
	{Key: "ARGUS_CRED_WARN_DAYS", Aliases: []string{"CRED_WARN_DAYS"}, Kind: Int, Default: "30", Min: 1, Max: 365, Help: "worker job cred-expiry: a credential expiring within this many days is a warning"},
	{Key: "ARGUS_CRED_CRIT_DAYS", Aliases: []string{"CRED_CRIT_DAYS"}, Kind: Int, Default: "7", Min: 0, Max: 90, Help: "worker job cred-expiry: within this many days it is an incident and a notice"},
	{Key: "ARGUS_CRED_ROTATE_DAYS", Aliases: []string{"CRED_ROTATE_DAYS"}, Kind: Int, Default: "180", Min: 7, Max: 3650, Help: "worker job cred-expiry: a credential without an expiry date older than this is due for rotation"},
	{Key: "ARGUS_CRED_TODAY_DAYS", Aliases: []string{"CRED_TODAY_DAYS"}, Kind: Int, Default: "14", Min: 0, Max: 365, Help: "worker job cred-expiry: within this many days it is on Today"},
	{Key: "ARGUS_BACKUP_PLANS_DIR", Aliases: []string{"OPS_BACKUP_PLANS"}, Kind: Path, Help: "backup plans (empty: the app's default)"},
	{Key: "ARGUS_BACKUP_REF_DIR", Aliases: []string{"OPS_BACKUP_REF"}, Kind: Path, Help: "backup reference data (empty: the app's default)"},
	{Key: "ARGUS_REPORTS_DIR", Aliases: []string{"OPS_REPORTS_DIR"}, Kind: Path, Default: "/opt/argus/reports/rca", Help: "merged RCA reports"},
	{Key: "ARGUS_SETTINGS_IN", Aliases: []string{"OPS_SETTINGS_IN"}, Kind: Path, Default: "/settings-in", Help: "request files the app writes for root"},
	{Key: "ARGUS_SETTINGS_OUT", Aliases: []string{"OPS_SETTINGS_OUT"}, Kind: Path, Default: "/settings-out", Help: "status files root writes for the app"},
	{Key: "ARGUS_SETTINGS_TIMER_PREFIX", Kind: Str, Default: "", Help: "argus settings apply: systemd timers <prefix><task>.timer get the saved schedules (empty: no timer changes, only the settings files)"},
	{Key: "ARGUS_SECRET_IN", Aliases: []string{"OPS_SECRET_IN"}, Kind: Path, Default: "/secret-in", Help: "tmpfs for write-once task secrets"},
	{Key: "ARGUS_STATIC_DIR", Aliases: []string{"OPS_STATIC"}, Kind: Path, Help: "static files (empty: ./static next to the binary)"},
	{Key: "ARGUS_BIND", Aliases: []string{"OPS_REPORT_BIND"}, Kind: Addr, Default: "127.0.0.1:8088", Help: "address the web app listens on"},
	{Key: "ARGUS_TRUSTED_PROXIES", Aliases: []string{"OPS_TRUSTED_PROXIES"}, Kind: List, Default: "127.0.0.1,::1", Help: "proxies whose X-Real-IP is trusted"},
	{Key: "ARGUS_STALE_HOURS", Aliases: []string{"OPS_STALE_HOURS"}, Kind: Int, Default: "25", Min: 1, Max: 720, Help: "a scheduled check older than this is stale"},
	{Key: "ARGUS_ADMIN_USER", Aliases: []string{"OPS_ADMIN_USER"}, Kind: Str, Default: "admin", Help: "the break-glass local admin"},
	{Key: "ARGUS_ADMIN_INITIAL_PASSWORD", Aliases: []string{"OPS_ADMIN_INITIAL_PASSWORD"}, Kind: Str, Secret: true, Help: "used once, at the first start"},
	{Key: "ARGUS_ADMINS", Aliases: []string{"OPS_ADMINS"}, Kind: List, Help: "users with the admin role"},
	{Key: "ARGUS_IMPORT_HTPASSWD", Aliases: []string{"OPS_IMPORT_HTPASSWD"}, Kind: Path, Default: "/import/htpasswd", Help: "htpasswd file imported at start (empty: none)"},
	{Key: "ARGUS_DEBUG_SQL", Aliases: []string{"OPS_DEBUG_SQL"}, Kind: Bool, Default: "0"},
	{Key: "ARGUS_NO_QUERY_CACHE", Aliases: []string{"OPS_NO_QUERY_CACHE"}, Kind: Bool, Default: "0"},
	{Key: "OIDC_ISSUER", Kind: URL, Help: "single sign-on issuer (empty: SSO off)"},
	{Key: "OIDC_CLIENT_ID", Kind: Str},
	{Key: "OIDC_CLIENT_SECRET", Kind: Str, Secret: true},
	{Key: "OIDC_CA_FILE", Kind: Path, Help: "CA bundle for the issuer (empty: the system roots)"},
	{Key: "OIDC_MFA_ACR", Kind: Str, Help: "the acr value that means the IdP checked a second factor"},
	{Key: "OIDC_MFA_AMR", Kind: List, Help: "amr values that mean the IdP checked a second factor"},
	{Key: "SESSION_MAX_HOURS", Kind: Int, Default: "12", Min: 1, Max: 24 * 31, Help: "session lifetime from sign-in"},
	{Key: "SESSION_IDLE_HOURS", Kind: Int, Default: "2", Min: 1, Max: 24 * 31, Help: "session idle timeout"},
	{Key: "SESSION_REMEMBER_DAYS", Kind: Int, Default: "30", Min: 1, Max: 365, Help: "\"keep me signed in\" lifetime"},
	{Key: "SESSION_REMEMBER_ABSOLUTE_DAYS", Kind: Int, Default: "30", Min: 1, Max: 365, Help: "upper bound of SESSION_REMEMBER_DAYS"},
	{Key: "SESSION_REMEMBER_IDLE_DAYS", Kind: Int, Default: "7", Min: 1, Max: 365, Help: "\"keep me signed in\" idle timeout"},
	{Key: "MFA_REQUIRED_FOR_ACTIONS", Kind: Bool, Default: "1", Help: "changes need a session that passed 2FA"},
	{Key: "MFA_REQUIRED_FOR_LOGIN", Kind: Bool, Default: "0", Help: "every sign-in needs 2FA"},
	{Key: "MFA_TRUST_IDP", Kind: Bool, Default: "0", Help: "trust the IdP's second factor (with OIDC_MFA_ACR or OIDC_MFA_AMR)"},
	{Key: "MFA_STEP_UP", Kind: Str, Default: "off", Help: "on: ask for a fresh code by route class"},
	{Key: "MFA_STEPUP_MINUTES", Kind: Int, Default: "720", Min: 5, Max: 60 * 24 * 31},
	{Key: "MFA_SENSITIVE_MINUTES", Kind: Int, Default: "10", Min: 1, Max: 24 * 60},
	{Key: "ARGUS_MASTER_KEY", Kind: Str, Secret: true, Help: "32 bytes, base64: encrypts the secrets in the volume (ARGUS_LAYOUT=volume); wins over ARGUS_MASTER_KEY_FILE"},
	{Key: "ARGUS_MASTER_KEY_FILE", Kind: Path, Help: "file holding the master key (volume default: ARGUS_STATE_DIR/keys/master.key, created at the first start)"},
	{Key: "ARGUS_MASTER_KEY_OLD", Kind: Str, Secret: true, Help: "argus secrets rekey: the key the secrets were written with, when it is not the current one"},
	{Key: "TOTP_KEY_FILE", Kind: Path, Default: "/keys/totp.key", Help: "key that encrypts the 2FA secrets"},
	{Key: "TOTP_KEY_FILE_OLD", Kind: Path, Help: "the previous key, for argus-api mfa rekey"},
	{Key: "TOTP_LEGACY_LABEL", Kind: Str, Help: "label of 2FA secrets an earlier install wrote under another product name (<name>-totp); read as a fallback, rewritten by argus-api mfa rekey"},
	{Key: "ARGUS_MODULES", Kind: List, Help: "optional modules that are on (ADR-0021 D6): marketing; an admin's switch in Settings -> General wins"},
	{Key: "FEATURE_TODAY", Kind: Bool, Default: "0", Help: "show the Today page without the root switch"},
	{Key: "FEATURE_PROBLEMS", Kind: Bool, Default: "0", Help: "show Problems without the root switch"},
	{Key: "CICD_STUCK_MINUTES", Kind: Int, Default: "10", Help: "a pipeline waiting longer than this is stuck"},
	// site display values (internal/app/site.go): names and links shown on the pages
	{Key: "ARGUS_SSH_ALIAS", Kind: Str, Default: "argus-01", Help: "the operator's SSH alias of the Argus server"},
	{Key: "ARGUS_ACCESS_BROKER", Kind: Str, Default: "", Help: "access broker shown on VM and cluster pages: teleport, boundary or empty"},
	{Key: "ARGUS_TELEPORT_PROXY", Kind: Str, Default: "", Help: "Teleport proxy host:port, in the Connect commands"},
	{Key: "ARGUS_ACCESS_SSH_USER", Kind: Str, Default: "", Help: "the login in the Connect commands"},
	{Key: "ARGUS_BOUNDARY_ADDR", Kind: Str, Default: "", Help: "Boundary controller URL, in the Connect commands"},
	{Key: "ARGUS_BOUNDARY_SCOPE", Kind: Str, Default: "", Help: "Boundary scope name, in the Connect commands"},
	{Key: "ARGUS_REPO", Kind: Str, Default: "ops/argus", Help: "group/project of Argus's own repository"},
	{Key: "ARGUS_TZ_LABEL", Kind: Str, Default: "UTC", Help: "the name of the time zone schedules are shown in"},
	{Key: "MATRIX_QA_ROOM_LABEL", Kind: Str, Default: "QA", Help: "the QA chat room's name"},
	{Key: "MATRIX_SERVER_NAME", Kind: Str, Default: "chat.example.com", Help: "the server part of a Matrix ID"},
	{Key: "GITLAB_URL", Kind: Str, Default: "https://gitlab.example.com", Help: "GitLab, for links"},
	{Key: "CICD_GROUPS", Kind: List, Default: "example", Help: "GitLab groups of the company's projects"},
	{Key: "CICD_ENGINE_PROJECT", Kind: Str, Default: "example/deployment", Help: "the shared CI engine project"},
	{Key: "CICD_ENGINE_PROJECT_ID", Kind: Str, Default: "100"},
	{Key: "BACKUP_TEMPLATES_REPO", Kind: Str, Default: "ops/backup-templates", Help: "group/project of the backup templates"},
	{Key: "OUTLINE_URL", Kind: Str, Default: "https://docs.example.com", Help: "the default Outline server"},
	{Key: "PUBLIC_DOMAIN", Kind: Str, Default: "example.com", Help: "the company's public domain"},
	{Key: "JIRA_PROJECT", Kind: Str, Default: "OPS", Help: "the Jira project key in examples"},
	{Key: "OBS_SITES", Kind: List, Default: "east,west", Help: "monitoring sites (datacenters)"},
	{Key: "OBS_SITE_*_LABEL", Kind: Str, Pattern: true, Help: "display name of one site (default: the id, capitalised)"},
	{Key: "ENV_LABELS", Kind: Str, Help: "display names of environments: release=production,pre-release=staging"},
	// collectors (argus checks / argus worker); each is off until its flag is 1
	{Key: "ARGUS_COLLECT_ALERTS", Kind: Bool, Default: "0", Help: "1: argus checks alerts writes ARGUS_ALERTS_FILE"},
	{Key: "ARGUS_ALERTS_SOURCES", Kind: List, Help: "name=url, ...: Alertmanager .../api/v2/alerts or Prometheus .../api/v1/alerts"},
	{Key: "ARGUS_ALERTS_TOKEN_FILE", Kind: Path, Help: "file holding a bearer token for the alert sources (empty: none)"},
	{Key: "ARGUS_ALERTS_CA_FILE", Kind: Path, Help: "extra CA certificates for the alert sources"},
	{Key: "ARGUS_ALERTS_TLS_VERIFY", Kind: Bool, Default: "1", Help: "0 turns TLS certificate checks off (not recommended)"},
	{Key: "ARGUS_ALERTS_TIMEOUT", Kind: Int, Default: "10", Min: 1, Max: 120, Help: "seconds per source"},
	{Key: "ARGUS_COLLECT_CERTS", Kind: Bool, Default: "0", Help: "1: argus checks certs records a cert-check run in ARGUS_DB"},
	{Key: "ARGUS_CERT_TARGETS", Kind: List, Help: "host or host:port (443) to probe with a TLS handshake"},
	{Key: "ARGUS_CERT_FILES", Kind: List, Help: "PEM certificate files to check"},
	{Key: "ARGUS_CERT_CA_FILE", Kind: Path, Help: "extra CA certificates for the chain check"},
	{Key: "ARGUS_CERT_WARN_DAYS", Aliases: []string{"CERT_WARN_DAYS"}, Kind: Int, Default: "30", Min: 1, Max: 365, Help: "fewer days left: WARN"},
	{Key: "ARGUS_CERT_FAIL_DAYS", Aliases: []string{"CERT_FAIL_DAYS"}, Kind: Int, Default: "14", Min: 0, Max: 365, Help: "fewer days left: FAIL"},
	{Key: "ARGUS_CERT_TIMEOUT", Kind: Int, Default: "10", Min: 1, Max: 120, Help: "seconds per TLS probe"},
	{Key: "ARGUS_COLLECT_BACKUPS", Kind: Bool, Default: "0", Help: "1: argus checks backups records a backup-check run in ARGUS_DB"},
	{Key: "ARGUS_BACKUP_SETS_FILE", Kind: Path, Default: "${ARGUS_ETC_DIR}/backup-sets.json", Help: "the backup sets to check (JSON)"},
	// notifications (argus notify; every job that posts a notice): Matrix, else Telegram, else a webhook, else none
	{Key: "ARGUS_INCIDENT_AUTORESOLVE_HOURS", Aliases: []string{"INCIDENT_AUTORESOLVE_HOURS"}, Kind: Int, Default: "6", Min: 1, Max: 720, Help: "an alert-backed incident resolves after this many hours with no matching alert firing"},
	{Key: "ARGUS_INCIDENT_CHAT_RESOLVE_HOURS", Aliases: []string{"INCIDENT_CHAT_RESOLVE_HOURS"}, Kind: Int, Default: "24", Min: 1, Max: 720, Help: "an incident with no alert behind it (a chat report) resolves after this many hours with no new report"},
	{Key: "ARGUS_INCIDENT_REOPEN_DAYS", Aliases: []string{"INCIDENT_REOPEN_DAYS"}, Kind: Int, Default: "7", Min: 1, Max: 365, Help: "a finding re-opens a resolved incident of the same problem within this many days"},
	{Key: "ARGUS_CUSTOMER_FACING", Kind: List, Help: "subjects that are customer-facing (glob patterns such as www.example.com, api.*)"},
	{Key: "ARGUS_INCIDENT_HOST_SUFFIXES", Kind: List, Default: "com,net,org,io,dev,app,cloud,internal,local,test", Help: "domain suffixes a host name in a finding's text may end in"},
	{Key: "ARGUS_INCIDENT_HOST_PREFIXES", Kind: List, Help: "prefixes of bare host names in a finding's text (such as web-, db-)"},
	{Key: "ARGUS_INCIDENT_FAMILY_WORDS", Kind: List, Help: "extra symptom words in the team's language, as family=word (latency=..., down=...), matched in alert names and reports"},
	{Key: "ARGUS_INCIDENT_SKIP_HOSTS", Kind: List, Help: "hosts never taken as an incident's subject (the own, GitLab and chat hosts are skipped anyway)"},
	{Key: "ARGUS_NOTIFY_MATRIX", Aliases: []string{"MATRIX_NOTIFY_ENABLED"}, Kind: Bool, Default: "0", Help: "1: notices go to MATRIX_NOTIFY_ROOM"},
	{Key: "MATRIX_HOMESERVER", Kind: Str, Help: "the Matrix homeserver URL notices are sent to"},
	{Key: "ARGUS_MATRIX_TOKEN_FILE", Kind: Path, Help: "file holding the Matrix access token (empty: ARGUS_SECRETS_DIR/matrix-token; missing file: MATRIX_ACCESS_TOKEN)"},
	{Key: "MATRIX_ACCESS_TOKEN", Kind: Str, Secret: true, Help: "the Matrix access token, when there is no token file"},
	{Key: "MATRIX_NOTIFY_ROOM", Kind: Str, Help: "the one room notices go to"},
	{Key: "MATRIX_WATCH_ROOMS", Kind: List, Help: "rooms that are read (evidence): never sent to"},
	{Key: "MATRIX_ALERT_ROOMS", Kind: List, Help: "alert rooms: never sent to"},
	{Key: "MATRIX_KUMA_ROOMS", Kind: List, Help: "uptime monitor rooms: never sent to"},
	{Key: "TELEGRAM_BOT_TOKEN", Kind: Str, Secret: true, Help: "Telegram bot token (used when Matrix is off)"},
	{Key: "TELEGRAM_CHAT_ID", Kind: Str, Help: "Telegram chat the bot posts to"},
	{Key: "NOTIFY_WEBHOOK", Kind: Str, Secret: true, Help: "URL a JSON {\"text\": ...} is posted to (used when Matrix and Telegram are off)"},
	{Key: "ARGUS_SECRET_ALLOWLIST", Aliases: []string{"OPS_SECRET_ALLOWLIST"}, Kind: Path, Help: "exact substrings the secret scan never redacts (reviewed fixtures)"},
	// the task board sync (argus board sync, root): requests from the app, two-way sync with Jira
	{Key: "ARGUS_BOARD_SYNC", Kind: Bool, Default: "0", Help: "1: argus board sync runs (else it logs \"disabled\" and exits 0)"},
	{Key: "ARGUS_BOARD_REQ_DIR", Aliases: []string{"BOARD_REQ_DIR"}, Kind: Path, Help: "board request files from the app (empty: ARGUS_SETTINGS_IN/board-requests)"},
	{Key: "ARGUS_BOARD_OUT", Aliases: []string{"BOARD_OUT"}, Kind: Path, Help: "where board-status.json is written (empty: ARGUS_SETTINGS_OUT)"},
	{Key: "ARGUS_BOARD_AUTH_DB", Aliases: []string{"BOARD_AUTH_DB"}, Kind: Path, Help: "users and roles the board sync checks, read only (empty: ARGUS_AUTH_DB)"},
	{Key: "ARGUS_BOARD_ENV_FILES", Aliases: []string{"BOARD_ENV_FILES"}, Kind: Str, Help: "colon-separated root-owned files with the JIRA_* settings; the first that sets a key wins (empty: ARGUS_ETC_DIR/jira.env)"},
	{Key: "ARGUS_GITLAB_ISSUES_REQ_DIR", Aliases: []string{"GITLAB_ISSUES_REQ_DIR"}, Kind: Path, Help: "GitLab issues connection requests from Settings -> Integrations (empty: ARGUS_SETTINGS_IN/gitlab-issues)"},
	{Key: "ARGUS_GITLAB_ISSUES_ENV_FILE", Aliases: []string{"GITLAB_ISSUES_ENV_FILE"}, Kind: Path, Help: "a KEY=value file whose GITLAB_URL gives the default GitLab URL and allowed host (empty: ARGUS_ETC_DIR/env)"},
	{Key: "ARGUS_BOARD_PAT_FILE", Aliases: []string{"BOARD_PAT_FILE"}, Kind: Path, Help: "the Jira personal access token, root-owned 0600 (empty: ARGUS_ETC_DIR/jira.pat)"},
	{Key: "ARGUS_BOARD_OWNER_UID", Aliases: []string{"BOARD_OWNER_UID"}, Kind: Int, Default: "0", Min: 0, Max: 2147483647, Help: "the owner the Jira settings and token files must have"},
	{Key: "ARGUS_BOARD_LABEL", Kind: Str, Default: "argus", Help: "label prefix of the Jira issues the board creates (<label>, <label>-src-<kind>-<id>, <label>-fixer)"},
	{Key: "ARGUS_BOARD_REF", Kind: Str, Default: "ARG", Help: "reference of a task that has no issue key yet (<ref>-<id>)"},
	// Settings -> Claude: the token the Claude runs use and the egress-country guard (argus secrets apply-claude, root)
	{Key: "ARGUS_CLAUDE_ENV_FILE", Kind: Path, Default: "${ARGUS_ETC_DIR}/claude.env", Help: "KEY=value file with CLAUDE_CODE_OAUTH_TOKEN (root:argus 0640); the Claude runs read it"},
	{Key: "ARGUS_CLAUDE_BIN", Kind: Path, Default: "/usr/local/bin/claude", Help: "the Claude CLI the token test runs"},
	{Key: "ARGUS_CLAUDE_RUN_AS", Kind: Str, Default: "argus", Help: "the user Claude runs as (the token test too)"},
	{Key: "ARGUS_CLAUDE_EGRESS_FILE", Kind: Path, Default: "${ARGUS_ETC_DIR}/claude-egress.json", Help: "egress guard settings (written by root from Settings -> Claude)"},
	{Key: "ARGUS_CLAUDE_EGRESS_CHECK", Kind: Path, Default: "${ARGUS_STATE_DIR}/claude-egress-check.json", Help: "the egress guard's last check (shown on Settings -> Claude)"},
	{Key: "ARGUS_CLAUDE_EGRESS_URLS", Kind: List, Default: "https://www.cloudflare.com/cdn-cgi/trace,https://api.country.is/", Help: "egress country sources, in order (https)"},
	// the model runtime (internal/model): every model call of the worker's jobs
	{Key: "ARGUS_MODEL_PROVIDER", Kind: Str, Default: "cli", Help: "cli: the Claude CLI with the token of Settings -> Claude; api: the Anthropic Messages API with the key of a Settings -> Data sources entry of type anthropic"},
	{Key: "ARGUS_MODEL", Kind: Str, Help: "the model of every call (empty: the CLI's own default; the api provider: claude-opus-5-5)"},
	{Key: "ARGUS_MODEL_API_URL", Kind: Str, Help: "the Messages API address when no anthropic data source gives one (empty: https://api.anthropic.com)"},
	{Key: "ARGUS_MODEL_MAX_BUDGET_USD", Kind: Str, Default: "5", Help: "one call never spends more than this (USD; the CLI's --max-budget-usd)"},
	{Key: "ARGUS_MODEL_MAX_MINUTES", Kind: Int, Default: "65", Min: 1, Max: 240, Help: "one call never runs longer than this"},
	{Key: "ARGUS_MODEL_LOG_DIR", Kind: Path, Default: "${ARGUS_STATE_DIR}/model-runs", Help: "the final result of each model call as JSON (redacted), one file per call"},
	// Settings -> Kubernetes: kubeconfigs root stores and checks (argus secrets apply-kube) and the agent's read-only tool
	{Key: "ARGUS_KUBE_DIR", Kind: Path, Default: "${ARGUS_ETC_DIR}/kube", Help: "kubeconfigs (root:<agent group> 0750; a file is readable by the group only while its check says read-only)"},
	{Key: "ARGUS_KUBECTL", Kind: Path, Default: "/usr/local/lib/argus/kubectl", Help: "the pinned kubectl (argus secrets install-kubectl puts it there)"},
	{Key: "ARGUS_KUBECTL_VERSION", Kind: Str, Default: "v1.34.13", Help: "kubectl version to install"},
	{Key: "ARGUS_KUBECTL_SHA256", Kind: Str, Default: "b4bb494e9466a7a94bb851414548bc0d3e2f8da90757e6eccc3371e648648591", Help: "sha256 of that kubectl (linux/amd64); a download that does not match is refused"},
	{Key: "ARGUS_KUBECTL_URL", Kind: Str, Default: "https://dl.k8s.io/release/{version}/bin/linux/amd64/kubectl", Help: "download URL ({version} is replaced); point it at an internal mirror when the host has no internet"},
	{Key: "ARGUS_KUBE_AGENT_GROUP", Kind: Str, Default: "argus", Help: "the group the agent runs in: it may read an enabled kubeconfig"},
	{Key: "ARGUS_KUBE_AUDIT_LOG", Kind: Path, Default: "${ARGUS_STATE_DIR}/kube-ro.log", Help: "one line per argus tools kube call"},
	{Key: "ARGUS_KUBE_CACHE_DIR", Kind: Path, Default: "${ARGUS_STATE_DIR}/kube-cache", Help: "kubectl discovery cache of the agent's tool"},
	// Settings -> Data sources (ADR-0017): every system Argus reads, many per type (argus sources apply/check, root)
	{Key: "ARGUS_SOURCES_DIR", Kind: Path, Default: "${ARGUS_ETC_DIR}/sources", Help: "data sources: registry.json (no secret) and <type>/<name>.cred (0600; 0640 root:<agent group> only while enabled for the agent)"},
	{Key: "ARGUS_SOURCES_ALLOW_HTTP", Kind: Bool, Default: "0", Help: "1: data sources may use plain http:// (test installations with fake services only)"},
	{Key: "ARGUS_SOURCES_AGENT_GROUP", Kind: Str, Default: "argus", Help: "the group the agent runs in: it may read the credential of a source enabled for the agent"},
	{Key: "ARGUS_SOURCES_LEGACY_ENV", Kind: Path, Default: "${ARGUS_ETC_DIR}/env", Help: "legacy KEY=value file the migration reads (GITLAB_URL, GITLAB_RO_TOKEN, GRAFANA_<ID>_URL/_TOKEN); never written"},
	{Key: "ARGUS_SOURCES_SERVICES", Kind: Path, Default: "${ARGUS_ETC_DIR}/services.json", Help: "the services the Data sources catalogue maps to ({\"prefixes\": [...], \"services\": {\"group/repo\": {\"gitlab\": path, \"domains\": [...]}}}); missing: the application projects of the CI/CD estate in ARGUS_DB"},
	// argus sources find (ADR-0017 P3): the router's threshold and the fan-out's hard limits
	{Key: "ARGUS_SOURCES_FIND_AUDIT_LOG", Kind: Path, Default: "${ARGUS_STATE_DIR}/sources-find.log", Help: "one JSON line per argus sources find: a hash of the question, the service, the chosen targets, the fan-out counts (never the question or a secret)"},
	{Key: "ARGUS_SOURCES_FIND_MIN_CONF", Kind: Str, Default: "0.6", Help: "a proposed route is a target from this confidence on (0..1)"},
	{Key: "ARGUS_SOURCES_FIND_TOP", Kind: Int, Default: "3", Min: 1, Max: 3, Help: "several routes: how many the agent is given"},
	{Key: "ARGUS_SOURCES_FANOUT_MAX_INSTANCES", Kind: Int, Default: "5", Min: 1, Max: 20, Help: "fan-out: instances searched at most"},
	{Key: "ARGUS_SOURCES_FANOUT_MAX_CALLS", Kind: Int, Default: "20", Min: 1, Max: 200, Help: "fan-out: HTTP calls at most, all instances together"},
	{Key: "ARGUS_SOURCES_FANOUT_MAX_SECONDS", Kind: Int, Default: "60", Min: 5, Max: 300, Help: "fan-out: wall time at most"},
	{Key: "ARGUS_SOURCES_FANOUT_MAX_OUTPUT", Kind: Int, Default: "6000", Min: 1000, Max: 50000, Help: "argus sources find: text output cap in bytes"},
	{Key: "GRAFANA_*_URL", Kind: Str, Pattern: true, Help: "the Grafana of one monitoring site; with the grafana-<id>-token secret it is listed as data source grafana/<id>"},
	{Key: "DEMO_MODE", Kind: Bool, Default: "0", Help: "1: sample data, every write refused"},
	{Key: "DEMO_PASSWORD", Kind: Str, Secret: true, Help: "the demo users' throwaway password"},
	{Key: "DEMO_VIEWER", Kind: Str, Default: "demo.viewer"},
	{Key: "DEMO_ADMIN", Kind: Str, Default: "demo.admin"},
	{Key: "DEMO_BANNER", Kind: Str, Default: "Demo — sample data, not a real company"},
}

// VolumeDefaults are the path defaults of ARGUS_LAYOUT=volume (ADR-0021 D1): one volume at ARGUS_STATE_DIR holds the
// databases, the state files, the saved settings, the secrets, the reports and the keys. A configured key still wins.
var VolumeDefaults = map[string]string{
	"ARGUS_ETC_DIR":         "${ARGUS_STATE_DIR}/etc",
	"ARGUS_SECRETS_DIR":     "${ARGUS_STATE_DIR}/secrets",
	"ARGUS_AUTH_DB":         "${ARGUS_STATE_DIR}/auth/auth.db",
	"ARGUS_REPORTS_DIR":     "${ARGUS_STATE_DIR}/reports/rca",
	"ARGUS_SETTINGS_IN":     "${ARGUS_STATE_DIR}/settings/in",
	"ARGUS_SETTINGS_OUT":    "${ARGUS_STATE_DIR}/settings/out",
	"ARGUS_SECRET_IN":       "${ARGUS_STATE_DIR}/secrets/in",
	"TOTP_KEY_FILE":         "${ARGUS_STATE_DIR}/keys/totp.key",
	"ARGUS_MASTER_KEY_FILE": "${ARGUS_STATE_DIR}/keys/master.key",
	"ARGUS_IMPORT_HTPASSWD": "",
	"ARGUS_CLAUDE_ENV_FILE": "${ARGUS_STATE_DIR}/secrets/claude.env",
	"ARGUS_CLAUDE_RUN_AS":   "",
}

// Value is the effective value of one key and where it came from.
type Value struct {
	Raw    string
	Source string // "default", "file", "env", or "file (OPS_DB)" / "env (OPS_DB)" for an alias
	Set    bool   // false: no default and not configured
}

// Config is the typed configuration.
type Config struct {
	CompanyName          string
	DisplayName          string
	PublicURL            string
	Hostname             string
	TZ                   string
	EtcDir               string
	SecretsDir           string
	StateDir             string
	DB                   string
	BoardDB              string
	AuthDB               string
	AlertsFile           string
	CredStatusFile       string
	SelfcheckFile        string
	ReadinessFile        string
	CredExpiryFile       string
	BackupPlansDir       string
	BackupRefDir         string
	ReportsDir           string
	SettingsIn           string
	SettingsOut          string
	SecretIn             string
	StaticDir            string
	Bind                 string
	TrustedProxies       []string
	StaleHours           int
	AdminUser            string
	AdminInitialPassword string
	Admins               []string
	ImportHtpasswd       string
	DebugSQL             bool
	NoQueryCache         bool
	OIDCIssuer           string
	OIDCClientID         string
	OIDCClientSecret     string
	DemoMode             bool
	DemoPassword         string

	File     string           // the file that was read ("" when none)
	Values   map[string]Value // by canonical key
	Warnings []string         // deprecations and unknown keys; not errors
}

// Errors lists every problem found; Load returns it as its error.
type Errors []string

func (e Errors) Error() string { return strings.Join(e, "\n") }

// Options are the inputs of Load; the zero value reads the real file and environment.
type Options struct {
	File    string   // overrides ARGUS_CONFIG and the default path
	Environ []string // KEY=value pairs; nil means os.Environ()
}

// Load reads, resolves and validates the configuration. The returned *Config is filled even when the error is
// non-nil (an Errors value), so a caller can print what it found next to the problems.
func Load(opt Options) (*Config, error) {
	envList := opt.Environ
	if envList == nil {
		envList = os.Environ()
	}
	env := map[string]string{}
	for _, kv := range envList {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	c := &Config{Values: map[string]Value{}}
	var errs Errors

	path, explicit := opt.File, opt.File != ""
	if path == "" {
		if p, ok := env["ARGUS_CONFIG"]; ok && p != "" {
			path, explicit = p, true
		} else {
			path = DefaultFile
		}
	}
	file := map[string]string{}
	if data, err := os.ReadFile(path); err == nil {
		c.File = path
		var ferrs Errors
		file, ferrs = ParseEnvFile(filepath.Base(path), data)
		errs = append(errs, ferrs...)
	} else if explicit || !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Sprintf("%s: %v", path, err))
	}

	imported := map[string]string{}
	if p := importedPath(env, file); p != "" {
		if data, err := os.ReadFile(p); err == nil {
			m, ierrs := ParseEnvFile(ImportedFile, data)
			errs = append(errs, ierrs...)
			var ignored []string
			for k, v := range m {
				if IsImportable(k) {
					imported[k] = v
				} else {
					ignored = append(ignored, k)
				}
			}
			sort.Strings(ignored)
			for _, k := range ignored {
				c.Warnings = append(c.Warnings, fmt.Sprintf("%s: %s ignored (not a setting an import carries)", ImportedFile, k))
			}
		}
	}

	known := map[string]bool{"ARGUS_CONFIG": true}
	for _, f := range Fields {
		known[f.Key] = true
		for _, a := range f.Aliases {
			known[a] = true
		}
	}
	var unknown []string
	for k := range file {
		if !known[k] && strings.HasPrefix(k, "ARGUS_") && fieldFor(k) == nil {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		c.Warnings = append(c.Warnings, fmt.Sprintf("%s: unknown key %s (a typo?)", filepath.Base(path), k))
	}

	// resolve: env canonical, env alias, file canonical, file alias, default
	for _, f := range Fields {
		if f.Pattern {
			for _, layer := range []struct {
				name string
				m    map[string]string
			}{{"imported", imported}, {"file", file}, {"env", env}} { // env last: it wins
				for k, raw := range layer.m {
					if matchPattern(f.Key, k) {
						c.Values[k] = Value{Raw: raw, Source: layer.name, Set: true}
					}
				}
			}
			continue
		}
		v, ok := Value{}, false
		for _, layer := range []struct {
			name string
			m    map[string]string
		}{{"env", env}, {"file", file}} {
			if raw, found := layer.m[f.Key]; found {
				v, ok = Value{Raw: raw, Source: layer.name, Set: true}, true
				break
			}
			for _, a := range f.Aliases {
				if raw, found := layer.m[a]; found {
					v, ok = Value{Raw: raw, Source: layer.name + " (" + a + ")", Set: true}, true
					break
				}
			}
			if ok {
				break
			}
		}
		if raw, found := imported[f.Key]; !ok && found {
			v, ok = Value{Raw: raw, Source: "imported", Set: true}, true
		}
		for _, a := range f.Aliases {
			_, inEnv := env[a]
			_, inFile := file[a]
			if !inEnv && !inFile {
				continue
			}
			if strings.HasSuffix(v.Source, "("+a+")") {
				c.Warnings = append(c.Warnings, fmt.Sprintf("%s is deprecated, rename it to %s", a, f.Key))
			} else {
				c.Warnings = append(c.Warnings, fmt.Sprintf("%s is ignored because %s is set", a, f.Key))
			}
		}
		if !ok && f.Default != "" {
			v = Value{Raw: f.Default, Source: "default", Set: true}
		}
		c.Values[f.Key] = v
	}
	// ARGUS_LAYOUT=volume: the path keys that are not configured default to places inside ARGUS_STATE_DIR
	if c.Values["ARGUS_LAYOUT"].Raw == "volume" {
		for k, d := range VolumeDefaults {
			if v := c.Values[k]; !v.Set || v.Source == "default" {
				c.Values[k] = Value{Raw: d, Source: "default", Set: true}
			}
		}
	}
	// defaults may name other keys: ${ARGUS_STATE_DIR}/ops.db; two passes resolve a default that names a default
	for pass := 0; pass < 2; pass++ {
		for k, v := range c.Values {
			if v.Source == "default" && strings.Contains(v.Raw, "${") {
				v.Raw = expandRefs(v.Raw, c.Values)
				c.Values[k] = v
			}
		}
	}
	// the board database lives next to the evidence database unless it is set
	if v, db := c.Values["ARGUS_BOARD_DB"], c.Values["ARGUS_DB"].Raw; !v.Set && filepath.IsAbs(db) {
		c.Values["ARGUS_BOARD_DB"] = Value{Raw: filepath.Join(filepath.Dir(db), "board", "board.db"), Source: "default", Set: true}
	}
	errs = append(errs, c.fill()...)
	if len(errs) > 0 {
		return c, errs
	}
	return c, nil
}

// ImportedFile is the file in ARGUS_ETC_DIR that `argus import` writes and the volume layout reads.
const ImportedFile = "imported.env"

// Importable are the keys `argus import` carries from an earlier installation's env files: names, time zone, the
// integrations' addresses, rooms and project keys. Never a secret, a path, a listen address or a sign-in setting.
var Importable = []string{"COMPANY_NAME", "ARGUS_DISPLAY_NAME", "ARGUS_TZ", "ARGUS_TZ_LABEL", "ENV_LABELS", "PUBLIC_DOMAIN",
	"OBS_SITES", "OBS_SITE_*_LABEL", "GITLAB_URL", "CICD_GROUPS", "CICD_ENGINE_PROJECT", "CICD_ENGINE_PROJECT_ID",
	"CICD_STUCK_MINUTES", "BACKUP_TEMPLATES_REPO", "OUTLINE_URL", "JIRA_PROJECT", "MATRIX_HOMESERVER", "MATRIX_SERVER_NAME",
	"MATRIX_QA_ROOM_LABEL", "MATRIX_NOTIFY_ROOM", "MATRIX_WATCH_ROOMS", "MATRIX_ALERT_ROOMS", "MATRIX_KUMA_ROOMS",
	"ARGUS_NOTIFY_MATRIX", "TELEGRAM_CHAT_ID", "ARGUS_CERT_WARN_DAYS", "ARGUS_CERT_FAIL_DAYS", "FEATURE_TODAY", "FEATURE_PROBLEMS",
	"ARGUS_INCIDENT_AUTORESOLVE_HOURS", "ARGUS_INCIDENT_CHAT_RESOLVE_HOURS", "ARGUS_INCIDENT_REOPEN_DAYS", "ARGUS_SELFCHECK_PAGE_AFTER", "ARGUS_CRED_WARN_DAYS", "ARGUS_CRED_CRIT_DAYS", "ARGUS_CRED_ROTATE_DAYS", "ARGUS_CRED_TODAY_DAYS"}

// IsImportable says whether key (a canonical name, or a name matching a pattern key) is one of Importable.
func IsImportable(key string) bool {
	for _, k := range Importable {
		if k == key || strings.Contains(k, "*") && matchPattern(k, key) {
			if f := fieldFor(key); f != nil && !f.Secret && f.Kind != Path && f.Kind != Addr {
				return true
			}
		}
	}
	return false
}

// Canonical is the canonical name of key, which may be an alias (OPS_DB -> ARGUS_DB); key itself when it is canonical
// or matches a pattern key; empty when unknown.
func Canonical(key string) string {
	if fieldFor(key) != nil {
		return key
	}
	for _, f := range Fields {
		if contains(f.Aliases, key) {
			return f.Key
		}
	}
	return ""
}

// importedPath is ARGUS_ETC_DIR/imported.env in ARGUS_LAYOUT=volume (from the environment and argus.env, before the
// keys are resolved); empty in the legacy layout.
func importedPath(env, file map[string]string) string {
	raw := func(key string) (string, bool) {
		f := fieldFor(key)
		for _, m := range []map[string]string{env, file} {
			if v, ok := m[key]; ok {
				return v, true
			}
			for _, a := range f.Aliases {
				if v, ok := m[a]; ok {
					return v, true
				}
			}
		}
		return f.Default, false
	}
	if l, _ := raw("ARGUS_LAYOUT"); l != "volume" {
		return ""
	}
	etc, set := raw("ARGUS_ETC_DIR")
	if !set || etc == "" {
		sd, _ := raw("ARGUS_STATE_DIR")
		etc = filepath.Join(sd, "etc")
	}
	if !filepath.IsAbs(etc) {
		return ""
	}
	return filepath.Join(etc, ImportedFile)
}

// matchPattern reports whether name matches a pattern key with one "*" standing for [A-Z0-9_]+.
func matchPattern(pattern, name string) bool {
	pre, suf, ok := strings.Cut(pattern, "*")
	if !ok || len(name) <= len(pre)+len(suf) || !strings.HasPrefix(name, pre) || !strings.HasSuffix(name, suf) {
		return false
	}
	for _, r := range name[len(pre) : len(name)-len(suf)] {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

var refRe = regexp.MustCompile(`\$\{([A-Z0-9_]+)\}`)

func expandRefs(s string, vals map[string]Value) string {
	return refRe.ReplaceAllStringFunc(s, func(m string) string {
		return vals[m[2:len(m)-1]].Raw
	})
}

var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseEnvFile parses KEY=value lines. Blank lines and lines starting with "#" are skipped; one pair of matching
// single or double quotes around a value is removed; nothing is expanded. name prefixes the error messages.
func ParseEnvFile(name string, data []byte) (map[string]string, Errors) {
	out := map[string]string{}
	line := map[string]int{}
	var errs Errors
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		k, v, ok := strings.Cut(s, "=")
		k = strings.TrimSpace(k)
		if !ok {
			errs = append(errs, fmt.Sprintf("%s:%d: expected KEY=value", name, n))
			continue
		}
		if !keyRe.MatchString(k) {
			errs = append(errs, fmt.Sprintf("%s:%d: %q is not a valid key name", name, n, k))
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		if first, dup := line[k]; dup {
			errs = append(errs, fmt.Sprintf("%s:%d: %s is already set on line %d", name, n, k, first))
			continue
		}
		line[k] = n
		out[k] = v
	}
	if err := sc.Err(); err != nil {
		errs = append(errs, fmt.Sprintf("%s: %v", name, err))
	}
	return out, errs
}

// fill parses every value into the struct and validates it.
func (c *Config) fill() Errors {
	var errs Errors
	bad := func(f Field, format string, a ...any) {
		v := c.Values[f.Key]
		where := v.Source
		if where == "" {
			where = "not set"
		}
		errs = append(errs, fmt.Sprintf("%s: %s (%s)", f.Key, fmt.Sprintf(format, a...), where))
	}
	field := map[string]Field{}
	var keys []string
	for _, f := range Fields {
		field[f.Key] = f
		if !f.Pattern {
			keys = append(keys, f.Key)
		}
	}
	for _, k := range c.patternKeys() {
		keys = append(keys, k)
	}
	for _, k := range keys {
		f := *fieldFor(k)
		f.Key = k
		v := c.Values[k]
		raw := v.Raw
		if f.Required && strings.TrimSpace(raw) == "" {
			bad(f, "required")
			continue
		}
		if raw == "" {
			continue
		}
		if len(f.Choices) > 0 && !contains(f.Choices, raw) {
			bad(f, "must be one of %s, got %q", strings.Join(f.Choices, ", "), raw)
		}
		switch f.Kind {
		case Path:
			if !filepath.IsAbs(raw) {
				bad(f, "must be an absolute path, got %q", raw)
			}
		case URL:
			if msg := checkURL(raw); msg != "" {
				bad(f, "%s", msg)
			}
		case Int:
			if n, err := strconv.Atoi(strings.TrimSpace(raw)); err != nil {
				bad(f, "must be an integer, got %q", raw)
			} else if f.Max > 0 && (n < f.Min || n > f.Max) {
				bad(f, "must be between %d and %d, got %d", f.Min, f.Max, n)
			}
		case Bool:
			if raw != "0" && raw != "1" {
				bad(f, "must be 0 or 1, got %q", raw)
			}
		case Addr:
			host, port, err := net.SplitHostPort(raw)
			p, perr := strconv.Atoi(port)
			if err != nil || perr != nil || p < 1 || p > 65535 {
				bad(f, "must be host:port, got %q", raw)
			} else if host != "" && net.ParseIP(host) == nil && !hostnameRe.MatchString(host) {
				bad(f, "bad host %q", host)
			}
		case Zone:
			if _, err := time.LoadLocation(raw); err != nil {
				bad(f, "unknown time zone %q", raw)
			}
		}
	}
	s := func(k string) string { return c.Values[k].Raw }
	list := func(k string) []string {
		var out []string
		for _, x := range strings.Split(s(k), ",") {
			if x = strings.TrimSpace(x); x != "" {
				out = append(out, x)
			}
		}
		return out
	}
	c.CompanyName, c.DisplayName, c.PublicURL = s("COMPANY_NAME"), s("ARGUS_DISPLAY_NAME"), s("ARGUS_PUBLIC_URL")
	c.Hostname, c.TZ, c.EtcDir, c.SecretsDir = s("ARGUS_HOSTNAME"), s("ARGUS_TZ"), s("ARGUS_ETC_DIR"), s("ARGUS_SECRETS_DIR")
	c.StateDir, c.DB, c.BoardDB, c.AuthDB = s("ARGUS_STATE_DIR"), s("ARGUS_DB"), s("ARGUS_BOARD_DB"), s("ARGUS_AUTH_DB")
	c.AlertsFile, c.CredStatusFile = s("ARGUS_ALERTS_FILE"), s("ARGUS_CRED_STATUS_FILE")
	c.SelfcheckFile, c.ReadinessFile, c.CredExpiryFile = s("ARGUS_SELFCHECK_FILE"), s("ARGUS_READINESS_FILE"), s("ARGUS_CRED_EXPIRY_FILE")
	c.BackupPlansDir, c.BackupRefDir, c.ReportsDir = s("ARGUS_BACKUP_PLANS_DIR"), s("ARGUS_BACKUP_REF_DIR"), s("ARGUS_REPORTS_DIR")
	c.SettingsIn, c.SettingsOut, c.SecretIn = s("ARGUS_SETTINGS_IN"), s("ARGUS_SETTINGS_OUT"), s("ARGUS_SECRET_IN")
	c.StaticDir, c.Bind, c.TrustedProxies = s("ARGUS_STATIC_DIR"), s("ARGUS_BIND"), list("ARGUS_TRUSTED_PROXIES")
	c.StaleHours, _ = strconv.Atoi(strings.TrimSpace(s("ARGUS_STALE_HOURS")))
	c.AdminUser, c.AdminInitialPassword, c.Admins = s("ARGUS_ADMIN_USER"), s("ARGUS_ADMIN_INITIAL_PASSWORD"), list("ARGUS_ADMINS")
	c.ImportHtpasswd, c.DebugSQL, c.NoQueryCache = s("ARGUS_IMPORT_HTPASSWD"), s("ARGUS_DEBUG_SQL") == "1", s("ARGUS_NO_QUERY_CACHE") == "1"
	c.OIDCIssuer, c.OIDCClientID, c.OIDCClientSecret = s("OIDC_ISSUER"), s("OIDC_CLIENT_ID"), s("OIDC_CLIENT_SECRET")
	c.DemoMode, c.DemoPassword = s("DEMO_MODE") == "1", s("DEMO_PASSWORD")

	// rules that look at more than one key, or at the content
	if n := len([]rune(c.CompanyName)); c.CompanyName != "" && (n > 60 || !printable(c.CompanyName)) {
		bad(field["COMPANY_NAME"], "must be 1-60 printable characters")
	}
	if n := len([]rune(c.DisplayName)); n == 0 || n > 30 {
		bad(field["ARGUS_DISPLAY_NAME"], "must be 1-30 characters")
	}
	if c.PublicURL == "" && !c.DemoMode {
		bad(field["ARGUS_PUBLIC_URL"], "required (unless DEMO_MODE=1)")
	}
	if c.Hostname != "" && !labelRe.MatchString(c.Hostname) {
		bad(field["ARGUS_HOSTNAME"], "must be one host name label (RFC 1123), got %q", c.Hostname)
	}
	if c.AdminUser == "" || !userRe.MatchString(c.AdminUser) {
		bad(field["ARGUS_ADMIN_USER"], "must be a user name ([a-z0-9._-], 1-64), got %q", c.AdminUser)
	}
	for _, u := range c.Admins {
		if !userRe.MatchString(u) {
			bad(field["ARGUS_ADMINS"], "bad user name %q", u)
		}
	}
	for _, p := range c.TrustedProxies {
		if net.ParseIP(p) == nil {
			if _, _, err := net.ParseCIDR(p); err != nil {
				bad(field["ARGUS_TRUSTED_PROXIES"], "%q is not an IP address or CIDR", p)
			}
		}
	}
	if s("ARGUS_COLLECT_ALERTS") == "1" {
		if len(list("ARGUS_ALERTS_SOURCES")) == 0 {
			bad(field["ARGUS_ALERTS_SOURCES"], "required when ARGUS_COLLECT_ALERTS=1")
		}
		for _, e := range list("ARGUS_ALERTS_SOURCES") {
			if name, u, ok := strings.Cut(e, "="); !ok || name == "" || !(strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")) {
				bad(field["ARGUS_ALERTS_SOURCES"], "want name=http(s)://..., got %q", e)
			}
		}
	}
	if s("ARGUS_COLLECT_CERTS") == "1" && len(list("ARGUS_CERT_TARGETS")) == 0 && len(list("ARGUS_CERT_FILES")) == 0 {
		bad(field["ARGUS_CERT_TARGETS"], "ARGUS_CERT_TARGETS or ARGUS_CERT_FILES is required when ARGUS_COLLECT_CERTS=1")
	}
	for _, p := range list("ARGUS_CERT_FILES") {
		if !filepath.IsAbs(p) {
			bad(field["ARGUS_CERT_FILES"], "must be absolute paths, got %q", p)
		}
	}
	if w, f := s("ARGUS_CERT_WARN_DAYS"), s("ARGUS_CERT_FAIL_DAYS"); w != "" && f != "" {
		if wi, err1 := strconv.Atoi(w); err1 == nil {
			if fi, err2 := strconv.Atoi(f); err2 == nil && fi > wi {
				bad(field["ARGUS_CERT_FAIL_DAYS"], "must not be above ARGUS_CERT_WARN_DAYS (%d)", wi)
			}
		}
	}
	if c.OIDCIssuer != "" || c.OIDCClientID != "" || c.OIDCClientSecret != "" {
		for _, k := range []string{"OIDC_ISSUER", "OIDC_CLIENT_ID", "OIDC_CLIENT_SECRET"} {
			if s(k) == "" {
				bad(field[k], "required when single sign-on is configured")
			}
		}
	}
	return errs
}

var (
	labelRe    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	userRe     = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)
)

func printable(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func checkURL(raw string) string {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return fmt.Sprintf("not a URL: %q", raw)
	case u.Scheme != "https":
		return fmt.Sprintf("must start with https://, got %q", raw)
	case u.Host == "":
		return fmt.Sprintf("has no host: %q", raw)
	case u.User != nil:
		return "must not contain a user or password"
	case u.RawQuery != "" || u.Fragment != "":
		return fmt.Sprintf("must not have a query or fragment: %q", raw)
	}
	return ""
}

// Masked is the printable form of a key's value: secrets show only whether they are set.
func (c *Config) Masked(key string) string {
	v := c.Values[key]
	if f := fieldFor(key); f != nil && f.Secret {
		if v.Raw == "" {
			return ""
		}
		return "******"
	}
	return v.Raw
}

// fieldFor is the schema entry of a canonical key or of a name matching a pattern key; nil when unknown.
func fieldFor(key string) *Field {
	for i := range Fields {
		if f := &Fields[i]; f.Key == key || f.Pattern && matchPattern(f.Key, key) {
			return f
		}
	}
	return nil
}

// patternKeys are the configured names that match a pattern key, sorted.
func (c *Config) patternKeys() []string {
	var out []string
	for k := range c.Values {
		if f := fieldFor(k); f != nil && f.Pattern {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Keys are the printable keys in schema order; a pattern key is replaced by the names that match it.
func (c *Config) Keys() []string {
	var out []string
	for _, f := range Fields {
		if f.Pattern {
			for _, k := range c.patternKeys() {
				if matchPattern(f.Key, k) {
					out = append(out, k)
				}
			}
			continue
		}
		out = append(out, f.Key)
	}
	return out
}

// Print writes the effective configuration, one KEY=value per line with its source; secrets are masked.
func (c *Config) Print(w io.Writer) {
	width := 0
	keys := c.Keys()
	for _, k := range keys {
		if n := len(k) + 1 + len(c.Masked(k)); n > width {
			width = n
		}
	}
	for _, k := range keys {
		v := c.Values[k]
		src := v.Source
		if !v.Set {
			src = "not set"
		}
		line := k + "=" + c.Masked(k)
		fmt.Fprintf(w, "%-*s  # %s\n", width, line, src)
	}
}

// Entry is one key of the effective configuration in machine-readable form (argus config show -json).
type Entry struct {
	Key    string `json:"key"`
	Value  string `json:"value"`            // masked for secrets
	Source string `json:"source"`           // default, file, env, alias, or unset
	Alias  string `json:"alias,omitempty"`  // the deprecated name the value was read from
	Secret bool   `json:"secret,omitempty"` // the value is masked
}

// Entries is the effective configuration, one Entry per key in Keys order.
func (c *Config) Entries() []Entry {
	var out []Entry
	for _, k := range c.Keys() {
		v := c.Values[k]
		e := Entry{Key: k, Value: c.Masked(k), Secret: fieldFor(k).Secret, Source: "unset"}
		if v.Set {
			e.Source = v.Source
			if _, alias, ok := strings.Cut(v.Source, " ("); ok {
				e.Source, e.Alias = "alias", strings.TrimSuffix(alias, ")")
			}
		}
		out = append(out, e)
	}
	return out
}

// SchemaJSON is the key schema as indented JSON (config/schema.json; a test keeps the file equal to this).
func SchemaJSON() []byte {
	kinds := map[Kind]string{Str: "str", Path: "path", URL: "url", Int: "int", Bool: "bool", List: "list", Addr: "addr", Zone: "zone"}
	type key struct {
		Key      string   `json:"key"`
		Aliases  []string `json:"aliases,omitempty"`
		Type     string   `json:"type"`
		Default  string   `json:"default,omitempty"`
		Required bool     `json:"required,omitempty"`
		Secret   bool     `json:"secret,omitempty"`
		Min      *int     `json:"min,omitempty"`
		Max      *int     `json:"max,omitempty"`
		Pattern  bool     `json:"pattern,omitempty"`
		Enum     []string `json:"enum,omitempty"`
		Help     string   `json:"help,omitempty"`
	}
	var keys []key
	for _, f := range Fields {
		k := key{Key: f.Key, Aliases: f.Aliases, Type: kinds[f.Kind], Default: f.Default, Required: f.Required,
			Secret: f.Secret, Pattern: f.Pattern, Enum: f.Choices, Help: f.Help}
		if f.Max > 0 {
			lo, hi := f.Min, f.Max
			k.Min, k.Max = &lo, &hi
		}
		keys = append(keys, k)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	enc.Encode(struct {
		Comment string `json:"$comment"`
		Keys    []key  `json:"keys"`
	}{"Generated by `argus config schema` from internal/config; do not edit.", keys})
	return b.Bytes()
}

// --- the process-wide configuration (argus-api reads every setting here) -------------------------------------------

var (
	procOnce sync.Once
	procCfg  *Config
	procErr  error
)

// Process is Load(Options{}) of this process, done once: the real file and the real environment. Package-level
// settings of the web app read it at start; main checks the error and stops on an invalid configuration.
func Process() (*Config, error) {
	procOnce.Do(func() { procCfg, procErr = Load(Options{}) })
	return procCfg, procErr
}

// Lookup is the effective value of a canonical key (or of a name matching a pattern key) in the process
// configuration. It panics on a name the schema does not know, so a setting read under an old or misspelt name
// fails every test instead of silently reading nothing.
func Lookup(key string) Value {
	if fieldFor(key) == nil {
		panic("config: unknown key " + key + " (add it to config.Fields)")
	}
	c, _ := Process()
	return c.Values[key]
}

// Known reports whether the schema has the key (canonical, or matching a pattern key).
func Known(key string) bool { return fieldFor(key) != nil }

// Get is Lookup's value and whether it is set (configured, or a default exists).
func Get(key string) (string, bool) {
	v := Lookup(key)
	return v.Raw, v.Set
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
