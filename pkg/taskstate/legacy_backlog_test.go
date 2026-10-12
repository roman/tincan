package taskstate

import (
	"slices"
	"strings"
	"testing"
)

func TestParseLegacyBacklogPreservesTaskFields(t *testing.T) {
	data := []byte(`# Backlog

## In flight
- [ ] task-a - Fix the worker (repo: exmachina) (kind: ship) (priority: 2) (since 2026-10-01) (hold: awaiting review) (hold-kind: captain) blocked-by: task-b - review first
  First paragraph.

  Second paragraph.
## Queued
- [ ] task-b - Review the worker (repo: exmachina) (kind: audit)
## Done
- [x] task-c - Write notes (repo: exmachina) (kind: docs) (done 2026-10-02)
`)
	rows, err := ParseLegacyBacklog(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	first := rows[0]
	if first.State != InFlight || first.Definition.ID != "task-a" ||
		first.Definition.Title != "Fix the worker" ||
		first.Definition.Body != "First paragraph.\n\nSecond paragraph." ||
		first.Definition.Repo != "exmachina" || first.Definition.Kind != "ship" ||
		first.Definition.Priority == nil || *first.Definition.Priority != 2 ||
		first.Definition.Created != "2026-10-01" ||
		first.Definition.Hold == nil || first.Definition.Hold.Reason != "awaiting review" ||
		first.Definition.Hold.Kind != "captain" ||
		len(first.Definition.Dependencies) != 1 ||
		first.Definition.Dependencies[0] != (Dependency{
			Type: "blocked-by", ID: "task-b", Reason: "review first",
		}) {
		t.Fatalf("first row = %#v", first)
	}
	if rows[1].State != Queued || rows[2].State != Done ||
		rows[2].Definition.Closed != "2026-10-02" {
		t.Fatalf("other rows = %#v", rows[1:])
	}
}

func TestParseLegacyBacklogAcceptsPinnedGrammar(t *testing.T) {
	data := []byte("## Queued\n" +
		"- [ ] task-a - Fix worker blocked-by: task-b (repo: exmachina) (kind: ship)   \n" +
		"- [ ] task-b - Fix report.md (reported 2026-06-22): (repo: selection) wording (repo: exmachina) (kind: audit)\n" +
		"- [ ] task-c - Check status(priority: 2) (repo: exmachina) (kind: audit)\n" +
		"- [ ] task-d - Repair notifier (repo: exmachina) (kind: ship) blocked-by: task-b - reason includes unblocked-by: text\n" +
		"- [ ] task-e - SHIP: release (repo: exmachina)\n")
	rows, err := ParseLegacyBacklog(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 || len(rows[0].Definition.Dependencies) != 1 ||
		rows[0].Definition.Dependencies[0].ID != "task-b" ||
		rows[0].Definition.Title != "Fix worker" ||
		rows[1].Definition.Title != "Fix report.md (reported 2026-06-22): (repo: selection) wording" ||
		rows[2].Definition.Priority == nil || *rows[2].Definition.Priority != 2 ||
		len(rows[3].Definition.Dependencies) != 1 ||
		rows[3].Definition.Dependencies[0].Reason != "reason includes unblocked-by: text" ||
		rows[4].Definition.Kind != "ship" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestParseLegacyBacklogUsesTabSectionHeader(t *testing.T) {
	data := []byte(
		"## Queued\n##\tIn flight\n- [ ] task-a - Run audit (repo: exmachina) (kind: audit)\n",
	)
	rows, err := ParseLegacyBacklog(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].State != InFlight {
		t.Fatalf("rows = %#v, want one in-flight task", rows)
	}
}

func TestParseLegacyBacklogPreservesDependencyOrder(t *testing.T) {
	data := []byte("## Queued\n" +
		"- [ ] task-a - Work blocked-by: task-b (repo: exmachina) (kind: ship) parent: task-c - parent reason discovered-from: task-d - source reason\n")
	rows, err := ParseLegacyBacklog(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []Dependency{
		{Type: "blocked-by", ID: "task-b"},
		{Type: "parent", ID: "task-c", Reason: "parent reason"},
		{Type: "discovered-from", ID: "task-d", Reason: "source reason"},
	}
	if len(rows) != 1 || !slices.Equal(rows[0].Definition.Dependencies, want) {
		t.Fatalf("rows = %#v, want dependencies %#v", rows, want)
	}
}

func TestParseLegacyBacklogRejectsUnsafeRows(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{
			"duplicate ID",
			"## Queued\n- [ ] task-a - First (repo: exmachina) (kind: audit)\n## Done\n- [x] task-a - Second (repo: exmachina) (kind: audit)\n",
		},
		{
			"malformed bullet",
			"## Queued\n- [x] task-a - Wrong checkbox (repo: exmachina) (kind: audit)\n",
		},
		{
			"unknown section",
			"## Parked\n- [ ] task-a - Hidden task (repo: exmachina) (kind: audit)\n",
		},
		{"missing repository", "## Queued\n- [ ] task-a - No repository (kind: audit)\n"},
		{
			"duplicate tag",
			"## Queued\n- [ ] task-a - Two repositories (repo: exmachina) (repo: tincan) (kind: audit)\n",
		},
		{
			"malformed priority",
			"## Queued\n- [ ] task-a - Wrong priority (priority: 5) (repo: exmachina) (kind: audit)\n",
		},
		{
			"malformed hold",
			"## Queued\n- [ ] task-a - Wrong hold (hold-kind: unknown) (repo: exmachina) (kind: audit)\n",
		},
		{"orphan body", "## Queued\n  text without a task\n"},
		{
			"unsupported metadata",
			"## Queued\n- [ ] task-a - Follow-up (repo: exmachina) (kind: public-followup)\n  <!-- tasks-axi:public-followup/v1:abc -->\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseLegacyBacklog([]byte(test.text)); err == nil {
				t.Fatal("unsafe backlog accepted")
			}
		})
	}
}

func TestParseLegacyBacklogRejectsOversizeInput(t *testing.T) {
	if _, err := ParseLegacyBacklog([]byte(strings.Repeat("x", maximumLegacyBacklogBytes+1))); err == nil {
		t.Fatal("oversize backlog accepted")
	}
}
