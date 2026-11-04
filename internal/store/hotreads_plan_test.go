package store

import (
	"strings"
	"testing"
)

// assertIndexOrderedPlan rejects the plan shapes a whole-population read must
// never use: a temporary sort or grouping B-tree, a materialized CTE, an
// automatic index, or an unindexed scan of the messages population. It also
// requires each wanted index to appear, so a planner change cannot silently
// move the read to another access path. The fixture planner can differ from
// live statistics; these assertions name the failure class rather than one
// exact plan.
func assertIndexOrderedPlan(t *testing.T, label, plan string, wantIndexes ...string) {
	t.Helper()
	for _, banned := range []string{"TEMP B-TREE", "MATERIALIZE", "AUTOMATIC"} {
		if strings.Contains(plan, banned) {
			t.Fatalf("%s plan uses %s:\n%s", label, banned, plan)
		}
	}
	for _, line := range strings.Split(plan, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "SCAN ") || strings.Contains(line, "USING") {
			continue
		}
		for _, table := range []string{"m", "messages", "ra", "route_answers"} {
			if strings.HasSuffix(line, " "+table) {
				t.Fatalf("%s plan scans %s without an index:\n%s", label, table, plan)
			}
		}
	}
	for _, index := range wantIndexes {
		if !strings.Contains(plan, index) {
			t.Fatalf("%s plan does not use %s:\n%s", label, index, plan)
		}
	}
}
