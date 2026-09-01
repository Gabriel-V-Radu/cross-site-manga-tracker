package connectors

import "testing"

func TestPrepareSearchRefusesEmptyQueries(t *testing.T) {
	for _, raw := range []string{"", "   ", "!!!", "---", "( )"} {
		if _, err := PrepareSearch(raw, 10); err == nil {
			t.Errorf("PrepareSearch(%q) must be refused", raw)
		}
	}
}

func TestPrepareSearchNormalizesAndClamps(t *testing.T) {
	query, err := PrepareSearch("  Solo-Leveling: Ragnarok  ", 0)
	if err != nil {
		t.Fatal(err)
	}
	if query.Raw != "Solo-Leveling: Ragnarok" {
		t.Errorf("Raw = %q", query.Raw)
	}
	if query.Normalized != "solo leveling ragnarok" {
		t.Errorf("Normalized = %q", query.Normalized)
	}
	if len(query.Tokens) != 3 {
		t.Errorf("Tokens = %v", query.Tokens)
	}
	if query.Limit != 10 {
		t.Errorf("Limit = %d, want the 10 default", query.Limit)
	}
	if capped, _ := PrepareSearch("x", 500); capped.Limit != 50 {
		t.Errorf("Limit = %d, want the 50 ceiling", capped.Limit)
	}
}

func TestSearchQueryMatches(t *testing.T) {
	query, err := PrepareSearch("leveling solo", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !query.Matches("Something Else", "Solo Leveling") {
		t.Error("a candidate carrying every token must match")
	}
	if query.Matches("Solo Max-Level Newbie") {
		t.Error("a candidate missing a token must not match")
	}
	if query.Matches() {
		t.Error("no candidates never match")
	}
}
