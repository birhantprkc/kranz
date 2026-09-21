package actionparams

import (
	"fmt"
	"sort"
	"strings"
)

// FieldError names one invalid parameter and a user-facing reason. Reasons
// describe the constraint that failed, never an internal Go type.
type FieldError struct {
	Name   string
	Reason string
}

// Error is a parameter validation or compilation failure. Every constructor
// preserves the same stable Code so delivery adapters can translate it into the
// existing error envelope without inspecting the message.
type Error struct {
	Code    string
	Message string
	Fields  []FieldError
	Cause   error
}

// Public codes shared with the delivery surfaces.
const (
	CodeInvalidArguments = "invalid_arguments"
	CodeConfigInvalid    = "config_invalid"
)

func (e *Error) Error() string {
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Cause }

// FieldMap returns the per-parameter diagnostics in deterministic name order.
func (e *Error) FieldMap() map[string]string {
	if e == nil || len(e.Fields) == 0 {
		return nil
	}
	fields := make(map[string]string, len(e.Fields))
	for _, field := range e.Fields {
		if _, exists := fields[field.Name]; !exists {
			fields[field.Name] = field.Reason
		}
	}
	return fields
}

func invalidField(name, reason string) *Error {
	return &Error{Code: CodeInvalidArguments, Message: "action parameters are invalid", Fields: []FieldError{{Name: name, Reason: reason}}}
}

func configError(reason string) *Error {
	return &Error{Code: CodeConfigInvalid, Message: reason}
}

func configField(name, reason string) *Error {
	return &Error{Code: CodeConfigInvalid, Message: fmt.Sprintf("parameter %q: %s", name, reason), Fields: []FieldError{{Name: name, Reason: reason}}}
}

// CombinedError joins several parameter diagnostics into one failure.
func CombinedError(fields []FieldError) *Error {
	return &Error{Code: CodeInvalidArguments, Message: "action parameters are invalid", Fields: fields}
}

// sortFields orders diagnostics by parameter name for stable output.
func sortFields(fields []FieldError) []FieldError {
	sort.SliceStable(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	return fields
}

// JoinMessages renders a compact comma-separated list of constraint reasons.
func JoinMessages(reasons []string) string {
	return strings.Join(reasons, "; ")
}
