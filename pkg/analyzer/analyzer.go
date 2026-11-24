package analyzer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client handles interaction with ClickHouse HTTP endpoint
type Client struct {
	BaseURL    string
	User       string
	Password   string
	Database   string
	HTTPClient *http.Client
}

// NewClient initializes a ClickHouse HTTP client
func NewClient(baseURL, user, password, database string, timeout time.Duration) *Client {
	if baseURL == "" {
		baseURL = "http://localhost:8123"
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	return &Client{
		BaseURL:    baseURL,
		User:       user,
		Password:   password,
		Database:   database,
		HTTPClient: &http.Client{Timeout: timeout},
	}
}

// TableEstimate holds metrics from EXPLAIN ESTIMATE
type TableEstimate struct {
	Database string `json:"database"`
	Table    string `json:"table"`
	Parts    uint64 `json:"parts"`
	Rows     uint64 `json:"rows"`
	Marks    uint64 `json:"marks"`
}

// EstimateResponse maps ClickHouse FORMAT JSON for EXPLAIN ESTIMATE
type EstimateResponse struct {
	Data []map[string]interface{} `json:"data"`
}

// TableScan records index usage for an individual ReadFromMergeTree node in the plan
type TableScan struct {
	Table            string
	MinMaxActive     bool
	PartitionActive  bool
	PrimaryKeyActive bool
	SkipIndexActive  bool
	InitialParts     uint64
	SelectedParts    uint64
	InitialGranules  uint64
	SelectedGranules uint64
}

// HasAnyIndex returns true if any index pruned data on this table scan
func (ts TableScan) HasAnyIndex() bool {
	return ts.MinMaxActive || ts.PartitionActive || ts.PrimaryKeyActive || ts.SkipIndexActive
}

// QueryAnalysis holds comprehensive performance analysis of a query
type QueryAnalysis struct {
	QueryPath          string
	Estimates          []TableEstimate
	TableScans         []TableScan
	TotalRows          uint64
	TotalParts         uint64
	TotalMarks         uint64
	RawPlan            string
	HasFilter          bool
	HasLimit           bool
	MinMaxActive       bool
	PartitionActive    bool
	PrimaryKeyActive   bool
	UnindexedTables    []string
	UnfilteredTables   []string
	ReadFromNullSource bool
	HasCrossJoin       bool
	Error              error
}

// planWrapper matches ClickHouse `EXPLAIN PLAN json = 1, indexes = 1` output
type planWrapper struct {
	Plan planNode `json:"Plan"`
}

type planIndex struct {
	Type             string   `json:"Type"`
	Keys             []string `json:"Keys"`
	Condition        string   `json:"Condition"`
	InitialParts     uint64   `json:"Initial Parts"`
	SelectedParts    uint64   `json:"Selected Parts"`
	InitialGranules  uint64   `json:"Initial Granules"`
	SelectedGranules uint64   `json:"Selected Granules"`
}

type planNode struct {
	NodeType    string      `json:"Node Type"`
	Description string      `json:"Description"`
	Type        string      `json:"Type"`       // e.g. Join type: INNER, LEFT, CROSS
	Strictness  string      `json:"Strictness"` // e.g. ALL, ANY
	Indexes     []planIndex `json:"Indexes"`
	Plans       []planNode  `json:"Plans"`
}

// AnalyzeQuery runs EXPLAIN ESTIMATE and EXPLAIN PLAN json=1, indexes=1 on a query
func (c *Client) AnalyzeQuery(ctx context.Context, queryPath, query string) *QueryAnalysis {
	analysis := &QueryAnalysis{
		QueryPath: queryPath,
	}

	// 1. Run EXPLAIN ESTIMATE
	estimates, err := c.explainEstimate(ctx, query)
	if err != nil {
		analysis.Error = fmt.Errorf("EXPLAIN ESTIMATE failed: %w", err)
		return analysis
	}
	analysis.Estimates = estimates
	for _, e := range estimates {
		analysis.TotalRows += e.Rows
		analysis.TotalParts += e.Parts
		analysis.TotalMarks += e.Marks
	}

	// 2. Run EXPLAIN PLAN json = 1, indexes = 1, description = 1
	planJSON, err := c.explainPlanJSON(ctx, query)
	if err != nil {
		analysis.Error = fmt.Errorf("EXPLAIN PLAN failed: %w", err)
		return analysis
	}
	analysis.RawPlan = planJSON

	if err := ParsePlanJSON(analysis, planJSON, query); err != nil {
		analysis.Error = fmt.Errorf("failed to parse EXPLAIN PLAN JSON: %w", err)
		return analysis
	}

	return analysis
}

// ParsePlanJSON parses ClickHouse `EXPLAIN PLAN json = 1, indexes = 1` output into QueryAnalysis.
// Exported for direct unit testing without requiring a live ClickHouse server.
func ParsePlanJSON(analysis *QueryAnalysis, planJSON, query string) error {
	var wrappers []planWrapper
	if err := json.Unmarshal([]byte(planJSON), &wrappers); err != nil {
		return err
	}

	upperQuery := strings.ToUpper(query)
	if strings.Contains(upperQuery, "WHERE") || strings.Contains(upperQuery, "PREWHERE") {
		analysis.HasFilter = true
	}
	if strings.Contains(upperQuery, "LIMIT") {
		analysis.HasLimit = true
	}

	for _, w := range wrappers {
		walkPlanNode(&w.Plan, analysis, false)
	}

	// Aggregate flags across all table scans
	for _, ts := range analysis.TableScans {
		if ts.MinMaxActive {
			analysis.MinMaxActive = true
		}
		if ts.PartitionActive {
			analysis.PartitionActive = true
		}
		if ts.PrimaryKeyActive {
			analysis.PrimaryKeyActive = true
		}
		if !ts.HasAnyIndex() {
			if analysis.HasFilter {
				analysis.UnindexedTables = append(analysis.UnindexedTables, ts.Table)
			} else if !analysis.HasLimit {
				analysis.UnfilteredTables = append(analysis.UnfilteredTables, ts.Table)
			}
		}
	}

	return nil
}

func walkPlanNode(node *planNode, analysis *QueryAnalysis, underFilter bool) {
	nodeTypeLower := strings.ToLower(node.NodeType)
	descLower := strings.ToLower(node.Description)

	if nodeTypeLower == "filter" {
		analysis.HasFilter = true
		underFilter = true
	}
	if nodeTypeLower == "limit" {
		analysis.HasLimit = true
	}

	if nodeTypeLower == "readnothing" || strings.Contains(descLower, "nullsource") {
		analysis.ReadFromNullSource = true
	}

	// Detect CROSS JOIN accurately from Join nodes
	if nodeTypeLower == "join" {
		if strings.EqualFold(node.Type, "CROSS") ||
			strings.Contains(descLower, "cross") ||
			strings.Contains(descLower, "cartesian") {
			analysis.HasCrossJoin = true
		}
	}

	// Inspect every ReadFromMergeTree node individually
	if nodeTypeLower == "readfrommergetree" {
		tableName := node.Description
		if tableName == "" {
			tableName = "<unknown_mergetree>"
		}
		ts := TableScan{Table: tableName}

		for _, idx := range node.Indexes {
			cond := strings.TrimSpace(strings.ToLower(idx.Condition))
			isActive := (cond != "" && cond != "true") || len(idx.Keys) > 0

			if idx.InitialParts > ts.InitialParts {
				ts.InitialParts = idx.InitialParts
			}
			if idx.SelectedParts > ts.SelectedParts {
				ts.SelectedParts = idx.SelectedParts
			}
			if idx.InitialGranules > ts.InitialGranules {
				ts.InitialGranules = idx.InitialGranules
			}
			if idx.SelectedGranules > ts.SelectedGranules {
				ts.SelectedGranules = idx.SelectedGranules
			}

			switch strings.ToLower(idx.Type) {
			case "minmax":
				if isActive {
					ts.MinMaxActive = true
				}
			case "partition":
				if isActive {
					ts.PartitionActive = true
				}
			case "primarykey":
				if isActive {
					ts.PrimaryKeyActive = true
				}
			case "skip":
				if isActive {
					ts.SkipIndexActive = true
				}
			}
		}

		analysis.TableScans = append(analysis.TableScans, ts)
	}

	for i := range node.Plans {
		walkPlanNode(&node.Plans[i], analysis, underFilter)
	}
}

func (c *Client) executeQuery(ctx context.Context, q string) ([]byte, error) {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}

	params := url.Values{}
	if c.Database != "" {
		params.Set("database", c.Database)
	}
	u.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewBufferString(q))
	if err != nil {
		return nil, err
	}

	if c.User != "" {
		req.SetBasicAuth(c.User, c.Password)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clickhouse error (status %d): %s", resp.StatusCode, string(body))
	}

	return body, nil
}

