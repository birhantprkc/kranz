package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kranz-org/kranz/internal/actionparams"
	"github.com/kranz-org/kranz/internal/config"
	"github.com/kranz-org/kranz/internal/service"
)

const (
	// OperationSchemaVersion 2 adds parameterized action fields to the plan.
	OperationSchemaVersion          = 2
	maxConfirmationTokensPerPurpose = 256
)

type PlanRequest struct {
	Operation           string          `json:"operation"`
	Selectors           []string        `json:"selectors,omitempty"`
	IncludeDependencies bool            `json:"include_dependencies,omitempty"`
	Action              config.ActionID `json:"action,omitempty"`
	// Params is nil when the caller sent no parameter object at all, which is
	// distinct from an empty object and is how a pre-parameters client is
	// recognized. It is deliberately not omitempty.
	Params map[string]json.RawMessage `json:"params"`
}

// InvalidArgumentsError reports invalid action parameters with per-field
// reasons. Delivery surfaces translate it to the invalid_arguments code.
type InvalidArgumentsError struct {
	Action  string
	Message string
	Fields  map[string]string
}

func (e *InvalidArgumentsError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "action parameters are invalid"
}

type OperationWave struct {
	Wave     int      `json:"wave"`
	Services []string `json:"services"`
}

type OperationPlan struct {
	SchemaVersion        int             `json:"schema_version"`
	SessionID            string          `json:"session_id"`
	Generation           uint64          `json:"generation"`
	Operation            string          `json:"operation"`
	Selectors            []string        `json:"selectors,omitempty"`
	Targets              []string        `json:"targets"`
	Waves                []OperationWave `json:"waves,omitempty"`
	Action               string          `json:"action,omitempty"`
	IncludeDependencies  bool            `json:"include_dependencies,omitempty"`
	RequiresConfirmation bool            `json:"requires_confirmation"`
	Fingerprint          string          `json:"fingerprint"`
	ConfirmationToken    string          `json:"confirmation_token,omitempty"`
	// Params holds the normalized parameter values and CommandPreview the
	// display-only preview of the exact invocation the plan will execute.
	Params         map[string]any `json:"params,omitempty"`
	CommandPreview string         `json:"command_preview,omitempty"`
}

type OperationResult struct {
	Plan         OperationPlan `json:"plan"`
	ActionResult *ActionResult `json:"action_result,omitempty"`
}

// OperationExecutionError preserves the resolved result when an operation
// reached execution but failed. IPC clients can therefore return an action's
// stable run identity and terminal snapshot together with its causal error.
type OperationExecutionError struct {
	Result OperationResult
	Cause  error
}

func (e *OperationExecutionError) Error() string { return e.Cause.Error() }
func (e *OperationExecutionError) Unwrap() error { return e.Cause }

type confirmationRecord struct {
	generation  uint64
	sessionID   string
	fingerprint string
	purpose     confirmationPurpose
	sequence    uint64
}

type confirmationPurpose uint8

const (
	confirmationPreview confirmationPurpose = iota
	confirmationExecution
)

type ConfirmationRequiredError struct{ Plan OperationPlan }

func (e *ConfirmationRequiredError) Error() string { return "operation requires confirmation" }

type ConfirmationError struct{ Code, Message string }

func (e *ConfirmationError) Error() string { return e.Message }

