package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kranzcli "github.com/kranz-org/kranz/internal/cli"
	"github.com/kranz-org/kranz/internal/config"
)

const actionProject = `project: Actions
services:
  api:
    command: sleep 60
    actions:
      seed:
        description: Load fixtures.
        command: echo seeded
        confirm: true
      migrate:
        command: echo migrated
        interactive: true
action_groups:
  toolbox:
    actions:
      seed:
        command: echo group seeded
`

func actionDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "kranz.yaml"), []byte(actionProject), 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestActionListAndFilterByOwner(t *testing.T) {
	directory := actionDirectory(t)

	all := runInspection(t, directory, "actions")
	for _, id := range []string{"api/seed", "api/migrate", "toolbox/seed"} {
		if !strings.Contains(all, id) {
			t.Errorf("action list omits %q: %q", id, all)
		}
	}
	if !strings.Contains(all, "CONFIRM") || !strings.Contains(all, "true") {
		t.Errorf("action list omits confirmation requirements: %q", all)
	}

	owned := runInspection(t, directory, "actions", "toolbox")
	if !strings.Contains(owned, "toolbox/seed") || strings.Contains(owned, "api/seed") {
		t.Errorf("action list toolbox = %q", owned)
	}
}

func TestActionListFormat(t *testing.T) {
	output := runInspection(t, actionDirectory(t), "actions", "api", "--format", "{{.Action}}:{{.Confirm}}")
	if !strings.Contains(output, "api/seed:true") || strings.Contains(output, "toolbox/seed") {
		t.Fatalf("formatted action list = %q", output)
	}
}

