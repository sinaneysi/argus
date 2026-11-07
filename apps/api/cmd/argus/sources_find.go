package main

// sources_find.go: `argus sources find` (ADR-0017 P3) for the agent: which instance to read for a service or a
// question; a capped fan-out when the catalogue has nothing; learned routes proposed for review.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/sinaneysi/argus/internal/sources/catalogue"
	"github.com/sinaneysi/argus/internal/sources/discover"
	"github.com/sinaneysi/argus/internal/sources/find"
)

var runIDRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

func runSourcesFind(args []string, stdout, stderr io.Writer) int {
	usage := func(msg string) int {
		fmt.Fprintln(stderr, "argus sources find: "+msg)
		fmt.Fprintln(stderr, `usage: argus sources find "<service or question>" [--type t]... [--release] [--no-fanout] [--json] [--run id]`)
		return 2
	}
	if len(args) == 0 {
		return usage("give a service name or a question")
	}
	text, rest := args[0], args[1:]
	if strings.TrimSpace(text) == "" || len(text) > 500 || strings.IndexFunc(text, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
		return usage("give a service name or a question (1..500 printable characters)")
	}
	var want []string
	asJSON, release, fan := false, false, true
	run := os.Getenv("ARGUS_RUN_ID")
	for i := 0; i < len(rest); i++ {
		switch a := rest[i]; {
		case (a == "--type" || a == "-type") && i+1 < len(rest):
			t, ok := find.Canon(rest[i+1])
			if !ok {
				return usage("--type is one of " + strings.Join(find.Order, ", "))
			}
			if !In(t, want...) {
				want = append(want, t)
			}
			i++
		case (a == "--run" || a == "-run") && i+1 < len(rest) && runIDRe.MatchString(rest[i+1]):
			run = rest[i+1]
			i++
		case a == "--json" || a == "-json":
			asJSON = true
		case a == "--release" || a == "-release":
			release = true
		case a == "--no-fanout" || a == "-no-fanout":
			fan = false
		default:
			if len(a) > 40 {
				a = a[:40]
			}
			return usage(fmt.Sprintf("unknown argument %q", a))
		}
	}
	if !runIDRe.MatchString(run) {
		run = ""
	}
	c, ok := loadQuiet("", stderr)
	if !ok {
		return 1
	}
	v := func(k string) string { return c.Values[k].Raw }
	do := discoverOptions(c)
	o := find.Options{Entries: do.Sources, Limits: find.LimitsFrom(v), RunID: run, AuditPath: v("ARGUS_SOURCES_FIND_AUDIT_LOG")}
	if c.DemoMode {
		fan, o.NoFanout = false, "fan-out is disabled in the demo"
	}
	var db *sql.DB
	var err error
	if fan && len(want) > 0 {
		// the agent's user: fan-out may learn routes, so ARGUS_DB is opened for writing
		if db, err = discover.Open(c.DB); err == nil {
			err = catalogue.Migrate(db)
		}
		if err != nil {
			fmt.Fprintln(stderr, "argus sources find:", err)
			return 1
		}
		o.Probe, o.Learn = do.Probe, true
	} else if _, serr := os.Stat(c.DB); serr == nil {
		db, err = sql.Open("sqlite", "file:"+c.DB+"?mode=ro&_pragma=busy_timeout(10000)")
		if err != nil {
			fmt.Fprintln(stderr, "argus sources find:", err)
			return 1
		}
	}
	if db != nil {
		defer db.Close()
		o.DB = db
	}
	var cdb catalogue.DB
	if db != nil {
		cdb = db
	}
	if o.Services, _, err = catalogue.LoadServices(v("ARGUS_SOURCES_SERVICES"), cdb); err != nil {
		fmt.Fprintln(stderr, "argus sources find:", err)
		return 1
	}
	out := find.Find(text, want, release, fan, o)
	if asJSON {
		b, _ := json.Marshal(out)
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	find.Render(out, stdout)
	return 0
}
