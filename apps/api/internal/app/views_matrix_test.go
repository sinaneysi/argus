package app

import (
	"strings"
	"testing"
	"time"
)

func TestMatrixIDsSetClearAndUnique(t *testing.T) {
	boardSetup(t)
	if msg, _ := matrixSetID("viewer1", "not-an-id"); !strings.Contains(msg, "looks like") {
		t.Fatal(msg)
	}
	if msg, err := matrixSetID("viewer1", " @v:chat.northwind.example "); msg != "" || err != nil {
		t.Fatal(msg, err)
	}
	if msg, _ := matrixSetID("alex.doe", "@v:chat.northwind.example"); !strings.Contains(msg, "already belongs to viewer1") {
		t.Fatal(msg)
	}

	matrixSetID("viewer1", "")

}

func TestMatrixPreviewOnlyTheForwardersOwn(t *testing.T) {
	e := boardSetup(t)
	now := time.Now().Unix()
	e.board.Exec(`INSERT INTO matrix_previews (pid, by, state, created, title, requester, project, summary, choices) VALUES
		('20261021000000-abcdef12', 'viewer1', 'pending', ?, ?, ?, 'DEV', ?, '[]')`, now, boardXSS, boardXSS, boardXSS)
	if c, _ := MatrixPostPreview(e.ctx(e.admin), boardForm("pid", "20261021000000-abcdef12", "action", "create"), e.admin); c != 404 {
		t.Fatal("another admin's preview", c)
	}
	if c, _ := MatrixPostPreview(e.ctx(e.admin), boardForm("pid", "x", "action", "create"), e.admin); c != 400 {
		t.Fatal("bad pid")
	}
	e.board.Exec(`UPDATE matrix_previews SET by='alex.doe'`)

	c, loc := MatrixPostPreview(e.ctx(e.admin), boardForm("pid", "20261021000000-abcdef12", "action", "discard"), e.admin)
	if c != 303 {
		t.Fatal(c, loc)
	}
	if p, _ := boardReadRequest(t, loc); p.S("kind") != "mx-preview" || p.S("action") != "discard" {
		t.Fatal(p.PyRepr())
	}
}
