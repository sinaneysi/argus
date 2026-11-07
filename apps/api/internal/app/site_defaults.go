package app

// Defaults for the site configuration (site.go): neutral example values only. Every company-specific value
// comes from the environment (see docs/architecture/CONFIG.md and demo/compose.yml for an example).

import (
	"regexp"
	"time"
)

const (
	defCompanyName         = "Example"
	defArgusHostname       = "argus-01"
	defArgusSSHAlias       = "argus-01"
	defArgusRepo           = "ops/argus"
	defQARoomLabel         = "QA"
	defMatrixServerName    = "chat.example.com"
	defGitlabURL           = "https://gitlab.example.com"
	defCicdGroup           = "example"
	defCicdEngineProject   = "example/deployment"
	defCicdEngineProjectID = "100"
	defBackupTemplatesRepo = "ops/backup-templates"
	defOutlineURL          = "https://docs.example.com"
	defPublicDomain        = "example.com"
	defJiraProject         = "OPS"
	defTZLabel             = "UTC"
	defObsSites            = "east,west"
	defAdmins              = ""
	defHelpSiteTags        = "the datacenter's short tag"
	defHelpChatFrom        = ""
	defHelpChatYes         = ""
	defIssueDeadTargets    = ""
	defIssueUnencrypted    = "unencrypted"
	defBackupGapExamples   = "Redis, queues, search, ..."
	defStageDockerLabel    = "dev docker"
	defHelpChatNo          = ""
	defTrustedProxies      = "127.0.0.1,::1"
)

var defTZ = time.UTC

// defEnvPrefixes: RFC 1918 example networks, first match wins.
var defEnvPrefixes = [][2]string{{"10.20.10.", "develop"}, {"10.20.20.", "pre-release"}, {"10.30.20.", "pre-release"},
	{"10.20.30.", "release"}, {"10.30.30.", "release"}}

// fixerMarkers: production names in a fix prompt (hosts prod-*, the production networks of defEnvPrefixes).
var fixerMarkers = []fixerMarker{
	{regexp.MustCompile(`(?i)prod-[a-z0-9](?:` + fixerW + `|-)*`), true, nil},
	{regexp.MustCompile(`(?i)10\.[23]0\.30\.\p{Nd}{1,3}`), true, fixerNoDigitAfter},
}

func fixerCompanyDomains(string) []string { return nil }
