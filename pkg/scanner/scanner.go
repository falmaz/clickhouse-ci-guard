package scanner

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SQLStatement holds an individual executable SQL query from a file
type SQLStatement struct {
	Path        string
	Statement   string
	IsJinja     bool
	IsQueryable bool // true if SELECT or WITH ... SELECT
}

// FindSQLFiles finds all .sql files and extracts individual executable queries.
func FindSQLFiles(dir string, specificFiles []string) ([]SQLStatement, error) {
	var rawFiles []struct {
		Path    string
		Content string
	}

	if len(specificFiles) > 0 {
		for _, f := range specificFiles {
			trimmed := strings.TrimSpace(f)
			if trimmed == "" || !strings.HasSuffix(strings.ToLower(trimmed), ".sql") {
				continue
			}
			data, err := os.ReadFile(trimmed)
			if err != nil {
				return nil, fmt.Errorf("failed to read file %s: %w", trimmed, err)
			}
			rawFiles = append(rawFiles, struct {
				Path    string
				Content string
			}{Path: trimmed, Content: string(data)})
		}
	} else {
		if dir == "" {
			dir = "."
		}
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if name == ".git" || name == "node_modules" || name == ".venv" || name == ".dbt" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(strings.ToLower(d.Name()), ".sql") {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				rawFiles = append(rawFiles, struct {
					Path    string
					Content string
				}{Path: path, Content: string(data)})
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("error walking directory %s: %w", dir, err)
		}
	}

	var statements []SQLStatement
	for _, rf := range rawFiles {
		parsed := ParseStatements(rf.Path, rf.Content)
		statements = append(statements, parsed...)
	}

	return statements, nil
}

// ParseStatements splits a SQL file into individual statements using a lexer-aware splitter
// that respects single/double/backtick quotes and -- or /* */ comments.
func ParseStatements(path, rawContent string) []SQLStatement {
	isJinja := strings.Contains(rawContent, "{{") && strings.Contains(rawContent, "}}")

	rawParts := splitSQLStatements(rawContent)
	var result []SQLStatement

	stmtIndex := 1
	for _, part := range rawParts {
		clean := stripLeadingCommentsAndWhitespace(part)
		if clean == "" {
			continue
		}

		upper := strings.ToUpper(clean)
		isSelect := strings.HasPrefix(upper, "SELECT") || strings.HasPrefix(upper, "WITH")

		stmtPath := path
		if len(rawParts) > 1 {
			stmtPath = fmt.Sprintf("%s#stmt%d", path, stmtIndex)
		}

		result = append(result, SQLStatement{
			Path:        stmtPath,
			Statement:   clean,
			IsJinja:     isJinja,
			IsQueryable: isSelect,
		})
		stmtIndex++
	}

	return result
}

// splitSQLStatements splits SQL on top-level semicolons, ignoring semicolons inside
// string literals, quoted identifiers, line comments (--), and block comments (/* */).
func splitSQLStatements(sql string) []string {
	var stmts []string
	var cur strings.Builder

	n := len(sql)
	inSingle := false
	inDouble := false
	inBacktick := false
	inLineComment := false
	inBlockComment := false

	for i := 0; i < n; i++ {
		ch := sql[i]
		next := byte(0)
		if i+1 < n {
			next = sql[i+1]
		}

		if inLineComment {
			cur.WriteByte(ch)
			if ch == '\n' {
				inLineComment = false
			}
			continue
		}

		if inBlockComment {
			if ch == '*' && next == '/' {
				inBlockComment = false
				i++ // skip '/'
			}
			continue
		}

		if inSingle {
			cur.WriteByte(ch)
			if ch == '\\' && i+1 < n {
				i++
				cur.WriteByte(sql[i])
			} else if ch == '\'' {
				if next == '\'' {
					i++
					cur.WriteByte(sql[i])
				} else {
					inSingle = false
				}
			}
			continue
		}

		if inDouble {
			cur.WriteByte(ch)
			if ch == '\\' && i+1 < n {
				i++
				cur.WriteByte(sql[i])
			} else if ch == '"' {
				if next == '"' {
					i++
					cur.WriteByte(sql[i])
				} else {
					inDouble = false
				}
			}
			continue
		}

		if inBacktick {
			cur.WriteByte(ch)
			if ch == '`' {
				if next == '`' {
					i++
					cur.WriteByte(sql[i])
				} else {
					inBacktick = false
				}
			}
			continue
		}

		// Not inside any literal or comment
		if ch == '-' && next == '-' {
			inLineComment = true
			cur.WriteByte(ch)
			cur.WriteByte(next)
			i++
			continue
		}

		if ch == '/' && next == '*' {
			inBlockComment = true
			i++
			continue
		}

		if ch == '\'' {
			inSingle = true
			cur.WriteByte(ch)
			continue
		}

		if ch == '"' {
			inDouble = true
			cur.WriteByte(ch)
			continue
		}

		if ch == '`' {
			inBacktick = true
			cur.WriteByte(ch)
			continue
		}

		if ch == ';' {
			if s := strings.TrimSpace(cur.String()); s != "" {
				stmts = append(stmts, s)
			}
			cur.Reset()
			continue
		}

		cur.WriteByte(ch)
	}

	if s := strings.TrimSpace(cur.String()); s != "" {
		stmts = append(stmts, s)
	}

	return stmts
}

func stripLeadingCommentsAndWhitespace(s string) string {
	sc := bufio.NewScanner(strings.NewReader(s))
	// Support large SQL queries (up to 4MB lines)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var lines []string
	foundStart := false

	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if !foundStart {
			if trimmed == "" || strings.HasPrefix(trimmed, "--") {
				continue
			}
			foundStart = true
		}
		lines = append(lines, line)
	}

	return strings.TrimSpace(strings.Join(lines, "\n"))
}
