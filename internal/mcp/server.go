package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/kranz-org/kranz/internal/app"
)

const maxMessageSize = 16 * 1024 * 1024

// A scoped handler is a method expression on *scope, so a tool can only be
// installed once a way to resolve a runtime for it exists.
type toolHandler func(*scope, context.Context, json.RawMessage) ResultEnvelope
type globalToolHandler func(*Server, context.Context, json.RawMessage) ResultEnvelope
type resourceHandler func(*scope, context.Context) ResultEnvelope
type globalResourceHandler func(*Server, context.Context) ResultEnvelope

type pendingRequest struct {
	cancel context.CancelFunc
}

type Server struct {
	resolver     *Resolver
	kranzVersion string
	stdin        io.Reader
	stdout       io.Writer
	stderr       io.Writer

	writeMu     sync.Mutex
	requestWG   sync.WaitGroup
	pendingMu   sync.Mutex
	pending     map[string]*pendingRequest
	initialized atomic.Bool

	tools         map[string]toolDefinition
	toolOrder     []string
	resources     map[string]resourceDefinition
	resourceOrder []string

	runtimeListOverride   func(context.Context) ([]RuntimeEntry, error)
	selectorMatchOverride func(context.Context, string) ([]RuntimeSelectorMatch, error)
}

// NewServer serves any number of runtimes through one resolver. The process
// itself is a client of runtimes and owns none of them.
func NewServer(resolver *Resolver, kranzVersion string, stdin io.Reader, stdout, stderr io.Writer) *Server {
	server := &Server{resolver: resolver, kranzVersion: kranzVersion, stdin: stdin, stdout: stdout, stderr: stderr, pending: map[string]*pendingRequest{}}
	server.installResources()
	server.installTools()
	return server
}

// NewServerForRuntime serves exactly one already-connected runtime, which is
// what a pinned launch and an in-process caller both have.
func NewServerForRuntime(api app.API, session SessionIdentity, stdin io.Reader, stdout, stderr io.Writer) *Server {
	return NewServer(StaticResolver(api, session), session.KranzVersion, stdin, stdout, stderr)
}

// Serve runs newline-delimited UTF-8 JSON-RPC. stdout is touched only by
// writeResponse; diagnostics, including malformed notification details, go to
// stderr so MCP framing cannot be corrupted.
func (s *Server) Serve(ctx context.Context) error {
	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(s.stdin)
		scanner.Buffer(make([]byte, 64*1024), maxMessageSize)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- line:
			case <-ctx.Done():
				readErr <- ctx.Err()
				return
			}
		}
		readErr <- scanner.Err()
	}()
	for {
		select {
		case <-ctx.Done():
			s.cancelAll()
			s.requestWG.Wait()
			return ctx.Err()
		case err := <-readErr:
			if err == nil {
				s.cancelAll()
				s.requestWG.Wait()
				return nil
			}
			s.cancelAll()
			s.requestWG.Wait()
			return err
		case line := <-lines:
			if len(line) == 0 {
				continue
			}
			var message rpcMessage
			if err := json.Unmarshal(line, &message); err != nil {
				_ = s.writeResponse(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "Parse error"}})
				continue
			}
			if message.JSONRPC != "2.0" || message.Method == "" {
				if len(message.ID) > 0 {
					_ = s.writeProtocolError(message.ID, -32600, "Invalid Request", nil)
				}
				continue
			}
			if len(message.ID) == 0 {
				s.handleNotification(message)
				continue
			}
			s.requestWG.Add(1)
			go func() {
				defer s.requestWG.Done()
				s.handleRequest(ctx, message)
			}()
		}
	}
}

func (s *Server) handleNotification(message rpcMessage) {
	switch message.Method {
	case "notifications/initialized":
		s.initialized.Store(true)
	case "notifications/cancelled":
		var params struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(message.Params, &params) != nil || len(params.RequestID) == 0 {
			return
		}
		s.pendingMu.Lock()
		pending := s.pending[requestKey(params.RequestID)]
		s.pendingMu.Unlock()
		if pending != nil {
			pending.cancel()
		}
	}
}

