package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/kranz-org/kranz/internal/actionparams"
	"github.com/kranz-org/kranz/internal/app"
	kranzcli "github.com/kranz-org/kranz/internal/cli"
	"github.com/kranz-org/kranz/internal/config"
	kranzruntime "github.com/kranz-org/kranz/internal/runtime"
	"github.com/kranz-org/kranz/internal/service"
)

// Actions are configured, so listing and describing them needs only the
// project. Running one is a runtime operation: the supervisor owns the
// execution slot, and running it anywhere else would let two callers run the
// same action at once.

type actionListEntry struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`
	OwnerKind   string `json:"owner_kind"`
	Description string `json:"description"`
	Interactive bool   `json:"interactive"`
	Confirm     bool   `json:"confirm"`
	Parameters  bool   `json:"parameters"`
}

func runActionList(options kranzcli.GlobalOptions, args []string, stdout io.Writer) error {
	formatter, args, err := extractRowFormat("actions", options.Output, args)
	if err != nil {
		return err
	}
	if len(args) > 1 {
		return &kranzcli.Error{Code: "invalid_arguments", Message: "actions accepts at most one owner", ExitCode: kranzcli.ExitUsage}
	}
	cfg, _, err := loadProject(options)
	if err != nil {
		return err
	}
	owner := ""
	if len(args) == 1 {
		owner = args[0]
	}
	entries := actionListEntries(cfg, owner)
	if owner != "" && len(entries) == 0 {
		return &kranzcli.Error{
			Code:     "owner_not_found",
			Message:  fmt.Sprintf("no service or action group named %q defines actions", owner),
			Hint:     "Run `kranz actions` to see every action this project defines.",
			ExitCode: kranzcli.ExitNotFound,
		}
	}
	if options.Output == kranzcli.OutputJSON {
		return kranzcli.WriteJSON(stdout, entries)
	}
	return writeActionList(stdout, entries, formatter)
}

func actionListEntries(cfg *config.Config, owner string) []actionListEntry {
	entries := make([]actionListEntry, 0)
	for _, id := range cfg.ActionIDs() {
		if owner != "" && id.Owner != owner {
			continue
		}
		action, ok := cfg.ResolveAction(id)
		if !ok {
			continue
		}
		entries = append(entries, actionListEntry{
			ID:          actionIDString(id),
			Owner:       id.Owner,
			OwnerKind:   string(id.OwnerKind),
			Description: action.Description,
			Interactive: action.Interactive != nil && *action.Interactive,
			Confirm:     action.Confirm != nil && *action.Confirm,
			Parameters:  len(action.Params) > 0,
		})
	}
	return entries
}

func writeActionList(stdout io.Writer, entries []actionListEntry, formatter *rowTemplate) error {
	if formatter != nil {
		rows := make([]map[string]any, 0, len(entries))
		for _, item := range entries {
			rows = append(rows, map[string]any{
				"Action": item.ID, "Owner": item.Owner, "Kind": item.OwnerKind,
				"Interactive": item.Interactive, "Confirm": item.Confirm, "Parameters": item.Parameters, "Description": item.Description,
			})
		}
		return formatter.write(stdout, map[string]any{
			"Action": "ACTION", "Owner": "OWNER", "Kind": "KIND",
			"Interactive": "INTERACTIVE", "Confirm": "CONFIRM", "Parameters": "PARAMETERS", "Description": "DESCRIPTION",
		}, rows)
	}
	if len(entries) == 0 {
		_, _ = fmt.Fprintln(stdout, "This project defines no actions.")
		return nil
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ACTION\tOWNER\tKIND\tINTERACTIVE\tCONFIRM\tPARAMETERS\tDESCRIPTION")
	for _, item := range entries {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%t\t%t\t%s\n", item.ID, item.Owner, item.OwnerKind, item.Interactive, item.Confirm, item.Parameters, orDash(item.Description))
	}
	return w.Flush()
}

// resolveActionID matches OWNER/ACTION against the configured actions rather
// than splitting the string and guessing an owner kind, so a service action and
// an action-group action of the same name stay distinguishable.
func resolveActionID(cfg *config.Config, reference string) (config.ActionID, config.Action, error) {
	var matches []config.ActionID
	for _, id := range cfg.ActionIDs() {
		if actionIDString(id) == reference {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 1:
		action, _ := cfg.ResolveAction(matches[0])
		return matches[0], action, nil
	case 0:
		hint := "Run `kranz actions` to see every action this project defines."
		if !strings.Contains(reference, "/") {
			hint = "Actions are named OWNER/ACTION."
			if ids := cfg.ActionIDs(); len(ids) > 0 {
				hint += fmt.Sprintf(" For example: `kranz actions info %s`.", actionIDString(ids[0]))
			} else {
				hint += " Run `kranz actions` to see what this project defines."
			}
		}
		return config.ActionID{}, config.Action{}, &kranzcli.Error{
			Code:     "action_not_found",
			Message:  fmt.Sprintf("action %q was not found", reference),
			Hint:     hint,
			ExitCode: kranzcli.ExitNotFound,
		}
	default:
		return config.ActionID{}, config.Action{}, &kranzcli.Error{
			Code:     "ambiguous_action",
			Message:  fmt.Sprintf("action %q is defined by more than one owner", reference),
			ExitCode: kranzcli.ExitConflict,
		}
	}
}

func runActionInfo(options kranzcli.GlobalOptions, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return &kranzcli.Error{Code: "invalid_arguments", Message: "action info takes exactly one OWNER/ACTION", ExitCode: kranzcli.ExitUsage}
	}
	cfg, _, err := loadProject(options)
	if err != nil {
		return err
	}
	id, action, err := resolveActionID(cfg, args[0])
	if err != nil {
		return err
	}
	interactive := action.Interactive != nil && *action.Interactive
	confirm := action.Confirm != nil && *action.Confirm
	execution := "shell"
	template := action.Command
	if len(action.Params) > 0 || len(action.Run) > 0 || len(action.Argv) > 0 {
		execution = "argv"
		template = strings.Join(actionTemplate(action), " ")
	}
	if options.Output == kranzcli.OutputJSON {
		return kranzcli.WriteJSON(stdout, struct {
			ID          string            `json:"id"`
			Owner       string            `json:"owner"`
			OwnerKind   string            `json:"owner_kind"`
			Description string            `json:"description"`
			Execution   string            `json:"execution"`
			Command     string            `json:"command"`
			Dir         string            `json:"dir"`
			Timeout     string            `json:"timeout"`
			Interactive bool              `json:"interactive"`
			Confirm     bool              `json:"confirm"`
			Params      []actionParamInfo `json:"params,omitempty"`
			Schema      map[string]any    `json:"params_schema,omitempty"`
		}{ID: actionIDString(id), Owner: id.Owner, OwnerKind: string(id.OwnerKind), Description: action.Description,
			Execution: execution, Command: template, Dir: action.Dir, Timeout: action.Timeout.String(), Interactive: interactive, Confirm: confirm,
			Params: actionParamInfos(action), Schema: actionParamsSchema(id, action)})
	}
	_, _ = fmt.Fprintf(stdout, "Action:      %s\n", actionIDString(id))
	_, _ = fmt.Fprintf(stdout, "Owner:       %s (%s)\n", id.Owner, id.OwnerKind)
	if action.Description != "" {
		_, _ = fmt.Fprintf(stdout, "Description: %s\n", action.Description)
	}
	_, _ = fmt.Fprintf(stdout, "Execution:   %s\n", execution)
	_, _ = fmt.Fprintf(stdout, "Command:     %s\n", orDash(template))
	_, _ = fmt.Fprintf(stdout, "Directory:   %s\n", orDash(action.Dir))
	if action.Timeout > 0 {
		_, _ = fmt.Fprintf(stdout, "Timeout:     %s\n", action.Timeout)
	}
	if infos := actionParamInfos(action); len(infos) > 0 {
		_, _ = fmt.Fprintln(stdout, "Parameters:")
		w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		for _, info := range infos {
			note := info.Prompt
			if info.Constraint != "" {
				if note != "" {
					note += " · "
				}
				note += info.Constraint
			}
			_, _ = fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", info.Name, info.describe(), info.defaultText(), note)
		}
		_ = w.Flush()
	}
	_, _ = fmt.Fprintf(stdout, "Interactive: %t\n", interactive)
	_, _ = fmt.Fprintf(stdout, "Confirm:     %t\n", confirm)
	// A statically unconfirmed action can still ask once a value turns it
	// destructive, so the values that do are named rather than discovered at
	// the confirmation prompt.
	if triggers := actionConfirmTriggers(action); len(triggers) > 0 {
		_, _ = fmt.Fprintln(stdout, "Values that ask:")
		w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		for _, trigger := range triggers {
			_, _ = fmt.Fprintf(w, "  %s\t%s\n", trigger.value, trigger.reason)
		}
		_ = w.Flush()
	}
	return nil
}

// actionConfirmTrigger is one value whose selection requires confirmation.
type actionConfirmTrigger struct {
	value  string
	reason string
}

func actionConfirmTriggers(action config.Action) []actionConfirmTrigger {
	var triggers []actionConfirmTrigger
	for _, info := range actionParamInfos(action) {
		if info.Confirm != "" {
			triggers = append(triggers, actionConfirmTrigger{value: info.Name, reason: info.Confirm})
		}
		for _, option := range info.Options {
			if reason := info.ConfirmOptions[option]; reason != "" {
				triggers = append(triggers, actionConfirmTrigger{value: info.Name + "=" + option, reason: reason})
			}
		}
	}
	return triggers
}

// actionParamInfo is one parameter declaration as reported by actions info.
type actionParamInfo struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Options  []string `json:"options,omitempty"`
	Default  any      `json:"default,omitempty"`
	Required bool     `json:"required"`
	Optional bool     `json:"optional,omitempty"`
	Prompt   string   `json:"description,omitempty"`
	// Confirm is the reason this parameter asks for confirmation when it is
	// switched on, and ConfirmOptions the same per selectable option.
	Confirm        string            `json:"confirm,omitempty"`
	ConfirmOptions map[string]string `json:"confirm_options,omitempty"`
	// Constraint is what a value has to satisfy, in the same words the MCP
	// specs and the TUI hints use.
	Constraint string `json:"constraint,omitempty"`
}

func actionParamInfos(action config.Action) []actionParamInfo {
	compiled, _ := config.CompileAction("", action)
	order := action.ParamOrder
	if len(order) == 0 {
		for name := range action.Params {
			order = append(order, name)
		}
		sort.Strings(order)
	}
	infos := make([]actionParamInfo, 0, len(order))
	for _, name := range order {
		param, exists := action.Params[name]
		if !exists {
			continue
		}
		info := actionParamInfo{Name: name, Type: param.Type, Default: param.Default, Required: param.Required, Optional: param.Optional, Prompt: param.Prompt, Confirm: param.Confirm}
		if compiled != nil {
			info.Constraint = compiled.Params[name].Constraint()
		}
		for _, option := range param.Options {
			info.Options = append(info.Options, option.Value)
			if option.Confirm != "" {
				if info.ConfirmOptions == nil {
					info.ConfirmOptions = map[string]string{}
				}
				info.ConfirmOptions[option.Value] = option.Confirm
			}
		}
		infos = append(infos, info)
	}
	return infos
}

func (i actionParamInfo) describe() string {
	switch {
	case len(i.Options) > 0:
		return "[" + strings.Join(i.Options, "|") + "]"
	case i.Type == "number":
		return "int"
	default:
		return i.Type
	}
}

func (i actionParamInfo) defaultText() string {
	if i.Default != nil {
		return fmt.Sprintf("default %v", i.Default)
	}
	if i.Required {
		return "required"
	}
	return "optional"
}

func actionTemplate(action config.Action) []string {
	if len(action.Argv) > 0 {
		return action.Argv
	}
	return action.Run
}

func actionParamsSchema(id config.ActionID, action config.Action) map[string]any {
	compiled, err := config.CompileAction(actionIDString(id), action)
	if err != nil || compiled == nil {
		return nil
	}
	return actionparams.JSONSchema(compiled)
}

func runActionRun(options kranzcli.GlobalOptions, args []string, stdout io.Writer) error {
	confirmed := false
	identities := make([]string, 0, 1)
	paramFlags := make([]string, 0)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--confirm":
			confirmed = true
		case arg == "--param":
			if index+1 >= len(args) {
				return &kranzcli.Error{Code: "invalid_arguments", Message: "--param requires NAME=VALUE", ExitCode: kranzcli.ExitUsage}
			}
			index++
			paramFlags = append(paramFlags, args[index])
		case strings.HasPrefix(arg, "--param="):
			paramFlags = append(paramFlags, strings.TrimPrefix(arg, "--param="))
		case arg == "--no-param":
			if index+1 >= len(args) {
				return &kranzcli.Error{Code: "invalid_arguments", Message: "--no-param requires NAME", ExitCode: kranzcli.ExitUsage}
			}
			index++
			// A name with no value leaves the parameter out entirely, which is
			// not what omitting the flag does: that falls back to the default.
			paramFlags = append(paramFlags, args[index]+omittedParamSuffix)
		case strings.HasPrefix(arg, "--no-param="):
			paramFlags = append(paramFlags, strings.TrimPrefix(arg, "--no-param=")+omittedParamSuffix)
		case strings.HasPrefix(arg, "-"):
			return &kranzcli.Error{Code: "unknown_option", Message: fmt.Sprintf("unknown actions run option %q", arg), Hint: "actions run accepts one OWNER/ACTION, --param NAME=VALUE, --no-param NAME, and --confirm.", ExitCode: kranzcli.ExitUsage}
		default:
			identities = append(identities, arg)
		}
	}
	if len(identities) != 1 {
		return &kranzcli.Error{Code: "invalid_arguments", Message: "actions run takes exactly one OWNER/ACTION", ExitCode: kranzcli.ExitUsage}
	}
	identity := identities[0]
	// In the ordinary project-local workflow, reject an interactive action
	// before looking for a runtime so the user gets the useful TUI instruction.
	// With an explicit -p, the selected runtime is authoritative and may belong
	// to a different directory, so its configuration is resolved after dialing.
	if options.Project == "" {
		cfg, _, err := loadProject(options)
		if err != nil {
			return err
		}
		id, action, err := resolveActionID(cfg, identity)
		if err != nil {
			return err
		}
		if err := rejectInteractiveAction(id, action); err != nil {
			return err
		}
		// Validate parameters locally so an invalid value is reported with
		// field details before a runtime is dialed. The runtime repeats the
		// same check as the authoritative boundary.
		if _, err := encodeActionParams(id, action, paramFlags); err != nil {
			return err
		}
	}

	record, err := resolveSession(options)
	if err != nil {
		return err
	}
	client, err := kranzruntime.DialContext(context.Background(), record.Socket, version)
	if err != nil {
		return classifyRuntimeError(err)
	}
	defer func() { _ = client.Close() }()

	id, action, err := resolveActionID(client.Config(), identity)
	if err != nil {
		return err
	}
	if err := rejectInteractiveAction(id, action); err != nil {
		return err
	}

	params, err := encodeActionParams(id, action, paramFlags)
	if err != nil {
		return err
	}
	operation, runErr := executePlanWithApproval(client, app.PlanRequest{Operation: "action", Action: id, Params: params}, confirmed)
	if operation.ActionResult == nil {
		return classifyRuntimeError(runErr)
	}
	result := *operation.ActionResult

	if options.Output == kranzcli.OutputJSON {
		if err := kranzcli.WriteJSON(stdout, struct {
			ID             string         `json:"id"`
			Run            uint32         `json:"run"`
			Status         string         `json:"status"`
			ExitCode       int            `json:"exit_code"`
			Duration       string         `json:"duration"`
			Stdout         []string       `json:"stdout"`
			Stderr         []string       `json:"stderr"`
			Error          string         `json:"error"`
			Params         map[string]any `json:"params,omitempty"`
			CommandPreview string         `json:"command_preview,omitempty"`
		}{ID: actionIDString(id), Run: result.Run, Status: result.Status.String(), ExitCode: result.ExitCode, Duration: result.Duration.String(),
			Stdout: actionOutputLines(result.Stdout), Stderr: actionOutputLines(result.Stderr), Error: result.Error,
			Params: result.Params, CommandPreview: result.CommandPreview}); err != nil {
			return err
		}
	} else {
		for _, line := range actionOutputLines(result.Stdout) {
			_, _ = fmt.Fprintln(stdout, line)
		}
		for _, line := range actionOutputLines(result.Stderr) {
			_, _ = fmt.Fprintln(stdout, line)
		}
		_, _ = fmt.Fprintf(stdout, "%s#%d %s in %s (exit %d)\n", actionIDString(id), result.Run, result.Status, result.Duration.Round(1e6), result.ExitCode)
		if result.CommandPreview != "" {
			_, _ = fmt.Fprintf(stdout, "command: %s\n", result.CommandPreview)
		}
	}

	// A failed action has to fail the command, or a script that runs a
	// migration through Kranz cannot tell that the migration did not apply.
	// The outcome is already reported above, so JSON output carries the exit
	// code alone rather than a second envelope contradicting the first.
	if result.Status != service.ActionSucceeded {
		if options.Output == kranzcli.OutputJSON {
			return requestedExitError{code: kranzcli.ExitInternal}
		}
		return &kranzcli.Error{
			Code:     "action_failed",
			Message:  fmt.Sprintf("action %q %s", actionIDString(id), result.Status),
			ExitCode: kranzcli.ExitInternal,
		}
	}
	return nil
}

// An interactive action needs the real terminal handed to it under a
// supervisor lease. Refusing it plainly is better than running it with no
// terminal, where it would block forever on a prompt nobody can answer.
func rejectInteractiveAction(id config.ActionID, action config.Action) error {
	if action.Interactive == nil || !*action.Interactive {
		return nil
	}
	return &kranzcli.Error{
		Code:     "interactive_action",
		Message:  fmt.Sprintf("action %q is interactive and cannot be run by this command yet", actionIDString(id)),
		Hint:     "Run it from the TUI with `kranz attach`.",
		ExitCode: kranzcli.ExitUsage,
	}
}

// encodeActionParams validates --param flags locally and encodes them as a
// JSON object typed by each parameter's control. The runtime still revalidates
// the same values as the authoritative boundary.
// omittedParamSuffix marks a --no-param flag inside the collected list. It
// cannot collide with a value because a name never contains '='.
const omittedParamSuffix = "=\x00omit"

func encodeActionParams(id config.ActionID, action config.Action, flags []string) (map[string]json.RawMessage, error) {
	compiled, err := config.CompileAction(actionIDString(id), action)
	if err != nil {
		return nil, &kranzcli.Error{Code: "invalid_arguments", Message: err.Error(), ExitCode: kranzcli.ExitUsage}
	}
	if len(flags) == 0 {
		if compiled == nil || !compiled.HasParams() {
			return nil, nil
		}
		// A parameterized action always travels with an explicit object, even
		// an empty one: its absence is how the runtime recognizes a client
		// that predates parameters and would run on defaults it never showed.
		return map[string]json.RawMessage{}, nil
	}
	if compiled == nil || !compiled.HasParams() {
		return nil, &kranzcli.Error{Code: "invalid_arguments", Message: fmt.Sprintf("action %q does not accept parameters", actionIDString(id)), ExitCode: kranzcli.ExitUsage}
	}
	raw := actionparams.RawValues{}
	seen := map[string]bool{}
	var fields []actionparams.FieldError
	for _, flag := range flags {
		if name, omitted := strings.CutSuffix(flag, omittedParamSuffix); omitted {
			if _, exists := compiled.Params[name]; !exists {
				fields = append(fields, actionparams.FieldError{Name: name, Reason: "unknown parameter"})
				continue
			}
			if seen[name] {
				fields = append(fields, actionparams.FieldError{Name: name, Reason: "given more than once"})
				continue
			}
			seen[name] = true
			raw[name] = actionparams.Raw{Omitted: true}
			continue
		}
		name, value, ok := strings.Cut(flag, "=")
		if !ok || name == "" {
			fields = append(fields, actionparams.FieldError{Name: name, Reason: "expected NAME=VALUE"})
			continue
		}
		param, exists := compiled.Params[name]
		if !exists {
			fields = append(fields, actionparams.FieldError{Name: name, Reason: "unknown parameter"})
			continue
		}
		group := param.Control == actionparams.Checkbox && len(param.Options) > 0
		if seen[name] && !group {
			fields = append(fields, actionparams.FieldError{Name: name, Reason: "parameter was repeated"})
			continue
		}
		seen[name] = true
		switch {
		case param.Control == actionparams.Checkbox && !group:
			if value != "true" && value != "false" {
				fields = append(fields, actionparams.FieldError{Name: name, Reason: "expected true or false"})
				continue
			}
			flagValue := value == "true"
			raw[name] = actionparams.Raw{Present: true, Bool: &flagValue}
		case group:
			existing := raw[name]
			existing.Present = true
			existing.Strings = append(existing.Strings, value)
			raw[name] = existing
		default:
			raw[name] = actionparams.Raw{Present: true, Text: value}
		}
	}
	if len(fields) > 0 {
		return nil, invalidArgumentsError(actionparams.CombinedError(fields))
	}
	invocation, err := actionparams.Normalize(compiled, raw)
	if err != nil {
		return nil, invalidArgumentsError(err)
	}
	native := actionparams.NativeValues(invocation)
	encoded := make(map[string]json.RawMessage, len(native))
	for name, value := range native {
		payload, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return nil, &kranzcli.Error{Code: "invalid_arguments", Message: fmt.Sprintf("parameter %q could not be encoded", name), ExitCode: kranzcli.ExitUsage}
		}
		encoded[name] = payload
	}
	// An omitted parameter travels as an explicit null, which is how every
	// surface spells "leave this one out" rather than "use its default".
	for name, value := range raw {
		if value.Omitted {
			encoded[name] = json.RawMessage("null")
		}
	}
	return encoded, nil
}

func invalidArgumentsError(err error) error {
	var paramErr *actionparams.Error
	if !errors.As(err, &paramErr) {
		return &kranzcli.Error{Code: "invalid_arguments", Message: err.Error(), ExitCode: kranzcli.ExitUsage}
	}
	fields := paramErr.FieldMap()
	lines := make([]string, 0, len(fields))
	for _, field := range paramErr.Fields {
		lines = append(lines, fmt.Sprintf("  %s: %s", field.Name, field.Reason))
	}
	message := "action parameters are invalid"
	if len(lines) > 0 {
		message += "\n" + strings.Join(lines, "\n")
	}
	return &kranzcli.Error{Code: "invalid_arguments", Message: message, Details: map[string]any{"fields": fields}, Hint: "Run `kranz actions info OWNER/ACTION` to inspect accepted parameters.", ExitCode: kranzcli.ExitUsage}
}

// actionOutputLines turns captured output into one entry per line. A pipe hands
// Kranz whatever chunk it read, so a JSON consumer counting array elements and
// a human counting printed lines would otherwise disagree.
func actionOutputLines(chunks []string) []string {
	lines := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		for _, line := range strings.Split(strings.TrimSuffix(chunk, "\n"), "\n") {
			lines = append(lines, strings.TrimSuffix(line, "\r"))
		}
	}
	return lines
}
