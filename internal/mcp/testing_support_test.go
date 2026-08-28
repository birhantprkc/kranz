package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kranz-org/kranz/internal/app"
)

// testTool invokes one tool the way a client does, through the same runtime
// resolution a real call goes through.
func testTool(t *testing.T, server *Server, name string, arguments string) ResultEnvelope {
	t.Helper()
	return testToolContext(t, context.Background(), server, name, arguments)
}

func testToolContext(t *testing.T, ctx context.Context, server *Server, name string, arguments string) ResultEnvelope {
	t.Helper()
	definition, ok := server.tools[name]
	if !ok {
		t.Fatalf("unknown tool %q", name)
	}
	if arguments == "" {
		arguments = "{}"
	}
	envelope, protocolErr := server.invokeTool(ctx, definition, json.RawMessage(arguments))
	if protocolErr != nil {
		t.Fatalf("tool %s protocol error: %v", name, protocolErr)
	}
	return envelope
}

func testResource(t *testing.T, server *Server, uri string) ResultEnvelope {
	t.Helper()
	definition, ok := server.resources[uri]
	if !ok {
		t.Fatalf("unknown resource %q", uri)
	}
	return server.readResourceDefinition(context.Background(), definition, "")
}

// testAPI and setTestAPI reach the single runtime a test server serves, which
// is the one a StaticResolver holds.
func (s *Server) testAPI() app.API {
	for _, scope := range s.resolver.clients {
		return scope.api
	}
	return nil
}

func (s *Server) setTestAPI(api app.API) {
	for id, scope := range s.resolver.clients {
		s.resolver.clients[id] = &runtimeScope{api: api, session: scope.session, close: scope.close}
	}
}

func (s *Server) setTestSessionName(name string) {
	for id, scope := range s.resolver.clients {
		session := scope.session
		session.Name = name
		s.resolver.clients[id] = &runtimeScope{api: scope.api, session: session, close: scope.close}
	}
}

func (s *Server) testScope() *scope {
	for _, resolved := range s.resolver.clients {
		return s.scopeFor(resolved)
	}
	return nil
}
