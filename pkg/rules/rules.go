package rules

import (
	"fmt"
	"strings"

	"github.com/falmaz/clickhouse-ci-guard/pkg/analyzer"
)

// Severity defines rule violation severity
type Severity string

const (
	SeverityError Severity = "ERROR"
	SeverityWarn  Severity = "WARN"
	SeverityPass  Severity = "PASS"
)

// Violation represents an issue discovered during query analysis
type Violation struct {
	Severity       Severity
	RuleName       string
	Message        string
	Recommendation string
	Table          string
}

// Config defines thresholds for guardrails
type Config struct {
	MaxEstimatedRows       uint64
	MaxEstimatedParts      uint64
	WarnOnUnfilteredScan   bool
	FailOnCrossJoin        bool
	FailOnUnknownTable     bool
	FailOnUnindexedFilter  bool
}

// DefaultConfig provides production-ready thresholds
func DefaultConfig() Config {
	return Config{
		MaxEstimatedRows:      10_000_000, // 10 million rows
		MaxEstimatedParts:     100,        // 100 parts scan
		WarnOnUnfilteredScan:  true,
		FailOnCrossJoin:       true,
		FailOnUnknownTable:    false,
		FailOnUnindexedFilter: true, // Block PRs with unindexed full table scans
	}
}

// Engine evaluates queries against defined rules
type Engine struct {
	Config Config
}

// NewEngine creates a new rules engine
func NewEngine(cfg Config) *Engine {
	return &Engine{Config: cfg}
}

