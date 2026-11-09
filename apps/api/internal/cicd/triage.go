package cicd

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// Failure triage: an ordered rule table over the redacted log tail (specific causes before generic symptoms), then
// GitLab's own failure_reason for runner problems. A log that matches nothing is "unknown".
// TODO: ask the model (internal/model, with style.Prepend) for an "unknown" log, at most CICD_LLM_MAX_DAY a day.

// Owner is who acts on a class.
var Owner = map[string]string{"code": "dev team", "package": "dev team", "lockfile": "devops", "variable": "devops",
	"registry": "devops", "runner": "devops", "ssh": "devops", "deploy": "devops", "health": "dev team + devops",
	"lock": "devops", "flaky": "-", "canceled": "-", "unknown": "devops"}

// Label is the class shown in titles.
var Label = map[string]string{"code": "code / build / test", "package": "package version", "lockfile": "lockfile / dependency drift",
	"variable": "missing or wrong variable", "registry": "registry / push / auth", "runner": "runner / infra",
	"ssh": "ssh / ansible to host", "deploy": "deploy step", "health": "deploy health check",
	"lock": "lock / resource_group", "flaky": "flaky (passed on retry)", "canceled": "canceled", "unknown": "unknown"}

var runnerReasons = map[string]bool{"runner_system_failure": true, "stuck_or_timeout_failure": true, "scheduler_failure": true,
	"api_failure": true, "runner_unsupported": true, "data_integrity_failure": true, "job_execution_timeout": true,
	"unmet_prerequisites": true, "archived_failure": true, "stale_schedule": true}

type matcher interface {
	MatchString(string) bool
}

type rule struct {
	name, klass string
	rx          matcher
	why         string
}

// enoentRule: an ENOENT on open/scandir/mkdir of a path that is not inside a deployed dependency tree
// (RE2 has no look-ahead).
type enoentRule struct{}

var enoentRe = regexp.MustCompile(`ENOENT: no such file or directory, (?:open|scandir|mkdir) '([^']*)`)

func (enoentRule) MatchString(s string) bool {
	for _, m := range enoentRe.FindAllStringSubmatchIndex(s, -1) {
		rest := s[m[2]:]
		if !(strings.HasPrefix(rest, "/data/services/") && strings.Contains(s[m[2]+len("/data/services/"):m[3]], "node_modules")) {
			return true
		}
	}
	return false
}

// unitRule: the first deploy without a unit: the "Verify every target ... already loaded" task fails within the next
// nine lines before another task starts, or systemd says the unit is not found.
type unitRule struct{}

var unitNotFound = regexp.MustCompile(`Unit [A-Za-z0-9@._-]+\.service (?:could not be found|not found)`)

func (unitRule) MatchString(s string) bool {
	if unitNotFound.MatchString(s) {
		return true
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if !strings.Contains(l, "Verify every target without a supplied unit is already loaded") || i == len(lines)-1 {
			continue
		}
		for k := i + 1; k < len(lines) && k <= i+9; k++ {
			if strings.HasPrefix(lines[k], "TASK [") {
				break
			}
			if strings.Contains(lines[k], "fatal:") || strings.Contains(lines[k], "FAILED!") {
				return true
			}
		}
	}
	return false
}

func re(p string) *regexp.Regexp { return regexp.MustCompile("(?m)" + p) }

