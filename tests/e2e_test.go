//go:build e2e

package tests

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func chURL() string {
	if v := os.Getenv("CLICKHOUSE_HOST"); v != "" {
		return v
	}
	return "http://localhost:8123"
}

func chUser() string {
	if v := os.Getenv("CLICKHOUSE_USER"); v != "" {
		return v
	}
	return "default"
}

func chPass() string {
	if v := os.Getenv("CLICKHOUSE_PASSWORD"); v != "" {
		return v
	}
	return "ci"
}

func execCH(t *testing.T, query string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, chURL(), bytes.NewBufferString(query))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.SetBasicAuth(chUser(), chPass())
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("clickhouse request failed (%s): %v", query, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clickhouse error (%d) for query %q: %s", resp.StatusCode, query, string(body))
	}
}

func TestE2E_ClickHouseGuardrails(t *testing.T) {
	db := "chguard_e2e"
	execCH(t, fmt.Sprintf("DROP DATABASE IF EXISTS %s", db))
	execCH(t, fmt.Sprintf("CREATE DATABASE %s", db))
	t.Cleanup(func() {
		execCH(t, fmt.Sprintf("DROP DATABASE IF EXISTS %s", db))
	})

	execCH(t, fmt.Sprintf(`CREATE TABLE %s.dim_users (
		user_id UInt64,
		name String,
		tag String
	) ENGINE = MergeTree ORDER BY user_id`, db))

	execCH(t, fmt.Sprintf(`CREATE TABLE %s.fct_events (
		event_date Date,
		user_id UInt64,
		tag String,
		phrase String
	) ENGINE = MergeTree
	PARTITION BY toYYYYMM(event_date)
	ORDER BY (event_date, user_id)`, db))

	execCH(t, fmt.Sprintf(`INSERT INTO %s.dim_users
		SELECT number, concat('user_', toString(number)), concat('tag_', toString(number %% 50))
		FROM numbers(5000)`, db))

	execCH(t, fmt.Sprintf(`INSERT INTO %s.fct_events
		SELECT toDate('2026-01-01') + (number %% 90), number %% 5000, concat('tag_', toString(number %% 50)), concat('phrase;', toString(number %% 500))
		FROM numbers(50000)`, db))

	// Build binary into temp dir
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "chguard")
	buildCmd := exec.Command("go", "build", "-o", binPath, "../cmd/chguard")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build chguard: %v\n%s", err, string(out))
	}

	cases := []struct {
		name           string
		sql            string
		expectExitZero bool
		expectContains string
	}{
		{
			name:           "indexed_query_passes",
			sql:            fmt.Sprintf("SELECT user_id, count() FROM %s.fct_events WHERE event_date = '2026-01-15' AND user_id = 42 GROUP BY user_id;", db),
			expectExitZero: true,
			expectContains: "[✅ PASS]",
		},
		{
			name:           "unindexed_filter_with_semicolon_in_string_fails",
			sql:            fmt.Sprintf("SELECT user_id, count() FROM %s.fct_events WHERE phrase = 'phrase;42' GROUP BY user_id;", db),
			expectExitZero: false,
			expectContains: "UNINDEXED_FULL_TABLE_SCAN",
		},
		{
			name:           "multi_table_join_catches_unindexed_fact_table",
			sql:            fmt.Sprintf("SELECT u.name, count() FROM %s.dim_users u JOIN %s.fct_events e ON u.tag = e.tag WHERE u.user_id = 42 AND e.phrase = 'phrase;10' GROUP BY u.name;", db, db),
			expectExitZero: false,
			expectContains: fmt.Sprintf("Table `%s.fct_events` is filtered", db),
		},
		{
			name:           "cross_join_fails",
			sql:            fmt.Sprintf("SELECT u.name, e.phrase FROM %s.dim_users u CROSS JOIN %s.fct_events e LIMIT 10;", db, db),
			expectExitZero: false,
			expectContains: "DANGEROUS_CROSS_JOIN",
		},
		{
			name:           "unfiltered_scan_warns_without_failing",
			sql:            fmt.Sprintf("SELECT phrase, count() FROM %s.fct_events GROUP BY phrase;", db),
			expectExitZero: true,
			expectContains: "UNFILTERED_FULL_TABLE_SCAN",
		},
		{
			name:           "uncompiled_jinja_warns_without_failing",
			sql:            "{{ config(materialized='table') }}\nSELECT * FROM {{ ref('fct_events') }}",
			expectExitZero: true,
			expectContains: "UNCOMPILED_JINJA_MODEL",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sqlFile := filepath.Join(tmpDir, tc.name+".sql")
			mdFile := filepath.Join(tmpDir, tc.name+".md")
			if err := os.WriteFile(sqlFile, []byte(tc.sql), 0644); err != nil {
				t.Fatalf("write sql file: %v", err)
			}

			cmd := exec.Command(binPath,
				"-host", chURL(),
				"-user", chUser(),
				"-password", chPass(),
				"-database", db,
				"-files", sqlFile,
				"-output-markdown", mdFile,
			)
			outBytes, err := cmd.CombinedOutput()
			out := string(outBytes)

			if tc.expectExitZero && err != nil {
				t.Fatalf("expected exit 0, got error %v\nOutput:\n%s", err, out)
			}
			if !tc.expectExitZero && err == nil {
				t.Fatalf("expected non-zero exit, got exit 0\nOutput:\n%s", out)
			}
			if !strings.Contains(out, tc.expectContains) {
				t.Fatalf("expected output to contain %q\nGot:\n%s", tc.expectContains, out)
			}

			mdBytes, err := os.ReadFile(mdFile)
			if err != nil {
				t.Fatalf("expected markdown report at %s: %v", mdFile, err)
			}
			if !strings.Contains(string(mdBytes), "ClickHouse CI Guard Performance Report") {
				t.Fatalf("unexpected markdown content:\n%s", string(mdBytes))
			}
		})
	}
}
