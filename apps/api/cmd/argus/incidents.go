package main

// incidents.go: argus incidents (the incident records job): tick applies the incident page's requests and
// auto-resolves; attach attaches one recorded run; backfill attaches every run without an incident. In the volume
// layout the worker runs it: the requests within seconds, the auto-resolve every 5 minutes once an alerts source
// is configured.

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/collect"
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/incidents"
	"github.com/sinaneysi/argus/internal/notify"
)

const incidentsHelp = `usage: argus incidents tick [-no-resolve] [-file PATH]
       argus incidents attach [-file PATH] <task> <run id> [data file]
       argus incidents backfill [-no-resolve] [-file PATH]

tick      apply the incident page's requests (ARGUS_SETTINGS_IN/incident-actions: ack, resolve, re-open) and
          auto-resolve the open incidents with no matching alert firing (the alerts snapshot ARGUS_ALERTS_FILE, the
          watch state and the open uptime monitors in ARGUS_STATE_DIR) for ARGUS_INCIDENT_AUTORESOLVE_HOURS, or with
          no new report for ARGUS_INCIDENT_CHAT_RESOLVE_HOURS when no alert is behind them. A changed incident that has
          a Matrix thread gets a note in it (ARGUS_NOTIFY_MATRIX=1). -no-resolve: requests only.
attach    attach the findings of one recorded run (task incident, chat-triage or log-rca) to their incidents; the
          data file is the incident job's {"alerts": [...]}. A run is attached once.
backfill  attach every recorded run that has no incident yet (data files: ARGUS_STATE_DIR/runs/<log name>).
`

func runIncidents(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args) {
		w := stderr
		if isHelp(args) {
			w = stdout
		}
		fmt.Fprint(w, incidentsHelp)
		if isHelp(args) {
			return 0
		}
		return 2
	}
	cmd := args[0]
	fs := flag.NewFlagSet("incidents "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	noResolve := fs.Bool("no-resolve", false, "apply the requests only, no auto-resolve")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	if c.DemoMode {
		fmt.Fprintln(stderr, "argus incidents: disabled in the demo (DEMO_MODE=1): nothing was changed")
		return 1
	}
	db, err := openOpsDB(c)
	if err != nil {
		fmt.Fprintln(stderr, "argus incidents:", err)
		return 1
	}
	defer db.Close()
	o := incidentOptions(c)
	switch {
	case cmd == "tick" && fs.NArg() == 0:
		ch, err := o.Tick(db, !*noResolve)
		if err != nil {
			fmt.Fprintln(stderr, "argus incidents tick:", err)
			return 1
		}
		fmt.Fprintf(stdout, "incidents: %d changed\n", len(ch))
	case cmd == "attach" && (fs.NArg() == 2 || fs.NArg() == 3):
		id, err := strconv.ParseInt(fs.Arg(1), 10, 64)
		if err != nil || id <= 0 {
			fmt.Fprintln(stderr, "argus incidents attach: the run id must be a number")
			return 2
		}
		tx, err := db.Begin()
		if err != nil {
			fmt.Fprintln(stderr, "argus incidents attach:", err)
			return 1
		}
		ids, err := o.AttachRun(tx, fs.Arg(0), id, fs.Arg(2))
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
		if err != nil {
			fmt.Fprintln(stderr, "argus incidents attach:", err)
			return 1
		}
		fmt.Fprintf(stdout, "incidents: run %d attached to %v\n", id, ids)
	case cmd == "backfill" && fs.NArg() == 0:
		n, err := o.Backfill(db, filepath.Join(c.StateDir, "runs"), !*noResolve)
		if err != nil {
			fmt.Fprintln(stderr, "argus incidents backfill:", err)
			return 1
		}
		fmt.Fprintf(stdout, "backfill: %d findings attached\n", n)
	default:
		fmt.Fprint(stderr, incidentsHelp)
		return 2
	}
	return 0
}