// ResolveServiceSelectors is the one application-level meaning of a service
// selector: exact service name first, then a case-insensitive tag.
func ResolveServiceSelectors(cfg *config.Config, selectors []string) ([]string, error) {
	selected := map[string]bool{}
	for _, selector := range selectors {
		if _, ok := cfg.Services[selector]; ok {
			selected[selector] = true
			continue
		}
		var sourceMatches []string
		matchedID := false
		for _, name := range cfg.ServiceNames() {
			if metadata, ok := cfg.ServiceMetadata[name]; ok {
				if metadata.ID == selector {
					selected[name] = true
					matchedID = true
					break
				}
				if metadata.SourceName == selector {
					sourceMatches = append(sourceMatches, name)
				}
			}
		}
		if matchedID {
			continue
		}
		if len(sourceMatches) == 1 {
			selected[sourceMatches[0]] = true
			continue
		}
		if len(sourceMatches) > 1 {
			return nil, &LogQueryError{Code: "selector_ambiguous", Selector: selector, Message: fmt.Sprintf("service name %q is ambiguous", selector), Hint: "Choose one of: " + strings.Join(sourceMatches, ", ")}
		}
		matched := false
		for _, name := range cfg.ServiceOrder {
			if slices.ContainsFunc(cfg.Services[name].Tags, func(tag string) bool { return strings.EqualFold(tag, selector) }) {
				selected[name], matched = true, true
			}
		}
		if !matched {
			return nil, &LogQueryError{Code: "selector_not_found", Selector: selector, Message: fmt.Sprintf("service or tag %q was not found", selector), Hint: "List services and tags before retrying."}
		}
	}
	names := make([]string, 0, len(selected))
	for _, name := range cfg.ServiceOrder {
		if selected[name] {
			names = append(names, name)
		}
	}
	return names, nil
}

func (l *Local) Plan(request PlanRequest) (OperationPlan, error) {
	plan, _, err := l.resolvePlan(request)
	if err != nil {
		return plan, err
	}
	if plan.RequiresConfirmation {
		plan.ConfirmationToken = l.issueConfirmation(plan, confirmationPreview)
	}
	return plan, nil
}

// resolvePlan renders the plan and, for an action, the immutable definition
// that plan will execute. The caller carries that definition through
// confirmation so nothing re-resolves the action by ID afterwards.
func (l *Local) resolvePlan(request PlanRequest) (OperationPlan, *resolvedAction, error) {
	project := l.Project()
	var resolved *resolvedAction
	plan := OperationPlan{SchemaVersion: OperationSchemaVersion, SessionID: project.SessionID, Generation: project.Generation, Operation: request.Operation, Selectors: append([]string(nil), request.Selectors...), IncludeDependencies: request.IncludeDependencies, Targets: []string{}}
	switch request.Operation {
	case "start", "stop", "restart":
		selectorConfig := l.Config()
		if request.Operation == "stop" || request.Operation == "restart" {
			selectorConfig = l.manager.Config()
		}
		selectors := request.Selectors
		if len(selectors) == 0 {
			selectors = append([]string(nil), selectorConfig.ServiceOrder...)
		}
		names, err := ResolveServiceSelectors(selectorConfig, selectors)
		if err != nil {
			return plan, nil, err
		}
		if request.Operation == "start" && request.IncludeDependencies {
			closure, err := l.manager.StartDependencyClosure(names)
			if err != nil {
				return plan, nil, err
			}
			order, err := service.TopologicalOrder(l.Config())
			if err != nil {
				return plan, nil, err
			}
			for _, name := range order {
				if closure[name] {
					plan.Targets = append(plan.Targets, name)
				}
			}
			// A plan with no targets has no waves: DependencyLevels answers a
			// request for the levels of nothing with one empty level, and a
			// reader counting waves would take that for work to do.
			if len(plan.Targets) > 0 {
				for index, wave := range service.DependencyLevels(l.Config(), plan.Targets) {
					plan.Waves = append(plan.Waves, OperationWave{Wave: index + 1, Services: wave})
				}
			}
		} else if request.Operation == "start" {
			plan.Targets = names
		} else {
			seen := map[string]bool{}
			for _, name := range names {
				for _, affected := range l.AffectedServices(name) {
					if !seen[affected] {
						seen[affected] = true
						plan.Targets = append(plan.Targets, affected)
					}
				}
			}
		}
		if request.Operation == "start" {
			plan.RequiresConfirmation = len(l.StartConfirmationNames(names, request.IncludeDependencies)) > 0
		} else {
			plan.RequiresConfirmation = l.RequiresStopConfirmation(plan.Targets)
		}
	case "action":
		action, ok := l.Config().ResolveAction(request.Action)
		if !ok {
			return plan, nil, fmt.Errorf("%w: %s/%s", ErrActionNotFound, request.Action.Owner, request.Action.Name)
		}
		plan.Action = request.Action.Owner + "/" + request.Action.Name
		plan.Targets = []string{plan.Action}
		execution, err := l.resolveActionDefinition(request.Action, action, request.Params)
		if err != nil {
			return plan, nil, err
		}
		resolved = &execution
		plan.Params = execution.values
		plan.CommandPreview = execution.preview
		plan.RequiresConfirmation = execution.confirm
	default:
		return plan, nil, &ConfirmationError{Code: "invalid_operation", Message: fmt.Sprintf("unsupported operation %q", request.Operation)}
	}
	plan.Fingerprint = operationFingerprint(plan)
	return plan, resolved, nil
}

