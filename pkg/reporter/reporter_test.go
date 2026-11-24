package reporter

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falmaz/clickhouse-ci-guard/pkg/analyzer"
	"github.com/falmaz/clickhouse-ci-guard/pkg/rules"
)

func TestPrintTerminalAndWriteMarkdown(t *testing.T) {
	reports := []QueryReport{
		{
			Analysis: &analyzer.QueryAnalysis{
				QueryPath:        "models/good.sql",
				TotalRows:        1024,
				TotalParts:       1,
				TotalMarks:       1,
				PrimaryKeyActive: true,
				PartitionActive:  true,
			},
			Violations: nil,
		},
		{
			Analysis: &analyzer.QueryAnalysis{
				QueryPath:  "models/warn.sql",
				TotalRows:  5000,
				TotalParts: 2,
				TotalMarks: 2,
			},
			Violations: []rules.Violation{
				{
					Severity:       rules.SeverityWarn,
					RuleName:       "UNFILTERED_FULL_TABLE_SCAN",
					Message:        "Table is read without WHERE.",
					Recommendation: "Add a WHERE clause.",
				},
			},
		},
		{
			Analysis: &analyzer.QueryAnalysis{
				QueryPath:  "models/fail.sql",
				TotalRows:  500000,
				TotalParts: 10,
				TotalMarks: 50,
				Error:      errors.New("simulated failure"),
			},
			Violations: []rules.Violation{
				{
					Severity:       rules.SeverityError,
					RuleName:       "UNINDEXED_FULL_TABLE_SCAN",
					Message:        "No index matched.",
					Recommendation: "Filter by ORDER BY key.",
				},
			},
		},
	}

	summary := PrintTerminal(reports)
	if summary.TotalQueries != 3 || summary.Passed != 1 || summary.Warnings != 1 || summary.Errors != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}

	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "report.md")
	if err := WriteMarkdown(outPath, reports, summary); err != nil {
		t.Fatalf("WriteMarkdown failed: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("failed to read markdown report: %v", err)
	}
	md := string(data)

	expectedSnippets := []string{
		"ClickHouse CI Guard Performance Report",
		"Found **1 error(s)** and **1 warning(s)**",
		"`models/good.sql`",
		"✅ Active",
		"`models/warn.sql`",
		"UNFILTERED_FULL_TABLE_SCAN",
		"`models/fail.sql`",
		"UNINDEXED_FULL_TABLE_SCAN",
		"github.com/falmaz/clickhouse-ci-guard",
	}
	for _, snippet := range expectedSnippets {
		if !strings.Contains(md, snippet) {
			t.Errorf("markdown report missing expected snippet %q", snippet)
		}
	}
}
