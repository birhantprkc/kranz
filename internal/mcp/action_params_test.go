package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kranz-org/kranz/internal/app"
	"github.com/kranz-org/kranz/internal/config"
)

// parameterizedServer exposes one action whose values reach echo as separate
// arguments, so a result shows exactly what the runtime built.
func parameterizedServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{Project: "demo", ActionGroups: map[string]config.ActionGroup{
		"infra": {Actions: map[string]config.Action{
			"seed": {
				Params: map[string]config.ActionParam{
					"env":   {Type: "select", Options: config.ActionParamOptions{{Value: "dev"}, {Value: "prod", Confirm: "production is live"}}, Default: "dev", Arg: "--env"},
					"count": {Type: "number", Default: 1, Min: int64Pointer(1), Max: int64Pointer(10), Arg: "--count"},
				},
				ParamOrder: []string{"env", "count"},
				Argv:       config.ArgvList{"/bin/echo", "{{env}}", "{{count}}"},
			},
			"plain": {Command: "exit 0", Shell: "/bin/sh"},
		}, ActionOrder: []string{"seed", "plain"}},
	}, ActionGroupOrder: []string{"infra"}}
	local := app.NewLocal(cfg, nil, app.Options{SessionID: "session-params"})
	t.Cleanup(func() { _ = local.Shutdown() })
	return NewServerForRuntime(local, SessionIdentity{ID: "session-params", Name: "demo", Project: "demo", ProtocolVersion: 2}, bytes.NewReader(nil), &bytes.Buffer{}, &bytes.Buffer{})
}

func int64Pointer(value int64) *int64 { return &value }

func TestActionInfoAdvertisesParametersAndSchema(t *testing.T) {
	server := parameterizedServer(t)
	result := testTool(t, server, "action_info", `{"action":"infra/seed"}`)
	if result.Error != nil {
		t.Fatalf("action_info: %#v", result.Error)
	}
	payload, err := json.Marshal(result.Data)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, fragment := range []string{`"parameters":true`, `"params_schema"`, `"name":"env"`, `"name":"count"`, `"confirm_options"`, "production is live"} {
		if !strings.Contains(text, fragment) {
			t.Errorf("action_info omits %s: %s", fragment, text)
		}
	}
	// Declaration order is the order a caller should present the form in.
	if strings.Index(text, `"name":"env"`) > strings.Index(text, `"name":"count"`) {
		t.Errorf("action_info reordered the parameters: %s", text)
	}
}

func TestActionRunAppliesTypedParameters(t *testing.T) {
	server := parameterizedServer(t)
	result := testTool(t, server, "action_run", `{"action":"infra/seed","params":{"env":"dev","count":7}}`)
	if result.Error != nil {
		t.Fatalf("action_run: %#v", result.Error)
	}
	payload, _ := json.Marshal(result.Data)
	text := string(payload)
	if !strings.Contains(text, `"command_preview":"/bin/echo --env dev --count 7"`) {
		t.Fatalf("preview did not come from the executed vector: %s", text)
	}
	if !strings.Contains(text, `"params":{"count":7,"env":"dev"}`) {
		t.Fatalf("result did not echo the normalized values: %s", text)
	}
}

func TestActionRunReportsInvalidValuesPerField(t *testing.T) {
	server := parameterizedServer(t)
	result := testTool(t, server, "action_run", `{"action":"infra/seed","params":{"env":"staging","count":99}}`)
	if result.Error == nil || result.Error.Code != "invalid_arguments" {
		t.Fatalf("result = %#v", result)
	}
	fields, ok := result.Error.Details["fields"].(map[string]string)
	if !ok {
		t.Fatalf("details = %#v", result.Error.Details)
	}
	if fields["env"] == "" || fields["count"] == "" {
		t.Fatalf("both invalid values must be reported together: %#v", fields)
	}
}

