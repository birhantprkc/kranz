package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// upTool is the only way an MCP process brings a runtime into existence, and
// it brings up nothing else. Starting services stays behind start, which
// resolves a plan and asks for confirmation against it; a project runtime with
// no services running is cheap to create and cheap to undo.
func (s *Server) upTool(ctx context.Context, raw json.RawMessage) ResultEnvelope {
	var args struct {
		Directory string `json:"directory"`
		Confirm   bool   `json:"confirm"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return s.globalArgError(err)
	}
	directory := args.Directory
	if directory == "" {
		if s.resolver == nil || s.resolver.projectDirectory == "" {
			return s.globalError(&CausalError{
				Code:    "invalid_arguments",
				Message: "up needs the project directory: this MCP server was not started inside a Kranz project",
				Hint:    "Pass directory with the absolute path of the project to start.",
			})
		}
		directory = s.resolver.projectDirectory
	}
	if !args.Confirm {
		return s.globalError(&CausalError{
			Code:    "confirmation_required",
			Message: fmt.Sprintf("starting a runtime for %s creates a background process that outlives this session", directory),
			Hint:    "Repeat the same call with confirm set to true once the user has asked for the project to be started.",
			Details: map[string]any{"directory": directory},
		})
	}
	resolved, created, causal := s.resolver.Launch(ctx, directory)
	if causal != nil {
		return s.globalError(causal)
	}
	identity := resolved.session
	return s.globalEnvelope(map[string]any{
		"created": created, "runtime": identity.Name, "id": identity.ID,
		"services_started": 0,
		"note":             "The runtime is up with no services running. Start services explicitly with start.",
	})
}

// downTool refuses any runtime this process did not create. A TUI someone is
// working in, or a runtime another agent started, is not this session's to
// stop, and "I did not create it" is a fact this process can check rather than
// a policy it has to be trusted with.
func (s *Server) downTool(ctx context.Context, raw json.RawMessage) ResultEnvelope {
	var args struct {
		Runtime string `json:"runtime"`
		Confirm bool   `json:"confirm"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return s.globalArgError(err)
	}
	resolved, causal := s.resolver.Resolve(ctx, args.Runtime)
	if causal != nil {
		return s.globalError(causal)
	}
	if !s.resolver.CreatedHere(resolved.session.ID) {
		return s.globalError(&CausalError{
			Code:    "not_owned",
			Message: fmt.Sprintf("runtime %q was not started by this MCP session and will not be stopped from here", resolved.session.Name),
			Hint:    "Ask the person using it to stop it, or run `kranz -p " + resolved.session.Name + " down` in a terminal.",
			Details: map[string]any{"runtime": resolved.session.Name, "id": resolved.session.ID},
		})
	}
	if !args.Confirm {
		return s.globalError(&CausalError{
			Code:    "confirmation_required",
			Message: fmt.Sprintf("stopping runtime %q stops everything running in it", resolved.session.Name),
			Hint:    "Repeat the same call with confirm set to true.",
			Details: map[string]any{"runtime": resolved.session.Name, "id": resolved.session.ID},
		})
	}
	if err := resolved.api.Shutdown(); err != nil {
		return s.globalError(causalError(err))
	}
	s.resolver.forget(resolved.session.ID)
	return s.globalEnvelope(map[string]any{"stopped": resolved.session.Name, "id": resolved.session.ID})
}