func (l *Local) ExecutePlan(ctx context.Context, request PlanRequest, token string) (OperationResult, error) {
	plan, resolved, err := l.resolvePlan(request)
	if err != nil {
		return OperationResult{}, err
	}
	if token != "" {
		// A presented token is always validated against the plan that would
		// run, including when that plan no longer requires confirming: the
		// caller confirmed a specific resolved plan, and a plan that changed
		// under it is exactly what confirmation exists to catch.
		if err := l.consumeConfirmation(token, plan); err != nil {
			return OperationResult{Plan: plan}, err
		}
	} else if plan.RequiresConfirmation {
		plan.ConfirmationToken = l.issueConfirmation(plan, confirmationExecution)
		return OperationResult{Plan: plan}, &ConfirmationRequiredError{Plan: plan}
	}
	result := OperationResult{Plan: plan}
	switch request.Operation {
	case "start":
		if request.IncludeDependencies {
			err = l.StartServicesContext(ctx, plan.Targets)
		} else {
			err = l.ForceStartServicesContext(ctx, plan.Targets)
		}
	case "stop":
		err = l.StopServices(plan.Targets)
	case "restart":
		err = l.RestartServicesContext(ctx, plan.Targets)
	case "action":
		if resolved == nil {
			err = fmt.Errorf("%w: %s/%s", ErrActionNotFound, request.Action.Owner, request.Action.Name)
			break
		}
		var actionResult ActionResult
		// A delivery request is only waiting for an action result; it does not
		// own the action lifetime. Cancellation is the separate CancelAction
		// application operation, so an MCP/IPC disconnect cannot kill a job.
		// The immutable resolved definition is executed directly, so a reload
		// after confirmation can never substitute a different action.
		actionResult, err = l.RunActionDefinition(context.WithoutCancel(ctx), request.Action, resolved.definition)
		result.ActionResult = &actionResult
	}
	return result, err
}

// resolvedAction is one plan-bound rendering of an action definition.
type resolvedAction struct {
	definition config.Action
	values     map[string]any
	preview    string
	confirm    bool
}

// resolveActionDefinition validates raw parameter values and renders the
// immutable definition the plan will execute.
func (l *Local) resolveActionDefinition(id config.ActionID, action config.Action, params map[string]json.RawMessage) (resolvedAction, error) {
	compiled, err := config.CompileAction(actionKey(id), action)
	if err != nil {
		return resolvedAction{}, &InvalidArgumentsError{Action: actionKey(id), Message: err.Error()}
	}
	if compiled == nil {
		if len(params) > 0 {
			return resolvedAction{}, &InvalidArgumentsError{Action: actionKey(id), Message: fmt.Sprintf("action %s does not accept parameters", actionKey(id))}
		}
		return resolvedAction{definition: action, confirm: action.ConfirmationRequired()}, nil
	}
	if params == nil {
		return resolvedAction{}, &InvalidArgumentsError{Action: actionKey(id), Message: fmt.Sprintf("action %s takes parameters and this client did not send any: upgrade the client, or pass an explicit parameter object", actionKey(id))}
	}
	raw, err := actionparams.DecodeJSON(compiled, params)
	if err != nil {
		return resolvedAction{}, actionParamsError(actionKey(id), err)
	}
	invocation, err := actionparams.Normalize(compiled, raw)
	if err != nil {
		return resolvedAction{}, actionParamsError(actionKey(id), err)
	}
	rendered, err := actionparams.Render(compiled, invocation)
	if err != nil {
		return resolvedAction{}, actionParamsError(actionKey(id), err)
	}
	values := actionparams.NativeValues(invocation)
	return resolvedAction{
		definition: action.RenderedAction(rendered, values),
		values:     values,
		preview:    rendered.Preview,
		confirm:    rendered.Confirm,
	}, nil
}