func TestActionRunRefusesParametersOnAPlainActionAndAPlainCallOnAParameterizedOne(t *testing.T) {
	server := parameterizedServer(t)
	unwanted := testTool(t, server, "action_run", `{"action":"infra/plain","params":{"env":"dev"}}`)
	if unwanted.Error == nil || unwanted.Error.Code != "invalid_arguments" {
		t.Fatalf("params on a plain action = %#v", unwanted)
	}
	// A caller that predates parameters sends no object at all. Running it on
	// defaults would execute values it never saw, so it is refused instead.
	silent := testTool(t, server, "action_run", `{"action":"infra/seed"}`)
	if silent.Error == nil || silent.Error.Code != "invalid_arguments" {
		t.Fatalf("a parameterless call ran anyway: %#v", silent)
	}
}

func TestParameterValueDrivesConfirmationRoundTrip(t *testing.T) {
	server := parameterizedServer(t)
	safe := testTool(t, server, "action_run", `{"action":"infra/seed","params":{"env":"dev"}}`)
	if safe.Error != nil {
		t.Fatalf("a safe value asked for confirmation: %#v", safe.Error)
	}

	asked := testTool(t, server, "action_run", `{"action":"infra/seed","params":{"env":"prod"}}`)
	if asked.Error == nil || asked.Error.Code != "confirmation_required" {
		t.Fatalf("a confirming value ran unconfirmed: %#v", asked)
	}
	// A token is spent by the attempt that presents it, so each half of this
	// check starts from its own fresh one.
	crossUse := testTool(t, server, "action_run", `{"action":"infra/seed","params":{"env":"dev"},"confirmation_token":"`+confirmationToken(t, asked)+`"}`)
	if crossUse.Error == nil {
		t.Fatal("a token issued for one invocation confirmed a different one")
	}

	again := testTool(t, server, "action_run", `{"action":"infra/seed","params":{"env":"prod"}}`)
	if again.Error == nil || again.Error.Code != "confirmation_required" {
		t.Fatalf("second attempt = %#v", again)
	}
	confirmed := testTool(t, server, "action_run", `{"action":"infra/seed","params":{"env":"prod"},"confirmation_token":"`+confirmationToken(t, again)+`"}`)
	if confirmed.Error != nil {
		t.Fatalf("the confirmed invocation was refused: %#v", confirmed.Error)
	}
	payload, _ := json.Marshal(confirmed.Data)
	if !strings.Contains(string(payload), `/bin/echo --env prod --count 1`) {
		t.Fatalf("the confirmed run did not execute what was confirmed: %s", payload)
	}
}

// confirmationToken reads the token a confirmation_required result carries.
func confirmationToken(t *testing.T, result ResultEnvelope) string {
	t.Helper()
	token, ok := result.Error.Details["confirmation_token"].(string)
	if !ok || token == "" {
		t.Fatalf("no confirmation token: %#v", result.Error.Details)
	}
	return token
}

func TestPlanPreviewsAParameterizedInvocationWithoutRunningIt(t *testing.T) {
	server := parameterizedServer(t)
	result := testTool(t, server, "plan", `{"operation":"action","action":"infra/seed","params":{"env":"prod","count":3}}`)
	if result.Error != nil {
		t.Fatalf("plan: %#v", result.Error)
	}
	payload, _ := json.Marshal(result.Data)
	if !strings.Contains(string(payload), `"command_preview":"/bin/echo --env prod --count 3"`) {
		t.Fatalf("plan did not resolve the invocation: %s", payload)
	}
	if !strings.Contains(string(payload), `"requires_confirmation":true`) {
		t.Fatalf("plan hid that the chosen value asks: %s", payload)
	}
	// Nothing ran, so the action has no result yet.
	state := testTool(t, server, "action_result", `{"action":"infra/seed"}`)
	if state.Error == nil && strings.Contains(mustJSON(t, state.Data), `"run":1`) {
		t.Fatalf("plan executed the action: %s", mustJSON(t, state.Data))
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}
