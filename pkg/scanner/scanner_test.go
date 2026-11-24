package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseStatements_WithStringsAndComments(t *testing.T) {
	raw := `
-- Header comment with ; semicolon
/* Block comment with ; semicolon */
SELECT splitByChar(';', col) FROM hits WHERE url LIKE '%;%';
SELECT 1;
`
	stmts := ParseStatements("test.sql", raw)
	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d: %+v", len(stmts), stmts)
	}

	expected := "SELECT splitByChar(';', col) FROM hits WHERE url LIKE '%;%'"
	if stmts[0].Statement != expected {
		t.Fatalf("expected:\n%s\ngot:\n%s", expected, stmts[0].Statement)
	}
	if !stmts[0].IsQueryable {
		t.Fatalf("expected stmt 0 to be queryable")
	}
	if stmts[0].IsJinja {
		t.Fatalf("expected stmt 0 not to be jinja")
	}
}

func TestJinjaDetection(t *testing.T) {
	raw := `
{{ config(materialized='table') }}
SELECT * FROM {{ ref('stg_users') }}
`
	stmts := ParseStatements("model.sql", raw)
	if len(stmts) == 0 {
		t.Fatalf("expected statements")
	}
	if !stmts[0].IsJinja {
		t.Fatalf("expected model to be marked as Jinja")
	}
}

func TestFindSQLFiles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "chguard-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	file1 := filepath.Join(tmpDir, "query1.sql")
	file2 := filepath.Join(tmpDir, "query2.sql")
	nonSql := filepath.Join(tmpDir, "ignore.txt")

	os.WriteFile(file1, []byte("SELECT 1;"), 0644)
	os.WriteFile(file2, []byte("-- comment\nSELECT 2;"), 0644)
	os.WriteFile(nonSql, []byte("ignore me"), 0644)

	found, err := FindSQLFiles(tmpDir, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(found) != 2 {
		t.Fatalf("expected 2 sql statements, found %d", len(found))
	}
}