func TestActionListRejectsAnUnknownOwner(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := execute([]string{"-C", actionDirectory(t), "actions", "nope"}, &stdout, &stderr); code != kranzcli.ExitNotFound {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
}

// A service action and an action-group action can share a name, so the owner
// is part of the identity rather than a prefix to be guessed at.
func TestActionInfoDistinguishesOwnersOfTheSameName(t *testing.T) {
	directory := actionDirectory(t)

	service := runInspection(t, directory, "actions", "info", "api/seed")
	if !strings.Contains(service, "(service)") || !strings.Contains(service, "echo seeded") {
		t.Errorf("api/seed = %q", service)
	}
	group := runInspection(t, directory, "actions", "info", "toolbox/seed")
	if !strings.Contains(group, "(group)") || !strings.Contains(group, "echo group seeded") {
		t.Errorf("toolbox/seed = %q", group)
	}
}

// A bare action name cannot identify an action, and the error is the only place
// the user learns the OWNER/ACTION shape.
func TestActionInfoOnABareNameTeachesTheShape(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := execute([]string{"-C", actionDirectory(t), "actions", "info", "seed"}, &stdout, &stderr); code != kranzcli.ExitNotFound {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "OWNER/ACTION") {
		t.Errorf("error does not teach the shape: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "kranz actions info api/seed") {
		t.Errorf("error does not use a real project action as its example: %q", stderr.String())
	}
}

// An interactive action needs a terminal handed to it under a supervisor lease.
// Running it without one would block on a prompt nobody can answer, so it is
// refused before any runtime is contacted.
func TestActionRunRefusesAnInteractiveAction(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := execute([]string{"-C", actionDirectory(t), "actions", "run", "api/migrate"}, &stdout, &stderr); code != kranzcli.ExitUsage {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "interactive") || !strings.Contains(stderr.String(), "kranz attach") {
		t.Errorf("refusal = %q", stderr.String())
	}
}

func TestActionRunNeedsARuntime(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := execute([]string{"-C", actionDirectory(t), "actions", "run", "api/seed"}, &stdout, &stderr)
	if code != kranzcli.ExitNotFound {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
}

// `kranz actions run --output=json` promises an array of output lines. A pipe
// hands Kranz whatever chunk it read, so without splitting, a consumer counting
// array elements and a human counting printed lines disagree.
func TestActionOutputLinesAreOneLineEach(t *testing.T) {
	lines := actionOutputLines([]string{"one\ntwo\n", "three\r\n", "four"})
	want := []string{"one", "two", "three", "four"}
	if len(lines) != len(want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
	for index := range lines {
		if lines[index] != want[index] {
			t.Fatalf("lines = %q, want %q", lines, want)
		}
	}
	if empty := actionOutputLines(nil); empty == nil {
		t.Error("nil output must still encode as an empty JSON array")
	}
}

const parameterizedProject = `project: Parameterized
action_groups:
  infra:
    actions:
      seed:
        description: Seed one environment.
        params:
          env:
            type: select
            options: [dev, prod]
            default: dev
            arg: "--env"
            prompt: Target environment
          targets:
            type: checkbox
            options: [phone, watch]
            optional: true
            arg: "--target"
          note:
            type: text
            optional: true
            arg: "--note"
          write:
            type: checkbox
            default: false
            flag: --write
            confirm: This writes to the target environment
        argv: [./seed.sh, "{{env}}", "{{targets}}"]
      plain:
        command: echo plain
`

func parameterizedDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "kranz.yaml"), []byte(parameterizedProject), 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func parameterizedAction(t *testing.T, directory, name string) (config.ActionID, config.Action) {
	t.Helper()
	cfg, err := config.Load(filepath.Join(directory, "kranz.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	id := config.ActionID{OwnerKind: config.ActionOwnerGroup, Owner: "infra", Name: name}
	action, ok := cfg.ResolveAction(id)
	if !ok {
		t.Fatalf("action %q not found", name)
	}
	return id, action
}

func TestActionListAndInfoAnnounceParameters(t *testing.T) {
	directory := parameterizedDirectory(t)

	list := runInspection(t, directory, "actions")
	if !strings.Contains(list, "PARAMETERS") {
		t.Fatalf("action list hides whether an action takes parameters: %q", list)
	}

	info := runInspection(t, directory, "actions", "info", "infra/seed")
	for _, fragment := range []string{"env", "[dev|prod]", "default dev", "Target environment", "targets", "note", "Values that ask", "This writes to the target environment"} {
		if !strings.Contains(info, fragment) {
			t.Errorf("action info omits %q: %q", fragment, info)
		}
	}

	plain := runInspection(t, directory, "actions", "info", "infra/plain")
	if strings.Contains(plain, "Parameters:") || strings.Contains(plain, "Values that ask") {
		t.Errorf("a plain action reports parameter sections: %q", plain)
	}
}

func TestEncodeActionParamsRejectsMalformedFlags(t *testing.T) {
	directory := parameterizedDirectory(t)
	id, action := parameterizedAction(t, directory, "seed")

	cases := map[string][]string{
		"unknown name":         {"nope=1"},
		"missing equals":       {"env"},
		"empty name":           {"=dev"},
		"value outside enum":   {"env=staging"},
		"repeated single name": {"env=dev", "env=prod"},
	}
	for name, flags := range cases {
		if _, err := encodeActionParams(id, action, flags); err == nil {
			t.Errorf("%s accepted: %v", name, flags)
		}
	}
}

func TestEncodeActionParamsKeepsValuesWhole(t *testing.T) {
	directory := parameterizedDirectory(t)
	id, action := parameterizedAction(t, directory, "seed")

	// Only the first '=' separates the name, so a value may contain its own.
	encoded, err := encodeActionParams(id, action, []string{"note=key=value; rm -rf /"})
	if err != nil {
		t.Fatal(err)
	}
	var note string
	if err := json.Unmarshal(encoded["note"], &note); err != nil {
		t.Fatal(err)
	}
	if note != "key=value; rm -rf /" {
		t.Fatalf("note = %q", note)
	}

	// A checkbox group is the one control a repeated name adds to.
	group, err := encodeActionParams(id, action, []string{"targets=phone", "targets=watch"})
	if err != nil {
		t.Fatal(err)
	}
	var targets []string
	if err := json.Unmarshal(group["targets"], &targets); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0] != "phone" || targets[1] != "watch" {
		t.Fatalf("targets = %#v", targets)
	}
}

func TestEncodeActionParamsSeparatesNoValuesFromNoParameters(t *testing.T) {
	directory := parameterizedDirectory(t)

	id, action := parameterizedAction(t, directory, "seed")
	empty, err := encodeActionParams(id, action, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A parameterized action always sends an object, so the runtime can tell a
	// current client running on defaults from one that predates parameters.
	if empty == nil {
		t.Fatal("a parameterized action sent no parameter object at all")
	}
	if len(empty) != 0 {
		t.Fatalf("defaults were filled in by the client: %#v", empty)
	}

	plainID, plain := parameterizedAction(t, directory, "plain")
	none, err := encodeActionParams(plainID, plain, nil)
	if err != nil {
		t.Fatal(err)
	}
	if none != nil {
		t.Fatalf("an action without parameters sent an object: %#v", none)
	}
	if _, err := encodeActionParams(plainID, plain, []string{"env=dev"}); err == nil {
		t.Fatal("--param on an action without parameters was accepted")
	}
}

func TestNoParamLeavesAParameterOutEntirely(t *testing.T) {
	directory := parameterizedDirectory(t)
	id, action := parameterizedAction(t, directory, "seed")

	// Omitting the flag falls back to the declared default.
	withDefault, err := encodeActionParams(id, action, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := withDefault["env"]; present {
		t.Fatalf("the client filled in a default: %#v", withDefault)
	}

	// Naming it with --no-param says "leave it out", which travels as null.
	omitted, err := encodeActionParams(id, action, []string{"env" + omittedParamSuffix})
	if err != nil {
		t.Fatal(err)
	}
	if string(omitted["env"]) != "null" {
		t.Fatalf("omitted parameter = %s", omitted["env"])
	}
	if _, err := encodeActionParams(id, action, []string{"nope" + omittedParamSuffix}); err == nil {
		t.Fatal("--no-param accepted an unknown parameter")
	}
}