// handleRequest answers one request. It recovers from a handler panic because
// this process may also be the supervisor: a nil dereference inside a tool
// must fail that one call, not take every managed service down with it.
func (s *Server) handleRequest(parent context.Context, message rpcMessage) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if s.stderr != nil {
				_, _ = fmt.Fprintf(s.stderr, "Kranz MCP handler panic in %s: %v\n%s\n", message.Method, recovered, debug.Stack())
			}
			_ = s.writeProtocolError(message.ID, -32603, "Internal error", map[string]any{"method": message.Method})
		}
	}()
	key := requestKey(message.ID)
	ctx, cancel := context.WithCancel(parent)
	pending := &pendingRequest{cancel: cancel}
	s.pendingMu.Lock()
	s.pending[key] = pending
	s.pendingMu.Unlock()
	defer func() { cancel(); s.pendingMu.Lock(); delete(s.pending, key); s.pendingMu.Unlock() }()

	var result any
	var protocolErr *rpcError
	switch message.Method {
	case "initialize":
		result, protocolErr = s.initialize(message.Params)
	case "ping":
		result = map[string]any{}
	default:
		if !s.initialized.Load() {
			protocolErr = &rpcError{Code: -32002, Message: "Server is not initialized"}
			break
		}
		switch message.Method {
		case "tools/list":
			result = map[string]any{"tools": s.listTools()}
		case "tools/call":
			result, protocolErr = s.callTool(ctx, message.Params)
		case "resources/list":
			result = map[string]any{"resources": s.listResources()}
		case "resources/templates/list":
			result = map[string]any{"resourceTemplates": s.resourceTemplates()}
		case "resources/read":
			result, protocolErr = s.readResource(ctx, message.Params)
		default:
			protocolErr = &rpcError{Code: -32601, Message: "Method not found"}
		}
	}
	_ = s.writeResponse(rpcResponse{JSONRPC: "2.0", ID: message.ID, Result: result, Error: protocolErr})
}

func (s *Server) initialize(raw json.RawMessage) (any, *rpcError) {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(raw, &params); err != nil || params.ProtocolVersion == "" {
		return nil, &rpcError{Code: -32602, Message: "Invalid initialize parameters"}
	}
	negotiated := ProtocolVersion
	if supportedProtocolVersions[params.ProtocolVersion] {
		negotiated = params.ProtocolVersion
	}
	s.initialized.Store(true)
	return map[string]any{
		"protocolVersion": negotiated,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}, "resources": map[string]any{"subscribe": false, "listChanged": false}},
		"serverInfo":      map[string]any{"name": "kranz", "title": "Kranz project runtimes", "version": s.kranzVersion},
		"instructions":    s.instructions(),
	}, nil
}

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil || params.Name == "" {
		return nil, &rpcError{Code: -32602, Message: "Invalid tool call parameters"}
	}
	definition, ok := s.tools[params.Name]
	if !ok {
		return nil, &rpcError{Code: -32602, Message: fmt.Sprintf("Unknown tool %q", params.Name)}
	}
	if len(params.Arguments) == 0 {
		params.Arguments = json.RawMessage("{}")
	}
	envelope, protocolErr := s.invokeTool(ctx, definition, params.Arguments)
	if protocolErr != nil {
		return nil, protocolErr
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: "encode tool result", Data: err.Error()}
	}
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(payload)}}, "structuredContent": envelope, "isError": envelope.Error != nil}, nil
}

func (s *Server) readResource(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(raw, &params); err != nil || params.URI == "" {
		return nil, &rpcError{Code: -32602, Message: "Invalid resource parameters"}
	}
	uri, requested := params.URI, ""
	if runtimeRef, short, scoped := runtimeScopedURI(uri); scoped {
		uri, requested = short, runtimeRef
	}
	definition, ok := s.resources[uri]
	if !ok {
		return nil, &rpcError{Code: -32002, Message: "Resource not found", Data: map[string]any{"uri": params.URI}}
	}
	if requested != "" && definition.scoped == nil {
		return nil, &rpcError{Code: -32002, Message: "Resource is not runtime-scoped", Data: map[string]any{"uri": params.URI}}
	}
	envelope := s.readResourceDefinition(ctx, definition, requested)
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: "encode resource", Data: err.Error()}
	}
	return map[string]any{"contents": []any{map[string]any{"uri": params.URI, "mimeType": "application/json", "text": string(payload)}}}, nil
}

func (s *Server) listTools() []toolDefinition {
	result := make([]toolDefinition, 0, len(s.toolOrder))
	for _, name := range s.toolOrder {
		result = append(result, s.tools[name])
	}
	return result
}

