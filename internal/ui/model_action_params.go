package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/kranz-org/kranz/internal/actionparams"
	"github.com/kranz-org/kranz/internal/app"
	"github.com/kranz-org/kranz/internal/config"
)

// paramUIValue is the editable state of one parameter in the inline tree.
type paramUIValue struct {
	Set bool
	// Disabled leaves the parameter out of the invocation entirely, which is
	// not the same as holding its default.
	Disabled bool
	Bool     bool
	Text     string
	Strings  []string
}

// paramRowFocus identifies the currently focused parameter row. It is separate
// from focusedAction because several rows of one action are individually
// selectable.
type paramRowFocus struct {
	ID      config.ActionID
	Name    string
	Value   string
	IsValue bool
	Preview bool
}

// paramEditState names the row the in-place editor is open on. It is a mode
// rather than a modal so the value stays visible in its own row while it is
// being typed, next to the command it changes. The text itself lives in the
// shared text input, which is where the blinking cursor comes from.
type paramEditState struct {
	ID   config.ActionID
	Name string
}

type paramUISpec struct {
	Name     string
	Control  actionparams.Control
	Options  []actionparams.Option
	Default  *actionparams.Value
	Required bool
	Optional bool
	Prompt   string
	Confirm  string
	// Constraint is what a value has to satisfy, in the same words every
	// surface uses.
	Constraint string
}

type actionParamRender struct {
	Preview string
	Fields  map[string]string
	Confirm bool
	Reasons []string
	Params  map[string]json.RawMessage
}

// actionPlanMsg carries the outcome of a parameterized plan/execute round trip.
type actionPlanMsg struct {
	id         config.ActionID
	request    app.PlanRequest
	result     app.OperationResult
	action     config.Action
	lease      string
	err        error
	sessionGen uint64
}

func actionParamKey(id config.ActionID, name string) string {
	return string(id.OwnerKind) + "\x00" + id.Owner + "\x00" + id.Name + "\x00" + name
}

func actionIDKey(id config.ActionID) string {
	return string(id.OwnerKind) + "/" + id.Owner + "/" + id.Name
}

// actionParamSpecs returns the declaration order specs of one action.
func (m *Model) actionParamSpecs(id config.ActionID) []paramUISpec {
	action, exists := m.cfg.ResolveAction(id)
	if !exists || len(action.Params) == 0 {
		return nil
	}
	compiled, err := config.CompileAction(actionIDKey(id), action)
	if err != nil || compiled == nil {
		return nil
	}
	specs := make([]paramUISpec, 0, len(compiled.Order))
	for _, name := range compiled.Order {
		param := compiled.Params[name]
		specs = append(specs, paramUISpec{Name: name, Control: param.Control, Options: param.Options, Default: param.Default, Required: param.Required, Optional: param.Optional, Prompt: param.Prompt, Confirm: param.Confirm, Constraint: param.Constraint()})
	}
	return specs
}

func (m *Model) actionHasParams(id config.ActionID) bool {
	action, exists := m.cfg.ResolveAction(id)
	return exists && len(action.Params) > 0
}

func (m *Model) paramValuesFor(id config.ActionID) map[string]paramUIValue {
	if m.actionParamValues[id] == nil {
		values := make(map[string]paramUIValue)
		for _, spec := range m.actionParamSpecs(id) {
			values[spec.Name] = initialParamValue(spec)
		}
		m.actionParamValues[id] = values
	}
	return m.actionParamValues[id]
}

func initialParamValue(spec paramUISpec) paramUIValue {
	if spec.Default != nil {
		switch spec.Default.Kind {
		case actionparams.KindBool:
			return paramUIValue{Set: true, Bool: spec.Default.Bool}
		case actionparams.KindString:
			return paramUIValue{Set: true, Text: spec.Default.Text}
		case actionparams.KindInt:
			return paramUIValue{Set: true, Text: strconv.FormatInt(spec.Default.Int, 10)}
		case actionparams.KindStrings:
			return paramUIValue{Set: true, Strings: append([]string(nil), spec.Default.Strings...)}
		}
	}
	if spec.Control == actionparams.Checkbox && len(spec.Options) == 0 {
		return paramUIValue{Set: true}
	}
	return paramUIValue{}
}

