// Package mcpbox serves tool calls from MCP servers and enforces the
// read-only policy: a tool is offered to the model only if its server marks
// it readOnlyHint, or the operator lists it in the server's allow list.
package mcpbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DushaneW/Hindcast/internal/tool"
)

// Server is one MCP server to connect to.
type Server struct {
	Name      string
	Transport mcp.Transport
	// Allow lists tool names (as the server names them) that are offered even
	// though the server does not mark them read-only.
	Allow []string
}

// Box is a tool.Box backed by MCP sessions.
type Box struct {
	sessions []*mcp.ClientSession
	specs    []tool.Spec
	routes   map[string]route
	hidden   []string
}

type route struct {
	session *mcp.ClientSession
	tool    string
}

// Connect opens a session to every server and lists its tools. On error,
// sessions opened so far are closed.
func Connect(ctx context.Context, version string, servers []Server) (*Box, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "hindcast", Version: version}, nil)
	b := &Box{routes: make(map[string]route)}
	for _, s := range servers {
		if err := b.add(ctx, client, s); err != nil {
			b.Close()
			return nil, fmt.Errorf("mcp server %q: %w", s.Name, err)
		}
	}
	sort.Slice(b.specs, func(i, j int) bool { return b.specs[i].Name < b.specs[j].Name })
	sort.Strings(b.hidden)
	return b, nil
}

func (b *Box) add(ctx context.Context, client *mcp.Client, s Server) error {
	session, err := client.Connect(ctx, s.Transport, nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	b.sessions = append(b.sessions, session)

	allowed := make(map[string]bool, len(s.Allow))
	for _, name := range s.Allow {
		allowed[name] = true
	}
	for t, err := range session.Tools(ctx, nil) {
		if err != nil {
			return fmt.Errorf("list tools: %w", err)
		}
		name := QualifiedName(s.Name, t.Name)
		readOnly := t.Annotations != nil && t.Annotations.ReadOnlyHint
		if !readOnly && !allowed[t.Name] {
			b.hidden = append(b.hidden, name)
			continue
		}
		if _, dup := b.routes[name]; dup {
			return fmt.Errorf("tool name %q collides after qualification", name)
		}
		schema, err := json.Marshal(t.InputSchema)
		if err != nil {
			return fmt.Errorf("tool %s: encode input schema: %w", t.Name, err)
		}
		b.specs = append(b.specs, tool.Spec{Name: name, Description: t.Description, InputSchema: schema})
		b.routes[name] = route{session: session, tool: t.Name}
	}
	return nil
}

// Tools returns the tools that passed the read-only policy.
func (b *Box) Tools() []tool.Spec { return b.specs }

// Hidden returns qualified names of tools withheld by the read-only policy.
func (b *Box) Hidden() []string { return b.hidden }

// Call forwards a call to the server that owns the tool.
func (b *Box) Call(ctx context.Context, name string, args json.RawMessage) (tool.Result, error) {
	r, ok := b.routes[name]
	if !ok {
		return tool.Result{Text: fmt.Sprintf("unknown tool %q", name), IsError: true}, nil
	}
	var arguments map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &arguments); err != nil {
			return tool.Result{Text: "arguments must be a JSON object: " + err.Error(), IsError: true}, nil
		}
	}
	res, err := r.session.CallTool(ctx, &mcp.CallToolParams{Name: r.tool, Arguments: arguments})
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: resultText(res), IsError: res.IsError}, nil
}

// Close ends all sessions.
func (b *Box) Close() error {
	var errs []error
	for _, s := range b.sessions {
		errs = append(errs, s.Close())
	}
	return errors.Join(errs...)
}

func resultText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		switch c := c.(type) {
		case *mcp.TextContent:
			parts = append(parts, c.Text)
		case *mcp.EmbeddedResource:
			if c.Resource != nil && c.Resource.Text != "" {
				parts = append(parts, c.Resource.Text)
			} else {
				parts = append(parts, "[binary resource omitted]")
			}
		case *mcp.ImageContent:
			parts = append(parts, "[image omitted]")
		case *mcp.AudioContent:
			parts = append(parts, "[audio omitted]")
		default:
			parts = append(parts, "[unsupported content omitted]")
		}
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		if data, err := json.Marshal(res.StructuredContent); err == nil {
			parts = append(parts, string(data))
		}
	}
	return strings.Join(parts, "\n")
}

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// QualifiedName is the name the model sees: server and tool joined by "__",
// restricted to the characters and length LLM APIs accept.
func QualifiedName(server, toolName string) string {
	name := unsafeChars.ReplaceAllString(server+"__"+toolName, "_")
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}
