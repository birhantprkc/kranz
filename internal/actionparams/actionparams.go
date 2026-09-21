// Package actionparams compiles and renders parameterized actions. It is a
// pure package: it never reads the environment, the filesystem, the clock, or
// global state, and it never starts a process.
package actionparams

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Schema limits keep a parameter declaration and one invocation bounded input.
const (
	MaxParams       = 64
	MaxParamName    = 64
	MaxOptions      = 256
	MaxValueBytes   = 4096
	MaxPatternBytes = 2048
	MaxInvocation   = 32 * 1024
)

// Control is the form control that declares a parameter's data and its TUI
// rendering. Wire types are derived from the control, never from its name.
type Control uint8

const (
	Checkbox Control = iota
	Radio
	Select
	Text
	Number
)

// String returns the configuration keyword for the control.
func (c Control) String() string {
	switch c {
	case Checkbox:
		return "checkbox"
	case Radio:
		return "radio"
	case Select:
		return "select"
	case Text:
		return "text"
	case Number:
		return "number"
	default:
		return "unknown"
	}
}

// ParseControl maps a configuration keyword to its control.
func ParseControl(value string) (Control, bool) {
	switch value {
	case "checkbox":
		return Checkbox, true
	case "radio":
		return Radio, true
	case "select":
		return Select, true
	case "text":
		return Text, true
	case "number":
		return Number, true
	default:
		return 0, false
	}
}

// ValueKind is the normalized wire kind of one parameter value.
type ValueKind uint8

const (
	KindBool ValueKind = iota
	KindString
	KindInt
	KindStrings
)

// Value is one normalized parameter value. Which field is meaningful is fixed
// by Kind, and therefore by the parameter's control.
type Value struct {
	Kind    ValueKind
	Bool    bool
	Text    string
	Int     int64
	Strings []string
}

// ProjectionKind identifies how a value reaches the command.
type ProjectionKind uint8

const (
	ProjectionNone ProjectionKind = iota
	ProjectionFlag
	ProjectionArg
	ProjectionPositional
	ProjectionEnv
)

// Projection is the single way a parameter's value enters the invocation.
type Projection struct {
	Kind ProjectionKind
	// Name is the flag, the joined argument prefix, or the environment
	// variable name, depending on Kind.
	Name string
	// Joined reports a trailing '=' on an arg projection, which glues the
	// value to the flag instead of following it as a separate element.
	Joined bool
}

// Option is one selectable value of a list control. It may carry its own
// projection, its own label, its own confirmation, and its own command.
type Option struct {
	Value   string
	Label   string
	Confirm string
	Flag    string
	Args    []string
	Run     []string
}

// Param is one compiled parameter declaration.
type Param struct {
	Control    Control
	Options    []Option
	Default    *Value
	Required   bool
	Optional   bool
	Projection Projection
	Confirm    string
	Prompt     string
	Min        *int64
	Max        *int64
	MinLength  *int
	MaxLength  *int
	Pattern    string
}

// Source is the neutral input the configuration adapter builds from an action.
type Source struct {
	ID          string
	Order       []string
	Params      map[string]Param
	Run         []string
	Argv        []string
	Env         map[string]string
	Confirm     bool
	Interactive bool
}

// Segment is one literal or one parameter-value span of a template string.
type Segment struct {
	Literal string
	Param   string
}

// ArgvElem is one compiled argv element. A non-empty Projection means the
// element inserted the projection of that parameter; otherwise Segments are
// interpolated as one element.
type ArgvElem struct {
	Projection string
	Segments   []Segment
}

// EnvVar is one compiled environment binding.
type EnvVar struct {
	Name     string
	Segments []Segment
}

// Compiled is an action definition with its placeholders parsed and its
// parameter schema validated. It is immutable after Compile.
type Compiled struct {
	ID          string
	Order       []string
	Params      map[string]Param
	Run         []string
	Argv        []ArgvElem
	ArgvSource  []string
	Env         []EnvVar
	Confirm     bool
	Interactive bool
	// OptionRunParam names the parameter whose options carry their own command.
	OptionRunParam string
	// Referenced names the parameters used by a template or an env binding.
	Referenced map[string]bool
}

// Constraint is the human sentence a parameter's limits amount to: what a
// value has to satisfy to be accepted. Every surface says the same thing.
func (p Param) Constraint() string {
	var parts []string
	switch {
	case p.Min != nil && p.Max != nil:
		parts = append(parts, fmt.Sprintf("from %d to %d", *p.Min, *p.Max))
	case p.Min != nil:
		parts = append(parts, fmt.Sprintf("%d or more", *p.Min))
	case p.Max != nil:
		parts = append(parts, fmt.Sprintf("%d or less", *p.Max))
	}
	switch {
	case p.MinLength != nil && p.MaxLength != nil:
		parts = append(parts, fmt.Sprintf("from %d to %d characters", *p.MinLength, *p.MaxLength))
	case p.MinLength != nil:
		parts = append(parts, fmt.Sprintf("at least %d characters", *p.MinLength))
	case p.MaxLength != nil:
		parts = append(parts, fmt.Sprintf("at most %d characters", *p.MaxLength))
	}
	if p.Pattern != "" {
		parts = append(parts, "matching "+p.Pattern)
	}
	return strings.Join(parts, ", ")
}

