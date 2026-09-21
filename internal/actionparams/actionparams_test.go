package actionparams

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func boolValue(value bool) *Value { return &Value{Kind: KindBool, Bool: value} }

func checkbox(flag string) Param {
	return Param{Control: Checkbox, Default: boolValue(false), Projection: Projection{Kind: ProjectionFlag, Name: flag}}
}

func rawText(value string) Raw { return Raw{Present: true, Text: value} }

func render(t *testing.T, compiled *Compiled, raw RawValues) Rendered {
	t.Helper()
	invocation, err := Normalize(compiled, raw)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	rendered, err := Render(compiled, invocation)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return rendered
}

func TestRunModeProjectsFlagsAndOptions(t *testing.T) {
	compiled, err := Compile(Source{
		ID:    "group/ios/install",
		Order: []string{"target", "clean", "launch"},
		Params: map[string]Param{
			"target": {Control: Checkbox, Options: []Option{{Value: "phone", Flag: "--phone"}, {Value: "watch", Flag: "--watch"}}, Required: true},
			"clean":  {Control: Checkbox, Default: boolValue(false), Projection: Projection{Kind: ProjectionFlag, Name: "--clean"}, Confirm: "simulator data will be erased"},
			"launch": checkbox("--launch"),
		},
		Run: []string{"./build-and-install.sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered := render(t, compiled, RawValues{
		"target": {Present: true, Strings: []string{"watch", "phone"}},
		"clean":  {Present: true, Bool: boolPtr(true)},
	})
	if got, want := rendered.Argv, []string{"./build-and-install.sh", "--phone", "--watch", "--clean"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
	if !rendered.Confirm || len(rendered.ConfirmReasons) != 1 {
		t.Fatalf("confirm = %v, reasons = %#v", rendered.Confirm, rendered.ConfirmReasons)
	}
}

func boolPtr(value bool) *bool { return &value }

func TestArgPositionalAndEnvProjections(t *testing.T) {
	compiled, err := Compile(Source{
		ID:    "group/infra/seed",
		Order: []string{"client", "target", "label", "dry_run"},
		Params: map[string]Param{
			"client":  {Control: Text, Default: &Value{Kind: KindString, Text: "all"}, Projection: Projection{Kind: ProjectionArg, Name: "--client"}},
			"target":  {Control: Text, Required: true, Projection: Projection{Kind: ProjectionArg, Name: "--target=", Joined: true}},
			"label":   {Control: Text, Required: true, Projection: Projection{Kind: ProjectionPositional}},
			"dry_run": {Control: Checkbox, Default: boolValue(false), Projection: Projection{Kind: ProjectionEnv, Name: "DRY_RUN"}},
		},
		Run: []string{"./seed.sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered := render(t, compiled, RawValues{"target": rawText("phone"), "label": rawText("build-1")})
	want := []string{"./seed.sh", "--client", "all", "--target=phone", "build-1"}
	if !reflect.DeepEqual(rendered.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", rendered.Argv, want)
	}
	if rendered.Env["DRY_RUN"] != "0" {
		t.Fatalf("env = %#v", rendered.Env)
	}
}

func TestOptionCommandSelectsBaseCommand(t *testing.T) {
	compiled, err := Compile(Source{
		ID:    "group/checks/check",
		Order: []string{"check", "verbose"},
		Params: map[string]Param{
			"check": {Control: Radio, Required: true, Options: []Option{
				{Value: "lint", Run: []string{"bun", "scripts/lint.mjs"}},
				{Value: "types", Run: []string{"bun", "scripts/types.mjs"}},
			}},
			"verbose": checkbox("--verbose"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if compiled.OptionRunParam != "check" {
		t.Fatalf("option run param = %q", compiled.OptionRunParam)
	}
	rendered := render(t, compiled, RawValues{"check": rawText("types")})
	if want := []string{"bun", "scripts/types.mjs"}; !reflect.DeepEqual(rendered.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", rendered.Argv, want)
	}
}

func TestArgvTemplateInterpolatesValueAndProjection(t *testing.T) {
	compiled, err := Compile(Source{
		ID:    "group/tools/build",
		Order: []string{"verbose", "target"},
		Params: map[string]Param{
			"verbose": checkbox("--verbose"),
			"target":  {Control: Text, Required: true, Projection: Projection{Kind: ProjectionPositional}},
		},
		Argv: []string{"./tool", "subcommand", "{{verbose}}", "--label=build-{{target}}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered := render(t, compiled, RawValues{"verbose": {Present: true, Bool: boolPtr(true)}, "target": rawText("phone")})
	want := []string{"./tool", "subcommand", "--verbose", "--label=build-phone"}
	if !reflect.DeepEqual(rendered.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", rendered.Argv, want)
	}
}

func TestOptionalAbsentGivesNothingButEmptyStringGivesAnElement(t *testing.T) {
	compiled, err := Compile(Source{
		ID:    "group/tools/say",
		Order: []string{"message"},
		Params: map[string]Param{
			"message": {Control: Text, Optional: true, Projection: Projection{Kind: ProjectionPositional}},
		},
		Run: []string{"./say.sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := render(t, compiled, nil).Argv; !reflect.DeepEqual(got, []string{"./say.sh"}) {
		t.Fatalf("absent argv = %#v", got)
	}
	if got := render(t, compiled, RawValues{"message": rawText("")}).Argv; !reflect.DeepEqual(got, []string{"./say.sh", ""}) {
		t.Fatalf("empty argv = %#v", got)
	}
}

func TestInterpolationIsSinglePass(t *testing.T) {
	compiled, err := Compile(Source{
		ID:    "group/tools/echo",
		Order: []string{"name", "count"},
		Params: map[string]Param{
			"name":  {Control: Text, Required: true, Projection: Projection{Kind: ProjectionArg, Name: "--name"}},
			"count": {Control: Number, Default: &Value{Kind: KindInt, Int: 3}, Projection: Projection{Kind: ProjectionArg, Name: "--count=", Joined: true}},
		},
		Run: []string{"./echo.sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered := render(t, compiled, RawValues{"name": rawText("{{count}}")})
	want := []string{"./echo.sh", "--name", "{{count}}", "--count=3"}
	if !reflect.DeepEqual(rendered.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", rendered.Argv, want)
	}
}

func TestInvocationKeyIsOrderIndependent(t *testing.T) {
	compiled, err := Compile(Source{
		ID:    "group/tools/echo",
		Order: []string{"a", "b"},
		Params: map[string]Param{
			"a": {Control: Text, Required: true},
			"b": {Control: Number, Required: true},
		},
		Run: []string{"./echo.sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := Normalize(compiled, RawValues{"a": rawText("x"), "b": {Present: true, Text: "2"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Normalize(compiled, RawValues{"b": {Present: true, Text: "2"}, "a": rawText("x")})
	if err != nil {
		t.Fatal(err)
	}
	if InvocationKey("group/tools/echo", first) != InvocationKey("group/tools/echo", second) {
		t.Fatal("invocation key depends on input order")
	}
	if string(CanonicalValues(first)) != `{"a":"x","b":2}` {
		t.Fatalf("canonical values = %s", CanonicalValues(first))
	}
}

func TestNormalizeReportsMissingAndUnknownValues(t *testing.T) {
	compiled, err := Compile(Source{
		ID:    "group/tools/echo",
		Order: []string{"name"},
		Params: map[string]Param{
			"name": {Control: Text, Required: true},
		},
		Run: []string{"./echo.sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Normalize(compiled, nil); err == nil {
		t.Fatal("expected missing parameter error")
	}
	if _, err := Normalize(compiled, RawValues{"other": rawText("x")}); err == nil {
		t.Fatal("expected unknown parameter error")
	}
}

func TestCompileRejectsInvalidDeclarations(t *testing.T) {
	cases := []Source{
		{ID: "g/a", Order: []string{"x"}, Params: map[string]Param{"x": {Control: Radio, Required: true}}, Run: []string{"./a"}},
		{ID: "g/a", Order: []string{"x"}, Params: map[string]Param{"x": {Control: Text, Required: true, Optional: true}}, Run: []string{"./a"}},
		{ID: "g/a", Order: []string{"x"}, Params: map[string]Param{"x": {Control: Checkbox, Default: boolValue(false), Projection: Projection{Kind: ProjectionFlag, Name: "--x"}}}, Argv: []string{"./a", "{{missing}}"}},
	}
	for index, source := range cases {
		if _, err := Compile(source); err == nil {
			t.Fatalf("case %d: expected compile error", index)
		}
	}
}

func TestNumberBoundariesAreInclusive(t *testing.T) {
	limit := int64(10)
	zero := int64(1)
	compiled, err := Compile(Source{
		ID:     "group/tools/count",
		Order:  []string{"count"},
		Params: map[string]Param{"count": {Control: Number, Required: true, Min: &zero, Max: &limit, Projection: Projection{Kind: ProjectionPositional}}},
		Run:    []string{"./count.sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"1", "10"} {
		if _, err := Normalize(compiled, RawValues{"count": {Present: true, Text: value}}); err != nil {
			t.Fatalf("value %s rejected: %v", value, err)
		}
	}
	for _, value := range []string{"0", "11", "1.5", "", "ten"} {
		if _, err := Normalize(compiled, RawValues{"count": {Present: true, Text: value}}); err == nil {
			t.Fatalf("value %q accepted", value)
		}
	}
}

func TestTextLengthCountsRunesNotBytes(t *testing.T) {
	minimum, maximum := 2, 3
	compiled, err := Compile(Source{
		ID:     "group/tools/label",
		Order:  []string{"label"},
		Params: map[string]Param{"label": {Control: Text, Required: true, MinLength: &minimum, MaxLength: &maximum, Projection: Projection{Kind: ProjectionPositional}}},
		Run:    []string{"./label.sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Three runes of two bytes each: a byte-counting limit would reject six.
	if _, err := Normalize(compiled, RawValues{"label": rawText("ключ"[:6])}); err != nil {
		t.Fatalf("three-rune value rejected: %v", err)
	}
	if _, err := Normalize(compiled, RawValues{"label": rawText("я")}); err == nil {
		t.Fatal("one-rune value accepted below min_length")
	}
}

func TestPatternMatchesTheWholeValue(t *testing.T) {
	compiled, err := Compile(Source{
		ID:     "group/tools/env",
		Order:  []string{"name"},
		Params: map[string]Param{"name": {Control: Text, Required: true, Pattern: "dev", Projection: Projection{Kind: ProjectionPositional}}},
		Run:    []string{"./env.sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Normalize(compiled, RawValues{"name": rawText("dev")}); err != nil {
		t.Fatalf("exact value rejected: %v", err)
	}
	for _, value := range []string{"my-dev-2", "dev ", "predev"} {
		if _, err := Normalize(compiled, RawValues{"name": rawText(value)}); err == nil {
			t.Fatalf("unanchored match accepted %q", value)
		}
	}
}

func TestShellPayloadStaysOneArgvElement(t *testing.T) {
	compiled, err := Compile(Source{
		ID:     "group/tools/say",
		Order:  []string{"message"},
		Params: map[string]Param{"message": {Control: Text, Required: true, Projection: Projection{Kind: ProjectionArg, Name: "--message"}}},
		Run:    []string{"./say.sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{
		"; rm -rf /",
		"$(whoami)",
		"`id`",
		"a && b",
		"one two\tthree",
		`he said "hi"`,
		"%USERPROFILE%",
		"a\nb",
	} {
		rendered := render(t, compiled, RawValues{"message": rawText(payload)})
		want := []string{"./say.sh", "--message", payload}
		if !reflect.DeepEqual(rendered.Argv, want) {
			t.Fatalf("payload %q rendered %#v", payload, rendered.Argv)
		}
	}
}

func TestCompileRejectsPlaceholderInRun(t *testing.T) {
	_, err := Compile(Source{
		ID:     "group/tools/seed",
		Order:  []string{"env"},
		Params: map[string]Param{"env": {Control: Text, Required: true}},
		Run:    []string{"./seed.sh", "--env={{env}}"},
	})
	if err == nil {
		t.Fatal("a placeholder in run must be rejected, not shipped literally")
	}
}

func TestSolePlaceholderFallsBackToTheValueWithoutAProjection(t *testing.T) {
	compiled, err := Compile(Source{
		ID:    "group/tools/build",
		Order: []string{"plain", "into_env"},
		Params: map[string]Param{
			"plain":    {Control: Text, Required: true},
			"into_env": {Control: Text, Required: true, Projection: Projection{Kind: ProjectionEnv, Name: "LABEL"}},
		},
		Argv: []string{"./build.sh", "{{plain}}", "{{into_env}}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered := render(t, compiled, RawValues{"plain": rawText("one"), "into_env": rawText("two")})
	want := []string{"./build.sh", "one", "two"}
	if !reflect.DeepEqual(rendered.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", rendered.Argv, want)
	}
	if rendered.Env["LABEL"] != "two" {
		t.Fatalf("env = %#v", rendered.Env)
	}
}

func TestConfirmCollectsEveryReason(t *testing.T) {
	compiled, err := Compile(Source{
		ID:    "group/infra/deploy",
		Order: []string{"clean", "host"},
		Params: map[string]Param{
			"clean": {Control: Checkbox, Default: boolValue(false), Projection: Projection{Kind: ProjectionFlag, Name: "--clean"}, Confirm: "stored data is erased"},
			"host": {Control: Radio, Required: true, Options: []Option{
				{Value: "staging", Flag: "--staging"},
				{Value: "prod", Flag: "--prod", Confirm: "production is live"},
			}},
		},
		Run: []string{"./deploy.sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	quiet := render(t, compiled, RawValues{"host": rawText("staging")})
	if quiet.Confirm || len(quiet.ConfirmReasons) != 0 {
		t.Fatalf("safe values asked for confirmation: %#v", quiet.ConfirmReasons)
	}
	loud := render(t, compiled, RawValues{"clean": {Present: true, Bool: boolPtr(true)}, "host": rawText("prod")})
	if !loud.Confirm || len(loud.ConfirmReasons) != 2 {
		t.Fatalf("confirm = %v, reasons = %#v", loud.Confirm, loud.ConfirmReasons)
	}
}

func TestStaticConfirmSurvivesValuesThatDoNotAsk(t *testing.T) {
	compiled, err := Compile(Source{
		ID:      "group/infra/wipe",
		Order:   []string{"force"},
		Params:  map[string]Param{"force": checkbox("--force")},
		Run:     []string{"./wipe.sh"},
		Confirm: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered := render(t, compiled, RawValues{})
	if !rendered.Confirm {
		t.Fatal("a statically confirmed action stopped asking once it took parameters")
	}
}

func TestLimitsRejectOversizedDeclarationsAndValues(t *testing.T) {
	order := make([]string, 0, MaxParams+1)
	params := map[string]Param{}
	for index := 0; index <= MaxParams; index++ {
		name := "p" + strconv.Itoa(index)
		order = append(order, name)
		params[name] = Param{Control: Text, Optional: true}
	}
	if _, err := Compile(Source{ID: "group/g/a", Order: order, Params: params, Run: []string{"./a.sh"}}); err == nil {
		t.Fatalf("more than %d parameters accepted", MaxParams)
	}
	compiled, err := Compile(Source{
		ID:     "group/g/a",
		Order:  []string{"text"},
		Params: map[string]Param{"text": {Control: Text, Required: true, Projection: Projection{Kind: ProjectionPositional}}},
		Run:    []string{"./a.sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Normalize(compiled, RawValues{"text": rawText(strings.Repeat("x", MaxValueBytes+1))}); err == nil {
		t.Fatalf("a value longer than %d bytes was accepted", MaxValueBytes)
	}
}

func TestUnusedCountsEveryWayAValueReachesTheCommand(t *testing.T) {
	compiled, err := Compile(Source{
		ID:    "group/g/a",
		Order: []string{"flagged", "carried", "chosen", "named", "orphan"},
		Params: map[string]Param{
			"flagged": checkbox("--flagged"),
			"carried": {Control: Radio, Required: true, Options: []Option{{Value: "one", Args: []string{"--one"}}, {Value: "two"}}},
			"chosen":  {Control: Text, Required: true, Projection: Projection{Kind: ProjectionEnv, Name: "CHOSEN"}},
			"named":   {Control: Text, Required: true},
			"orphan":  {Control: Text, Optional: true},
		},
		// A fixed run appends every projection it is given, so placing them by
		// hand is not required here.
		Run: []string{"./a.sh"},
		Env: map[string]string{"NAME": "{{named}}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A parameter that reaches the command through a projection, through an
	// option that carries arguments, or through a template is used. Only the
	// one that reaches it in no way is worth warning about.
	if got := compiled.Unused(); len(got) != 1 || got[0] != "orphan" {
		t.Fatalf("unused = %#v, want only the orphan", got)
	}
}

func TestPreviewShowsEnvironmentAssignments(t *testing.T) {
	compiled, err := Compile(Source{
		ID:    "group/infra/seed",
		Order: []string{"write", "env"},
		Params: map[string]Param{
			"write": {Control: Checkbox, Default: boolValue(false), Projection: Projection{Kind: ProjectionEnv, Name: "WRITE"}},
			"env":   {Control: Text, Required: true, Projection: Projection{Kind: ProjectionArg, Name: "--env"}},
		},
		Run: []string{"./seed.sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A value that only reaches the command through the environment still has
	// to be visible in the line that says what will run.
	off := render(t, compiled, RawValues{"env": rawText("dev")})
	if off.Preview != "WRITE=0 ./seed.sh --env dev" {
		t.Fatalf("preview = %q", off.Preview)
	}
	on := render(t, compiled, RawValues{"env": rawText("dev"), "write": {Present: true, Bool: boolPtr(true)}})
	if on.Preview != "WRITE=1 ./seed.sh --env dev" {
		t.Fatalf("preview after the switch = %q", on.Preview)
	}
	if off.Preview == on.Preview {
		t.Fatal("switching an environment value changed nothing in the preview")
	}
}

func TestAnOmittedParameterContributesNothing(t *testing.T) {
	compiled, err := Compile(Source{
		ID:    "group/infra/seed",
		Order: []string{"count", "label", "target"},
		Params: map[string]Param{
			"count":  {Control: Number, Default: &Value{Kind: KindInt, Int: 100}, Projection: Projection{Kind: ProjectionArg, Name: "--count"}},
			"label":  {Control: Text, Optional: true, Projection: Projection{Kind: ProjectionArg, Name: "--label"}},
			"target": {Control: Text, Required: true, Projection: Projection{Kind: ProjectionArg, Name: "--target"}},
		},
		Run: []string{"./seed.sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Leaving a defaulted parameter out is not the same as not mentioning it:
	// the default would be passed, and omitting means passing nothing.
	rendered := render(t, compiled, RawValues{"count": {Omitted: true}, "target": rawText("phone")})
	if want := []string{"./seed.sh", "--target", "phone"}; !reflect.DeepEqual(rendered.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", rendered.Argv, want)
	}

	// A required parameter cannot be left out.
	if _, err := Normalize(compiled, RawValues{"target": {Omitted: true}}); err == nil {
		t.Fatal("a required parameter was omitted without complaint")
	}
}

func TestAnOmittedParameterRemovesItsInterpolatedArgvElement(t *testing.T) {
	compiled, err := Compile(Source{
		ID:    "group/infra/check",
		Order: []string{"env"},
		Params: map[string]Param{
			"env": {
				Control: Radio,
				Default: &Value{Kind: KindString, Text: "dev"},
				Options: []Option{{Value: "dev"}, {Value: "prod"}},
			},
		},
		Argv: []string{"./check.sh", "--env={{env}}"},
	})
	if err != nil {
		t.Fatal(err)
	}

	rendered := render(t, compiled, RawValues{"env": {Omitted: true}})
	if want := []string{"./check.sh"}; !reflect.DeepEqual(rendered.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", rendered.Argv, want)
	}
	if strings.Contains(rendered.Preview, "--env=") {
		t.Fatalf("disabled radio left an empty argument in preview: %q", rendered.Preview)
	}
}

func TestAProjectionNobodyPlacedInArgvIsUnused(t *testing.T) {
	compiled, err := Compile(Source{
		ID:    "group/g/a",
		Order: []string{"placed", "stranded", "exported"},
		Params: map[string]Param{
			"placed":   {Control: Text, Required: true, Projection: Projection{Kind: ProjectionArg, Name: "--placed"}},
			"stranded": {Control: Text, Optional: true, Projection: Projection{Kind: ProjectionArg, Name: "--stranded"}},
			"exported": {Control: Text, Optional: true, Projection: Projection{Kind: ProjectionEnv, Name: "EXPORTED"}},
		},
		// An explicit argv places its values by hand, so a projection nobody
		// wrote into it never reaches the command.
		Argv: []string{"./a.sh", "{{placed}}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := compiled.Unused(); len(got) != 1 || got[0] != "stranded" {
		t.Fatalf("unused = %#v, want only the one argv never places", got)
	}
}