// Rules is the ordered rule table. Order matters: specific causes before generic symptoms.
var Rules = []rule{
	{"no-hosts-to-target", "variable", re(`leaves us with no hosts to target|Could not match supplied host pattern`),
		"the deploy resolved no inventory hosts: TARGET_HOSTS@<env> (or ENVIRONMENT) is missing or names no group in the inventory"},
	{"dotenv-base64", "variable", re(`(?i)invalid base64|base64: invalid input|DOTENV[A-Z_]* (?:is )?(?:empty|missing|not set)`),
		"DOTENV / DOTENV_<MODE> is missing, empty or not valid base64 (a File variable is expected)"},
	{"env-undefined", "variable", re(`(?i)(?:required|missing) (?:env(?:ironment)? )?variable|variable ` + "[`'\"]?[A-Z_]{3,}[`'\"]?" +
		` (?:is )?(?:not set|undefined|missing|empty)|unbound variable|\b[A-Z_]{3,}: parameter (?:null or )?not set|` +
		`\b[A-Z][A-Z0-9_]{2,} must (?:point to|be set|be defined|be a |contain)`),
		"a required CI or runtime variable is not set in this scope"},
	{"mongo-uri", "variable", re(`MongoParseError|Invalid scheme, expected connection string|connection string names no database|URI malformed|Invalid connection string`),
		"the service's connection string from DOTENV is malformed or empty in this environment"},
	{"db-auth", "variable", re(`(?i)(?:mongo|mysql|postgres|maria|redis|database|_db\b|DATABASE_ERROR|Sequelize)[^\n]{0,200}` +
		`(?:authentication failed|auth failed|access denied)|password authentication failed|Access denied for user|` +
		`(?:authentication failed|auth failed)[^\n]{0,120}(?:mongo|database|_db\b)`),
		"the database credentials in DOTENV are rejected in this environment"},
	{"ssh-key-format", "ssh", re(`(?i)load key .*: (?:invalid format|error in libcrypto)`),
		"SSH_PRIVATE_KEY_<ENV> is not a valid key (File variable needs a trailing newline)"},
	{"ssh-unreachable", "ssh", re(`UNREACHABLE!|Permission denied \(publickey|ssh: connect to host|Failed to connect to the host via ssh|banner exchange|No route to host`),
		"ansible could not reach the target host over ssh"},
	{"registry-auth", "registry", re(`(?i)denied: requested access|unauthorized: (?:authentication required|access to the requested)|pull access denied|HTTP Basic: Access denied|401 Unauthorized.*(?:registry|/v2/)`),
		"registry login/push/pull rejected (the registry token or account)"},
	{"registry-missing-image", "registry", re(`No such image: |manifest unknown|manifest for .* not found|failed to push|blob upload unknown|toomanyrequests`),
		"the image was not pushed or cannot be pulled from the registry"},
	{"placeholder-image", "deploy", re(`still references the placeholder image|was not updated for [0-9T]+Z?-`),
		"the stack file on the host still has the placeholder image: its compose file does not use the image variable of the CI engine"},
	{"candidate-missing", "deploy", re(`Candidate is missing or not on the application filesystem`),
		"the deploy found no candidate (the build step did not finish or the candidate was removed); never retry the deploy job"},
	{"mv-overwrite", "deploy", re(`mv: cannot overwrite`),
		"the two-move publish found a leftover directory at the target path"},
	{"docker-daemon", "runner", re(`Cannot connect to the Docker daemon|Could not find a working container runtime strategy|error during connect: .*docker`),
		"the job needs Docker but the runner gives none (testcontainers, DinD)"},
	{"disk-full", "runner", re(`(?i)no space left on device|\bENOSPC\b|disk quota exceeded|out of inodes|inodes? (?:exhausted|are full)`),
		"the runner or build host ran out of disk or inodes"},
	{"runner-dns", "runner", re(`(?i)temporary failure in name resolution|dial tcp: lookup .*(?:no such host|i/o timeout|server misbehaving)|getaddrinfo E(?:AI_AGAIN|NOTFOUND)|read udp 127\.0\.0\.53`),
		"DNS on the runner failed"},
	{"service-health", "runner", re(`FATAL: No HOST or PORT found|Health check error`),
		"a CI service container did not start on the runner"},
	{"debian-mirror", "runner", re(`(?i)(?:404\s+Not Found|Unable to fetch some archives|does not have a Release file).*(?:debian|security)|E: Failed to fetch .*debian`),
		"the Debian/security package mirror returned 404 or nothing"},
	{"node14-syntax", "lockfile", re(`SyntaxError: Unexpected token '(?:\|\|=|\?\?=|&&=|\?\.)'`),
		"a dependency needs a newer Node than the env runs: set NODE_VERSION or pin the dependency"},
	{"lock-out-of-sync", "lockfile", re(`(?i)can only install packages when your package\.json and package-lock\.json|Missing: .* from lock file|` +
		`lockfile (?:needs to be updated|is out of date)|frozen-lockfile|ERR_PNPM_OUTDATED_LOCKFILE|npm reported success but these production dependencies are not installed`),
		"package-lock.json is missing or out of sync with package.json"},
	{"dep-resolve", "lockfile", re(`ERESOLVE|ETARGET|No matching version found for|npm (?:ERR!|error) code E404|404 Not Found - GET .*registry|notarget`),
		"a dependency version cannot be resolved from the package registry (drift or an unpublished internal package)"},
	{"node-gyp", "lockfile", re(`(?i)gyp ERR!|node-pre-gyp|prebuild-install (?:warn|err)|make: \*\*\*.*Error`),
		"a native module needs headers/binaries (node-gyp) that the build cannot download (no egress: use a proxy for the node headers)"},
	{"heap", "code", re(`JavaScript heap out of memory|Reached heap limit|FATAL ERROR: .*Allocation failed`),
		"the build ran out of V8 heap (NODE_OPTIONS=--max-old-space-size or a smaller build)"},
	{"publish-version", "package", re(`Publishing the same version again is not allowed|version must be in x\.y\.z|cannot publish over the previously published|is not a valid npm option`),
		"npm publish rules: bump the version (x.y.z on release, x.y.z-alpha.N elsewhere)"},
	{"unit-missing", "variable", unitRule{},
		"first deploy without a unit: SYSTEMD_UNIT_FILE@<env> (File variable) is missing and no unit exists on the host yet"},
	{"systemd-vars", "variable", re(`(?i)invalid (?:systemd|SYSTEMD_[A-Z_]+)|SYSTEMD_BASE_DIR .*(?:invalid|must)`),
		"a SYSTEMD_* CI variable is invalid for this app (multi-mode, base dir)"},
	{"native-abi", "lockfile", re(`No native build was found|was compiled against a different Node\.js version|NODE_MODULE_VERSION`),
		"a native module was built for another Node ABI than the env runs (rebuild under the env's NODE_VERSION or pin)"},
	{"mongo-timeout", "health", re(`MongoNetworkTimeoutError|MongoServerSelectionError|Server selection timed out`),
		"the service cannot reach its Mongo (network or firewall) or the URI names the wrong hosts"},
	{"runtime-enoent", "code", enoentRule{},
		"the app opens a relative or missing runtime path (a log dir, a config file) that does not exist under the deployed dir"},
	{"unhealthy", "health", re(`did not become healthy|Main process exited|Failed with result '(?:exit-code|signal|timeout)'|is not active|health ?check (?:failed|timed out)|Startup failed|failed to start`),
		"the service started by the deploy did not stay healthy: the journal lines above show why"},
	{"ts-error", "code", re(`error TS[0-9]{3,5}:|SyntaxError: |TypeError: |ReferenceError: |Cannot find module '|Module not found: Error|ESLint|✖ [0-9]+ problems|Tests?:\s+[0-9]+ failed|FAIL\s+\S+\.(?:spec|test)\.|AssertionError`),
		"the application code does not build or its tests fail"},
	{"missing-file", "code", re(`cp: (?:no such file or directory|cannot stat)|ENOENT: no such file or directory`),
		"a file the build copies does not exist in the repository"},
	{"npm-script", "code", re(`npm ERR! Missing script|Missing script: "start"|npm error Missing script`),
		"package.json has no such script (BUILD_SCRIPTS or start)"},
	{"candidate-build", "code", re(`Stop after candidate preparation failure|Stop with every failed parallel mode build|candidate preparation failed`),
		"the build step could not prepare the candidate (clone, install or build failed on the target host): the first npm/build error above it is the cause"},
	{"build-failed", "code", re(`did not complete successfully: exit code|ELIFECYCLE|Command failed with exit code|npm (?:ERR!|error) code [1-9]`),
		"the build command failed (see the first error above it)"},
}