// Unused reports declared parameters that no template, option command, or env
// binding consumes. They are a warning, not a load error.
func (c *Compiled) Unused() []string {
	if c == nil || c.OptionRunParam != "" {
		return nil
	}
	var unused []string
	for _, name := range c.Order {
		if c.Referenced[name] {
			continue
		}
		// An explicit argv places every value itself, so a projection nobody
		// placed reaches nothing. A fixed run appends them, so there a
		// projection is enough on its own.
		if len(c.Argv) > 0 {
			if c.Params[name].Projection.Kind == ProjectionEnv {
				continue
			}
			unused = append(unused, name)
			continue
		}
		if contributes(c.Params[name]) {
			continue
		}
		unused = append(unused, name)
	}
	return unused
}

// contributes reports whether a parameter reaches the invocation at all, by a
// projection of its own or through what its options carry. Only a parameter
// that reaches it in no way and is named by no template is unused.
func contributes(param Param) bool {
	if param.Projection.Kind != ProjectionNone {
		return true
	}
	for _, option := range param.Options {
		if option.Flag != "" || len(option.Args) > 0 || len(option.Run) > 0 {
			return true
		}
	}
	return false
}

// HasParams reports whether the action declares any parameter.
func (c *Compiled) HasParams() bool { return c != nil && len(c.Order) > 0 }

// Template returns the display template of the command before projection.
func (c *Compiled) Template() []string {
	if c == nil {
		return nil
	}
	if len(c.ArgvSource) > 0 {
		return append([]string(nil), c.ArgvSource...)
	}
	return append([]string(nil), c.Run...)
}

// ValueSource records where a normalized value came from.
type ValueSource uint8

const (
	SourceDefault ValueSource = iota
	SourceExplicit
)

// Invocation is a complete normalized parameter set. Values only contains
// parameters whose value is present: an absent optional parameter is omitted.
type Invocation struct {
	Values  map[string]Value
	Sources map[string]ValueSource
}

// Rendered is the immutable, ready-to-execute outcome of a render. Preview is
// display-only and is never parsed back.
type Rendered struct {
	Argv           []string
	Env            map[string]string
	Confirm        bool
	ConfirmReasons []string
	Preview        string
}

// Raw is one raw value as it arrived from a surface.
type Raw struct {
	Present bool
	// Omitted says the caller deliberately left this parameter out. It differs
	// from simply not sending one, which falls back to the default: an omitted
	// parameter contributes nothing to the invocation at all.
	Omitted bool
	Bool    *bool
	Text    string
	Int     *int64
	Strings []string
}

// RawValues is a raw invocation keyed by parameter name.
type RawValues map[string]Raw

var paramNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateParamName reports whether a name obeys the placeholder grammar.
func ValidateParamName(name string) bool {
	return len(name) <= MaxParamName && paramNamePattern.MatchString(name)
}

// Compile validates a source declaration and parses its templates.
func Compile(src Source) (*Compiled, error) {
	compiled := &Compiled{ID: src.ID, Run: append([]string(nil), src.Run...), ArgvSource: append([]string(nil), src.Argv...), Confirm: src.Confirm, Interactive: src.Interactive}
	if err := validateParamNames(src.Order, src.Params); err != nil {
		return nil, err
	}
	compiled.Order = append([]string(nil), src.Order...)
	compiled.Params = src.Params
	if len(compiled.Order) > MaxParams {
		return nil, configError(fmt.Sprintf("an action cannot declare more than %d parameters", MaxParams))
	}
	for _, name := range compiled.Order {
		param := compiled.Params[name]
		if err := validateParam(name, param, len(compiled.Order) > 0); err != nil {
			return nil, err
		}
	}
	if err := validateOptionRuns(compiled); err != nil {
		return nil, err
	}

	referenced := map[string]bool{}
	// 'run' is a fixed vector whose projections are appended, so a placeholder
	// there would ship literally into the command instead of being substituted.
	for index, element := range compiled.Run {
		if strings.Contains(element, "{{") {
			return nil, configError(fmt.Sprintf("run[%d]: placeholders belong in 'argv'; 'run' is a fixed vector", index))
		}
	}
	if len(compiled.ArgvSource) > 0 {
		if len(compiled.Order) == 0 {
			return nil, configError("field 'argv' requires 'params'; use 'command' for a static action")
		}
		elements, err := compileArgv(compiled.ArgvSource, compiled.Params, referenced)
		if err != nil {
			return nil, err
		}
		compiled.Argv = elements
	}
	envNames := make([]string, 0, len(src.Env))
	for name := range src.Env {
		envNames = append(envNames, name)
	}
	sort.Strings(envNames)
	for _, name := range envNames {
		if name == "" {
			return nil, configError("environment variable name cannot be empty")
		}
		segments, err := parseTemplate(src.Env[name], referenced)
		if err != nil {
			return nil, configError(fmt.Sprintf("env %s: %s", name, err))
		}
		compiled.Env = append(compiled.Env, EnvVar{Name: name, Segments: segments})
	}
	// Every referenced name must resolve to a declared parameter.
	for name := range referenced {
		if _, exists := compiled.Params[name]; !exists {
			return nil, configError(fmt.Sprintf("template references unknown parameter %q", name))
		}
	}
	compiled.Referenced = referenced
	if compiled.OptionRunParam != "" {
		if len(compiled.Run) > 0 || len(compiled.Argv) > 0 {
			return nil, configError("an action with option commands cannot also declare 'run' or 'argv'")
		}
	}
	return compiled, nil
}

