package main

// rekey.go: argus secrets rekey (ADR-0021 D4). Seals every secret of the one-volume layout with the current master
// key: values sealed with ARGUS_MASTER_KEY_OLD (or already with the current key) and plaintext values from before
// encryption. Values are never printed; the report names files only.

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sinaneysi/argus/internal/claudetoken"
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/secret"
)

const rekeyHelp = `
rekey         ARGUS_LAYOUT=volume: seal every secret in the volume (data-source credentials, the Claude token and
              its backups, the Matrix token file) with the current master key (ARGUS_MASTER_KEY, else
              ARGUS_MASTER_KEY_FILE). Values sealed with ARGUS_MASTER_KEY_OLD are opened with it; plaintext values
              are sealed. -dry-run reports only. To change the key: stop the api and the worker, set
              ARGUS_MASTER_KEY_OLD to the old key and the new key in ARGUS_MASTER_KEY (or its file; argus secrets
              new-key prints a fresh one), run rekey, start again.
new-key       print a fresh master key (32 bytes, base64).
`

func runRekey(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("secrets rekey", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	dry := fs.Bool("dry-run", false, "report what would change, write nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	get := func(k string) string { return c.Values[k].Raw }
	if get("ARGUS_LAYOUT") != "volume" {
		fmt.Fprintln(stderr, "argus secrets rekey: only in ARGUS_LAYOUT=volume (the legacy layout keeps its secrets in host files)")
		return 1
	}
	key, from, err := secret.Load(secret.Source{Env: get("ARGUS_MASTER_KEY"), File: get("ARGUS_MASTER_KEY_FILE")})
	if err != nil {
		fmt.Fprintln(stderr, "argus secrets rekey:", err)
		return 1
	}
	var old secret.Key
	if v := get("ARGUS_MASTER_KEY_OLD"); strings.TrimSpace(v) != "" {
		if old, err = secret.ParseKey(v); err != nil {
			fmt.Fprintln(stderr, "argus secrets rekey: ARGUS_MASTER_KEY_OLD:", err)
			return 1
		}
	}
	r := rekeyFiles(secret.FilesOf(get), key, old, *dry)
	fmt.Fprintf(stdout, "master key from %s: %d sealed, %d already current, %d failed%s\n", from, r.sealed, r.current, len(r.failed),
		map[bool]string{true: " (dry run: nothing written)", false: ""}[*dry])
	for _, f := range r.failed {
		fmt.Fprintln(stderr, "  could not rekey", f)
	}
	if len(r.failed) > 0 {
		return 1
	}
	return 0
}

type rekeyResult struct {
	sealed, current int
	failed          []string
}

// reseal opens stored (with old, else key; plaintext as it is) and seals it with key. same: it was already current.
func reseal(key, old secret.Key, name, stored string) (out string, same bool, err error) {
	stored = strings.TrimSpace(stored)
	if secret.Sealed(stored) {
		if _, err := key.Open(name, stored); err == nil {
			return stored, true, nil
		}
		if old == nil {
			return "", false, fmt.Errorf("sealed with another key (set ARGUS_MASTER_KEY_OLD)")
		}
		plain, err := old.Open(name, stored)
		if err != nil {
			return "", false, err
		}
		stored = plain
	}
	s, err := key.Seal(name, stored)
	return s, false, err
}

func rekeyFiles(f secret.Files, key, old secret.Key, dry bool) rekeyResult {
	var r rekeyResult
	write := func(path string, data []byte) error {
		if dry {
			return nil
		}
		mode := os.FileMode(0o600)
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
		if err := secret.WriteFile(path, data); err != nil {
			return err
		}
		return os.Chmod(path, mode)
	}
	one := func(path, name, stored string, put func(string) []byte) {
		if strings.TrimSpace(stored) == "" {
			return
		}
		out, same, err := reseal(key, old, name, stored)
		switch {
		case err != nil:
			r.failed = append(r.failed, path+": "+err.Error())
		case same:
			r.current++
		default:
			if err := write(path, put(out)); err != nil {
				r.failed = append(r.failed, path+": "+err.Error())
				return
			}
			r.sealed++
		}
	}
	for path, name := range f.Creds() {
		b, _ := os.ReadFile(path)
		one(path, name, string(b), func(s string) []byte { return []byte(s + "\n") })
	}
	for _, path := range f.ClaudeFiles() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		v := claudetoken.ParseEnv(string(data))[claudetoken.Var]
		one(path, secret.ClaudeName, v, func(s string) []byte { return []byte(replaceEnvLine(string(data), claudetoken.Var, s)) })
	}
	if b, err := os.ReadFile(f.MatrixFile); err == nil {
		one(f.MatrixFile, secret.MatrixName, string(b), func(s string) []byte { return []byte(s + "\n") })
	}
	return r
}

// replaceEnvLine sets KEY=value in place of the line(s) that set KEY; every other line stays as it is.
func replaceEnvLine(text, key, value string) string {
	var out []string
	done := false
	for _, line := range strings.SplitAfter(text, "\n") {
		k, _, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(strings.TrimPrefix(k, "export ")) == key {
			if !done {
				out = append(out, key+"="+value+"\n")
				done = true
			}
			continue
		}
		if line != "" {
			out = append(out, line)
		}
	}
	if !done {
		out = append([]string{key + "=" + value + "\n"}, out...)
	}
	return strings.Join(out, "")
}