func (s *Server) listResources() []resourceDefinition {
	result := make([]resourceDefinition, 0, len(s.resourceOrder))
	for _, uri := range s.resourceOrder {
		result = append(result, s.resources[uri])
	}
	return result
}

// requestKey normalizes a JSON-RPC id for pending-request lookup. The raw
// bytes of an id are not a reliable key: a client may write 1 in the request
// and 1.0 in the cancellation and mean the same request.
func requestKey(id json.RawMessage) string {
	var value any
	if err := json.Unmarshal(id, &value); err != nil {
		return string(id)
	}
	switch typed := value.(type) {
	case float64:
		return "n:" + strconv.FormatFloat(typed, 'f', -1, 64)
	case string:
		return "s:" + typed
	default:
		return string(id)
	}
}

func (s *Server) writeProtocolError(id json.RawMessage, code int, message string, data any) error {
	return s.writeResponse(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message, Data: data}})
}

func (s *Server) writeResponse(response rpcResponse) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	encoder := json.NewEncoder(s.stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(response); err != nil {
		if s.stderr != nil {
			_, _ = fmt.Fprintf(s.stderr, "Kranz MCP write error: %v\n", err)
		}
		return err
	}
	return nil
}

func (s *Server) cancelAll() {
	s.pendingMu.Lock()
	pending := make([]*pendingRequest, 0, len(s.pending))
	for _, request := range s.pending {
		pending = append(pending, request)
	}
	s.pendingMu.Unlock()
	for _, request := range pending {
		request.cancel()
	}
}

// invokeTool resolves the runtime a scoped call names before the handler runs.
// The runtime argument is consumed here rather than by each tool, so no tool
// has to know how addressing works and none can quietly ignore it.
func (s *Server) invokeTool(ctx context.Context, definition toolDefinition, arguments json.RawMessage) (ResultEnvelope, *rpcError) {
	if definition.global != nil {
		return definition.global(s, ctx, arguments), nil
	}
	requested, rest, err := splitRuntimeArgument(arguments)
	if err != nil {
		return s.globalArgError(err), nil
	}
	resolved, causal := s.resolver.Resolve(ctx, requested)
	if causal != nil {
		return s.globalError(causal), nil
	}
	return definition.scoped(s.scopeFor(resolved), ctx, rest), nil
}

func (s *Server) readResourceDefinition(ctx context.Context, definition resourceDefinition, requested string) ResultEnvelope {
	if definition.global != nil {
		return definition.global(s, ctx)
	}
	resolved, causal := s.resolver.Resolve(ctx, requested)
	if causal != nil {
		return s.globalError(causal)
	}
	return definition.scoped(s.scopeFor(resolved), ctx)
}

// splitRuntimeArgument lifts runtime out of the arguments object. Tool
// argument decoding rejects unknown fields on purpose, so the address has to
// be removed rather than left for a handler that does not declare it.
func splitRuntimeArgument(arguments json.RawMessage) (string, json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(arguments, &fields); err != nil {
		return "", nil, fmt.Errorf("arguments must be a JSON object: %w", err)
	}
	raw, ok := fields["runtime"]
	if !ok {
		return "", arguments, nil
	}
	var requested string
	if err := json.Unmarshal(raw, &requested); err != nil {
		return "", nil, fmt.Errorf("runtime must be a string: %w", err)
	}
	delete(fields, "runtime")
	rest, err := json.Marshal(fields)
	if err != nil {
		return "", nil, err
	}
	return requested, rest, nil
}

// instructions tell the agent how addressing works, because the alternative is
// an agent that discovers it by failing.
func (s *Server) instructions() string {
	if s.resolver != nil && s.resolver.Pinned() {
		return "Inspect and operate one pinned Kranz runtime. This server was launched with -C or -p and refuses any other runtime. Use explicit start/stop operations and honor confirmation_required results."
	}
	return "Inspect and operate live Kranz project runtimes. Every tool except runtimes, up, and down takes an optional runtime argument naming a project by name or id; omit it to use the project this server was started in. Read kranz://runtimes to see what is running, and answer runtime_required or runtime_not_found by retrying with a runtime from its candidates. Start a project that is not running with up. Use explicit start/stop operations and honor confirmation_required results."
}
