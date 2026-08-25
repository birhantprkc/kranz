package app

import (
	"testing"

	"github.com/kranz-org/kranz/internal/config"
)

func TestFailedPrerequisiteIsReportedAsAStructuredCause(t *testing.T) {
	cfg := &config.Config{Project: "Causes", ServiceOrder: []string{"api"}, Services: map[string]config.Service{
		"api": {
			Command:     "sleep 30",
			Shell:       "/bin/sh",
			BeforeStart: []config.Prerequisite{{Action: "migrate"}},
			Actions:     map[string]config.Action{"migrate": {Command: "exit 3", Shell: "/bin/sh"}},
			ActionOrder: []string{"migrate"},
		},
	}}
	local := NewLocal(cfg, nil, Options{SessionID: "session-cause"})
	t.Cleanup(func() { _ = local.Shutdown() })

	if err := local.ForceStartServices([]string{"api"}); err == nil {
		t.Fatal("a failed prerequisite must not start the service")
	}
	snapshot, ok := local.Service("api")
	if !ok || snapshot.State.Cause == nil {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	cause := snapshot.State.Cause
	// The reason has to be readable without parsing log text: which action,
	// which run of it, and what the failure was.
	if cause.Type != "prerequisite_failed" || cause.Action != "api/migrate" || cause.ActionRun != 1 {
		t.Fatalf("cause = %#v", cause)
	}
	if cause.Message == "" || cause.At.IsZero() {
		t.Fatalf("cause detail = %#v", cause)
	}
	// A new start attempt is a new story, so the stale reason is dropped.
	local.SetServiceStatusForTest("api", config.StatusStarting)
	if snapshot, _ := local.Service("api"); snapshot.State.Cause != nil {
		t.Fatalf("cause survived a new start: %#v", snapshot.State.Cause)
	}
}

func TestDependencyFailureIsReportedOnTheBlockedService(t *testing.T) {
	cfg := &config.Config{Project: "Causes", ServiceOrder: []string{"db", "api"}, Services: map[string]config.Service{
		"db":  {Command: "exit 1", Shell: "/bin/sh"},
		"api": {Command: "sleep 30", Shell: "/bin/sh", DependsOn: []string{"db"}},
	}}
	local := NewLocal(cfg, nil, Options{SessionID: "session-dependency"})
	t.Cleanup(func() { _ = local.Shutdown() })

	local.SetServiceStateForTest("db", config.ServiceState{Status: config.StatusStopped, Completed: true, ExitCode: 1})
	local.SetServiceDesiredRunningForTest("api", true)

	snapshot, ok := local.Service("api")
	if !ok || snapshot.State.Cause == nil || snapshot.State.Cause.Type != "dependency_failed" {
		t.Fatalf("cause = %#v", snapshot.State.Cause)
	}
	if snapshot.State.Cause.Dependency != "db" || snapshot.State.Cause.ExitCode == nil || *snapshot.State.Cause.ExitCode != 1 {
		t.Fatalf("cause detail = %#v", snapshot.State.Cause)
	}
}
