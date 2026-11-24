package rules

import (
	"testing"

	"github.com/falmaz/clickhouse-ci-guard/pkg/analyzer"
)

func TestExcessiveRowScan(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxEstimatedRows = 1_000_000
	engine := NewEngine(cfg)

	analysis := &analyzer.QueryAnalysis{
		QueryPath: "models/huge_table.sql",
		TotalRows: 5_000_000,
	}

	violations := engine.Evaluate(analysis)
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(violations))
	}

	if violations[0].Severity != SeverityError {
		t.Fatalf("expected SeverityError, got %s", violations[0].Severity)
	}
	if violations[0].RuleName != "EXCESSIVE_ROW_SCAN" {
		t.Fatalf("expected EXCESSIVE_ROW_SCAN, got %s", violations[0].RuleName)
	}
}

func TestCrossJoinDetection(t *testing.T) {
	cfg := DefaultConfig()
	engine := NewEngine(cfg)

	analysis := &analyzer.QueryAnalysis{
		QueryPath:    "models/bad_join.sql",
		HasCrossJoin: true,
	}

	violations := engine.Evaluate(analysis)
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(violations))
	}

	if violations[0].RuleName != "DANGEROUS_CROSS_JOIN" {
		t.Fatalf("expected DANGEROUS_CROSS_JOIN, got %s", violations[0].RuleName)
	}
}