func actionParamsError(action string, err error) error {
	var paramErr *actionparams.Error
	if errors.As(err, &paramErr) {
		return &InvalidArgumentsError{Action: action, Message: paramErr.Message, Fields: paramErr.FieldMap()}
	}
	return err
}

// actionKey names an action for parameter diagnostics and invocation keys.
func actionKey(id config.ActionID) string {
	return string(id.OwnerKind) + "/" + id.Owner + "/" + id.Name
}

func operationFingerprint(plan OperationPlan) string {
	copy := plan
	copy.Fingerprint, copy.ConfirmationToken = "", ""
	payload, _ := json.Marshal(copy)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (l *Local) issueConfirmation(plan OperationPlan, purpose confirmationPurpose) string {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		panic(fmt.Sprintf("generate confirmation token: %v", err))
	}
	token := hex.EncodeToString(bytes)
	l.confirmMu.Lock()
	defer l.confirmMu.Unlock()
	oldest, count := l.oldestConfirmationLocked(purpose)
	if count >= maxConfirmationTokensPerPurpose {
		delete(l.confirmations, oldest)
	}
	l.nextConfirmationSequence++
	l.confirmations[token] = confirmationRecord{generation: plan.Generation, sessionID: plan.SessionID, fingerprint: plan.Fingerprint, purpose: purpose, sequence: l.nextConfirmationSequence}
	return token
}

func (l *Local) oldestConfirmationLocked(purpose confirmationPurpose) (string, int) {
	var oldestToken string
	var oldestSequence uint64
	count := 0
	for token, record := range l.confirmations {
		if record.purpose != purpose {
			continue
		}
		count++
		if oldestToken != "" && record.sequence >= oldestSequence {
			continue
		}
		oldestToken = token
		oldestSequence = record.sequence
	}
	return oldestToken, count
}

func (l *Local) consumeConfirmation(token string, plan OperationPlan) error {
	l.confirmMu.Lock()
	record, ok := l.confirmations[token]
	delete(l.confirmations, token)
	l.confirmMu.Unlock()
	if !ok {
		return &ConfirmationError{Code: "confirmation_expired", Message: "confirmation token is unknown, expired, or already used"}
	}
	if record.sessionID != plan.SessionID || record.generation != plan.Generation {
		return &ConfirmationError{Code: "confirmation_expired", Message: "confirmation token belongs to another session or config generation"}
	}
	if record.fingerprint != plan.Fingerprint {
		return &ConfirmationError{Code: "confirmation_plan_changed", Message: "resolved operation plan changed after confirmation"}
	}
	return nil
}

func (l *Local) invalidateConfirmations() {
	l.confirmMu.Lock()
	l.confirmations = map[string]confirmationRecord{}
	l.nextConfirmationSequence = 0
	l.confirmMu.Unlock()
}

type WaitRequest struct {
	Selectors []string `json:"selectors"`
	Condition string   `json:"condition"`
	// Timeout bounds the wait inside the runtime. A delivery adapter that
	// instead relied only on its own request deadline reported a timeout as a
	// cancellation, because the transport gave up before the runtime could say
	// what it had still been waiting for.
	Timeout time.Duration `json:"timeout,omitempty"`
}

