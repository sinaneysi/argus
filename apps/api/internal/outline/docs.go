package outline

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// The ownership rule: Argus owns only the text between its begin and end markers. Merge keeps everything a person
// wrote outside the block. If a person edited inside the block (its hash is neither what Argus last published nor
// what it proposed), their text stays, the page is flagged, and Argus's version goes into a "proposed update"
// section under the block.

// Marks are the markers and headings of one marker word (OUTLINE_MARKER, default "argus"; an installation that
// moves from an earlier product sets its word so the existing pages are recognised).
type Marks struct {
	Begin, End, PBegin, PEnd, ProposedHead, NotesHead, NotesHint, ProposedNote string
	beginRe, endRe, pbeginRe, pendRe                                           *regexp.Regexp
}

func markerRe(word string) *regexp.Regexp {
	return regexp.MustCompile(`(?:\\?<|&lt;)\\?!\\?-\\?-\s*` + regexp.QuoteMeta(word) + `\s*\\?-\\?-\\?(?:>|&gt;)`)
}

// NewMarks builds the markers of word; name is the product name in the headings.
func NewMarks(word, name string) *Marks {
	if word == "" {
		word = "argus"
	}
	if name == "" {
		name = "Argus"
	}
	return &Marks{Begin: "<!-- " + word + ":begin -->", End: "<!-- " + word + ":end -->",
		PBegin: "<!-- " + word + ":proposed:begin -->", PEnd: "<!-- " + word + ":proposed:end -->",
		ProposedHead: "## " + name + " proposed update", NotesHead: "## Team notes",
		NotesHint: "_Write your own notes here. " + name + " only rewrites the block above, between its markers._",
		ProposedNote: "_A person edited " + name + "'s block above, so " + name + " did not overwrite it. This is what " + name +
			" would write now: copy what is right into the block, or undo the edits there, and this section goes away at the next sync._",
		beginRe: markerRe(word + ":begin"), endRe: markerRe(word + ":end"),
		pbeginRe: markerRe(word + ":proposed:begin"), pendRe: markerRe(word + ":proposed:end")}
}

func sha(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])[:16]
}

