package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/sinaneysi/argus/internal/config"
)

const configHelp = `usage: argus config check [-file PATH] [-q]
       argus config show  [-file PATH] [-json]
       argus config schema

check loads the configuration (defaults, then the argus.env file, then the environment), validates every key and
prints the effective values with their source. Secrets are masked. Deprecated names (OPS_*) print a warning.
show prints the same table (or JSON with -json: key, value, source default|file|env|alias, alias name) and lists
problems as warnings. schema prints the key schema as JSON (config/schema.json is this output).
The file is -file, else $ARGUS_CONFIG, else /etc/argus/argus.env (optional).
Exit status 0: valid; 1: invalid (every problem is listed); 2: usage error.

flags:
`

type configOpts struct {
	file  *string
	quiet *bool
	json  *bool
}

func configFlags(out io.Writer) (*flag.FlagSet, configOpts) {
	fs := flag.NewFlagSet("argus config", flag.ContinueOnError)
	fs.SetOutput(out)
	o := configOpts{
		file:  fs.String("file", "", "configuration file (default: $ARGUS_CONFIG or "+config.DefaultFile+")"),
		quiet: fs.Bool("q", false, "check: print only problems and warnings"),
		json:  fs.Bool("json", false, "show: print JSON"),
	}
	fs.Usage = func() { fmt.Fprint(out, configHelp); fs.PrintDefaults() }
	return fs, o
}

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args[:1]) {
		fs, _ := configFlags(stdout)
		fs.Usage()
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	sub := args[0]
	if sub != "check" && sub != "show" && sub != "schema" {
		fmt.Fprintf(stderr, "argus config: unknown subcommand %q (check, show, schema)\n", sub)
		return 2
	}
	if sub == "schema" && !isHelp(args[1:]) {
		if len(args) > 1 {
			fmt.Fprintln(stderr, "argus config schema: takes no arguments")
			return 2
		}
		stdout.Write(config.SchemaJSON())
		return 0
	}
	if isHelp(args[1:]) {
		fs, _ := configFlags(stdout)
		fs.Usage()
		return 0
	}
	fs, o := configFlags(stderr)
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	c, err := config.Load(config.Options{File: *o.file})
	for _, w := range c.Warnings {
		fmt.Fprintln(stderr, "warning:", w)
	}
	if sub == "show" {
		if *o.json {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			enc.Encode(struct {
				File    string         `json:"file"`
				Entries []config.Entry `json:"keys"`
			}{c.File, c.Entries()})
		} else {
			c.Print(stdout)
		}
	} else if !*o.quiet {
		if c.File != "" {
			fmt.Fprintf(stdout, "# file: %s\n", c.File)
		} else {
			fmt.Fprintln(stdout, "# file: none (defaults and environment only)")
		}
		c.Print(stdout)
	}
	if errs, ok := err.(config.Errors); ok {
		for _, e := range errs {
			fmt.Fprintln(stderr, "CONFIG ERROR:", e)
		}
		fmt.Fprintf(stderr, "config: %d problem(s)\n", len(errs))
		return 1
	}
	fmt.Fprintln(stderr, "config: OK")
	return 0
}