// NativeValues converts normalized values into JSON-native Go values for a
// plan, result, or history record.
func NativeValues(invocation Invocation) map[string]any {
	if len(invocation.Values) == 0 {
		return nil
	}
	values := make(map[string]any, len(invocation.Values))
	for name, value := range invocation.Values {
		switch value.Kind {
		case KindBool:
			values[name] = value.Bool
		case KindString:
			values[name] = value.Text
		case KindInt:
			values[name] = value.Int
		case KindStrings:
			values[name] = append([]string(nil), value.Strings...)
		}
	}
	return values
}

func validateParamNames(order []string, params map[string]Param) error {
	seen := map[string]bool{}
	for _, name := range order {
		if !ValidateParamName(name) {
			return configError(fmt.Sprintf("invalid parameter name %q", name))
		}
		if seen[name] {
			return configError(fmt.Sprintf("duplicate parameter name %q", name))
		}
		seen[name] = true
	}
	for name := range params {
		if !ValidateParamName(name) {
			return configError(fmt.Sprintf("invalid parameter name %q", name))
		}
	}
	return nil
}

func validateParam(name string, param Param, actionHasParams bool) error {
	switch param.Control {
	case Radio, Select:
		if len(param.Options) == 0 {
			return configField(name, fmt.Sprintf("%s requires 'options'", param.Control))
		}
	case Text, Number:
		if len(param.Options) > 0 {
			return configField(name, fmt.Sprintf("%s does not accept 'options'", param.Control))
		}
	}
	if len(param.Options) > MaxOptions {
		return configField(name, fmt.Sprintf("an option list cannot exceed %d values", MaxOptions))
	}
	optionValues := map[string]bool{}
	for _, option := range param.Options {
		if !utf8.ValidString(option.Value) || len(option.Value) > MaxValueBytes {
			return configField(name, "an option value is too long")
		}
		if optionValues[option.Value] {
			return configField(name, fmt.Sprintf("duplicate option value %q", option.Value))
		}
		optionValues[option.Value] = true
	}
	if param.Required && param.Optional {
		return configField(name, "required and optional cannot be used together")
	}
	if param.Required && param.Default != nil {
		return configField(name, "required and default cannot be used together")
	}
	if param.Optional && param.Default != nil {
		return configField(name, "optional and default cannot be used together")
	}
	if !param.Required && !param.Optional && param.Default == nil {
		return configField(name, "set one of 'default', 'required: true', or 'optional: true'")
	}
	if err := validateDefault(name, param); err != nil {
		return err
	}
	if err := validateProjection(name, param); err != nil {
		return err
	}
	if param.Pattern != "" {
		if param.Control != Text {
			return configField(name, "'pattern' applies only to text parameters")
		}
		if len(param.Pattern) > MaxPatternBytes {
			return configField(name, fmt.Sprintf("'pattern' cannot exceed %d bytes", MaxPatternBytes))
		}
		if _, err := compilePattern(param.Pattern); err != nil {
			return configField(name, "pattern is not a valid regular expression")
		}
	}
	if param.MinLength != nil || param.MaxLength != nil {
		if param.Control != Text {
			return configField(name, "length limits apply only to text parameters")
		}
	}
	return nil
}

func validateDefault(name string, param Param) error {
	if param.Default == nil {
		return nil
	}
	value := *param.Default
	if len(param.Options) > 0 {
		if value.Kind != KindString && value.Kind != KindStrings {
			return configField(name, "default has the wrong type for an option control")
		}
		for _, selected := range valueTexts(value) {
			if !containsOption(param, selected) {
				return configField(name, fmt.Sprintf("default %q is not one of the declared options", selected))
			}
		}
		return nil
	}
	switch param.Control {
	case Checkbox:
		if value.Kind != KindBool {
			return configField(name, "default must be true or false")
		}
	case Text:
		if value.Kind != KindString {
			return configField(name, "default must be a string")
		}
	case Number:
		if value.Kind != KindInt {
			return configField(name, "default must be an integer")
		}
		if param.Min != nil && value.Int < *param.Min || param.Max != nil && value.Int > *param.Max {
			return configField(name, "default is outside the declared range")
		}
	}
	if value.Kind == KindString || value.Kind == KindStrings {
		for _, text := range valueTexts(value) {
			if err := checkText(name, param, text); err != nil {
				return err
			}
		}
	}
	if value.Kind == KindInt {
		if err := checkNumber(name, param, value.Int); err != nil {
			return err
		}
	}
	return nil
}