func (m *Model) paramRawValue(spec paramUISpec, value paramUIValue) actionparams.Raw {
	if value.Disabled {
		return actionparams.Raw{Omitted: true}
	}
	switch {
	case spec.Control == actionparams.Checkbox && len(spec.Options) == 0:
		flag := value.Bool
		return actionparams.Raw{Present: true, Bool: &flag}
	case len(spec.Options) > 0 && spec.Control == actionparams.Checkbox:
		return actionparams.Raw{Present: value.Set || len(value.Strings) > 0, Strings: append([]string(nil), value.Strings...)}
	default:
		// An untouched field is absent, not an empty string: a required
		// parameter must stay unsatisfied until somebody supplies a value,
		// while an empty string somebody typed on purpose still counts.
		return actionparams.Raw{Present: value.Set, Text: value.Text}
	}
}

// renderActionParams normalizes the inline form exactly the way execution will,
// so the preview and the run cannot disagree.
func (m *Model) renderActionParams(id config.ActionID) (actionParamRender, error) {
	action, exists := m.cfg.ResolveAction(id)
	if !exists {
		return actionParamRender{}, fmt.Errorf("action %s is no longer configured", actionIDKey(id))
	}
	compiled, err := config.CompileAction(actionIDKey(id), action)
	if err != nil || compiled == nil {
		return actionParamRender{}, err
	}
	raw := actionparams.RawValues{}
	values := m.paramValuesFor(id)
	for _, spec := range m.actionParamSpecs(id) {
		raw[spec.Name] = m.paramRawValue(spec, values[spec.Name])
	}
	invocation, err := actionparams.Normalize(compiled, raw)
	if err != nil {
		var paramErr *actionparams.Error
		if errors.As(err, &paramErr) {
			return actionParamRender{Fields: paramErr.FieldMap()}, err
		}
		return actionParamRender{}, err
	}
	rendered, err := actionparams.Render(compiled, invocation)
	if err != nil {
		return actionParamRender{}, err
	}
	native := actionparams.NativeValues(invocation)
	params := make(map[string]json.RawMessage, len(native))
	for name, value := range native {
		payload, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return actionParamRender{}, marshalErr
		}
		params[name] = payload
	}
	return actionParamRender{Preview: rendered.Preview, Confirm: rendered.Confirm, Reasons: rendered.ConfirmReasons, Params: params}, nil
}

// paramValueText renders one form value for the list.
func (m *Model) paramValueText(id config.ActionID, spec paramUISpec) string {
	value := m.paramValuesFor(id)[spec.Name]
	if value.Disabled {
		return ParamDisabledStyle.Render(disabledParamLabel)
	}
	switch {
	case spec.Control == actionparams.Checkbox && len(spec.Options) == 0:
		if value.Bool {
			return markChecked
		}
		return markUnchecked
	case len(spec.Options) > 0:
		if len(value.Strings) > 0 {
			return strings.Join(value.Strings, ", ")
		}
		if value.Text != "" {
			return value.Text
		}
		return "—"
	default:
		// A typed parameter is underlined so an empty one still reads as a
		// field waiting for input rather than as a missing line.
		if value.Text == "" {
			return ParamInputStyle.Render("______")
		}
		return value.Text
	}
}

// paramRowLabel names a parameter as a label, padded so every value under one
// action starts in the same column. A bare gap between the two read as two
// words rather than as a setting and its value.
func (m *Model) paramRowLabel(id config.ActionID, name string) string {
	label := name + ":"
	if width, aligned := m.paramLabelWidth(id); aligned {
		for len([]rune(label)) < width+1 {
			label += " "
		}
	}
	return m.paramLabelStyle(id, name).Render(label)
}

// paramLabelStyle dresses a parameter's name: red while it holds the command
// back, receded while it is switched off, and the ordinary label otherwise.
func (m *Model) paramLabelStyle(id config.ActionID, name string) lipgloss.Style {
	switch {
	case m.paramRowBlocked(id, name):
		return ParamBlockedStyle
	case m.paramValuesFor(id)[name].Disabled:
		return ParamDisabledStyle
	default:
		return DetailLabelStyle
	}
}

