# ClickHouse CI Guard (`chguard`) 🛡️

**Fast, automated ClickHouse & dbt query performance guardrails for CI/CD pipelines.**

Written in Go. Zero dependencies. Sub-second execution.

---

## The Problem It Solves

When data engineers and analysts write dbt models or ClickHouse SQL:
1. Standard SQL linters (like `sqlfluff`) only check **syntax formatting** (commas, indentation).
2. A single PR can introduce a query with a missing `PARTITION BY` filter, missing sorting key, or accidental Cartesian cross-join.
3. The PR merges into `main`, hits production ClickHouse, and causes an **Out-Of-Memory (OOM) crash** or blocks the cluster.

**ClickHouse CI Guard catches this in Pull Requests BEFORE merge.**

---

## How It Works

`chguard` runs `EXPLAIN ESTIMATE` and `EXPLAIN PLAN indexes=1` directly against your ClickHouse test/staging instance during CI:

* 🚨 **Fails PR** if an unindexed query scans > 10,000,000 rows.
* 🚨 **Fails PR** on unconstrained Cartesian cross-joins.
* ⚠️ **Warns** if a partitioned table is queried without active MinMax partition pruning.
* ⚠️ **Warns** if table sorting/primary keys are completely ignored.
* 📝 **Outputs a clean Markdown table** ready to be posted as a GitHub PR comment.

---

## Installation

### Binary (Go)
```bash
go build -o /usr/local/bin/chguard ./cmd/chguard
```

### GitHub Action
Add to `.github/workflows/dbt_ci.yml`:

```yaml
- name: ClickHouse Performance Guard
  uses: falmaz/clickhouse-ci-guard@v1
  with:
    clickhouse_host: ${{ secrets.CLICKHOUSE_HOST }}
    clickhouse_user: ${{ secrets.CLICKHOUSE_USER }}
    clickhouse_password: ${{ secrets.CLICKHOUSE_PASSWORD }}
    dir: 'target/compiled/'
    max_rows: '10000000'
    fail_on_error: 'true'
    output_markdown: 'chguard-report.md'

- name: Post PR Comment
  if: always()
  uses: actions/github-script@v7
  with:
    script: |
      const fs = require('fs');
      if (fs.existsSync('chguard-report.md')) {
        const body = fs.readFileSync('chguard-report.md', 'utf8');
        github.rest.issues.createComment({
          issue_number: context.issue.number,
          owner: context.repo.owner,
          repo: context.repo.repo,
          body: body
        });
      }
```

---

## CLI Usage

```bash
# Scan all compiled dbt models
chguard -dir target/compiled/ -host http://localhost:8123 -database analytics

# Scan specific files from git diff
chguard -files "models/marts/fct_orders.sql,models/staging/stg_users.sql"

# Set strict row thresholds
chguard -dir target/compiled/ -max-rows 5000000 -fail-on-error=true -output-markdown report.md
```

---

## License & Commercial Support

* **MIT Licensed (Free & Open Source).**
* Need a fixed-price async ClickHouse schema, query performance, or cloud cost audit? Open an issue or reach out via GitHub profile.