func (c *Client) explainEstimate(ctx context.Context, query string) ([]TableEstimate, error) {
	explainQuery := fmt.Sprintf("EXPLAIN ESTIMATE %s FORMAT JSON", query)
	body, err := c.executeQuery(ctx, explainQuery)
	if err != nil {
		return nil, err
	}

	var resp EstimateResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse JSON from ClickHouse: %w", err)
	}

	var results []TableEstimate
	for _, row := range resp.Data {
		db, _ := row["database"].(string)
		tbl, _ := row["table"].(string)

		results = append(results, TableEstimate{
			Database: db,
			Table:    tbl,
			Parts:    parseUInt64(row["parts"]),
			Rows:     parseUInt64(row["rows"]),
			Marks:    parseUInt64(row["marks"]),
		})
	}

	return results, nil
}

func (c *Client) explainPlanJSON(ctx context.Context, query string) (string, error) {
	explainQuery := fmt.Sprintf("EXPLAIN PLAN json = 1, indexes = 1, actions = 1, description = 1 %s FORMAT RawBLOB", query)
	body, err := c.executeQuery(ctx, explainQuery)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func parseUInt64(val interface{}) uint64 {
	switch v := val.(type) {
	case float64:
		return uint64(v)
	case string:
		n, _ := strconv.ParseUint(v, 10, 64)
		return n
	case int:
		return uint64(v)
	default:
		return 0
	}
}