var noiseRe = regexp.MustCompile(`(?i)^\s*(?:ERROR: No files to upload|ERROR: Job failed: exit code|Cleaning up|Uploading artifacts|` +
	`INFO: Treating unresolved|section_end|\s*$)`)

var (
	normHex  = regexp.MustCompile(`\b[0-9a-f]{7,64}\b`)
	normNum  = regexp.MustCompile(`\b[0-9]+(?:\.[0-9]+)*\b`)
	normTime = regexp.MustCompile(`\d{4}-\d\d-\d\dT[\d:.+]+`)
	normHost = regexp.MustCompile(`\[[A-Za-z0-9._-]+\]`)
	normWS   = regexp.MustCompile(`\s+`)
)

func norm(line string) string {
	s := normHex.ReplaceAllString(line, "<h>")
	s = normNum.ReplaceAllString(s, "<n>")
	s = normTime.ReplaceAllString(s, "<t>")
	s = normHost.ReplaceAllString(s, "[<host>]")
	return runeHead(strings.TrimSpace(normWS.ReplaceAllString(s, " ")), 200)
}

// Signature is the dedupe key of one failure cause.
func Signature(project, job, klass, line string) string {
	h := sha1.Sum([]byte(project + "|" + job + "|" + klass + "|" + norm(line)))
	return hex.EncodeToString(h[:])[:16]
}

