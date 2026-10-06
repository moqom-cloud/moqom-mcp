// Package mcpserver exposes a MOQOM tenant to AI agents over the Model Context
// Protocol: rooms, join tokens, moderation, billing, API keys, and docs search.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/moqom-cloud/moqom-mcp/internal/billing"
	"github.com/moqom-cloud/moqom-mcp/internal/docs"
)

// Deps is what the tools call.
type Deps struct {
	Admin   Admin
	Billing *billing.Client
	Docs    *docs.Client

	// CurrentKeyID is the ID of the key this server runs on, so revoke_api_key
	// can refuse to cut off its own credential before asking the API.
	CurrentKeyID string

	// Version is reported to clients in the server's implementation info.
	Version string
}

const instructions = `This server operates one MOQOM tenant (real-time media over QUIC) with the operator's backend API key.

- Rooms, join tokens and moderation act on live users. Prefer read tools (list_rooms, get_room, list_participants, get_balance) to check state before acting.
- Tools marked destructive (close_room, ban_participant, revoke_api_key) require "confirm": true. Only set it after the human user has explicitly approved that specific action in this conversation.
- Join tokens and issued API keys are credentials. Do not paste them anywhere other than where the user asked for them, and never into public channels or logs.
- Moderation results include "enforced"; when it is false, say enforcement is still propagating instead of reporting success.
- For how-to questions about MOQOM, use search_docs and get_doc_page and cite the URLs.`

// New builds the MCP server with every tool, resource, and prompt registered.
func New(deps Deps) *mcp.Server {
	version := deps.Version
	if version == "" {
		version = "dev"
	}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "moqom-mcp",
		Title:   "MOQOM",
		Version: version,
	}, &mcp.ServerOptions{Instructions: instructions})

	t := &tools{deps: deps}
	if deps.Admin != nil {
		t.registerRooms(server)
		t.registerTokens(server)
		t.registerModeration(server)
	}
	if deps.Billing != nil {
		t.registerBilling(server)
		t.registerKeys(server)
		registerResources(server, deps.Billing)
	}
	if deps.Docs != nil {
		t.registerDocs(server)
	}
	if deps.Admin != nil {
		registerPrompts(server)
	}
	return server
}

type tools struct {
	deps Deps
}

// errNotConfirmed is returned by destructive tools called without confirm.
var errNotConfirmed = errors.New(`not done: this action is destructive and needs "confirm": true. ` +
	`Describe exactly what will happen to the user, get their explicit approval, then call again with confirm set to true`)

func boolPtr(b bool) *bool { return &b }

// Annotation presets.
var (
	readOnly = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(true)}
	additive = &mcp.ToolAnnotations{DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(true)}
)

func destructive(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, DestructiveHint: boolPtr(true), OpenWorldHint: boolPtr(true)}
}

func titled(base *mcp.ToolAnnotations, title string) *mcp.ToolAnnotations {
	copy := *base
	copy.Title = title
	return &copy
}

// schema infers the input schema for T and applies enum constraints and
// numeric bounds the struct tags cannot express.
func schema[T any](enums map[string][]any, minimums map[string]float64) *jsonschema.Schema {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("schema for %T: %v", *new(T), err))
	}
	for name, values := range enums {
		property := s.Properties[name]
		if property == nil {
			panic(fmt.Sprintf("schema for %T: no property %q", *new(T), name))
		}
		if property.Items != nil && (property.Type == "array" || containsString(property.Types, "array")) {
			property.Items.Enum = values
		} else {
			property.Enum = values
		}
	}
	for name, minimum := range minimums {
		property := s.Properties[name]
		if property == nil {
			panic(fmt.Sprintf("schema for %T: no property %q", *new(T), name))
		}
		property.Minimum = &minimum
	}
	return s
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func strings2any(values ...string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

func requireText(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

func addTool[In, Out any](server *mcp.Server, tool *mcp.Tool, handler func(context.Context, In) (Out, error)) {
	mcp.AddTool(server, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		out, err := handler(ctx, in)
		return nil, out, err
	})
}
