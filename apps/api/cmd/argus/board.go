package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sinaneysi/argus/internal/board"
	"github.com/sinaneysi/argus/internal/collect"
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/notify"
)

const boardHelp = `usage: argus board sync [-full] [-dry-run] [-file PATH]

sync  root, run by argus-board-sync.timer every 2 minutes and by argus-board-sync.path when the web app drops a
      request. Applies the request files in ARGUS_BOARD_REQ_DIR (create, move, comment, assign, sync, settings,
      board-move; each file is consumed and its result written to board-status.json), pushes the queued changes
      to Jira, pulls the changed issues (a full reconcile nightly, with -full, or when an admin asked for one) and
      writes ARGUS_BOARD_OUT/board-status.json. The board database is ARGUS_BOARD_DB. Jira settings (JIRA_URL,
      JIRA_PROJECT, ...) come from the root-owned ARGUS_BOARD_ENV_FILES, the token from ARGUS_BOARD_PAT_FILE;
      without them the board works locally and changes wait in the outbox.
      -dry-run: reads Jira, logs every write it would send, leaves the request files, the database and the status
      file as they are. Off unless ARGUS_BOARD_SYNC=1 (exit 0 with a log line).

flags:
`

func runBoard(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args) || args[0] != "sync" {
		w := stdout
		if len(args) == 0 || !isHelp(args) {
			w = stderr
		}
		fmt.Fprint(w, boardHelp)
		if isHelp(args) {
			return 0
		}
		return 2
	}
	fs := flag.NewFlagSet("board sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	full := fs.Bool("full", false, "a full reconcile this run")
	dry := fs.Bool("dry-run", false, "log the Jira writes instead of sending them; change nothing")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	if c.DemoMode {
		fmt.Fprintln(stderr, "argus board sync: disabled in the demo (DEMO_MODE=1): nothing was changed")
		return 1
	}
	if c.Values["ARGUS_BOARD_SYNC"].Raw != "1" {
		collect.Logger.Info("disabled", "job", "board-sync", "flag", "ARGUS_BOARD_SYNC")
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := board.Run(ctx, boardOptions(c, *full, *dry)); err != nil {
		fmt.Fprintln(stderr, "argus board sync:", err)
		return 1
	}
	return 0
}

func boardOptions(c *config.Config, full, dry bool) board.Options {
	v := func(k string) string { return c.Values[k].Raw }
	or := func(k, def string) string {
		if x := v(k); x != "" {
			return x
		}
		return def
	}
	var envFiles []string
	for _, p := range strings.Split(or("ARGUS_BOARD_ENV_FILES", filepath.Join(c.EtcDir, "jira.env")), ":") {
		if p != "" {
			envFiles = append(envFiles, p)
		}
	}
	uid, _ := strconv.Atoi(v("ARGUS_BOARD_OWNER_UID"))
	return board.Options{
		DB: c.BoardDB, ReqDir: or("ARGUS_BOARD_REQ_DIR", filepath.Join(c.SettingsIn, "board-requests")),
		Out: or("ARGUS_BOARD_OUT", c.SettingsOut), AuthDB: or("ARGUS_BOARD_AUTH_DB", c.AuthDB), EnvFiles: envFiles,
		PATFile: or("ARGUS_BOARD_PAT_FILE", filepath.Join(c.EtcDir, "jira.pat")), OwnerUID: uid,
		AppURL: strings.TrimRight(c.PublicURL, "/"), Name: c.DisplayName, Label: v("ARGUS_BOARD_LABEL"), Ref: v("ARGUS_BOARD_REF"),
		DryRun: dry, Full: full, Log: collect.Logger, Allow: notify.LoadAllowlist(v("ARGUS_SECRET_ALLOWLIST")),
	}
}
