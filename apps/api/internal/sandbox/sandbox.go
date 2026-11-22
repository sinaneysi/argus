// Package sandbox confines a child program's file access with Landlock (Linux 5.13+, no privilege needed) before it
// runs: `argus model-exec` applies the rules to itself and then execs the program, so the program and everything it
// starts can read, write or execute only beneath the paths the rules name. Network access is not restricted.
//
// Landlock also stops the confined program from tracing a process outside its domain, so /proc/<pid>/environ, mem,
// fd and root of the worker (and of other model calls) cannot be read even when /proc itself is readable.
package sandbox

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Rules are the only paths the confined program may use; everything else is denied.
type Rules struct {
	Exec  []string // read, list and execute beneath these (programs, libraries)
	Read  []string // read and list beneath these
	Write []string // read, list, create, write, remove beneath these (no execute)
}

// Args are the model-exec flags for r (the order is stable: Exec, Read, Write).
func (r Rules) Args() []string {
	var out []string
	for _, x := range [][2]any{{"-x", r.Exec}, {"-r", r.Read}, {"-w", r.Write}} {
		for _, p := range x[1].([]string) {
			out = append(out, x[0].(string), p)
		}
	}
	return out
}

// check refuses a rule that would open the whole file system or is not absolute.
func (r Rules) check() error {
	for _, l := range [][]string{r.Exec, r.Read, r.Write} {
		for _, p := range l {
			if !filepath.IsAbs(p) {
				return fmt.Errorf("not an absolute path: %q", p)
			}
			if filepath.Clean(p) == "/" {
				return errors.New("a rule for / would allow everything")
			}
		}
	}
	return nil
}

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(s string) error { *l = append(*l, s); return nil }

// Unavailable is the exit code of model-exec when the sandbox cannot be applied (nothing was started).
const Unavailable = 126

// Main is `argus model-exec [-x DIR]... [-r DIR]... [-w DIR]... -- PROGRAM [ARG]...`: apply the rules, then exec
// PROGRAM with the same environment. It returns only when it could not exec (exit Unavailable when the sandbox could
// not be applied: it never runs the program unconfined).
func Main(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("model-exec", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var r Rules
	fs.Var((*list)(&r.Exec), "x", "read and execute beneath this path (repeat)")
	fs.Var((*list)(&r.Read), "r", "read beneath this path (repeat)")
	fs.Var((*list)(&r.Write), "w", "read and write beneath this path (repeat)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: argus model-exec [-x DIR]... [-r DIR]... [-w DIR]... -- PROGRAM [ARG]...")
		fmt.Fprintln(stderr, "\nRuns PROGRAM confined by Landlock to the paths given (the model runtime starts the Claude CLI this way).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	argv := fs.Args()
	if len(argv) == 0 {
		fs.Usage()
		return 2
	}
	if err := r.check(); err != nil {
		fmt.Fprintln(stderr, "model-exec:", err)
		return 2
	}
	err := Exec(r, argv, os.Environ())
	fmt.Fprintln(stderr, "model-exec:", err)
	if errors.Is(err, ErrUnavailable) {
		return Unavailable
	}
	return 127
}

// ErrUnavailable is returned when Landlock cannot be applied (old kernel, not in the LSM list, or a seccomp profile
// that blocks the calls).
var ErrUnavailable = errors.New("the Landlock sandbox is not available")