var (
	unescRe   = regexp.MustCompile(`\\([\\` + "`" + `*_{}\[\]()#+\-.!<>|~])`)
	bulletRe  = regexp.MustCompile(`(?m)^\s*[*+]\s`)
	spacesRe  = regexp.MustCompile(`[ \t]+`)
	tableSep  = regexp.MustCompile(`\|[ \t]*:?-{3,}:?[ \t]*`)
	tablePipe = regexp.MustCompile(`[ \t]*\|[ \t]*`)
)

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// normBlock is the hash input for "did a person edit the block?": Outline round-trips Markdown (escapes, bullets,
// blank lines), so a normalised form is compared, not the bytes.
func normBlock(text string) string {
	t := unescRe.ReplaceAllString(text, "$1")
	t = bulletRe.ReplaceAllString(t, "- ")
	t = spacesRe.ReplaceAllString(t, " ")
	var keep []string
	for _, l := range splitLines(t) {
		if l = strings.TrimSpace(l); l != "" {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

// normTables: Outline pads table cells and widens |---| separators on its way back.
func normTables(t string) string {
	var b strings.Builder
	last := 0
	for _, m := range tableSep.FindAllStringIndex(t, -1) {
		if m[1] < len(t) && t[m[1]] == '|' {
			b.WriteString(t[last:m[0]])
			b.WriteString("|---")
			last = m[1]
		}
	}
	b.WriteString(t[last:])
	return tablePipe.ReplaceAllString(b.String(), " | ")
}

// BlockHash is the hash of a block's normalised content.
func BlockHash(text string) string { return sha(normTables(normBlock(text))) }

func blockHashLegacy(text string) string { return sha(normBlock(text)) }

// Wrap puts the markers around a block.
func (m *Marks) Wrap(block string) string {
	return m.Begin + "\n" + strings.TrimSpace(block) + "\n" + m.End
}

// NewDocument is a new page: the block and an empty Team notes section.
func (m *Marks) NewDocument(block string) string {
	return m.Wrap(block) + "\n\n" + m.NotesHead + "\n\n" + m.NotesHint + "\n"
}

// Split is (before, block, after); ok false when the markers are missing or out of order.
func (m *Marks) Split(text string) (string, string, string, bool) {
	b := m.beginRe.FindStringIndex(text)
	if b == nil {
		return "", "", "", false
	}
	e := m.endRe.FindStringIndex(text[b[1]:])
	if e == nil {
		return "", "", "", false
	}
	es, ee := b[1]+e[0], b[1]+e[1]
	return text[:b[0]], strings.Trim(text[b[1]:es], "\n"), text[ee:], true
}

func (m *Marks) stripProposed(after string) string {
	pb := m.pbeginRe.FindStringIndex(after)
	if pb == nil {
		return after
	}
	end := pb[1]
	if pe := m.pendRe.FindStringIndex(after[pb[1]:]); pe != nil {
		end = pb[1] + pe[1]
	}
	start := pb[0]
	if h := strings.LastIndex(after[:pb[0]], m.ProposedHead); h >= 0 {
		start = h
	}
	keep, rest := strings.TrimRight(after[:start], "\n"), strings.TrimLeft(after[end:], "\n")
	if strings.TrimSpace(rest) != "" {
		if strings.TrimSpace(keep) != "" {
			return keep + "\n\n" + rest
		}
		return "\n\n" + rest
	}
	if strings.TrimSpace(keep) != "" {
		return keep + "\n"
	}
	return "\n"
}

// Proposed is the content of the proposed-update section; ok false when there is none.
func (m *Marks) Proposed(text string) (string, bool) {
	pb := m.pbeginRe.FindStringIndex(text)
	if pb == nil {
		return "", false
	}
	pe := m.pendRe.FindStringIndex(text[pb[1]:])
	if pe == nil {
		return "", false
	}
	return strings.Trim(text[pb[1]:pb[1]+pe[0]], "\n"), true
}

func tail(after string) string {
	if strings.TrimSpace(after) != "" {
		return "\n\n" + strings.TrimLeft(after, "\n")
	}
	return "\n"
}

func sameText(a, b string) bool { return normTables(normBlock(a)) == normTables(normBlock(b)) }

// Merge is the new text of an existing page: (text, state, flags); state is unchanged, updated, human-edit or
// no-markers. known are the block hashes Argus wrote itself (as Outline returned them).
func (m *Marks) Merge(remote, block string, known []string) (string, string, []string) {
	if strings.TrimSpace(remote) == "" {
		return m.NewDocument(block), "updated", []string{}
	}
	prop := m.ProposedHead + "\n\n" + m.ProposedNote + "\n\n" + m.PBegin + "\n" + strings.TrimSpace(block) + "\n" + m.PEnd
	before, cur, after, ok := m.Split(remote)
	if !ok { // the markers are gone: touch nothing a person wrote
		base := strings.TrimRight(m.stripProposed(remote), "\n")
		text := prop + "\n"
		if strings.TrimSpace(base) != "" {
			text = base + "\n\n" + text
		}
		if sameText(text, remote) {
			return remote, "unchanged", []string{"markers-missing"}
		}
		return text, "no-markers", []string{"markers-missing"}
	}
	clean := m.stripProposed(after)
	if BlockHash(cur) == BlockHash(block) {
		if sameText(clean, after) {
			return remote, "unchanged", []string{}
		}
		return before + m.Wrap(cur) + tail(clean), "updated", []string{}
	}
	ks := map[string]bool{}
	for _, k := range known {
		ks[k] = true
	}
	if ks[BlockHash(cur)] || ks[blockHashLegacy(cur)] {
		return before + m.Wrap(block) + tail(clean), "updated", []string{}
	}
	text := before + m.Wrap(cur) + "\n\n" + prop + tail(clean)
	if sameText(text, remote) {
		return remote, "unchanged", []string{"human-edit"}
	}
	return text, "human-edit", []string{"human-edit"}
}