func validateProjection(name string, param Param) error {
	count := 0
	if param.Projection.Kind == ProjectionFlag {
		count++
	}
	if param.Projection.Kind == ProjectionArg {
		count++
	}
	if param.Projection.Kind == ProjectionPositional {
		count++
	}
	if param.Projection.Kind == ProjectionEnv {
		count++
	}
	if count > 1 {
		return configField(name, "a parameter can declare at most one projection")
	}
	if param.Projection.Kind == ProjectionFlag && (param.Control != Checkbox || len(param.Options) > 0) {
		return configField(name, "'flag' applies only to a boolean checkbox")
	}
	if param.Projection.Kind == ProjectionEnv && param.Projection.Name == "" {
		return configField(name, "environment projection requires a variable name")
	}
	if param.Confirm != "" && (param.Control != Checkbox || len(param.Options) > 0) {
		return configField(name, "'confirm' on a parameter applies only to a boolean checkbox; put it on options instead")
	}
	return nil
}

func validateOptionRuns(compiled *Compiled) error {
	found := ""
	for _, name := range compiled.Order {
		param := compiled.Params[name]
		withRun := 0
		for _, option := range param.Options {
			if len(option.Run) > 0 {
				withRun++
			}
		}
		if withRun == 0 {
			continue
		}
		if param.Control != Radio && param.Control != Select {
			return configField(name, "option commands are allowed only on radio or select parameters")
		}
		if withRun != len(param.Options) {
			return configField(name, "every option must declare 'run' when one does")
		}
		if found != "" {
			return configField(name, "only one parameter may declare option commands")
		}
		found = name
	}
	compiled.OptionRunParam = found
	return nil
}

func compileArgv(source []string, params map[string]Param, referenced map[string]bool) ([]ArgvElem, error) {
	elements := make([]ArgvElem, 0, len(source))
	for index, value := range source {
		if index == 0 {
			if strings.Contains(value, "{{") {
				return nil, configError("argv[0] is the executable and cannot contain placeholders")
			}
			elements = append(elements, ArgvElem{Segments: []Segment{{Literal: value}}})
			continue
		}
		// An element that is nothing but one placeholder inserts the whole
		// projection of that parameter, which is zero, one, or several
		// arguments. The same placeholder inside surrounding text inserts the
		// value as a literal, so both spellings stay one syntax.
		if name, sole := solePlaceholder(value); sole && projects(params[name]) {
			referenced[name] = true
			elements = append(elements, ArgvElem{Projection: name})
			continue
		}
		segments, err := parseTemplate(value, referenced)
		if err != nil {
			return nil, configError(fmt.Sprintf("argv[%d]: %s", index, err))
		}
		elements = append(elements, ArgvElem{Segments: segments})
	}
	return elements, nil
}

// solePlaceholder reports the parameter name when a string is exactly one
// "{{name}}" placeholder and nothing else.
func solePlaceholder(value string) (string, bool) {
	if len(value) < 5 || !strings.HasPrefix(value, "{{") || !strings.HasSuffix(value, "}}") {
		return "", false
	}
	name := value[2 : len(value)-2]
	if !ValidateParamName(name) {
		return "", false
	}
	return name, true
}

// projects reports whether a parameter contributes argv elements of its own,
// through a projection or through option-level flags and arguments. An env
// projection contributes none, so it interpolates as a literal instead.
func projects(param Param) bool {
	if param.Projection.Kind != ProjectionNone && param.Projection.Kind != ProjectionEnv {
		return true
	}
	for _, option := range param.Options {
		if option.Flag != "" || len(option.Args) > 0 {
			return true
		}
	}
	return false
}

// parseTemplate splits a string into literal and {{name}} parameter segments.
func parseTemplate(value string, referenced map[string]bool) ([]Segment, error) {
	var segments []Segment
	remaining := value
	for {
		start := strings.Index(remaining, "{{")
		if start < 0 {
			if strings.Contains(remaining, "}}") {
				return nil, fmt.Errorf("unmatched '}}' in %q", value)
			}
			if remaining != "" {
				segments = append(segments, Segment{Literal: remaining})
			}
			return segments, nil
		}
		if start > 0 {
			segments = append(segments, Segment{Literal: remaining[:start]})
		}
		rest := remaining[start+2:]
		end := strings.Index(rest, "}}")
		if end < 0 {
			return nil, fmt.Errorf("unclosed '{{' in %q", value)
		}
		name := rest[:end]
		if !ValidateParamName(name) {
			return nil, fmt.Errorf("invalid placeholder %q", "{{"+name+"}}")
		}
		referenced[name] = true
		segments = append(segments, Segment{Param: name})
		remaining = rest[end+2:]
	}
}

func compilePattern(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile(`\A(?:` + pattern + `)\z`)
}

