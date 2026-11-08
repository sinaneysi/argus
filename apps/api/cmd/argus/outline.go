package main

// outline.go: argus outline (the documentation publisher, internal/outline): requests applies the Settings ->
// Integrations -> Outline requests (connect, settings, sync, hosts, disconnect); publish creates or updates the
// rendered service pages (ops.db outline_pages) under the configured parent document, in publish mode only. In the
// volume layout the worker runs both: the requests within seconds (a sync request publishes at once), the publish
// once a day.

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/outline"
)

const outlineHelp = `usage: argus outline requests|publish [-file PATH]

requests  apply the Outline requests of Settings -> Integrations (ARGUS_SETTINGS_IN/outline-requests): connect (URL,
          API key, collection, parent document OUTLINE_PARENT), settings (preview|publish, pilot|all), sync, hosts,
          disconnect. Admins only. The API key is stored sealed in ARGUS_ETC_DIR/outline.token (0600).
publish   in publish mode: create or update every rendered page in scope under the parent document. If the parent is
          missing, in the trash, archived or in another collection, nothing is published and the status says why;
          pages are never made at the collection root; a lost page is found again by title among the parent's
          children only. Text a person wrote outside the block is kept.
Status: ARGUS_SETTINGS_OUT/outline-status.json (never the key).
`

func runOutline(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args) || (args[0] != "requests" && args[0] != "publish") {
		if isHelp(args) {
			fmt.Fprint(stdout, outlineHelp)
			return 0
		}
		fmt.Fprint(stderr, outlineHelp)
		return 2
	}
	fs := flag.NewFlagSet("outline "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	if c.DemoMode {
		fmt.Fprintln(stderr, "argus outline: disabled in the demo (DEMO_MODE=1)")
		return 1
	}
	if err := outlineRun(c, args[0], stdout); err != nil {
		fmt.Fprintln(stderr, "argus outline "+args[0]+":", err)
		return 1
	}
	return 0
}

func outlineOptions(c *config.Config, log io.Writer) *outline.Options {
	v := func(k string) string { return c.Values[k].Raw }
	def := strings.TrimRight(v("OUTLINE_URL"), "/")
	var hosts []string
	if u, err := url.Parse(def); err == nil && u.Hostname() != "" {
		h := strings.ToLower(u.Hostname())
		if p := u.Port(); p != "" && p != "443" {
			h += ":" + p
		}
		hosts = []string{h}
	}
	o := &outline.Options{ReqDir: filepath.Join(c.SettingsIn, "outline-requests"), OutDir: c.SettingsOut,
		StateFile: filepath.Join(c.StateDir, "outline", "state.json"), EtcDir: c.EtcDir, AuthDB: c.AuthDB, Key: volumeKey(c),
		DefaultURL: def, DefaultHosts: hosts, DefaultParent: v("OUTLINE_PARENT"), Pilot: list(c, "OUTLINE_PILOT"),
		Name: orName(c.DisplayName), AppUID: os.Getuid(), Log: log}
	o.Marks = outline.NewMarks(v("OUTLINE_MARKER"), o.Name)
	if e := firstSource(c, "outline"); e != nil && e.Purpose == "docs" && e.Enabled {
		so := sourcesOptions(c)
		o.SourceURL, o.SourceToken = e.URL, so.Credential(e)
	}
	return o
}

func orName(s string) string {
	if strings.TrimSpace(s) == "" {
		return "Argus"
	}
	return s
}

func outlineRun(c *config.Config, task string, stdout io.Writer) error {
	o := outlineOptions(c, stdout)
	db, err := openOpsDB(c)
	if err != nil {
		return err
	}
	defer db.Close()
	o.DB = db
	if task == "requests" {
		n, err := o.Requests()
		if err != nil || n == 0 {
			return err
		}
	}
	r, err := o.Publish()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "outline publish: mode=%s %v calls=%d%s\n", r.Mode, r.Counts, r.Calls, map[bool]string{true: " error=" + r.Error}[r.Error != ""])
	return nil
}