func keyLine(trace string, rx matcher) string {
	last := ""
	for _, l := range splitLines(trace) {
		if rx.MatchString(l) && !noiseRe.MatchString(l) {
			last = l
		}
	}
	return runeHead(strings.TrimSpace(last), 400)
}

// Class is the triage of one failed job.
type Class struct {
	Klass, Rule, Line, Why, Owner, Signature string
	LLM                                      int
	Fix                                      string
}

// Job is the fields of a GitLab job the triage reads.
type Job struct {
	ID            int64
	Name, Status  string
	FailureReason string
	StartedAt     string
	AllowFailure  bool
	Downstream    bool // a bridge to a downstream pipeline (no log of its own)
	Stage         string
	Username      string
}

func jobOf(m map[string]any) Job {
	j := Job{ID: toI(m["id"]), Name: str(m["name"]), Status: str(m["status"]), FailureReason: str(m["failure_reason"]),
		StartedAt: str(m["started_at"]), Stage: str(m["stage"])}
	j.AllowFailure, _ = m["allow_failure"].(bool)
	_, j.Downstream = m["downstream_pipeline"]
	if u, ok := m["user"].(map[string]any); ok {
		j.Username = str(u["username"])
	}
	return j
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprint(int64(x))
		}
	}
	return fmt.Sprint(v)
}

// Classify is the triage of one job's log tail.
func Classify(trace string, j Job, project string) Class {
	if j.Status == "canceled" {
		k, rname, line, why := "canceled", "canceled", "canceled", "canceled by a person"
		if j.StartedAt == "" && (j.Name == "trigger_deploy" || j.Name == "deploy_systemd") {
			k, rname, line, why = "lock", "canceled-waiting", "canceled while waiting for the deploy lock", "the job was canceled before it started (a lock wait)"
		}
		return out(project, j.Name, k, rname, line, why)
	}
	for _, r := range Rules {
		if r.rx.MatchString(trace) {
			return out(project, j.Name, r.klass, r.name, orS(keyLine(trace, r.rx), r.name), r.why)
		}
	}
	fr := j.FailureReason
	if runnerReasons[fr] {
		why := map[string]string{"stuck_or_timeout_failure": "the job got stuck or hit its timeout (no runner, or a hung step)",
			"job_execution_timeout": "the job hit its timeout"}[fr]
		if why == "" {
			why = "GitLab failure reason " + fr + ": the runner, not the code"
		}
		return out(project, j.Name, "runner", "gitlab:"+fr, fr, why)
	}
	if fr == "downstream_pipeline_creation_failed" {
		return out(project, j.Name, "deploy", "downstream-creation", fr,
			"the deploy pipeline in the CI engine project could not be created (variables forwarded or permissions)")
	}
	if fr == "downstream_failed_no_job" {
		return out(project, j.Name, "deploy", "downstream-no-failed-job", "deploy pipeline failed with no failed job",
			"the deploy pipeline in the CI engine project ended failed without a failed job (a manual job left unplayed, or a job canceled there)")
	}
	lines := splitLines(trace)
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" && !noiseRe.MatchString(lines[i]) {
			last = lines[i]
			break
		}
	}
	return out(project, j.Name, "unknown", "unknown", runeHead(strings.TrimSpace(last), 400), "no rule matched")
}

func out(project, name, klass, rname, line, why string) Class {
	sigLine := line
	if klass == "canceled" || klass == "lock" {
		sigLine = rname
	}
	return Class{Klass: klass, Rule: rname, Line: line, Why: why, Owner: Owner[klass], Signature: Signature(project, name, klass, sigLine)}
}

// Excerpt is a short redacted excerpt around the key line.
func Excerpt(trace, line string) string {
	const n = 12
	lines := splitLines(trace)
	idx := len(lines) - 1
	if line != "" {
		key := runeHead(line, 60)
		for i, l := range lines {
			if strings.Contains(l, key) {
				idx = i
			}
		}
	}
	var keep []string
	for i := max(0, idx-n+3); i < len(lines) && i < idx+3; i++ {
		if !noiseRe.MatchString(lines[i]) {
			keep = append(keep, runeHead(lines[i], 240))
		}
	}
	s := strings.Join(keep, "\n")
	if r := []rune(s); len(r) > 3000 {
		s = string(r[len(r)-3000:])
	}
	return s
}

