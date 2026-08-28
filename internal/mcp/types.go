// Package mcp implements Kranz's foreground stdio delivery adapter. It owns
// JSON-RPC framing and an explicit resource/tool allow-list, while every
// runtime operation is delegated to app.API.
package mcp

import (
	"encoding/json"

	"github.com/kranz-org/kranz/internal/runtime"
)

const (
	SchemaVersion   = 2
	ProtocolVersion = "2025-11-25"
)

var supportedProtocolVersions = map[string]bool{
	"2025-11-25": true,
	"2025-06-18": true,
	"2025-03-26": true,
	"2024-11-05": true,
}

// SessionIdentity names the runtime that answered one call. Before v0.11 it
// named what the connection was bound to; with per-call addressing that
// reading has no referent, and a field that means "who answered" is the only
// one a caller can act on.
type SessionIdentity struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Project         string `json:"project"`
	Directory       string `json:"directory"`
	RuntimeMode     string `json:"runtime_mode"`
	KranzVersion    string `json:"kranz_version"`
	ProtocolVersion int    `json:"runtime_protocol_version"`
	// Pinned marks the one runtime a -C/-p launch admits.
	Pinned bool `json:"pinned"`
	// CreatedBy is "mcp" for a runtime this MCP process started through up.
	CreatedBy string `json:"created_by,omitempty"`
}

func SessionFromMetadata(metadata runtime.SessionMetadata) SessionIdentity {
	return SessionIdentity{
		ID: metadata.ID, Name: metadata.Name, Project: metadata.Project, Directory: metadata.Directory,
		RuntimeMode: metadata.Mode, KranzVersion: metadata.KranzVersion, ProtocolVersion: metadata.ProtocolMax,
	}
}

func SessionFromRecord(record runtime.SessionRecord) SessionIdentity {
	return SessionFromMetadata(record.SessionMetadata)
}

type CausalError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Hint    string         `json:"hint,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// ResultEnvelope carries session and generation only for an answer a runtime
// actually served. A global tool such as runtimes is answered by the MCP
// process itself, and stamping it with somebody's session identity would be a
// claim about provenance that is not true.
type ResultEnvelope struct {
	SchemaVersion int              `json:"schema_version"`
	Generation    uint64           `json:"generation,omitempty"`
	Session       *SessionIdentity `json:"session,omitempty"`
	Data          any              `json:"data,omitempty"`
	Error         *CausalError     `json:"error,omitempty"`
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// toolDefinition carries exactly one handler. A runtime-scoped tool is written
// against a resolved runtime and cannot run without one; a global tool answers
// for the MCP process and takes no runtime argument.
type toolDefinition struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
	Annotations  map[string]any `json:"annotations,omitempty"`
	scoped       toolHandler
	global       globalToolHandler
}

// envelopeSchema describes the result envelope every tool returns. Declaring
// it means a client does not have to discover the shape of a Kranz answer by
// running a call and reading what came back.
func envelopeSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"schema_version": map[string]any{"type": "integer", "description": "Envelope contract version."},
			"generation":     map[string]any{"type": "integer", "description": "Configuration generation the answer was read at; absent when no runtime served the call."},
			"session":        map[string]any{"type": "object", "description": "The runtime that served this call; absent for global tools and for failures that resolved no runtime."},
			"data":           map[string]any{"description": "Tool-specific payload; absent on failure."},
			"error": map[string]any{"type": "object", "description": "Causal failure, absent on success.", "properties": map[string]any{
				"code":    map[string]any{"type": "string"},
				"message": map[string]any{"type": "string"},
				"hint":    map[string]any{"type": "string"},
				"details": map[string]any{"type": "object"},
			}},
		},
		"required": []string{"schema_version"},
	}
}

type resourceDefinition struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
	scoped      resourceHandler
	global      globalResourceHandler
}

// resourceTemplate publishes the addressable form of a runtime-scoped
// resource. The short URI still works and resolves the same way a tool call
// without a runtime argument does; the template is how a client reads a
// runtime it has to name.
type resourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}
