package app

import "github.com/sinaneysi/argus/internal/py"

// Types shared across the view modules (the contract between the ported groups).

// Handoff is the fix handoff dict of views_fix / fix_handoff: options, prompt, state, flags (and the
// markdown body when fix_handoff.parse produced it).
type Handoff struct {
	Options []*py.OMap
	Prompt  string
	State   string
	Flags   []string
	Body    string
}

// Proposal is one (n, title, text) of views_board.proposals.
type Proposal struct {
	N     int64
	Title string
	Text  string
}

// Fact is one (label, html) of views_rca.doc.
type Fact struct{ Label, HTML string }
