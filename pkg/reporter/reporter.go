package reporter

import (
	"fmt"
	"os"
	"strings"

	"github.com/falmaz/clickhouse-ci-guard/pkg/analyzer"
	"github.com/falmaz/clickhouse-ci-guard/pkg/rules"
)

// QueryReport combines analysis with rule evaluation
type QueryReport struct {
	Analysis   *analyzer.QueryAnalysis
	Violations []rules.Violation
}

// Summary summarizes the overall test run
type Summary struct {
	TotalQueries int
	Passed       int
	Warnings     int
	Errors       int
}

// PrintTerminal outputs clean colored summaries to stdout
func PrintTerminal(reports []QueryReport) Summary {
	summary := Summary{TotalQueries: len(reports)}

	fmt.Println("\n========================================================")
	fmt.Println("🛡️  CLICKHOUSE CI GUARD: QUERY PERFORMANCE REPORT")
	fmt.Println("========================================================")

	for _, rep := range reports {
		hasError := false
		hasWarn := false

		for _, v := range rep.Violations {
			if v.Severity == rules.SeverityError {
				hasError = true
			} else if v.Severity == rules.SeverityWarn {
				hasWarn = true
			}
		}

		status := "✅ PASS"
		if hasError {
			status = "❌ FAIL"
			summary.Errors++
		} else if hasWarn {
			status = "⚠️  WARN"
			summary.Warnings++
		} else {
			summary.Passed++
		}

		fmt.Printf("\n[%s] %s\n", status, rep.Analysis.QueryPath)
		fmt.Printf("   Est. Rows: %s | Est. Parts: %d | Marks: %d\n",
			formatNumber(rep.Analysis.TotalRows), rep.Analysis.TotalParts, rep.Analysis.TotalMarks)

		if len(rep.Violations) > 0 {
			for _, v := range rep.Violations {
				prefix := "⚠️  WARN"
				if v.Severity == rules.SeverityError {
					prefix = "❌ ERROR"
				}
				fmt.Printf("   %s [%s]: %s\n", prefix, v.RuleName, v.Message)
				if v.Recommendation != "" {
					fmt.Printf("      ↳ Fix: %s\n", v.Recommendation)
				}
			}
		}
	}

	fmt.Println("\n--------------------------------------------------------")
	fmt.Printf("SUMMARY: Total: %d | Passed: %d | Warnings: %d | Errors: %d\n",
		summary.TotalQueries, summary.Passed, summary.Warnings, summary.Errors)
	fmt.Println("========================================================")

	return summary
}

// WriteMarkdown writes a rich GitHub PR comment artifact
func WriteMarkdown(filepath string, reports []QueryReport, summary Summary) error {
	var sb strings.Builder

	sb.WriteString("## 🛡️ ClickHouse CI Guard Performance Report\n\n")

	if summary.Errors > 0 {
		sb.WriteString(fmt.Sprintf("> ❌ **CI Failed:** Found **%d error(s)** and **%d warning(s)** across %d modified query/dbt model(s).\n\n",
			summary.Errors, summary.Warnings, summary.TotalQueries))
	} else if summary.Warnings > 0 {
		sb.WriteString(fmt.Sprintf("> ⚠️ **Passed with warnings:** Found **%d warning(s)** across %d query/dbt model(s).\n\n",
			summary.Warnings, summary.TotalQueries))
	} else {
		sb.WriteString(fmt.Sprintf("> ✅ **All checks passed!** All %d query/dbt model(s) comply with partition and index guardrails.\n\n",
			summary.TotalQueries))
	}

	sb.WriteString("| Status | File / Model | Est. Rows | Est. Parts | Primary Key | Partition Key |\n")
	sb.WriteString("| :---: | :--- | :---: | :---: | :---: | :---: |\n")

	for _, rep := range reports {
		status := "✅"
		hasError := false
		for _, v := range rep.Violations {
			if v.Severity == rules.SeverityError {
				status = "❌"
				hasError = true
				break
			} else if v.Severity == rules.SeverityWarn {
				status = "⚠️"
			}
		}

		pk := "❌ Inactive"
		if rep.Analysis.PrimaryKeyActive {
			pk = "✅ Active"
		}
		part := "❌ Inactive"
		if rep.Analysis.PartitionActive || rep.Analysis.MinMaxActive {
			part = "✅ Active"
		}

		sb.WriteString(fmt.Sprintf("| %s | `%s` | %s | %d | %s | %s |\n",
			status, rep.Analysis.QueryPath, formatNumber(rep.Analysis.TotalRows), rep.Analysis.TotalParts, pk, part))

		if len(rep.Violations) > 0 {
			for _, v := range rep.Violations {
				badge := "⚠️ **WARN**"
				if v.Severity == rules.SeverityError {
					badge = "❌ **FAIL**"
				}
				sb.WriteString(fmt.Sprintf("  * %s `%s`: %s\n", badge, v.RuleName, v.Message))
				if v.Recommendation != "" {
					sb.WriteString(fmt.Sprintf("    * *Recommendation:* %s\n", v.Recommendation))
				}
			}
		}
		if hasError && rep.Analysis.Error != nil {
			sb.WriteString(fmt.Sprintf("  * 💥 **Error Detail:** `%v`\n", rep.Analysis.Error))
		}
	}

	sb.WriteString("\n---\n*Report generated automatically by [ClickHouse CI Guard](https://github.com/falmaz/clickhouse-ci-guard)*\n")

	return os.WriteFile(filepath, []byte(sb.String()), 0644)
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
