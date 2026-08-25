package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kranz-org/kranz/internal/config"
	"github.com/kranz-org/kranz/internal/service"
)

func changeTestLocal(t *testing.T) *Local {
	t.Helper()
	cfg := &config.Config{Project: "Changes", ServiceOrder: []string{"api", "worker"}, Services: map[string]config.Service{
		"api":    {Command: "sleep 30", Shell: "/bin/sh", Tags: []string{"backend"}, Actions: map[string]config.Action{"migrate": {Command: "true", Shell: "/bin/sh"}}, ActionOrder: []string{"migrate"}},
		"worker": {Command: "sleep 30", Shell: "/bin/sh"},
	}}
	local := NewLocal(cfg, nil, Options{SessionID: "session-changes"})
	t.Cleanup(func() { _ = local.Shutdown() })
	return local
}

func TestChangesReportsWhatHappenedNotWhereThingsEndedUp(t *testing.T) {
	local := changeTestLocal(t)
	// A restart returns the service to the state it started in. A diff of two
	// status snapshots shows nothing; the journal has to show the round trip.
	before, err := local.Changes(ChangeQuery{})
	if err != nil {
		t.Fatal(err)
	}
	local.SetServiceStatusForTest("api", config.StatusStarting)
	local.SetServiceStatusForTest("api", config.StatusRunning)
	local.SetServiceStatusForTest("api", config.StatusStopped)
	local.SetServiceStatusForTest("api", config.StatusStarting)
	local.SetServiceStatusForTest("api", config.StatusRunning)

	result, err := local.Changes(ChangeQuery{Since: before.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) != 5 {
		t.Fatalf("changes = %d: %#v", len(result.Changes), result.Changes)
	}
	if result.Cursor <= before.Cursor {
		t.Fatalf("cursor did not advance: %d -> %d", before.Cursor, result.Cursor)
	}
	first, last := result.Changes[0], result.Changes[len(result.Changes)-1]
	if first.Kind != service.TransitionServiceState || first.Service != "api" || first.To != "starting" {
		t.Fatalf("first = %#v", first)
	}
	if last.Run != 2 {
		t.Fatalf("second start must be run 2: %#v", last)
	}
	// Reading again from the returned cursor yields nothing new.
	empty, err := local.Changes(ChangeQuery{Since: result.Cursor})
	if err != nil || len(empty.Changes) != 0 {
		t.Fatalf("replay = %#v, %v", empty.Changes, err)
	}
}

func TestChangesFiltersByKindAndSelector(t *testing.T) {
	local := changeTestLocal(t)
	start, _ := local.Changes(ChangeQuery{})
	local.SetServiceStatusForTest("worker", config.StatusStarting)
	if _, err := local.RunAction(context.Background(), config.ActionID{OwnerKind: config.ActionOwnerService, Owner: "api", Name: "migrate"}); err != nil {
		t.Fatal(err)
	}

	actions, err := local.Changes(ChangeQuery{Since: start.Cursor, Kinds: []string{service.TransitionActionState}})
	if err != nil {
		t.Fatal(err)
	}
	if len(actions.Changes) != 2 {
		t.Fatalf("action changes = %#v", actions.Changes)
	}
	if actions.Changes[1].Action != "api/migrate" || actions.Changes[1].To != "succeeded" {
		t.Fatalf("completion = %#v", actions.Changes[1])
	}

	// A service selector covers the actions that service owns, because "what
	// happened to api" includes what api ran.
	owned, err := local.Changes(ChangeQuery{Since: start.Cursor, Selectors: []string{"api"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(owned.Changes) != 2 {
		t.Fatalf("api changes = %#v", owned.Changes)
	}
	if _, err := local.Changes(ChangeQuery{Kinds: []string{"nonsense"}}); err == nil {
		t.Fatal("unknown kind must be rejected")
	}
}

func TestChangesAnchorsOnAConfigurationGeneration(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "kranz.yaml")
	if err := os.WriteFile(path, []byte("project: Changes\nservices:\n  api:\n    command: sleep 30\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFiles([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	local := NewLocal(cfg, []string{path}, Options{SessionID: "session-generation"})
	t.Cleanup(func() { _ = local.Shutdown() })

	local.SetServiceStatusForTest("api", config.StatusStarting)
	if _, err := local.Reload(true); err != nil {
		t.Fatal(err)
	}
	local.SetServiceStatusForTest("api", config.StatusStopped)

	generation := local.Project().Generation
	result, err := local.Changes(ChangeQuery{SinceGeneration: generation})
	if err != nil {
		t.Fatal(err)
	}
	// Everything from the reload onwards, and nothing that preceded it.
	if len(result.Changes) != 2 {
		t.Fatalf("changes = %#v", result.Changes)
	}
	if result.Changes[0].Kind != service.TransitionConfigReload || result.Changes[0].Generation != generation {
		t.Fatalf("first = %#v", result.Changes[0])
	}
	if result.Changes[1].To != "stopped" {
		t.Fatalf("second = %#v", result.Changes[1])
	}
}