type WaitResult struct {
	Condition  string             `json:"condition"`
	Generation uint64             `json:"generation"`
	Services   []*ServiceSnapshot `json:"services"`
	// Cursor is the journal sequence at which the wait finished. A caller that
	// passes the cursor it held before the wait to Changes reads exactly what
	// happened while it was waiting, rather than only where things ended up.
	Cursor uint64 `json:"cursor"`
}

type WaitError struct {
	Code, Message string
	Services      []*ServiceSnapshot
}

func (e *WaitError) Error() string { return e.Message }

func (l *Local) Wait(ctx context.Context, request WaitRequest) (WaitResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !slices.Contains([]string{"ready", "running", "stopped", "healthy", "unhealthy"}, request.Condition) {
		return WaitResult{}, &WaitError{Code: "invalid_condition", Message: fmt.Sprintf("unsupported wait condition %q", request.Condition)}
	}
	names, err := ResolveServiceSelectors(l.Config(), request.Selectors)
	if err != nil {
		return WaitResult{}, err
	}
	if len(names) == 0 {
		return WaitResult{}, &WaitError{Code: "selector_required", Message: "wait requires at least one service or tag selector"}
	}
	if request.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, request.Timeout)
		defer cancel()
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshots := make([]*ServiceSnapshot, 0, len(names))
		matched := true
		terminal := false
		blocked := false
		for _, name := range names {
			snapshot, ok := l.Service(name)
			if !ok {
				return WaitResult{}, &WaitError{Code: "selector_not_found", Message: fmt.Sprintf("service %q disappeared while waiting", name)}
			}
			snapshots = append(snapshots, snapshot)
			matched = matched && serviceCondition(snapshot, request.Condition)
			terminal = terminal || snapshot.State.Completed && snapshot.State.ExitCode != 0 && snapshot.State.Status == config.StatusStopped
			// One meaning of "blocked": the same derived cause status reports,
			// so a wait cannot disagree with what the snapshot says.
			blocked = blocked || snapshot.State.Cause != nil && snapshot.State.Cause.Type == "dependency_failed"
		}
		result := WaitResult{Condition: request.Condition, Generation: l.Project().Generation, Services: snapshots, Cursor: l.manager.Journal().Latest()}
		if matched {
			return result, nil
		}
		if blocked {
			return result, &WaitError{Code: "dependency_blocked", Message: "a selected service is blocked by a dependency that terminated unsuccessfully", Services: snapshots}
		}
		if terminal && request.Condition != "stopped" {
			return result, &WaitError{Code: "terminal_failure", Message: "a selected service terminated before satisfying the wait condition", Services: snapshots}
		}
		select {
		case <-ctx.Done():
			code := "wait_cancelled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				code = "wait_timeout"
			}
			return result, &WaitError{Code: code, Message: ctx.Err().Error(), Services: snapshots}
		case <-ticker.C:
		}
	}
}

func serviceCondition(snapshot *ServiceSnapshot, condition string) bool {
	status := snapshot.State.Status
	running := status == config.StatusRunning || status == config.StatusUnhealthy
	readinessConfigured := snapshot.Config.HealthCheck != nil && snapshot.Config.HealthCheck.Readiness != nil
	livenessConfigured := snapshot.Config.HealthCheck != nil && snapshot.Config.HealthCheck.Liveness != nil
	readinessOK := !readinessConfigured || snapshot.Health.Observed && snapshot.Health.Ready
	livenessOK := !livenessConfigured || snapshot.Health.Observed && snapshot.Health.Alive
	switch condition {
	case "running":
		return running
	case "stopped":
		return status == config.StatusStopped
	case "ready":
		return running && readinessOK
	case "healthy":
		return running && readinessOK && livenessOK
	case "unhealthy":
		return status == config.StatusUnhealthy || snapshot.Health.Observed && (!readinessOK || !livenessOK)
	default:
		return false
	}
}