// Normalize applies defaults, validates every value, and returns a complete
// typed invocation. Unknown and invalid values are reported together.
func Normalize(compiled *Compiled, raw RawValues) (Invocation, error) {
	if compiled == nil {
		return Invocation{}, configError("action has no compiled parameter schema")
	}
	invocation := Invocation{Values: map[string]Value{}, Sources: map[string]ValueSource{}}
	var fields []FieldError
	total := 0
	for name := range raw {
		if _, exists := compiled.Params[name]; !exists {
			fields = append(fields, FieldError{Name: name, Reason: "unknown parameter"})
		}
	}
	for _, name := range compiled.Order {
		param := compiled.Params[name]
		rawValue, present := raw[name]
		if present && rawValue.Omitted {
			if param.Required {
				fields = append(fields, FieldError{Name: name, Reason: "a value is required"})
			}
			continue
		}
		if present && rawValue.Present {
			total += rawSize(rawValue)
			value, err := normalizeRaw(name, param, rawValue)
			if err != nil {
				fields = append(fields, FieldError{Name: name, Reason: err.Error()})
				continue
			}
			invocation.Values[name] = value
			invocation.Sources[name] = SourceExplicit
			continue
		}
		if param.Default != nil {
			invocation.Values[name] = *param.Default
			invocation.Sources[name] = SourceDefault
			continue
		}
		if param.Optional {
			continue
		}
		fields = append(fields, FieldError{Name: name, Reason: "a value is required"})
	}
	if len(fields) > 0 {
		return Invocation{}, &Error{Code: CodeInvalidArguments, Message: "action parameters are invalid", Fields: sortFields(fields)}
	}
	if total > MaxInvocation {
		return Invocation{}, &Error{Code: CodeInvalidArguments, Message: fmt.Sprintf("parameter input exceeds %d bytes", MaxInvocation), Fields: []FieldError{{Name: "", Reason: "input is too large"}}}
	}
	return invocation, nil
}

func rawSize(raw Raw) int {
	size := len(raw.Text)
	for _, value := range raw.Strings {
		size += len(value)
	}
	return size
}

func normalizeRaw(name string, param Param, raw Raw) (Value, error) {
	if param.Control == Checkbox && len(param.Options) == 0 {
		value := false
		switch {
		case raw.Bool != nil:
			value = *raw.Bool
		case raw.Text != "":
			parsed, err := strconv.ParseBool(strings.ToLower(raw.Text))
			if err != nil || (raw.Text != "true" && raw.Text != "false") {
				return Value{}, fmt.Errorf("expected true or false")
			}
			value = parsed
		default:
			return Value{}, fmt.Errorf("expected true or false")
		}
		return Value{Kind: KindBool, Bool: value}, nil
	}
	if len(param.Options) > 0 {
		selected := raw.Strings
		if len(selected) == 0 && raw.Text != "" {
			selected = []string{raw.Text}
		}
		if len(selected) == 0 {
			return Value{}, fmt.Errorf("select at least one of %s", optionList(param))
		}
		order := map[string]int{}
		for index, option := range param.Options {
			order[option.Value] = index
		}
		seen := map[string]bool{}
		unique := make([]string, 0, len(selected))
		for _, value := range selected {
			if _, exists := order[value]; !exists {
				return Value{}, fmt.Errorf("expected one of %s", optionList(param))
			}
			if seen[value] {
				continue
			}
			seen[value] = true
			unique = append(unique, value)
		}
		sort.SliceStable(unique, func(i, j int) bool { return order[unique[i]] < order[unique[j]] })
		if param.Control == Checkbox {
			return Value{Kind: KindStrings, Strings: unique}, nil
		}
		return Value{Kind: KindString, Text: unique[0]}, nil
	}
	switch param.Control {
	case Text:
		if err := checkText(name, param, raw.Text); err != nil {
			return Value{}, err
		}
		return Value{Kind: KindString, Text: raw.Text}, nil
	case Number:
		var number int64
		switch {
		case raw.Int != nil:
			number = *raw.Int
		case raw.Text != "":
			parsed, err := parseStrictInt(raw.Text)
			if err != nil {
				return Value{}, fmt.Errorf("expected an integer between %s", rangeText(param))
			}
			number = parsed
		default:
			return Value{}, fmt.Errorf("expected an integer between %s", rangeText(param))
		}
		if err := checkNumber(name, param, number); err != nil {
			return Value{}, err
		}
		return Value{Kind: KindInt, Int: number}, nil
	default:
		if raw.Text == "" && len(raw.Strings) == 0 {
			return Value{}, fmt.Errorf("a value is required")
		}
		text := raw.Text
		if text == "" {
			text = raw.Strings[0]
		}
		return Value{Kind: KindString, Text: text}, nil
	}
}

// parseStrictInt accepts a plain decimal integer, rejecting exponents, spaces,
// fractions, and overflow.
func parseStrictInt(text string) (int64, error) {
	if text == "" || strings.TrimSpace(text) != text {
		return 0, fmt.Errorf("not an integer")
	}
	for index, r := range text {
		if r == '-' || r == '+' {
			if index != 0 {
				return 0, fmt.Errorf("not an integer")
			}
			continue
		}
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not an integer")
		}
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("not an integer")
	}
	return value, nil
}

