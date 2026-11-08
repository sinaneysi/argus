package main

// sources_p2.go: Data sources P2 (ADR-0017 D7) on the command line: discovery, the routing catalogue, the Catalogue
// page's actions (the agent's user, writes ARGUS_DB) and the agent's read-only view (routes, unmapped).

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/kube"
	"github.com/sinaneysi/argus/internal/sources"
	"github.com/sinaneysi/argus/internal/sources/catalogue"
	"github.com/sinaneysi/argus/internal/sources/discover"
)

var (
	serviceArgRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,120}$`)
	sourceKeyRe  = regexp.MustCompile(`^[a-z]{3,20}/[a-z0-9][a-z0-9-]{0,30}$`)
)

// In is whether x is one of xs.
func In(x string, xs ...string) bool {
	for _, y := range xs {
		if x == y {
			return true
		}
	}
	return false
}

// runSourcesRead is routes and unmapped: ARGUS_DB read-only.
func runSourcesRead(sub string, args []string, stdout, stderr io.Writer) int {
	if sub == "routes" && (len(args) != 1 || !serviceArgRe.MatchString(args[0])) {
		fmt.Fprintln(stderr, "usage: argus sources routes <service, e.g. shop/cart or cart>")
		return 2
	}
	typ := ""
	if sub == "unmapped" {
		if len(args) > 1 || (len(args) == 1 && !sourceTypeRe.MatchString(args[0])) {
			fmt.Fprintln(stderr, "usage: argus sources unmapped [type]  (the type is one word, e.g. gitlab)")
			return 2
		}
		if len(args) == 1 {
			typ = args[0]
		}
	}
	c, ok := loadQuiet("", stderr)
	if !ok {
		return 1
	}
	if _, err := os.Stat(c.DB); err != nil {
		fmt.Fprintln(stdout, "the catalogue is not built yet (argus sources discover)")
		return 0
	}
	db, err := sql.Open("sqlite", "file:"+c.DB+"?mode=ro&_pragma=busy_timeout(10000)")
	if err != nil {
		fmt.Fprintln(stderr, "argus sources:", err)
		return 1
	}
	defer db.Close()
	if sub == "routes" {
		var n int
		if db.QueryRow("SELECT COUNT(*) FROM source_routes").Scan(&n) != nil {
			fmt.Fprintln(stdout, "the catalogue is not built yet (argus sources discover)")
			return 0
		}
		catalogue.PrintRoutes(db, args[0], stdout)
		return 0
	}
	catalogue.PrintUnmapped(db, typ, stdout, 300)
	return 0
}

func discoverOptions(c *config.Config) *discover.Options {
	v := func(k string) string { return c.Values[k].Raw }
	host, _ := os.Hostname()
	sources.AllowHTTP = v("ARGUS_SOURCES_ALLOW_HTTP") == "1"
	return &discover.Options{DB: c.DB, Sources: sources.LoadStatus(filepath.Join(c.SettingsOut, "sources-status.json")),
		SourcesDir: v("ARGUS_SOURCES_DIR"), SecretsDir: c.SecretsDir, Key: volumeKey(c),
		Kube: &kube.Tool{KubeDir: v("ARGUS_KUBE_DIR"), Kubectl: v("ARGUS_KUBECTL"), AuditLog: v("ARGUS_KUBE_AUDIT_LOG"),
			CacheDir: v("ARGUS_KUBE_CACHE_DIR"), OwnerUID: 0, Host: host}}
}

// build rebuilds the catalogue in one transaction.
func buildCatalogue(c *config.Config, db *sql.DB) (catalogue.Summary, error) {
	svcs, _, err := catalogue.LoadServices(c.Values["ARGUS_SOURCES_SERVICES"].Raw, db)
	if err != nil {
		return catalogue.Summary{}, err
	}
	tx, err := db.Begin()
	if err != nil {
		return catalogue.Summary{}, err
	}
	defer tx.Rollback()
	sum, err := catalogue.Build(tx, svcs, time.Now().Unix())
	if err != nil {
		return sum, err
	}
	return sum, tx.Commit()
}

func runSourcesAgent(c *config.Config, sub string, force bool, only, trigger string, stdout, stderr io.Writer) int {
	if trigger != "timer" && trigger != "manual" {
		fmt.Fprintln(stderr, "argus sources discover: -trigger is timer or manual")
		return 2
	}
	set := map[string]bool{}
	for _, k := range strings.Split(only, ",") {
		if k = strings.TrimSpace(k); k == "" {
			continue
		}
		if !sourceKeyRe.MatchString(k) {
			fmt.Fprintf(stderr, "argus sources discover: -only takes type/name, e.g. gitlab/main\n")
			return 2
		}
		set[k] = true
	}
	o := discoverOptions(c)
	db, err := discover.Open(c.DB)
	if err != nil {
		fmt.Fprintf(stderr, "argus sources %s: %v\n", sub, err)
		return 1
	}
	defer db.Close()
	if err := catalogue.Migrate(db); err != nil {
		fmt.Fprintf(stderr, "argus sources %s: %v\n", sub, err)
		return 1
	}
	switch sub {
	case "discover":
		db.Close()
		runs, err := o.All(set, force, trigger)
		if err != nil {
			fmt.Fprintln(stderr, "argus sources discover:", err)
			return 1
		}
		for _, r := range runs {
			fmt.Fprintln(stdout, r.Line())
		}
		if db, err = discover.Open(c.DB); err != nil {
			fmt.Fprintln(stderr, "argus sources discover:", err)
			return 1
		}
		fallthrough
	case "catalogue":
		sum, err := buildCatalogue(c, db)
		if err != nil {
			fmt.Fprintln(stderr, "argus sources catalogue:", err)
			return 1
		}
		fmt.Fprintln(stdout, sum.Line())
		return 0
	}
	ao := catalogue.ActionOptions{ReqDir: filepath.Join(c.SettingsIn, "source-actions"), AppUID: 65534, Log: stdout,
		Discover: func(src string) (string, error) {
			oo := discoverOptions(c)
			oo.Sources = o.Sources
			if discover.Eligible(oo.Sources)[src] == nil {
				return "", fmt.Errorf("%s is not a source the agent may read (purpose agent, read-only verified)", src)
			}
			r := oo.One(db, src, discover.Eligible(oo.Sources)[src], true, "manual")
			return r.Line(), nil
		},
		Rebuild: func(catalogue.DB) error { _, err := buildCatalogue(c, db); return err }}
	if _, err := catalogue.Actions(db, ao); err != nil {
		fmt.Fprintln(stderr, "argus sources actions:", err)
		return 1
	}
	return 0
}