func openOpsDB(c *config.Config) (*sql.DB, error) {
	if c.DB == "" {
		return nil, errors.New("ARGUS_DB is not set")
	}
	db, err := sql.Open("sqlite", "file:"+c.DB+"?_pragma=busy_timeout(30000)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func atoiDef(c *config.Config, k string) int {
	n, _ := strconv.Atoi(c.Values[k].Raw)
	return n
}

// incidentOptions are the job's options from the configuration. The own, GitLab and chat hosts are never subjects.
func incidentOptions(c *config.Config) *incidents.Options {
	skip := list(c, "ARGUS_INCIDENT_SKIP_HOSTS")
	hosts := []string{c.PublicURL, gitlabURL(c), c.Values["MATRIX_HOMESERVER"].Raw}
	if e := firstSource(c, "matrix"); e != nil {
		hosts = append(hosts, e.URL)
	}
	if e := firstSource(c, "gitlab"); e != nil {
		hosts = append(hosts, e.URL)
	}
	for _, u := range hosts {
		if h := incidents.HostOf(u); h != "" {
			skip = append(skip, h)
		}
	}
	o := &incidents.Options{StateDir: c.StateDir, AlertsFile: c.AlertsFile, ActionsDir: filepath.Join(c.SettingsIn, "incident-actions"),
		AutoHours: atoiDef(c, "ARGUS_INCIDENT_AUTORESOLVE_HOURS"), ChatHours: atoiDef(c, "ARGUS_INCIDENT_CHAT_RESOLVE_HOURS"),
		ReopenDays: atoiDef(c, "ARGUS_INCIDENT_REOPEN_DAYS"), Sites: list(c, "OBS_SITES"), Customer: list(c, "ARGUS_CUSTOMER_FACING"),
		Suffixes: list(c, "ARGUS_INCIDENT_HOST_SUFFIXES"), Prefixes: list(c, "ARGUS_INCIDENT_HOST_PREFIXES"), SkipHosts: skip,
		Words: familyWords(list(c, "ARGUS_INCIDENT_FAMILY_WORDS")), Log: collect.Logger.With("job", "incidents")}
	snd := senderFrom(c)
	loc, err := time.LoadLocation(c.Values["ARGUS_TZ"].Raw)
	if err != nil {
		loc = time.UTC
	}
	base := strings.TrimRight(c.PublicURL, "/")
	o.Note = func(inc map[string]any, text string) {
		room := fmt.Sprint(inc["thread_room"])
		if inc["thread_room"] != nil && room != "" && room != snd.Room {
			return // the thread is in another room: never sent there
		}
		msg := incidentNote(inc, text, base, time.Now().In(loc))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := snd.SendThread(ctx, msg, "incident", fmt.Sprint(inc["thread_root"])); err != nil && !errors.Is(err, notify.ErrNotSent) {
			o.Log.Warn("thread note not sent", "incident", inc["id"], "error", err.Error())
		}
	}
	return o
}

// familyWords parses family=word entries.
func familyWords(entries []string) map[string][]string {
	out := map[string][]string{}
	for _, e := range entries {
		if f, w, ok := strings.Cut(e, "="); ok && strings.TrimSpace(f) != "" && strings.TrimSpace(w) != "" {
			f = strings.TrimSpace(f)
			out[f] = append(out[f], strings.TrimSpace(w))
		}
	}
	return out
}

// incidentNote is the one-line thread reply for a changed incident: "<icon> HH:MM resolved — <what> · Incident #N".
func incidentNote(inc map[string]any, text, base string, now time.Time) notify.Content {
	state, icon := "update", "🔔"
	if strings.Contains(text, "resolved") {
		state, icon = "resolved", "✅"
	}
	id := fmt.Sprint(inc["id"])
	link := base + "/incidents/view?id=" + url.QueryEscape(id)
	what := notify.Clean(text, 200)
	body := fmt.Sprintf("%s %s %s — %s · Incident #%s %s", icon, now.Format("15:04"), state, what, id, link)
	fb := fmt.Sprintf(`%s <b>%s %s</b> — %s · <a href="%s">Incident #%s</a>`, icon, now.Format("15:04"), state,
		html.EscapeString(what), html.EscapeString(link), html.EscapeString(id))
	return notify.Content{MsgType: "m.notice", Body: body, Format: "org.matrix.custom.html", FormattedBody: fb}
}
