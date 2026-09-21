package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/kranz-org/kranz/internal/actionparams"
	"github.com/kranz-org/kranz/internal/config"
)

// prereqKey distinguishes one prerequisite invocation. Two services that
// declare the same action with different values are different prerequisites and
// must never collapse into one satisfied result.
type prereqKey struct {
	id         config.ActionID
	invocation string
}

func actionRuntimeKey(id config.ActionID) string {
	return string(id.OwnerKind) + "/" + id.Owner + "/" + id.Name
}

// ErrPrerequisiteFailed reports that a service did not start because one of its
// before_start actions did not succeed. The service stays stopped; Kranz never
// starts a service whose declared prerequisite failed.
var ErrPrerequisiteFailed = errors.New("prerequisite failed")

// prereqRun lets several services waiting on the same prerequisite share one
// execution instead of each running it independently.
type prereqRun struct {
	done chan struct{}
	err  error
}

// runPrerequisites executes a service's before_start sequence in declared
// order. It runs after dependencies are ready and before the service itself
// starts, so a prerequisite may rely on everything the service depends on.
func (m *Manager) runPrerequisites(ctx context.Context, svc *Service) error {
	prerequisites := svc.Config.BeforeStart
	if len(prerequisites) == 0 {
		return nil
	}
	for _, prerequisite := range prerequisites {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.runPrerequisite(ctx, svc, prerequisite); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) runPrerequisite(ctx context.Context, svc *Service, prerequisite config.Prerequisite) error {
	id := prerequisite.ActionID(svc.Name)
	label := prerequisite.String(svc.Name)
	once := prerequisite.RunPolicy() == config.PrerequisiteOnce
	definition, invocationKey, err := m.resolvePrerequisiteDefinition(id, prerequisite)
	if err != nil {
		return prerequisiteError(svc, id, 0, label, err)
	}
	key := prereqKey{id: id, invocation: invocationKey}

	m.prereqMu.Lock()
	if once && m.prereqSatisfied[key] {
		m.prereqMu.Unlock()
		svc.AppendLog("[Kranz] Prerequisite already satisfied: " + label)
		return nil
	}
	if active, running := m.prereqRuns[key]; running {
		m.prereqMu.Unlock()
		// Another service reached the same prerequisite invocation first. Wait
		// for its result rather than starting a second copy of the same command.
		svc.AppendLog("[Kranz] Waiting for prerequisite: " + label)
		select {
		case <-active.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		if active.err != nil {
			return prerequisiteError(svc, id, 0, label, active.err)
		}
		svc.AppendLog("[Kranz] Prerequisite satisfied: " + label)
		return nil
	}
	run := &prereqRun{done: make(chan struct{})}
	m.prereqRuns[key] = run
	m.prereqMu.Unlock()

	svc.AppendLog("[Kranz] Running prerequisite: " + label)
	result, err := m.actions.RunDefinition(ctx, id, definition)
	prerequisiteRun := result.Run
	if err != nil {
		err = describePrerequisiteFailure(result, err)
	}

	m.prereqMu.Lock()
	delete(m.prereqRuns, key)
	if err == nil && once {
		if m.prereqSatisfied == nil {
			m.prereqSatisfied = make(map[prereqKey]bool)
		}
		m.prereqSatisfied[key] = true
	}
	m.prereqMu.Unlock()
	run.err = err
	close(run.done)

	if err != nil {
		return prerequisiteError(svc, id, prerequisiteRun, label, err)
	}
	svc.AppendLog("[Kranz] Prerequisite satisfied: " + label)
	return nil
}

// resolvePrerequisiteDefinition renders a prerequisite's immutable definition
// and its invocation identity. A prerequisite without params keeps its
// configured action unchanged.
func (m *Manager) resolvePrerequisiteDefinition(id config.ActionID, prerequisite config.Prerequisite) (config.Action, string, error) {
	action, exists := m.cfg.ResolveAction(id)
	if !exists {
		return config.Action{}, "", fmt.Errorf("%w: %s/%s", ErrActionNotFound, id.Owner, id.Name)
	}
	compiled, err := config.CompileAction(actionRuntimeKey(id), action)
	if err != nil {
		return config.Action{}, "", err
	}
	if compiled == nil || !compiled.HasParams() {
		return action, "", nil
	}
	raw, err := config.RawValuesFromAny(prerequisite.Params)
	if err != nil {
		return config.Action{}, "", err
	}
	invocation, err := actionparams.Normalize(compiled, raw)
	if err != nil {
		return config.Action{}, "", err
	}
	rendered, err := actionparams.Render(compiled, invocation)
	if err != nil {
		return config.Action{}, "", err
	}
	values := actionparams.NativeValues(invocation)
	return action.RenderedAction(rendered, values), actionparams.InvocationKey(actionRuntimeKey(id), invocation), nil
}

// PrerequisiteError reports which service did not start, which action gated it,
// and which run of that action failed. The same facts reach a structured client
// as a causal error and stay on the service as its state cause.
type PrerequisiteError struct {
	Service string
	Action  config.ActionID
	Run     uint32
	Label   string
	Cause   error
}

func (e *PrerequisiteError) Error() string {
	return fmt.Sprintf("%s not started: %s: %s %s", e.Service, ErrPrerequisiteFailed, e.Label, e.Cause)
}

func (e *PrerequisiteError) Unwrap() error { return e.Cause }

// Is lets errors.Is(err, ErrPrerequisiteFailed) keep working for callers that
// only need to know the kind of failure.
func (e *PrerequisiteError) Is(target error) bool { return target == ErrPrerequisiteFailed }

func prerequisiteError(svc *Service, id config.ActionID, run uint32, label string, err error) error {
	// The service stays stopped for a reason a reader should not have to
	// recover from log text: name the action and the run that failed.
	svc.SetCause(&config.StateCause{Type: "prerequisite_failed", Action: id.Owner + "/" + id.Name, ActionRun: run, Message: err.Error()})
	svc.AppendLog(fmt.Sprintf("[Kranz] Prerequisite failed: %s · %s", label, err))
	return &PrerequisiteError{Service: svc.Name, Action: id, Run: run, Label: label, Cause: err}
}

// describePrerequisiteFailure turns a runner error into one short clause that
// reads correctly after the action reference, so the notification says what
// went wrong instead of repeating identifiers the user can already see.
func describePrerequisiteFailure(result ActionResult, err error) error {
	var busy *ActionBusyError
	if errors.As(err, &busy) {
		return fmt.Errorf("could not run: %s is already running action %q", busy.Running.Owner, busy.Running.Name)
	}
	switch result.Status {
	case ActionTimedOut:
		return errors.New("timed out")
	case ActionCancelled:
		return errors.New("was canceled")
	}
	var exit *ActionExitError
	if errors.As(err, &exit) {
		return fmt.Errorf("exited with code %d", exit.ExitCode)
	}
	return fmt.Errorf("failed: %w", err)
}

// forgetChangedPrerequisites drops remembered once-per-session results whose
// action definition changed or disappeared during a configuration reload. A
// prerequisite that now runs a different command has not been satisfied.
func (m *Manager) forgetChangedPrerequisites(current, next *config.Config) {
	m.prereqMu.Lock()
	defer m.prereqMu.Unlock()
	for key := range m.prereqSatisfied {
		currentAction, currentExists := current.ResolveAction(key.id)
		nextAction, nextExists := next.ResolveAction(key.id)
		if !currentExists || !nextExists || !reflect.DeepEqual(currentAction, nextAction) {
			delete(m.prereqSatisfied, key)
		}
	}
}
