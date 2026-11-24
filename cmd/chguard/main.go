package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/falmaz/clickhouse-ci-guard/pkg/analyzer"
	"github.com/falmaz/clickhouse-ci-guard/pkg/reporter"
	"github.com/falmaz/clickhouse-ci-guard/pkg/rules"
	"github.com/falmaz/clickhouse-ci-guard/pkg/scanner"
)

func main() {
	// Flags
	host := flag.String("host", envOrDefault("CLICKHOUSE_HOST", "http://localhost:8123"), "ClickHouse HTTP URL")
	user := flag.String("user", envOrDefault("CLICKHOUSE_USER", "default"), "ClickHouse Username")
	password := flag.String("password", envOrDefault("CLICKHOUSE_PASSWORD", ""), "ClickHouse Password")
	database := flag.String("database", envOrDefault("CLICKHOUSE_DATABASE", "default"), "ClickHouse Database")

	dir := flag.String("dir", "", "Directory containing .sql files to inspect (e.g. target/compiled/)")
	filesArg := flag.String("files", "", "Comma-separated list of specific .sql files to scan")
	maxRows := flag.Uint64("max-rows", 10_000_000, "Maximum allowed estimated row scan before error")
	maxParts := flag.Uint64("max-parts", 100, "Maximum allowed estimated parts scan before warning")
	failOnError := flag.Bool("fail-on-error", true, "Exit with non-zero exit code if violations with ERROR occur")
	failOnMissingTable := flag.Bool("fail-on-missing-table", false, "Fail if a referenced table is not yet created in ClickHouse")
	outputMarkdown := flag.String("output-markdown", "", "Optional path to export a GitHub PR markdown comment")
	concurrency := flag.Int("concurrency", 8, "Number of concurrent EXPLAIN queries")
	timeoutSec := flag.Int("timeout", 15, "Timeout in seconds per EXPLAIN query")

	flag.Parse()

	// 1. Discover statements
	var specificFiles []string
	if *filesArg != "" {
		specificFiles = strings.Split(*filesArg, ",")
	}

	statements, err := scanner.FindSQLFiles(*dir, specificFiles)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error discovering SQL files: %v\n", err)
		os.Exit(1)
	}

	if len(statements) == 0 {
		fmt.Println("No SQL statements found to analyze. Exiting.")
		os.Exit(0)
	}

	// 2. Initialize analyzer and rules engine
	client := analyzer.NewClient(*host, *user, *password, *database, time.Duration(*timeoutSec)*time.Second)
	ruleCfg := rules.DefaultConfig()
	ruleCfg.MaxEstimatedRows = *maxRows
	ruleCfg.MaxEstimatedParts = *maxParts
	ruleCfg.FailOnUnknownTable = *failOnMissingTable
	engine := rules.NewEngine(ruleCfg)

	fmt.Printf("🔍 Discovered %d statement(s). Connecting to ClickHouse at %s...\n", len(statements), *host)

	// 3. Filter queryable statements vs DDL vs Jinja
	var reports []reporter.QueryReport
	var toAnalyze []scanner.SQLStatement

	for _, stmt := range statements {
		if stmt.IsJinja {
			reports = append(reports, reporter.QueryReport{
				Analysis: &analyzer.QueryAnalysis{
					QueryPath: stmt.Path,
				},
				Violations: []rules.Violation{{
					Severity:       rules.SeverityWarn,
					RuleName:       "UNCOMPILED_JINJA_MODEL",
					Message:        "Model contains uncompiled Jinja {{ ... }} tags.",
					Recommendation: "Run 'dbt compile' before running chguard to analyze compiled SQL.",
				}},
			})
			continue
		}

		if !stmt.IsQueryable {
			// Skip DDL (CREATE, INSERT, DROP, etc.) without error
			continue
		}

		toAnalyze = append(toAnalyze, stmt)
	}

	// 4. Concurrently analyze queryable statements
	analyzedReports := make([]reporter.QueryReport, len(toAnalyze))
	sem := make(chan struct{}, *concurrency)
	var wg sync.WaitGroup
	ctx := context.Background()

	for i, stmt := range toAnalyze {
		wg.Add(1)
		go func(idx int, s scanner.SQLStatement) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			analysis := client.AnalyzeQuery(ctx, s.Path, s.Statement)
			violations := engine.Evaluate(analysis)

			analyzedReports[idx] = reporter.QueryReport{
				Analysis:   analysis,
				Violations: violations,
			}
		}(i, stmt)
	}

	wg.Wait()

	reports = append(reports, analyzedReports...)

	// 5. Output results
	summary := reporter.PrintTerminal(reports)

	// 6. Generate Markdown if requested
	if *outputMarkdown != "" {
		if err := reporter.WriteMarkdown(*outputMarkdown, reports, summary); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to write markdown report: %v\n", err)
		} else {
			fmt.Printf("📝 Wrote markdown report to %s\n", *outputMarkdown)
		}
	}

	// 7. Exit code
	if *failOnError && summary.Errors > 0 {
		fmt.Fprintf(os.Stderr, "\n❌ CI check failed with %d error(s).\n", summary.Errors)
		os.Exit(1)
	}

	fmt.Println("\n✅ All checks completed successfully.")
}

func envOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
