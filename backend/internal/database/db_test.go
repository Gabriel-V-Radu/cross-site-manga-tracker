package database

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestUnicodeLowerFoldsBeyondASCII pins why unicode_lower exists: SQLite's
// own lower() leaves 'É' and 'Ō' as they are, so it cannot be compared with a
// value Go lowered. NULL has to stay NULL so COALESCE and IS keep working
// around it.
func TestUnicodeLowerFoldsBeyondASCII(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cases := []struct {
		input any
		want  sql.NullString
	}{
		{input: "Época", want: sql.NullString{String: "época", Valid: true}},
		{input: "ŌKAMI Kakushi", want: sql.NullString{String: "ōkami kakushi", Valid: true}},
		{input: "already lower", want: sql.NullString{String: "already lower", Valid: true}},
		{input: nil, want: sql.NullString{}},
	}

	for _, testCase := range cases {
		var got sql.NullString
		if err := db.QueryRow(`SELECT unicode_lower(?)`, testCase.input).Scan(&got); err != nil {
			t.Fatalf("unicode_lower(%v): %v", testCase.input, err)
		}
		if got != testCase.want {
			t.Fatalf("unicode_lower(%v) = %#v, want %#v", testCase.input, got, testCase.want)
		}
	}
}
