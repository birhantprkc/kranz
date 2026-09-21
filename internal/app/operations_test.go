package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kranz-org/kranz/internal/config"
)

func parameterizedTestLocal(t *testing.T) (*Local, config.ActionID) {
	t.Helper()
	directory := t.TempDir()
	data := "project: Param\n" +
		"action_groups:\n" +
		"  g:\n" +
		"    dir: " + directory + "\n" +
		"    actions:\n" +
		"      seed:\n" +
		"        params:\n" +
		"          target:\n" +
		"            type: text\n" +
		"            required: true\n" +
		"        run:\n" +
		"          - /bin/sh\n" +
		"          - -c\n" +
		"          - 'printenv TARGET > result.txt'\n" +
		"        env:\n" +
		"          TARGET: \"{{target}}\"\n" +
		"        confirm: true\n"
	path := filepath.Join(directory, "kranz.yaml")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	local := NewLocal(cfg, []string{path}, Options{SessionID: "param-test"})
	t.Cleanup(func() { _ = local.Shutdown() })
	return local, config.ActionID{OwnerKind: config.ActionOwnerGroup, Owner: "g", Name: "seed"}
}

func TestParameterizedPlanBindsValuesAndRendersDefinition(t *testing.T) {
	local, id := parameterizedTestLocal(t)
	request := PlanRequest{Operation: "action", Action: id, Params: map[string]json.RawMessage{"target": json.RawMessage(`"phone"`)}}
	plan, err := local.Plan(request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Params["target"] != "phone" {
		t.Fatalf("plan params = %#v", plan.Params)
	}
	if plan.CommandPreview == "" || !plan.RequiresConfirmation {
		t.Fatalf("plan = %#v", plan)
	}
	if plan.SchemaVersion != OperationSchemaVersion {
		t.Fatalf("schema version = %d", plan.SchemaVersion)
	}

	changed := request
	changed.Params = map[string]json.RawMessage{"target": json.RawMessage(`"watch"`)}
	if _, err := local.ExecutePlan(context.Background(), changed, plan.ConfirmationToken); err == nil {
		t.Fatal("expected a changed parameter set to invalidate the token")
	}

	fresh, err := local.Plan(request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := local.ExecutePlan(context.Background(), request, fresh.ConfirmationToken)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.ActionResult == nil || result.ActionResult.Status.String() != "succeeded" {
		t.Fatalf("result = %#v", result.ActionResult)
	}
	if result.ActionResult.Params["target"] != "phone" {
		t.Fatalf("result params = %#v", result.ActionResult.Params)
	}
	directory := local.Config().ActionGroups["g"].Dir
	content, err := os.ReadFile(filepath.Join(directory, "result.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(content)) != "phone" {
		t.Fatalf("env projection result = %q", content)
	}
}

func TestParameterizedInteractivePlanHandsOffTheConfirmedDefinition(t *testing.T) {
	interactive := true
	cfg := &config.Config{Project: "Interactive params", ActionGroups: map[string]config.ActionGroup{
		"tools": {Actions: map[string]config.Action{
			"console": {
				Interactive: &interactive,
				Params: map[string]config.ActionParam{
					"mode": {Type: "select", Options: config.ActionParamOptions{{Value: "safe"}, {Value: "write"}}, Default: "safe", Arg: "--mode"},
				},
				ParamOrder: []string{"mode"},
				Run:        config.ArgvList{"/bin/echo"},
			},
		}}}, ActionGroupOrder: []string{"tools"}}
	local := NewLocal(cfg, nil, Options{SessionID: "interactive-params"})
	defer func() { _ = local.Shutdown() }()
	id := config.ActionID{OwnerKind: config.ActionOwnerGroup, Owner: "tools", Name: "console"}
	request := PlanRequest{Operation: "action", Action: id, Params: map[string]json.RawMessage{"mode": json.RawMessage(`"write"`)}}

	_, _, _, err := local.AcquireInteractivePlan(context.Background(), request, "")
	var required *ConfirmationRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("first handoff = %v, want confirmation", err)
	}
	plan, action, lease, err := local.AcquireInteractivePlan(context.Background(), request, required.Plan.ConfirmationToken)
	if err != nil {
		t.Fatalf("confirmed handoff: %v", err)
	}
	if lease == "" || plan.CommandPreview != "/bin/echo --mode write" {
		t.Fatalf("handoff = plan %#v, lease %q", plan, lease)
	}
	if got := []string(action.Argv); !slices.Equal(got, []string{"/bin/echo", "--mode", "write"}) {
		t.Fatalf("rendered argv = %#v", got)
	}
	if action.ParamValues["mode"] != "write" {
		t.Fatalf("rendered params = %#v", action.ParamValues)
	}
	result, err := local.CompleteInteractiveAction(id, lease, nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.CommandPreview != plan.CommandPreview || result.Params["mode"] != "write" {
		t.Fatalf("recorded result = %#v", result)
	}
}

func TestParameterizedPlanRejectsUnknownAndMissingValues(t *testing.T) {
	local, id := parameterizedTestLocal(t)
	_, err := local.Plan(PlanRequest{Operation: "action", Action: id, Params: map[string]json.RawMessage{"missing": json.RawMessage(`"x"`)}})
	var invalid *InvalidArgumentsError
	if !errors.As(err, &invalid) {
		t.Fatalf("unknown parameter error = %v", err)
	}
	_, err = local.Plan(PlanRequest{Operation: "action", Action: id})
	if !errors.As(err, &invalid) {
		t.Fatalf("missing parameter error = %v", err)
	}
}

func operationTestLocal(t *testing.T) *Local {
	t.Helper()
	confirm := true
	cfg := &config.Config{Project: "Operations", ServiceOrder: []string{"db", "api"}, Services: map[string]config.Service{
		"db":  {Command: "sleep 30", Shell: "/bin/sh", Tags: []string{"backend"}},
		"api": {Command: "sleep 30", Shell: "/bin/sh", DependsOn: []string{"db"}, Actions: map[string]config.Action{"deploy": {Command: "true", Shell: "/bin/sh", Confirm: &confirm}}, ActionOrder: []string{"deploy"}},
	}}
	local := NewLocal(cfg, nil, Options{SessionID: "session-test"})
	t.Cleanup(func() { _ = local.Shutdown() })
	return local
}

func reloadOperationTestLocal(t *testing.T) (*Local, PlanRequest, string) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "kranz.yaml")
	data := "project: Operations\nservices:\n  api:\n    command: \"true\"\n    actions:\n      deploy:\n        command: \"true\"\n        confirm: true\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	local := NewLocal(cfg, []string{path}, Options{SessionID: "reload-test"})
	t.Cleanup(func() { _ = local.Shutdown() })
	id := config.ActionID{OwnerKind: config.ActionOwnerService, Owner: "api", Name: "deploy"}
	return local, PlanRequest{Operation: "action", Action: id}, path
}

func TestPlanUsesSharedSelectorsAndDependencyWaves(t *testing.T) {
	local := operationTestLocal(t)
	plan, err := local.Plan(PlanRequest{Operation: "start", Selectors: []string{"api"}, IncludeDependencies: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.SchemaVersion != OperationSchemaVersion || plan.SessionID != "session-test" || plan.Generation != 1 {
		t.Fatalf("identity = %#v", plan)
	}
	if len(plan.Targets) != 2 || plan.Targets[0] != "db" || plan.Targets[1] != "api" || len(plan.Waves) != 2 {
		t.Fatalf("plan = %#v", plan)
	}
	selected, err := ResolveServiceSelectors(local.Config(), []string{"backend"})
	if err != nil || len(selected) != 1 || selected[0] != "db" {
		t.Fatalf("selector = %v, %v", selected, err)
	}
}

func TestConfirmationTokenIsOneShotAndPlanBound(t *testing.T) {
	local := operationTestLocal(t)
	id := config.ActionID{OwnerKind: config.ActionOwnerService, Owner: "api", Name: "deploy"}
	request := PlanRequest{Operation: "action", Action: id}
	plan, err := local.Plan(request)
	if err != nil || plan.ConfirmationToken == "" {
		t.Fatalf("plan = %#v, %v", plan, err)
	}
	result, err := local.ExecutePlan(context.Background(), request, plan.ConfirmationToken)
	if err != nil || result.ActionResult == nil || result.ActionResult.Run != 1 {
		t.Fatalf("execute = %#v, %v", result, err)
	}
	if local.nextConfirmationSequence != 1 {
		t.Fatalf("confirmation sequence = %d, want 1 without a throwaway execution token", local.nextConfirmationSequence)
	}
	_, err = local.ExecutePlan(context.Background(), request, plan.ConfirmationToken)
	var confirmation *ConfirmationError
	if !errors.As(err, &confirmation) || confirmation.Code != "confirmation_expired" {
		t.Fatalf("reuse err = %#v", err)
	}

	changed, _ := local.Plan(request)
	action := local.cfg.Services["api"].Actions["deploy"]
	confirm := false
	action.Confirm = &confirm
	service := local.cfg.Services["api"]
	service.Actions["deploy"] = action
	local.cfg.Services["api"] = service
	_, err = local.ExecutePlan(context.Background(), request, changed.ConfirmationToken)
	if !errors.As(err, &confirmation) || confirmation.Code != "confirmation_plan_changed" {
		t.Fatalf("changed err = %#v", err)
	}
}

func TestReloadInvalidatesConfirmationToken(t *testing.T) {
	local, request, path := reloadOperationTestLocal(t)
	plan, _ := local.Plan(request)
	updated := "project: Operations\nservices:\n  api:\n    command: \"true\"\n    actions:\n      deploy:\n        command: \"true\"\n        description: Updated\n        confirm: true\n"
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Reload(true); err != nil {
		t.Fatal(err)
	}
	if len(local.confirmations) != 0 {
		t.Fatalf("confirmations after accepted reload = %d, want 0", len(local.confirmations))
	}
	_, err := local.ExecutePlan(context.Background(), request, plan.ConfirmationToken)
	var confirmation *ConfirmationError
	if !errors.As(err, &confirmation) || confirmation.Code != "confirmation_expired" {
		t.Fatalf("reload err = %#v", err)
	}
}

func TestNoOpDebouncedAndFailedReloadsPreserveConfirmationTokens(t *testing.T) {
	t.Run("no-op", func(t *testing.T) {
		local, request, _ := reloadOperationTestLocal(t)
		plan, err := local.Plan(request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := local.Reload(false); err != nil {
			t.Fatal(err)
		}
		if _, err := local.ExecutePlan(context.Background(), request, plan.ConfirmationToken); err != nil {
			t.Fatalf("execute after no-op reload: %v", err)
		}
	})

	t.Run("debounced", func(t *testing.T) {
		local, request, _ := reloadOperationTestLocal(t)
		if _, err := local.Reload(false); err != nil {
			t.Fatal(err)
		}
		plan, err := local.Plan(request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := local.Reload(false); err != nil {
			t.Fatal(err)
		}
		if _, err := local.ExecutePlan(context.Background(), request, plan.ConfirmationToken); err != nil {
			t.Fatalf("execute after debounced reload: %v", err)
		}
	})

	t.Run("failed", func(t *testing.T) {
		local, request, path := reloadOperationTestLocal(t)
		plan, err := local.Plan(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("project: ["), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := local.Reload(true); err == nil {
			t.Fatal("invalid configuration reload succeeded")
		}
		if _, err := local.ExecutePlan(context.Background(), request, plan.ConfirmationToken); err != nil {
			t.Fatalf("execute after failed reload: %v", err)
		}
	})
}

func TestPreviewConfirmationPressurePreservesExecutionTokenAndEvictsFIFO(t *testing.T) {
	local := operationTestLocal(t)
	id := config.ActionID{OwnerKind: config.ActionOwnerService, Owner: "api", Name: "deploy"}
	request := PlanRequest{Operation: "action", Action: id}

	result, err := local.ExecutePlan(context.Background(), request, "")
	var required *ConfirmationRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("initial execution = %#v, %v", result, err)
	}
	executionToken := required.Plan.ConfirmationToken

	previews := make([]string, 0, maxConfirmationTokensPerPurpose+1)
	for range maxConfirmationTokensPerPurpose + 1 {
		plan, err := local.Plan(request)
		if err != nil {
			t.Fatal(err)
		}
		previews = append(previews, plan.ConfirmationToken)
	}
	if got, want := len(local.confirmations), maxConfirmationTokensPerPurpose+1; got != want {
		t.Fatalf("pending confirmations = %d, want bounded execution plus preview pools = %d", got, want)
	}

	_, err = local.ExecutePlan(context.Background(), request, previews[0])
	var confirmation *ConfirmationError
	if !errors.As(err, &confirmation) || confirmation.Code != "confirmation_expired" {
		t.Fatalf("oldest preview err = %#v, want deterministic FIFO eviction", err)
	}
	if _, err := local.ExecutePlan(context.Background(), request, executionToken); err != nil {
		t.Fatalf("execution token was evicted by previews: %v", err)
	}
	if _, err := local.ExecutePlan(context.Background(), request, previews[len(previews)-1]); err != nil {
		t.Fatalf("newest preview token was evicted before the oldest: %v", err)
	}
}

func TestWaitConditionsAndCancellation(t *testing.T) {
	local := operationTestLocal(t)
	local.SetServiceStateForTest("api", config.ServiceState{Status: config.StatusRunning})
	result, err := local.Wait(context.Background(), WaitRequest{Selectors: []string{"api"}, Condition: "ready"})
	if err != nil || len(result.Services) != 1 {
		t.Fatalf("ready = %#v, %v", result, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = local.Wait(ctx, WaitRequest{Selectors: []string{"api"}, Condition: "stopped"})
	var waitErr *WaitError
	if !errors.As(err, &waitErr) || waitErr.Code != "wait_timeout" {
		t.Fatalf("wait err = %#v", err)
	}
}

func TestWaitDistinguishesBlockedDependency(t *testing.T) {
	local := operationTestLocal(t)
	local.SetServiceStateForTest("db", config.ServiceState{Status: config.StatusStopped, Completed: true, ExitCode: 1})
	local.SetServiceDesiredRunningForTest("api", true)
	_, err := local.Wait(context.Background(), WaitRequest{Selectors: []string{"api"}, Condition: "ready"})
	var waitErr *WaitError
	if !errors.As(err, &waitErr) || waitErr.Code != "dependency_blocked" {
		t.Fatalf("wait err = %#v", err)
	}
}

func TestPrimaryServiceAction(t *testing.T) {
	if got := PrimaryServiceAction(&ServiceSnapshot{CanStart: true, State: config.ServiceState{Status: config.StatusStopped}}); got != "start" {
		t.Fatalf("stopped = %q", got)
	}
	if got := PrimaryServiceAction(&ServiceSnapshot{CanStop: true, DesiredRunning: true, State: config.ServiceState{Status: config.StatusStarting}}); got != "stop" {
		t.Fatalf("starting = %q", got)
	}
	if got := PrimaryServiceAction(&ServiceSnapshot{State: config.ServiceState{Status: config.StatusStopped}}); got != "" {
		t.Fatalf("disabled = %q", got)
	}
}

func TestEmptyPlanHasNoWavesAndWaitReturnsACursor(t *testing.T) {
	local := operationTestLocal(t)
	// A configuration with no services resolves to no targets, and a reader
	// counting waves must not see work that does not exist.
	bare := NewLocal(&config.Config{Project: "Empty"}, nil, Options{SessionID: "session-empty"})
	t.Cleanup(func() { _ = bare.Shutdown() })
	plan, err := bare.Plan(PlanRequest{Operation: "start", IncludeDependencies: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Targets) != 0 || len(plan.Waves) != 0 {
		t.Fatalf("empty plan = %#v", plan)
	}

	local.SetServiceStatusForTest("db", config.StatusRunning)
	local.SetServiceStatusForTest("api", config.StatusRunning)
	result, err := local.Wait(context.Background(), WaitRequest{Selectors: []string{"api"}, Condition: "running"})
	if err != nil {
		t.Fatal(err)
	}
	// The cursor is what makes "what happened while I waited" answerable.
	if result.Cursor == 0 {
		t.Fatalf("wait cursor = %#v", result)
	}
	changes, err := local.Changes(ChangeQuery{Since: result.Cursor})
	if err != nil || len(changes.Changes) != 0 {
		t.Fatalf("changes after wait cursor = %#v, %v", changes.Changes, err)
	}
}

// hostileParamLocal builds an action whose single parameter lands in argv and
// in the environment, next to a probe script that records exactly what it was
// given. Every marker a payload could create is checked afterwards, so the test
// proves inertness instead of asserting a rendering.
func hostileParamLocal(t *testing.T) (*Local, config.ActionID, string) {
	t.Helper()
	directory := t.TempDir()
	probe := filepath.Join(directory, "probe.sh")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > argv.txt\n" +
		"printf '%s' \"$PAYLOAD\" > env.txt\n"
	if err := os.WriteFile(probe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	data := "project: Hostile\n" +
		"action_groups:\n" +
		"  g:\n" +
		"    dir: " + directory + "\n" +
		"    actions:\n" +
		"      probe:\n" +
		"        params:\n" +
		"          payload:\n" +
		"            type: text\n" +
		"            required: true\n" +
		"            env: PAYLOAD\n" +
		"        argv:\n" +
		"          - " + probe + "\n" +
		"          - \"{{payload}}\"\n" +
		"      control:\n" +
		"        command: touch injected-control\n"
	path := filepath.Join(directory, "kranz.yaml")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	local := NewLocal(cfg, []string{path}, Options{SessionID: "hostile-test"})
	t.Cleanup(func() { _ = local.Shutdown() })
	return local, config.ActionID{OwnerKind: config.ActionOwnerGroup, Owner: "g", Name: "probe"}, directory
}

func TestParameterValueNeverReachesAShell(t *testing.T) {
	local, id, directory := hostileParamLocal(t)
	// Each payload names the marker it would create if it were ever parsed by
	// a shell. The markers stay inside the test's own temporary directory.
	payloads := map[string]string{
		"; touch injected-semicolon":     "injected-semicolon",
		"$(touch injected-substitution)": "injected-substitution",
		"`touch injected-backtick`":      "injected-backtick",
		"&& touch injected-and":          "injected-and",
		"| touch injected-pipe":          "injected-pipe",
		"$(echo hi) > injected-redirect": "injected-redirect",
	}
	for payload, marker := range payloads {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		request := PlanRequest{Operation: "action", Action: id, Params: map[string]json.RawMessage{"payload": encoded}}
		if _, err := local.ExecutePlan(context.Background(), request, ""); err != nil {
			t.Fatalf("payload %q: execute: %v", payload, err)
		}
		argv, err := os.ReadFile(filepath.Join(directory, "argv.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimRight(string(argv), "\n"); got != payload {
			t.Fatalf("payload %q arrived as %q: it was split or rewritten on the way", payload, got)
		}
		env, err := os.ReadFile(filepath.Join(directory, "env.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(env) != payload {
			t.Fatalf("payload %q reached the environment as %q", payload, env)
		}
		if _, err := os.Stat(filepath.Join(directory, marker)); !os.IsNotExist(err) {
			t.Fatalf("payload %q executed: %s exists", payload, marker)
		}
	}
	// A legacy shell action creating its own marker proves the check above can
	// see such a file at all, so the passing payloads mean inertness rather
	// than a probe that never looks.
	control := config.ActionID{OwnerKind: config.ActionOwnerGroup, Owner: "g", Name: "control"}
	if _, err := local.ExecutePlan(context.Background(), PlanRequest{Operation: "action", Action: control}, ""); err != nil {
		t.Fatalf("control action: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "injected-control")); err != nil {
		t.Fatalf("the marker check cannot observe a created file: %v", err)
	}
}

func TestParameterValueIsNotGlobbedOrWordSplit(t *testing.T) {
	local, id, directory := hostileParamLocal(t)
	if err := os.WriteFile(filepath.Join(directory, "one.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{"*.txt", "two  spaces", "~", "$HOME", "%USERPROFILE%"} {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		request := PlanRequest{Operation: "action", Action: id, Params: map[string]json.RawMessage{"payload": encoded}}
		if _, err := local.ExecutePlan(context.Background(), request, ""); err != nil {
			t.Fatalf("payload %q: execute: %v", payload, err)
		}
		argv, err := os.ReadFile(filepath.Join(directory, "argv.txt"))
		if err != nil {
			t.Fatal(err)
		}
		// One line means one argument: expansion or splitting would add more.
		if got := strings.TrimRight(string(argv), "\n"); got != payload {
			t.Fatalf("payload %q arrived as %q", payload, got)
		}
	}
}

func TestParameterizedActionRefusesAClientThatSendsNoParameters(t *testing.T) {
	local, id := parameterizedTestLocal(t)
	_, err := local.Plan(PlanRequest{Operation: "action", Action: id})
	var invalid *InvalidArgumentsError
	if !errors.As(err, &invalid) {
		t.Fatalf("a caller that sent no parameter object ran on defaults: %v", err)
	}
	if _, err := local.Plan(PlanRequest{Operation: "action", Action: id, Params: map[string]json.RawMessage{"target": json.RawMessage(`"phone"`)}}); err != nil {
		t.Fatalf("an explicit object was rejected: %v", err)
	}
}