func checkText(name string, param Param, text string) error {
	if !utf8.ValidString(text) {
		return fmt.Errorf("expected valid UTF-8 text")
	}
	if len(text) > MaxValueBytes {
		return fmt.Errorf("value cannot exceed %d bytes", MaxValueBytes)
	}
	length := utf8.RuneCountInString(text)
	if param.MinLength != nil && length < *param.MinLength {
		return fmt.Errorf("expected at least %d characters", *param.MinLength)
	}
	if param.MaxLength != nil && length > *param.MaxLength {
		return fmt.Errorf("expected at most %d characters", *param.MaxLength)
	}
	if param.Pattern != "" {
		compiled, err := compilePattern(param.Pattern)
		if err == nil && !compiled.MatchString(text) {
			return fmt.Errorf("value does not match the required pattern")
		}
	}
	return nil
}

func checkNumber(name string, param Param, number int64) error {
	if param.Min != nil && number < *param.Min || param.Max != nil && number > *param.Max {
		return fmt.Errorf("expected an integer between %s", rangeText(param))
	}
	return nil
}

func rangeText(param Param) string {
	if param.Min != nil && param.Max != nil {
		return fmt.Sprintf("%d and %d", *param.Min, *param.Max)
	}
	if param.Min != nil {
		return fmt.Sprintf("%d or more", *param.Min)
	}
	if param.Max != nil {
		return fmt.Sprintf("%d or less", *param.Max)
	}
	return fmt.Sprintf("%d and %d", math.MinInt64, int64(math.MaxInt64))
}

func optionList(param Param) string {
	values := make([]string, 0, len(param.Options))
	for _, option := range param.Options {
		values = append(values, option.Value)
	}
	return strings.Join(values, ", ")
}

func containsOption(param Param, value string) bool {
	for _, option := range param.Options {
		if option.Value == value {
			return true
		}
	}
	return false
}

// Render produces the immutable argv, environment, confirmation, and preview
// of one normalized invocation.
func Render(compiled *Compiled, invocation Invocation) (Rendered, error) {
	if compiled == nil {
		return Rendered{}, configError("action has no compiled parameter schema")
	}
	out := Rendered{Env: map[string]string{}}
	var argv []string
	switch {
	case compiled.OptionRunParam != "":
		selector := compiled.Params[compiled.OptionRunParam]
		value, present := invocation.Values[compiled.OptionRunParam]
		if !present {
			return Rendered{}, invalidField(compiled.OptionRunParam, "a value is required")
		}
		option, ok := selector.option(value)
		if !ok {
			return Rendered{}, invalidField(compiled.OptionRunParam, "selected option has no command")
		}
		argv = append(argv, option.Run...)
		for _, name := range compiled.Order {
			if name == compiled.OptionRunParam {
				continue
			}
			value, present := invocation.Values[name]
			if !present {
				continue
			}
			argv = append(argv, projectionArgs(compiled.Params[name], value)...)
		}
	case len(compiled.Argv) > 0:
		for _, element := range compiled.Argv {
			if element.Projection != "" {
				value, present := invocation.Values[element.Projection]
				if !present {
					continue
				}
				argv = append(argv, projectionArgs(compiled.Params[element.Projection], value)...)
				continue
			}
			// An omitted parameter removes the argv element that embeds it. Keeping
			// only the surrounding literal would turn a disabled `--env={{env}}`
			// parameter into the unintended argument `--env=`.
			if !templateValuesPresent(element.Segments, invocation) {
				continue
			}
			argv = append(argv, interpolate(element.Segments, invocation))
		}
	default:
		argv = append(argv, compiled.Run...)
		for _, name := range compiled.Order {
			value, present := invocation.Values[name]
			if !present {
				continue
			}
			argv = append(argv, projectionArgs(compiled.Params[name], value)...)
		}
	}
	for _, name := range compiled.Order {
		param := compiled.Params[name]
		if param.Projection.Kind != ProjectionEnv {
			continue
		}
		value, present := invocation.Values[name]
		if !present {
			continue
		}
		out.Env[param.Projection.Name] = envText(value)
	}
	for _, variable := range compiled.Env {
		out.Env[variable.Name] = interpolate(variable.Segments, invocation)
	}
	out.Argv = argv
	out.Confirm = compiled.Confirm
	for _, name := range compiled.Order {
		param := compiled.Params[name]
		value, present := invocation.Values[name]
		if !present {
			continue
		}
		if param.Confirm != "" && value.Kind == KindBool && value.Bool {
			out.Confirm = true
			out.ConfirmReasons = append(out.ConfirmReasons, param.Confirm)
		}
		for _, option := range param.Options {
			if option.Confirm != "" && valueContains(value, option.Value) {
				out.Confirm = true
				out.ConfirmReasons = append(out.ConfirmReasons, option.Confirm)
			}
		}
	}
	// A value that only reaches the command through the environment would
	// otherwise change nothing anybody can see. The preview spells those
	// assignments the way a shell would, so every parameter is visible in the
	// one line that says what will run.
	assignments := make([]string, 0, len(compiled.Order))
	for _, name := range compiled.Order {
		param := compiled.Params[name]
		if param.Projection.Kind != ProjectionEnv {
			continue
		}
		if text, present := out.Env[param.Projection.Name]; present {
			assignments = append(assignments, param.Projection.Name+"="+quoteArg(text))
		}
	}
	out.Preview = strings.TrimSpace(strings.Join(assignments, " ") + " " + Preview(argv))
	return out, nil
}

