package analyzer

import "testing"

func TestParsePlanJSON_MultiTableJoinCatchesUnindexedTable(t *testing.T) {
	planJSON := `[
		{
			"Plan": {
				"Node Type": "Join",
				"Type": "INNER",
				"Strictness": "ALL",
				"Plans": [
					{
						"Node Type": "ReadFromMergeTree",
						"Description": "analytics.dim_users",
						"Indexes": [
							{
								"Type": "PrimaryKey",
								"Keys": ["user_id"],
								"Condition": "(user_id in [1, 1])"
							}
						]
					},
					{
						"Node Type": "Filter",
						"Plans": [
							{
								"Node Type": "ReadFromMergeTree",
								"Description": "analytics.fct_events",
								"Indexes": [
									{
										"Type": "MinMax",
										"Condition": "true"
									},
									{
										"Type": "PrimaryKey",
										"Condition": "true"
									}
								]
							}
						]
					}
				]
			}
		}
	]`

	analysis := &QueryAnalysis{QueryPath: "join_query.sql"}
	err := ParsePlanJSON(analysis, planJSON, "SELECT * FROM dim_users JOIN fct_events ON dim_users.id = fct_events.uid WHERE fct_events.phrase = 'x'")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(analysis.TableScans) != 2 {
		t.Fatalf("expected 2 table scans, got %d", len(analysis.TableScans))
	}
	if len(analysis.UnindexedTables) != 1 || analysis.UnindexedTables[0] != "analytics.fct_events" {
		t.Fatalf("expected analytics.fct_events in UnindexedTables, got %v", analysis.UnindexedTables)
	}
	if analysis.HasCrossJoin {
		t.Fatalf("did not expect cross join on INNER join")
	}
}

func TestParsePlanJSON_CrossJoinAndUnfilteredScan(t *testing.T) {
	planJSON := `[
		{
			"Plan": {
				"Node Type": "Join",
				"Type": "CROSS",
				"Plans": [
					{
						"Node Type": "ReadFromMergeTree",
						"Description": "analytics.orders",
						"Indexes": [
							{"Type": "PrimaryKey", "Condition": "true"}
						]
					}
				]
			}
		}
	]`

	analysis := &QueryAnalysis{QueryPath: "cross.sql"}
	err := ParsePlanJSON(analysis, planJSON, "SELECT * FROM analytics.orders CROSS JOIN analytics.items")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !analysis.HasCrossJoin {
		t.Fatalf("expected HasCrossJoin to be true")
	}
	if len(analysis.UnfilteredTables) != 1 || analysis.UnfilteredTables[0] != "analytics.orders" {
		t.Fatalf("expected analytics.orders in UnfilteredTables, got %v", analysis.UnfilteredTables)
	}
}
