package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/kranz-org/kranz/internal/config"
)

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

	m.prereqMu.Lock()
	if once && m.prereqSatisfied[id] {
		m.prereqMu.Unlock()
		svc.AppendLog("[Kranz] Prerequisite already satisfied: " + label)
		return nil
	}
	if active, running := m.prereqRuns[id]; running {
		m.prereqMu.Unlock()
		// Another service reached the same prerequisite first. Wait for its
		// result rather than starting a second copy of the same command.
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
	m.prereqRuns[id] = run
	m.prereqMu.Unlock()

	svc.AppendLog("[Kranz] Running prerequisite: " + label)
	result, err := m.actions.Run(ctx, id)
	prerequisiteRun := result.Run
	if err != nil {
		err = describePrerequisiteFailure(result, err)
	}

	m.prereqMu.Lock()
	delete(m.prereqRuns, id)
	if err == nil && once {
		if m.prereqSatisfied == nil {
			m.prereqSatisfied = make(map[config.ActionID]bool)
		}
		m.prereqSatisfied[id] = true
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
	for id := range m.prereqSatisfied {
		currentAction, currentExists := current.ResolveAction(id)
		nextAction, nextExists := next.ResolveAction(id)
		if !currentExists || !nextExists || !reflect.DeepEqual(currentAction, nextAction) {
			delete(m.prereqSatisfied, id)
		}
	}
}