func templateValuesPresent(segments []Segment, invocation Invocation) bool {
	for _, segment := range segments {
		if segment.Param == "" {
			continue
		}
		if _, present := invocation.Values[segment.Param]; !present {
			return false
		}
	}
	return true
}

func (p Param) option(value Value) (Option, bool) {
	for _, option := range p.Options {
		if valueContains(value, option.Value) {
			return option, true
		}
	}
	return Option{}, false
}

func valueContains(value Value, candidate string) bool {
	switch value.Kind {
	case KindString:
		return value.Text == candidate
	case KindStrings:
		for _, item := range value.Strings {
			if item == candidate {
				return true
			}
		}
	}
	return false
}

func projectionArgs(param Param, value Value) []string {
	if len(param.Options) > 0 {
		var out []string
		carried := false
		for _, option := range param.Options {
			if !valueContains(value, option.Value) {
				continue
			}
			if option.Flag != "" {
				out = append(out, option.Flag)
				carried = true
			}
			if len(option.Args) > 0 {
				out = append(out, option.Args...)
				carried = true
			}
		}
		if carried {
			return out
		}
	}
	switch param.Projection.Kind {
	case ProjectionFlag:
		if value.Kind == KindBool && value.Bool {
			return []string{param.Projection.Name}
		}
	case ProjectionArg:
		var out []string
		for _, text := range valueTexts(value) {
			if param.Projection.Joined {
				out = append(out, param.Projection.Name+text)
			} else {
				out = append(out, param.Projection.Name, text)
			}
		}
		return out
	case ProjectionPositional:
		return valueTexts(value)
	}
	return nil
}

func interpolate(segments []Segment, invocation Invocation) string {
	var builder strings.Builder
	for _, segment := range segments {
		if segment.Param == "" {
			builder.WriteString(segment.Literal)
			continue
		}
		if value, present := invocation.Values[segment.Param]; present {
			builder.WriteString(valueText(value))
		}
	}
	return builder.String()
}

func valueTexts(value Value) []string {
	switch value.Kind {
	case KindBool:
		return []string{strconv.FormatBool(value.Bool)}
	case KindString:
		return []string{value.Text}
	case KindInt:
		return []string{strconv.FormatInt(value.Int, 10)}
	case KindStrings:
		return append([]string(nil), value.Strings...)
	default:
		return nil
	}
}

func valueText(value Value) string {
	return strings.Join(valueTexts(value), ",")
}

// envText is the environment-variable spelling of a value. A boolean is "1" or
// "0" because scripts read such variables through ${VAR:-0}.
func envText(value Value) string {
	if value.Kind == KindBool {
		if value.Bool {
			return "1"
		}
		return "0"
	}
	return valueText(value)
}

// Preview renders argv as a display-only string with quoted element
// boundaries. It is never executed or parsed back.
func Preview(argv []string) string {
	parts := make([]string, len(argv))
	for index, element := range argv {
		parts[index] = quoteArg(element)
	}
	return strings.Join(parts, " ")
}