// Evaluate runs all guardrail rules against the analyzed query
func (e *Engine) Evaluate(analysis *analyzer.QueryAnalysis) []Violation {
	var violations []Violation

	if analysis.Error != nil {
		errStr := analysis.Error.Error()

		if strings.Contains(errStr, "Code: 60") || strings.Contains(errStr, "UNKNOWN_TABLE") ||
			strings.Contains(errStr, "Code: 81") || strings.Contains(errStr, "UNKNOWN_DATABASE") ||
			(strings.Contains(errStr, "Dictionary") && strings.Contains(errStr, "not found")) {
			sev := SeverityWarn
			if e.Config.FailOnUnknownTable {
				sev = SeverityError
			}
			ruleName := "TARGET_TABLE_NOT_FOUND"
			if strings.Contains(errStr, "Dictionary") {
				ruleName = "TARGET_DICTIONARY_NOT_FOUND"
			}
			violations = append(violations, Violation{
				Severity:       sev,
				RuleName:       ruleName,
				Message:        fmt.Sprintf("Referenced dependency is missing in test ClickHouse: %v", extractClickHouseMessage(errStr)),
				Recommendation: "Ensure database migrations, seed dictionaries, or staging fixtures are loaded before running chguard.",
			})
			return violations
		}

		if strings.Contains(errStr, "HTTP request failed") || strings.Contains(errStr, "connection refused") || strings.Contains(errStr, "connection reset") {
			violations = append(violations, Violation{
				Severity:       SeverityError,
				RuleName:       "CLICKHOUSE_CONNECTION_ERROR",
				Message:        fmt.Sprintf("Could not reach ClickHouse server: %v", errStr),
				Recommendation: "Verify ClickHouse host/port, credentials, and that the server is ready before running chguard.",
			})
			return violations
		}

		violations = append(violations, Violation{
			Severity:       SeverityError,
			RuleName:       "CLICKHOUSE_SYNTAX_ERROR",
			Message:        fmt.Sprintf("ClickHouse failed to explain query: %v", extractClickHouseMessage(errStr)),
			Recommendation: "Verify ClickHouse SQL syntax and ensure model does not contain uncompiled Jinja templating.",
		})
		return violations
	}

	// Rule 1: Per-table Structural Unindexed Scan Check (catches unindexed tables even inside multi-table JOINs!)
	for _, tbl := range analysis.UnindexedTables {
		sev := SeverityWarn
		if e.Config.FailOnUnindexedFilter {
			sev = SeverityError
		}
		violations = append(violations, Violation{
			Severity:       sev,
			RuleName:       "UNINDEXED_FULL_TABLE_SCAN",
			Table:          tbl,
			Message:        fmt.Sprintf("Table `%s` is filtered, but NEITHER PrimaryKey, MinMax, Partition, nor Skip index was matched.", tbl),
			Recommendation: "Include columns from the table's ORDER BY key in your WHERE clause, or add a skip index / projection.",
		})
	}

	// Rule 2: Unfiltered Full Table Scan (no WHERE and no LIMIT on a MergeTree table)
	if e.Config.WarnOnUnfilteredScan {
		for _, tbl := range analysis.UnfilteredTables {
			violations = append(violations, Violation{
				Severity:       SeverityWarn,
				RuleName:       "UNFILTERED_FULL_TABLE_SCAN",
				Table:          tbl,
				Message:        fmt.Sprintf("Table `%s` is read with no WHERE/PREWHERE filter and no LIMIT (full table scan).", tbl),
				Recommendation: "Add a partition or primary key filter if this table grows large in production.",
			})
		}
	}

	// Advisory Rule: Empty Table Warning
	if analysis.ReadFromNullSource {
		violations = append(violations, Violation{
			Severity:       SeverityWarn,
			RuleName:       "EMPTY_TEST_TABLE_NULL_SOURCE",
			Message:        "Table has 0 data parts in ClickHouse test instance. Physical index selectivity could not be measured.",
			Recommendation: "Insert at least 1 mock row into test tables to enable full index plan inspection.",
		})
	}

	// Rule 3: Cross Join / Cartesian product
	if e.Config.FailOnCrossJoin && analysis.HasCrossJoin {
		violations = append(violations, Violation{
			Severity:       SeverityError,
			RuleName:       "DANGEROUS_CROSS_JOIN",
			Message:        "Query contains an unconstrained CROSS JOIN / Cartesian product.",
			Recommendation: "Specify explicit JOIN ON / USING conditions to prevent memory exhaustion.",
		})
	}

	// Rule 4: Max Scanned Rows Check
	if e.Config.MaxEstimatedRows > 0 && analysis.TotalRows > e.Config.MaxEstimatedRows {
		violations = append(violations, Violation{
			Severity: SeverityError,
			RuleName: "EXCESSIVE_ROW_SCAN",
			Message: fmt.Sprintf("Query is estimated to scan %s rows (threshold: %s rows).",
				formatNumber(analysis.TotalRows), formatNumber(e.Config.MaxEstimatedRows)),
			Recommendation: "Add stricter partition pruning filters (WHERE date >= ...) or filter by PRIMARY KEY.",
		})
	}

	// Rule 5: Max Parts Check
	if e.Config.MaxEstimatedParts > 0 && analysis.TotalParts > e.Config.MaxEstimatedParts {
		violations = append(violations, Violation{
			Severity: SeverityWarn,
			RuleName: "HIGH_PART_SCAN_COUNT",
			Message: fmt.Sprintf("Query will scan %d data parts (threshold: %d parts).",
				analysis.TotalParts, e.Config.MaxEstimatedParts),
			Recommendation: "Prune partitions explicitly using the table PARTITION BY column.",
		})
	}

	return violations
}

func extractClickHouseMessage(errStr string) string {
	if idx := strings.Index(errStr, "DB::Exception:"); idx != -1 {
		return strings.TrimSpace(errStr[idx:])
	}
	return errStr
}

func formatNumber(n uint64) string {
	in := fmt.Sprintf("%d", n)
	out := make([]byte, len(in)+(len(in)-1)/3)
	for i, j, k := len(in)-1, len(out)-1, 0; i >= 0; i, j, k = i-1, j-1, k+1 {
		if k > 0 && k%3 == 0 {
			out[j] = ','
			j--
		}
		out[j] = in[i]
	}
	return string(out)
}