var varKeyRe = regexp.MustCompile(`\b(DOTENV(?:_[A-Z0-9_]+)?|[A-Z][A-Z0-9_]{2,})\b (?:is )?(?:not set|empty|missing|undefined|must )`)

// Proposals are the suggested fixes of one failure.
func (w *Watcher) Proposals(c Class, f F) []Proposal {
	env, _ := f["env"].(string)
	pid := f["project_id"]
	jname, _ := f["job_name"].(string)
	jid := toI(f["job_id"])
	ref, _ := f["ref"].(string)
	author, _ := f["author"].(string)
	var proj any = pid
	if toI(pid) == 0 {
		proj = f["project"]
	}
	var outp []Proposal
	add := func(text, action string, target any, mod func(*Proposal)) {
		pr := w.proposal(action, proj, env, target, text, func(p *Proposal) {
			p.JobName = jname
			if mod != nil {
				mod(p)
			}
		})
		pr.Text = text
		outp = append(outp, pr)
	}
	switch c.Klass {
	case "code":
		add(fmt.Sprintf("Dev team (%s): fix the build/test error on %s: %s. Nothing to change in CI.", orS(author, "commit author"),
			ref, runeHead(c.Line, 160)), "none", nil, nil)
	case "package":
		add("Dev team: bump the package version (release x.y.z, other branches x.y.z-alpha.N) and push again.", "none", nil, nil)
	case "lockfile":
		if c.Rule == "node14-syntax" {
			add("Set NODE_VERSION@"+env+" to the Node major the lockfile was built with (the `*` scope's), or pin the "+
				"dependency that uses the newer syntax in a package-only MR; then start a new pipeline.", "set_variable", "NODE_VERSION", nil)
		} else {
			add("Regenerate package-lock.json with npm under the env's Node and commit it on "+ref+" as a "+
				"package-only MR (latest version in the same major).", "pin_lockfile", nil, nil)
		}
	case "variable":
		key := "DOTENV"
		if c.Rule == "no-hosts-to-target" {
			key = "TARGET_HOSTS"
		}
		if m := varKeyRe.FindStringSubmatch(c.Line); m != nil {
			key = m[1]
		}
		add("Add or fix "+key+"@"+env+" (Argus checks keys only; the operator sets the value), then start a "+
			"NEW pipeline: DOTENV and the unit are baked at build/trigger time.", "set_variable", key, nil)
		if env == "develop" {
			add("After the variable is fixed: start a new develop pipeline.", "new_pipeline", orS(ref, "develop"), nil)
		}
	case "registry":
		add("Check the registry account or token and that the image tag was pushed by the build job; "+
			"then rebuild (new pipeline).", "none", nil, nil)
	case "runner":
		add("Runner/infra: "+c.Why+". Fix the runner (disk, Docker, DNS), then retry the job.", "none", nil, nil)
		if env == "develop" && !inList(jname, DeployJobs) && jid != 0 {
			add("Retry the job once the runner is fixed.", "retry_job", jid, nil)
		}
	case "ssh":
		add("Check SSH_PRIVATE_KEY_<ENV> (File type, trailing newline) and that the target host answers SSH from "+
			"the runner; TARGET_HOSTS must name the right inventory group.", "none", nil, nil)
	case "deploy":
		switch c.Rule {
		case "candidate-missing":
			add("Never retry the deploy job: start a new app pipeline so the build step makes the candidate again, then the "+
				"operator plays the deploy.", "none", nil, nil)
		case "placeholder-image":
			add("Fix the stack file on the deploy host (its docker-compose.yml) to use the image "+
				"variable the CI engine substitutes, then redeploy.", "none", nil, nil)
		default:
			add("Deploy step failed: "+c.Why+".", "none", nil, nil)
		}
	case "health":
		add("The new version did not stay up: read the journal lines in the evidence; the fix is usually in DOTENV or "+
			"the code. Production: the operator decides on rollback.", "none", nil, nil)
	case "lock":
		add("Lock wait: see Stuck pipelines; cancel the older pipeline that holds the deploy lock (rule applied there).", "none", nil, nil)
	case "flaky":
		add("Passed on retry: no action; watch the rate.", "none", nil, nil)
		if env == "develop" && jid != 0 && !inList(jname, DeployJobs) {
			add("Retry the job.", "retry_job", jid, nil)
		}
	default:
		add("Unclassified: read the log excerpt; add a triage rule when the cause is known.", "none", nil, nil)
	}
	return outp
}
