package config

import (
	"testing"
	"time"
)

func TestLoadRecordsParameterDeclarationOrder(t *testing.T) {
	data := []byte(`project: sample
action_groups:
  ios:
    actions:
      install:
        params:
          target:
            type: checkbox
            options: [phone, watch]
            required: true
          clean:
            type: checkbox
            default: false
            flag: --clean
        run: ./install.sh
`)
	cfg, err := loadNative(data)
	if err != nil {
		t.Fatal(err)
	}
	action := cfg.ActionGroups["ios"].Actions["install"]
	if got, want := action.ParamOrder, []string{"target", "clean"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("param order = %#v, want %#v", got, want)
	}
	if _, err := CompileAction("group/ios/install", action); err != nil {
		t.Fatalf("compile: %v", err)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestMergeActionKeepsCommandGroupAtomic(t *testing.T) {
	base := Action{
		Run:        ArgvList{"./x.sh"},
		Params:     map[string]ActionParam{"a": {Type: "text", Required: true}},
		ParamOrder: []string{"a"},
	}
	timeoutOnly := mergeAction(base, Action{Timeout: 5 * time.Second})
	if len(timeoutOnly.Run) == 0 || len(timeoutOnly.Params) == 0 || len(timeoutOnly.ParamOrder) == 0 {
		t.Fatalf("timeout-only override lost the command group: %#v", timeoutOnly)
	}
	replaced := mergeAction(base, Action{Run: ArgvList{"./y.sh"}})
	if len(replaced.Params) != 0 || len(replaced.Run) != 1 || replaced.Run[0] != "./y.sh" {
		t.Fatalf("command-group override did not replace params: %#v", replaced)
	}
}

func TestValidateRejectsParamsWithLegacyCommand(t *testing.T) {
	cfg := &Config{
		Project: "sample",
		ActionGroups: map[string]ActionGroup{
			"g": {Actions: map[string]Action{"a": {
				Command: "./x.sh",
				Params:  map[string]ActionParam{"p": {Type: "text", Required: true}},
				Run:     ArgvList{"./x.sh"},
			}}},
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected params with command to be rejected")
	}
}

func TestValidateRejectsUnknownParamType(t *testing.T) {
	cfg := &Config{
		Project: "sample",
		ActionGroups: map[string]ActionGroup{
			"g": {Actions: map[string]Action{"a": {
				Params: map[string]ActionParam{"p": {Type: "colour", Required: true}},
				Run:    ArgvList{"./x.sh"},
			}}},
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected unknown type to be rejected")
	}
}

func TestValidateWarnsAboutUnusedParameter(t *testing.T) {
	cfg := &Config{
		Project: "sample",
		ActionGroups: map[string]ActionGroup{
			"g": {Actions: map[string]Action{"a": {
				Params: map[string]ActionParam{
					"used":   {Type: "text", Required: true},
					"unused": {Type: "text", Optional: true},
				},
				Argv: ArgvList{"./x.sh", "--used={{used}}"},
			}}},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(cfg.Diagnostics) == 0 || cfg.Diagnostics[0] == "" {
		t.Fatal("expected an unused-parameter diagnostic")
	}
}

func TestValidatePrerequisiteParams(t *testing.T) {
	valid := &Config{
		Project: "sample",
		ActionGroups: map[string]ActionGroup{
			"infra": {Actions: map[string]Action{"seed": {
				Params: map[string]ActionParam{"env": {Type: "select", Options: ActionParamOptions{{Value: "dev"}, {Value: "prod"}}, Required: true}},
				Argv:   ArgvList{"./seed.sh", "--env={{env}}"},
			}}},
		},
		Services: map[string]Service{
			"api": {Command: "true", BeforeStart: []Prerequisite{{Group: "infra", Action: "seed", Params: map[string]any{"env": "prod"}}}},
		},
	}
	if err := Validate(valid); err != nil {
		t.Fatalf("valid prerequisite params rejected: %v", err)
	}
	invalid := &Config{
		Project: "sample",
		ActionGroups: map[string]ActionGroup{
			"infra": {Actions: map[string]Action{"seed": {
				Params: map[string]ActionParam{"env": {Type: "select", Options: ActionParamOptions{{Value: "dev"}, {Value: "prod"}}, Required: true}},
				Argv:   ArgvList{"./seed.sh", "--env={{env}}"},
			}}},
		},
		Services: map[string]Service{
			"api": {Command: "true", BeforeStart: []Prerequisite{{Group: "infra", Action: "seed", Params: map[string]any{"env": "staging"}}}},
		},
	}
	if err := Validate(invalid); err == nil {
		t.Fatal("expected invalid prerequisite value to be rejected")
	}
}

func TestValidateRejectsParamsOnLifecycleActions(t *testing.T) {
	for role, lifecycle := range map[string]LifecycleConfig{
		"start": {Start: &Action{Command: "./start.sh", Params: map[string]ActionParam{"p": {Type: "text", Required: true}}}},
		"stop":  {Start: &Action{Command: "./start.sh"}, Stop: &Action{Command: "./stop.sh", Argv: ArgvList{"./stop.sh"}}},
		"logs":  {Start: &Action{Command: "./start.sh"}, Logs: &Action{Command: "./logs.sh", Run: ArgvList{"./logs.sh"}}},
	} {
		cfg := &Config{
			Project:  "sample",
			Services: map[string]Service{"api": {Supervision: SupervisionDetached, Lifecycle: lifecycle}},
		}
		if err := Validate(cfg); err == nil {
			t.Fatalf("lifecycle.%s accepted a parameterized declaration", role)
		}
	}
}

func TestLoadRejectsShellOnParameterizedActionButNotOnItsOwner(t *testing.T) {
	directory := t.TempDir()
	path := writeConfigFile(t, directory, "kranz.yaml", `project: sample
action_groups:
  g:
    shell: /bin/bash
    actions:
      a:
        params:
          x: {type: text, required: true, positional: true}
        run: [./a.sh]
`)
	if _, err := Load(path); err != nil {
		t.Fatalf("an owner shell must stay irrelevant rather than fatal: %v", err)
	}

	explicit := writeConfigFile(t, t.TempDir(), "kranz.yaml", `project: sample
action_groups:
  g:
    actions:
      a:
        shell: /bin/bash
        params:
          x: {type: text, required: true, positional: true}
        run: [./a.sh]
`)
	if _, err := Load(explicit); err == nil {
		t.Fatal("a shell declared on an action that never reaches one was accepted")
	}
}

func TestOverrideOfOneFieldKeepsTheParameterSchema(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, "kranz.yaml", `project: root
overrides: [local.yaml]
action_groups:
  g:
    actions:
      seed:
        params:
          env: {type: select, options: [dev, prod], default: dev, arg: "--env"}
        run: [./seed.sh]
`)
	writeConfigFile(t, root, "local.yaml", `action_groups:
  g:
    actions:
      seed:
        timeout: 30s
`)
	cfg, err := Compose(LoadOptions{Directory: root})
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	action := cfg.ActionGroups["g"].Actions["seed"]
	if action.Timeout != 30*time.Second {
		t.Fatalf("timeout = %s", action.Timeout)
	}
	if len(action.Params) != 1 || len(action.Run) != 1 {
		t.Fatalf("an unrelated override dropped the command group: %#v", action)
	}
	if got := action.ParamOrder; len(got) != 1 || got[0] != "env" {
		t.Fatalf("an unrelated override lost the declaration order: %#v", got)
	}
	if got := cfg.ActionGroups["g"].ActionOrder; len(got) != 1 || got[0] != "seed" {
		t.Fatalf("an unrelated override lost the action order: %#v", got)
	}
}

func TestLegacyCommandWithLiteralBracesIsUntouched(t *testing.T) {
	root := t.TempDir()
	const command = `echo "{{not_a_param}} {{ }}"`
	writeConfigFile(t, root, "kranz.yaml", "project: root\n"+
		"overrides: [local.yaml]\n"+
		"action_groups:\n"+
		"  g:\n"+
		"    actions:\n"+
		"      legacy:\n"+
		"        command: '"+command+"'\n")
	writeConfigFile(t, root, "local.yaml", `action_groups:
  g:
    actions:
      legacy:
        timeout: 10s
`)
	cfg, err := Compose(LoadOptions{Directory: root})
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	action := cfg.ActionGroups["g"].Actions["legacy"]
	if action.Command != command {
		t.Fatalf("command = %q, want %q", action.Command, command)
	}
	compiled, err := CompileAction("group/g/legacy", action)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if compiled != nil {
		t.Fatal("a legacy shell action must not compile into a parameter schema")
	}
}

func TestCompositionCacheRoundTripsParameterOrder(t *testing.T) {
	data := []byte(`project: sample
action_groups:
  g:
    actions:
      seed:
        params:
          first: {type: text, required: true, positional: true}
          second: {type: text, required: true, positional: true}
        run: [./seed.sh]
`)
	cfg, err := loadNative(data)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := cloneAutonomousConfig(cfg)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	order := clone.ActionGroups["g"].Actions["seed"].ParamOrder
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("param order after round trip = %#v", order)
	}
	clone.ActionGroups["g"].Actions["seed"].ParamOrder[0] = "mutated"
	if cfg.ActionGroups["g"].Actions["seed"].ParamOrder[0] != "first" {
		t.Fatal("the clone shares its parameter order with the source")
	}
}

func TestDoubleDollarEscapesAShellVariable(t *testing.T) {
	directory := t.TempDir()
	path := writeConfigFile(t, directory, "kranz.yaml", `project: sample
action_groups:
  g:
    actions:
      loop:
        command: 'for f in a b; do echo "$$f"; done'
      expanded:
        command: 'echo "$KRANZ_TEST_EXPANDED"'
`)
	t.Setenv("KRANZ_TEST_EXPANDED", "from-environment")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// Expansion runs over the whole file before it is parsed, so without an
	// escape a shell loop variable is emptied and the command still runs.
	if got := cfg.ActionGroups["g"].Actions["loop"].Command; got != `for f in a b; do echo "$f"; done` {
		t.Fatalf("escaped shell variable = %q", got)
	}
	if got := cfg.ActionGroups["g"].Actions["expanded"].Command; got != `echo "from-environment"` {
		t.Fatalf("environment expansion stopped working: %q", got)
	}
}