// paramDisabled reports whether one parameter is switched off.
func (m *Model) paramDisabled(id config.ActionID, name string) bool {
	return m.paramValuesFor(id)[name].Disabled
}

// paramLabelAlignSpread is how much longer the longest parameter name may be
// than the shortest before a shared column starts pushing every value away
// from its name. Past it the values follow their own labels directly.
const paramLabelAlignSpread = 4

// paramLabelWidth is the shared label column of one action, and whether the
// names are close enough in length to share one at all.
func (m *Model) paramLabelWidth(id config.ActionID) (int, bool) {
	longest, shortest := 0, 0
	for _, spec := range m.actionParamSpecs(id) {
		length := len([]rune(spec.Name))
		if length > longest {
			longest = length
		}
		if shortest == 0 || length < shortest {
			shortest = length
		}
	}
	return longest, longest-shortest <= paramLabelAlignSpread
}

func (m *Model) paramRowText(id config.ActionID, name string) string {
	for _, spec := range m.actionParamSpecs(id) {
		if spec.Name == name {
			return m.paramValueText(id, spec)
		}
	}
	return ""
}

// commandRowIndent is the gutter the command row is drawn in, and the two
// columns after it belong to the prompt.
const commandRowIndent = 10

// wrapCommandRow breaks a command that does not fit the list into continued
// lines, ending each one with a backslash the way a shell continues a long
// command. A single empty string keeps the row present when there is no
// command to show at all.
func (m *Model) wrapCommandRow(command string) []string {
	available := m.dashboardLeftWidth() - 2 - commandRowIndent
	if command == "" || available < 16 || lipgloss.Width(command) <= available {
		return []string{command}
	}
	parts := splitCommandLine(command, available-2)
	for index := range parts[:len(parts)-1] {
		parts[index] += ` \`
	}
	return parts
}

// splitCommandLine breaks a command at its argument boundaries, and only
// inside an argument when that argument alone is wider than the room there is.
// Nothing is inserted and nothing is dropped, so the lines joined back up are
// the command again.
func splitCommandLine(command string, width int) []string {
	var lines []string
	current := ""
	flush := func() {
		if current != "" {
			lines = append(lines, current)
			current = ""
		}
	}
	for _, token := range strings.Split(command, " ") {
		for lipgloss.Width(token) > width {
			flush()
			runes := []rune(token)
			lines = append(lines, string(runes[:width]))
			token = string(runes[width:])
		}
		switch {
		case current == "":
			current = token
		case lipgloss.Width(current)+1+lipgloss.Width(token) <= width:
			current += " " + token
		default:
			flush()
			current = token
		}
	}
	flush()
	if len(lines) == 0 {
		return []string{command}
	}
	return lines
}

// paramTemplate is the declared command before any value reaches it, which is
// what a blocked action can still show.
func (m *Model) paramTemplate(id config.ActionID) []string {
	action, exists := m.cfg.ResolveAction(id)
	if !exists {
		return nil
	}
	compiled, err := config.CompileAction(actionIDKey(id), action)
	if err != nil || compiled == nil {
		return nil
	}
	return compiled.Template()
}

// paramFallbackCommand keeps the command row informative when invalid values
// cannot produce either a preview or a static template. This happens when all
// parameters are disabled and one of them owns the executable itself.
func (m *Model) paramFallbackCommand(id config.ActionID) string {
	command := strings.Join(m.paramTemplate(id), " ")
	if command == "" {
		return disabledParamLabel
	}
	return command
}

// paramCommandProblem explains why the current values build no command.
func (m *Model) paramCommandProblem(id config.ActionID) string {
	render, err := m.renderActionParams(id)
	if err == nil {
		return ""
	}
	for _, spec := range m.actionParamSpecs(id) {
		if reason := render.Fields[spec.Name]; reason != "" {
			return spec.Name + ": " + reason
		}
	}
	return err.Error()
}

// paramRowBlocked reports whether this parameter is the one holding the command
// back, so its row can say so instead of only the command line.
func (m *Model) paramRowBlocked(id config.ActionID, name string) bool {
	return m.paramRowError(id, name) != ""
}

func (m *Model) paramRowError(id config.ActionID, name string) string {
	render, err := m.renderActionParams(id)
	if err != nil {
		return render.Fields[name]
	}
	return ""
}

// Marks for the two kinds of choice. A set of values is drawn as boxes and a
// single choice as dots, so the shape alone says whether more than one can be
// picked, without spelling it out in brackets.
const (
	markChecked   = "▣"
	markUnchecked = "▢"
	markChosen    = "◉"
	markUnchosen  = "○"
)

func (m *Model) paramValueMarker(id config.ActionID, name, candidate string) string {
	for _, spec := range m.actionParamSpecs(id) {
		if spec.Name != name {
			continue
		}
		selected := m.paramSelected(id, name, candidate)
		if spec.Control == actionparams.Checkbox {
			if selected {
				return markChecked
			}
			return markUnchecked
		}
		if selected {
			return markChosen
		}
		return markUnchosen
	}
	return markUnchosen
}

// actionParamRows expands one action's inline parameter tree.
func (m *Model) actionParamRows(id config.ActionID) []actionListRow {
	if !m.expandedActionParams[id] {
		return nil
	}
	specs := m.actionParamSpecs(id)
	if len(specs) == 0 {
		return nil
	}
	rows := make([]actionListRow, 0, len(specs)+1)
	// The command row stays even when the values cannot build one: a row that
	// disappears reads as "nothing to run here" instead of "fix this".
	render, err := m.renderActionParams(id)
	command, blocked := render.Preview, err != nil
	if blocked {
		command = m.paramFallbackCommand(id)
	}
	for index, part := range m.wrapCommandRow(command) {
		rows = append(rows, actionListRow{Kind: actionRowPreview, Action: id, Preview: part, Continued: index > 0, Blocked: blocked})
	}
	for _, spec := range specs {
		rows = append(rows, actionListRow{Kind: actionRowParam, Action: id, Param: spec.Name})
		if len(spec.Options) > 0 && m.expandedParamOptions[actionParamKey(id, spec.Name)] {
			for _, option := range spec.Options {
				rows = append(rows, actionListRow{Kind: actionRowParamValue, Action: id, Param: spec.Name, Value: option.Value})
			}
		}
	}
	return rows
}

// toggleFocusedParamRow reacts to Enter on a parameter or value row.
func (m *Model) toggleFocusedParamRow() (tea.Cmd, bool) {
	focus := m.focusedParam
	if focus == nil {
		return nil, false
	}
	for _, spec := range m.actionParamSpecs(focus.ID) {
		if spec.Name != focus.Name {
			continue
		}
		values := m.paramValuesFor(focus.ID)
		value := values[focus.Name]
		switch {
		case focus.IsValue:
			if spec.Control == actionparams.Checkbox {
				value.Strings = toggleListValue(value.Strings, focus.Value)
			} else {
				value.Text = focus.Value
			}
			value.Set = true
			values[focus.Name] = value
			m.actionParamValues[focus.ID] = values
		case spec.Control == actionparams.Checkbox && len(spec.Options) == 0:
			value.Bool = !value.Bool
			value.Set = true
			values[focus.Name] = value
			m.actionParamValues[focus.ID] = values
		case len(spec.Options) > 0:
			key := actionParamKey(focus.ID, focus.Name)
			m.expandedParamOptions[key] = !m.expandedParamOptions[key]
		default:
			return m.beginParamEdit(focus.ID, spec, value), true
		}
		return nil, true
	}
	return nil, false
}

func toggleListValue(values []string, candidate string) []string {
	for index, value := range values {
		if value == candidate {
			return append(values[:index:index], values[index+1:]...)
		}
	}
	return append(values, candidate)
}

// paramSelected reports whether a value is currently selected.
func (m *Model) paramSelected(id config.ActionID, name, candidate string) bool {
	value := m.paramValuesFor(id)[name]
	for _, selected := range value.Strings {
		if selected == candidate {
			return true
		}
	}
	// A single-choice control keeps its selection as text, so a radio or a
	// select would otherwise draw every one of its options as unchosen.
	return value.Text == candidate
}

// runParameterizedAction validates the inline form and starts the plan. A plan
// that needs confirmation opens the existing modal with the exact invocation.
func (m *Model) runParameterizedAction(id config.ActionID) tea.Cmd {
	render, err := m.renderActionParams(id)
	if err != nil {
		m.addNotification("action", id.Name+": "+firstParamReason(render, err), config.LogError)
		return nil
	}
	request := app.PlanRequest{Operation: "action", Action: id, Params: render.Params}
	// The ExecutePlan round trip reports confirmation_required with the bound
	// token; handleActionPlan opens the modal and records the exact request.
	return m.executeParamPlan(request, "")
}

func firstParamReason(render actionParamRender, err error) string {
	for _, reason := range render.Fields {
		return err.Error() + ": " + reason
	}
	return err.Error()
}

// executeParamPlan runs one plan round trip. A required confirmation is
// delivered back as a message so the caller can open the modal.
func (m *Model) executeParamPlan(request app.PlanRequest, token string) tea.Cmd {
	application, sessionGen := m.app, m.sessionGeneration
	id := request.Action
	release := retainRuntimeApplication(application)
	action, _ := m.cfg.ResolveAction(id)
	interactive := action.InteractiveEnabled()
	return func() tea.Msg {
		defer release()
		if interactive {
			plan, rendered, lease, err := application.AcquireInteractivePlan(context.Background(), request, token)
			return actionPlanMsg{id: id, request: request, result: app.OperationResult{Plan: plan}, action: rendered, lease: lease, err: err, sessionGen: sessionGen}
		}
		result, err := application.ExecutePlan(context.Background(), request, token)
		return actionPlanMsg{id: id, request: request, result: result, err: err, sessionGen: sessionGen}
	}
}

func (m *Model) handleActionPlan(msg actionPlanMsg) (tea.Model, tea.Cmd) {
	var required *app.ConfirmationRequiredError
	if errors.As(msg.err, &required) {
		pending := msg.request
		m.pendingParamRequest = &pending
		m.pendingParamToken = required.Plan.ConfirmationToken
		m.pendingAction = &msg.id
		m.pendingActionStop = false
		m.mode = ModeConfirmAction
		return m, nil
	}
	if msg.err == nil && msg.lease != "" {
		return m, m.runAcquiredInteractiveAction(msg.id, msg.action, msg.lease)
	}
	var result app.ActionResult
	if msg.result.ActionResult != nil {
		result = *msg.result.ActionResult
	}
	return m.handleActionResult(actionResultMsg{id: msg.id, result: result, err: msg.err, sessionGen: msg.sessionGen})
}

// newParamInput is the editor a text or number parameter is typed into. It is
// the same component the log search uses, so the cursor blinks and editing
// keys behave the way they do everywhere else.
func newParamInput() textinput.Model {
	input := textinput.New()
	input.Prompt = ""
	input.CharLimit = 0
	return input
}

// beginParamEdit opens the in-place editor on a free-text or number row.
func (m *Model) beginParamEdit(id config.ActionID, spec paramUISpec, value paramUIValue) tea.Cmd {
	m.paramEdit = &paramEditState{ID: id, Name: spec.Name}
	m.paramInput.SetValue(value.Text)
	m.paramInput.CursorEnd()
	m.paramInput.Width = paramEditorWidth
	m.mode = ModeParamEdit
	return m.paramInput.Focus()
}

// paramEditorWidth is the visible window of the in-place editor. A longer value
// scrolls inside it rather than pushing the row off the panel.
const paramEditorWidth = 28

// disabledParamLabel is what a switched-off parameter reads as.
const disabledParamLabel = "DISABLED"

// toggleParamDisabled switches one parameter out of the invocation and back.
func (m *Model) toggleParamDisabled(id config.ActionID, name string) bool {
	for _, spec := range m.actionParamSpecs(id) {
		if spec.Name != name {
			continue
		}
		values := m.paramValuesFor(id)
		value := values[name]
		value.Disabled = !value.Disabled
		values[name] = value
		m.actionParamValues[id] = values
		return true
	}
	return false
}

// stepFocusedParamValue answers left and right on a parameter row: it walks the
// values of a list, and counts a number up and down, by ten while shifted. A
// text field and a plain checkbox have nothing to walk.
func (m *Model) stepFocusedParamValue(step int, large bool) bool {
	focus := m.focusedParam
	if focus == nil || focus.Preview || focus.IsValue {
		return false
	}
	for _, spec := range m.actionParamSpecs(focus.ID) {
		if spec.Name != focus.Name {
			continue
		}
		return m.stepParamValue(focus.ID, spec, step, large)
	}
	return false
}

// stepParamValue is the shared move used by the tree and the form.
func (m *Model) stepParamValue(id config.ActionID, spec paramUISpec, step int, large bool) bool {
	values := m.paramValuesFor(id)
	value := values[spec.Name]
	if value.Disabled {
		return false
	}
	switch {
	case len(spec.Options) > 0 && spec.Control != actionparams.Checkbox:
		index := 0
		for position, option := range spec.Options {
			if option.Value == value.Text {
				index = position
			}
		}
		index = max(0, min(len(spec.Options)-1, index+step))
		value.Text, value.Set = spec.Options[index].Value, true
	case spec.Control == actionparams.Number:
		amount := int64(step)
		if large {
			amount *= 10
		}
		current, err := strconv.ParseInt(strings.TrimSpace(value.Text), 10, 64)
		if err != nil {
			current = 0
			if spec.Default != nil && spec.Default.Kind == actionparams.KindInt {
				current = spec.Default.Int
			}
			amount = 0
		}
		value.Text, value.Set = strconv.FormatInt(current+amount, 10), true
	default:
		return false
	}
	values[spec.Name] = value
	m.actionParamValues[id] = values
	return true
}

// paramRowFocused reports whether the cursor sits on a parameter or one of its
// values, as opposed to the action row or the command it builds.
func (m *Model) paramRowFocused() bool {
	return m.focusedParam != nil && !m.focusedParam.Preview
}

// paramEditing reports whether the editor is open on one parameter row.
func (m *Model) paramEditing(id config.ActionID, name string) bool {
	return m.paramEdit != nil && m.paramEdit.ID == id && m.paramEdit.Name == name
}

// handleParamEditKeys types into one parameter row. Enter keeps the value, Esc
// restores the one the row had, and nothing else leaves the row.
func (m *Model) handleParamEditKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	edit := m.paramEdit
	if edit == nil {
		m.mode = ModeNormal
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.closeParamEdit()
		return m, nil
	case tea.KeyEnter:
		values := m.paramValuesFor(edit.ID)
		value := values[edit.Name]
		value.Text = m.paramInput.Value()
		value.Set = true
		values[edit.Name] = value
		m.actionParamValues[edit.ID] = values
		m.closeParamEdit()
		return m, nil
	}
	// Some terminals report a space as its own key type, which the text input
	// ignores. Typing one has to insert a space like any other rune.
	if msg.Type == tea.KeySpace {
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}
	}
	var command tea.Cmd
	m.paramInput, command = m.paramInput.Update(msg)
	return m, command
}

func (m *Model) closeParamEdit() {
	m.paramInput.Blur()
	m.paramInput.SetValue("")
	m.paramEdit = nil
	m.mode = ModeNormal
}

// markFocusedParamRow answers Space on a parameter row. It only ever marks a
// value: a free-text parameter is typed with Enter, so Space stays a literal
// space there instead of opening an editor nobody asked for.
func (m *Model) markFocusedParamRow() bool {
	focus := m.focusedParam
	if focus == nil || focus.Preview {
		return false
	}
	for _, spec := range m.actionParamSpecs(focus.ID) {
		if spec.Name != focus.Name {
			continue
		}
		if focus.IsValue || len(spec.Options) > 0 || spec.Control == actionparams.Checkbox {
			_, handled := m.toggleFocusedParamRow()
			return handled
		}
		return false
	}
	return false
}