func quoteArg(value string) string {
	if value == "" {
		return "''"
	}
	safe := true
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.=/:@%+,", r) {
			continue
		}
		safe = false
		break
	}
	if safe {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// CanonicalValues serializes normalized values with sorted keys so map order
// and input order never affect an invocation identity.
func CanonicalValues(invocation Invocation) []byte {
	values := make(map[string]any, len(invocation.Values))
	for name, value := range invocation.Values {
		switch value.Kind {
		case KindBool:
			values[name] = value.Bool
		case KindString:
			values[name] = value.Text
		case KindInt:
			values[name] = value.Int
		case KindStrings:
			values[name] = append([]string(nil), value.Strings...)
		}
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return nil
	}
	return payload
}

// InvocationKey is a stable SHA-256 digest of an action identity and its
// canonical values.
func InvocationKey(id string, invocation Invocation) string {
	hash := sha256.New()
	hash.Write([]byte(id))
	hash.Write([]byte{0})
	hash.Write(CanonicalValues(invocation))
	return hex.EncodeToString(hash.Sum(nil))
}

// DecodeJSON converts a JSON object of raw values into surface-neutral raw
// values, checking the JSON type against each parameter's control.
func DecodeJSON(compiled *Compiled, values map[string]json.RawMessage) (RawValues, error) {
	raw := RawValues{}
	var fields []FieldError
	for name, message := range values {
		param, exists := compiled.Params[name]
		if !exists {
			fields = append(fields, FieldError{Name: name, Reason: "unknown parameter"})
			continue
		}
		value, err := decodeRawJSON(name, param, message)
		if err != nil {
			fields = append(fields, FieldError{Name: name, Reason: err.Error()})
			continue
		}
		raw[name] = value
	}
	if len(fields) > 0 {
		return nil, &Error{Code: CodeInvalidArguments, Message: "action parameters are invalid", Fields: sortFields(fields)}
	}
	return raw, nil
}

func decodeRawJSON(name string, param Param, message json.RawMessage) (Raw, error) {
	trimmed := strings.TrimSpace(string(message))
	if trimmed == "" || trimmed == "null" {
		// An explicit null is how a caller leaves a parameter out, which is not
		// the same as never mentioning it.
		return Raw{Omitted: true}, nil
	}
	if param.Control == Checkbox && len(param.Options) == 0 {
		var value bool
		if err := json.Unmarshal(message, &value); err != nil {
			return Raw{}, fmt.Errorf("expected true or false")
		}
		return Raw{Present: true, Bool: &value}, nil
	}
	if len(param.Options) > 0 {
		var list []string
		if err := json.Unmarshal(message, &list); err == nil {
			return Raw{Present: true, Strings: list}, nil
		}
		var single string
		if err := json.Unmarshal(message, &single); err != nil {
			return Raw{}, fmt.Errorf("expected one of %s", optionList(param))
		}
		return Raw{Present: true, Text: single}, nil
	}
	if param.Control == Number {
		if !regexp.MustCompile(`^-?\d+$`).MatchString(trimmed) {
			return Raw{}, fmt.Errorf("expected an integer between %s", rangeText(param))
		}
		var value int64
		if err := json.Unmarshal(message, &value); err != nil {
			return Raw{}, fmt.Errorf("expected an integer between %s", rangeText(param))
		}
		return Raw{Present: true, Int: &value}, nil
	}
	var value string
	if err := json.Unmarshal(message, &value); err != nil {
		return Raw{}, fmt.Errorf("expected text")
	}
	return Raw{Present: true, Text: value}, nil
}

// JSONSchema builds the advisory JSON Schema fragment for a compiled action.
// It helps a client build a form; runtime validation remains authoritative.
func JSONSchema(compiled *Compiled) map[string]any {
	properties := map[string]any{}
	var required []string
	for _, name := range compiled.Order {
		param := compiled.Params[name]
		properties[name] = paramSchema(param)
		if param.Required {
			required = append(required, name)
		}
	}
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func paramSchema(param Param) map[string]any {
	schema := map[string]any{}
	switch {
	case param.Control == Checkbox && len(param.Options) == 0:
		schema["type"] = "boolean"
	case len(param.Options) > 0:
		values := make([]string, 0, len(param.Options))
		for _, option := range param.Options {
			values = append(values, option.Value)
		}
		item := map[string]any{"type": "string", "enum": values}
		if param.Control == Checkbox {
			schema["type"] = "array"
			schema["items"] = item
		} else {
			schema["type"] = "string"
			schema["enum"] = values
		}
	case param.Control == Number:
		schema["type"] = "integer"
		if param.Min != nil {
			schema["minimum"] = *param.Min
		}
		if param.Max != nil {
			schema["maximum"] = *param.Max
		}
	default:
		schema["type"] = "string"
	}
	if param.Default != nil {
		schema["default"] = defaultAny(param)
	}
	if param.Prompt != "" {
		schema["description"] = param.Prompt
	}
	return schema
}

func defaultAny(param Param) any {
	if param.Default == nil {
		return nil
	}
	switch param.Default.Kind {
	case KindBool:
		return param.Default.Bool
	case KindString:
		return param.Default.Text
	case KindInt:
		return param.Default.Int
	case KindStrings:
		return append([]string(nil), param.Default.Strings...)
	}
	return nil
}

// ValueFromAny converts a decoded YAML/JSON scalar or list into a normalized
// value. The configuration adapter uses it to build defaults.
func ValueFromAny(input any) (Value, bool) {
	switch value := input.(type) {
	case bool:
		return Value{Kind: KindBool, Bool: value}, true
	case string:
		return Value{Kind: KindString, Text: value}, true
	case int:
		return Value{Kind: KindInt, Int: int64(value)}, true
	case int64:
		return Value{Kind: KindInt, Int: value}, true
	case uint64:
		if value > math.MaxInt64 {
			return Value{}, false
		}
		return Value{Kind: KindInt, Int: int64(value)}, true
	case float64:
		if value != math.Trunc(value) || value < math.MinInt64 || value > math.MaxInt64 {
			return Value{}, false
		}
		return Value{Kind: KindInt, Int: int64(value)}, true
	case []any:
		items := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return Value{}, false
			}
			items = append(items, text)
		}
		return Value{Kind: KindStrings, Strings: items}, true
	case []string:
		return Value{Kind: KindStrings, Strings: append([]string(nil), value...)}, true
	default:
		return Value{}, false
	}
}
