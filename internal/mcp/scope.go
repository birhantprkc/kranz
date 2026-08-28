package mcp

import (
	"github.com/kranz-org/kranz/internal/app"
)

// scope is one runtime resolved for one call. Handlers are written against it
// rather than against the server, so a tool physically cannot reach a runtime
// the call did not address, and every answer it produces is stamped with the
// runtime that actually served it.
type scope struct {
	*Server
	api     app.API
	session SessionIdentity
}

func (s *Server) scopeFor(resolved *runtimeScope) *scope {
	return &scope{Server: s, api: resolved.api, session: resolved.session}
}

// globalEnvelope answers for the MCP process itself. It carries no session,
// because no runtime served it.
func (s *Server) globalEnvelope(data any) ResultEnvelope {
	return ResultEnvelope{SchemaVersion: SchemaVersion, Data: data}
}

func (s *Server) globalError(causal *CausalError) ResultEnvelope {
	return ResultEnvelope{SchemaVersion: SchemaVersion, Error: causal}
}

func (s *Server) globalArgError(err error) ResultEnvelope {
	return s.globalError(&CausalError{Code: "invalid_arguments", Message: err.Error()})
}
