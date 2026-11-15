package main

// approvalkey.go: `argus approval-key`, the api's approval signing key (ADR-0022 amendment 1, item A). Run it in the
// api service: only the api mounts ARGUS_APPROVAL_KEY_DIR.

import (
	"crypto/ed25519"
	"flag"
	"fmt"
	"io"

	"github.com/sinaneysi/argus/internal/approvalkey"
	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/config"
)

const approvalKeyHelp = `usage: argus approval-key [-file F] init     create the key in ARGUS_APPROVAL_KEY_DIR when missing (the api also does at start)
       argus approval-key [-file F] public   print the public key (PEM), for actd:
         docker compose exec api /app/argus approval-key public | docker compose exec -T actd /app/argus actd approval-key set
       argus approval-key [-file F] show     print the key's fingerprint (compare with argus actd approval-key show)
`

func runApprovalKey(args []string, stdout, stderr io.Writer) int {
	if isHelp(args) || len(args) == 0 {
		fmt.Fprint(stdout, approvalKeyHelp)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("approval-key", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	dir := c.Values["ARGUS_APPROVAL_KEY_DIR"].Raw
	var create bool
	switch sub {
	case "init":
		create = true
	case "public", "show":
	default:
		fmt.Fprint(stderr, approvalKeyHelp)
		return 2
	}
	k, err := approvalkey.Load(dir, create)
	if err != nil {
		fmt.Fprintln(stderr, "argus approval-key:", err)
		return 1
	}
	switch sub {
	case "public":
		b, err := approvalkey.PublicPEM(k)
		if err != nil {
			fmt.Fprintln(stderr, "argus approval-key:", err)
			return 1
		}
		stdout.Write(b)
	default:
		fmt.Fprintln(stdout, capability.KeyFingerprint(k.Public().(ed25519.PublicKey)))
	}
	return 0
}
