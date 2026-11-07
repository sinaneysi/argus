package app

import (
	"os"
	"path/filepath"
)

// BoardDBPath is views_board.board_db(): ARGUS_BOARD_DB when set, else board/board.db next to ops.db.
func BoardDBPath() string {
	if v := envSet("ARGUS_BOARD_DB"); v != "" {
		return v
	}
	return filepath.Join(filepath.Dir(DB), "board", "board.db")
}

// BoardBQ is views_board.bq(sql, *args): a read-only query on board.db; nil when it does not exist yet or on
// any SQLite error.
func BoardBQ(query string, args ...any) []Row {
	path := BoardDBPath()
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	rows, err := QueryPath(path, query, args...)
	if err != nil {
		return nil
	}
	return rows
}
