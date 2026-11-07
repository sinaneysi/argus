package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/sinaneysi/argus/internal/demo"
)

const demoHelp = `usage: argus demo generate -out DIR [-auth DIR] [-now EPOCH] [-seed N] [-loop] [-every SECONDS]
       argus demo ready [-out DIR] [-auth DIR]

generate  writes the public demo's sample data (the fictional company Northwind Commerce): ops.db, board/board.db,
          the state JSON files and settings-out/ into -out, and auth.db into -auth (only when it is missing there, so
          demo sessions survive a regeneration). The same -seed and -now give the same data.
          With -loop it keeps running: it regenerates every -every seconds and refreshes the live files (alert
          snapshot, VM pulse, CI/CD watcher) every minute.
ready     exits 0 when ops.db and auth.db exist (the data container's health check), else 1.
`

func runDemo(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args[:1]) {
		fmt.Fprint(stdout, demoHelp)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch args[0] {
	case "generate":
		return demoGenerate(args[1:], stdout, stderr)
	case "ready":
		return demoReady(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "argus demo: unknown subcommand %q (generate, ready)\n", args[0])
	return 2
}

func demoGenerate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("argus demo generate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "data directory (mounted read-only into the app as /data)")
	auth := fs.String("auth", "", "directory for auth.db (writable by the app); written only when missing")
	now := fs.Float64("now", 0, "reference time in epoch seconds (default: now)")
	seed := fs.Int64("seed", demo.DefaultSeed, "random seed")
	loop := fs.Bool("loop", false, "keep running: regenerate every -every seconds, refresh the live files every minute")
	every := fs.Int("every", 6*3600, "seconds between regenerations with -loop")
	fs.Usage = func() { fmt.Fprint(stderr, demoHelp); fs.PrintDefaults() }
	if isHelp(args) {
		fs.SetOutput(stdout)
		fmt.Fprint(stdout, demoHelp)
		fs.PrintDefaults()
		return 0
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" {
		fmt.Fprintln(stderr, "argus demo generate: -out is required")
		return 2
	}
	t := *now
	if t == 0 {
		t = float64(time.Now().UnixNano()) / 1e9
	}
	if err := demo.Generate(*out, *auth, t, *seed); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "demo data written to %s (now=%d, seed=%d)\n", *out, int64(t), *seed)
	if !*loop {
		return 0
	}
	demo.Loop(*out, *auth, *seed, time.Duration(*every)*time.Second, func(f string, a ...any) {
		fmt.Fprintf(stderr, f+"\n", a...)
	})
	return 0
}

func demoReady(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("argus demo ready", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "/data", "data directory")
	auth := fs.String("auth", "/auth", "auth.db directory")
	if isHelp(args) {
		fmt.Fprint(stdout, demoHelp)
		return 0
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if demo.Ready(*out, *auth) {
		return 0
	}
	fmt.Fprintln(stderr, "demo data not ready")
	return 1
}
