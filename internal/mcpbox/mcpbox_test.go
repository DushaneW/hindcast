package mcpbox

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DushaneW/Hindcast/internal/tool"
)

type podsArgs struct {
	Namespace string `json:"namespace"`
}

func fakeServer(t *testing.T) mcp.Transport {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "v0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_pods", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(_ context.Context, _ *mcp.CallToolRequest, in podsArgs) (*mcp.CallToolResult, any, error) {
			if in.Namespace == "missing" {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "namespace not found"}}}, nil, nil
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pods in " + in.Namespace}}}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "delete_pod"},
		func(context.Context, *mcp.CallToolRequest, podsArgs) (*mcp.CallToolResult, any, error) {
			t.Error("delete_pod must not be called")
			return &mcp.CallToolResult{}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "rollout_restart"},
		func(context.Context, *mcp.CallToolRequest, podsArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "restarted"}}}, nil, nil
		})

	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(context.Background(), serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	return clientT
}

func names(specs []tool.Spec) []string {
	var out []string
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

func TestReadOnlyPolicy(t *testing.T) {
	ctx := context.Background()
	box, err := Connect(ctx, "test", []Server{{Name: "k8s", Transport: fakeServer(t), Allow: []string{"rollout_restart"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()

	if got, want := names(box.Tools()), []string{"k8s__get_pods", "k8s__rollout_restart"}; !slices.Equal(got, want) {
		t.Errorf("offered tools = %v, want %v", got, want)
	}
	if got, want := box.Hidden(), []string{"k8s__delete_pod"}; !slices.Equal(got, want) {
		t.Errorf("hidden tools = %v, want %v", got, want)
	}

	res, err := box.Call(ctx, "k8s__delete_pod", json.RawMessage(`{"namespace":"prod"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("calling a hidden tool must fail")
	}
}

func TestCall(t *testing.T) {
	ctx := context.Background()
	box, err := Connect(ctx, "test", []Server{{Name: "k8s", Transport: fakeServer(t)}})
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()

	res, err := box.Call(ctx, "k8s__get_pods", json.RawMessage(`{"namespace":"prod"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || res.Text != "pods in prod" {
		t.Errorf("result = %+v", res)
	}

	res, err = box.Call(ctx, "k8s__get_pods", json.RawMessage(`{"namespace":"missing"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || res.Text != "namespace not found" {
		t.Errorf("tool error result = %+v", res)
	}

	res, err = box.Call(ctx, "k8s__get_pods", json.RawMessage(`[1]`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("non-object arguments must be rejected")
	}
}

func TestQualifiedName(t *testing.T) {
	if got := QualifiedName("my.server", "get logs"); got != "my_server__get_logs" {
		t.Errorf("got %q", got)
	}
	long := QualifiedName("s", string(make([]byte, 100)))
	if len(long) != 64 {
		t.Errorf("length %d, want 64", len(long))
	}
}

// TestMain lets the test binary double as a stdio MCP server, so the
// subprocess path is tested without building anything else.
func TestMain(m *testing.M) {
	if os.Getenv("HINDCAST_FAKE_MCP") == "1" {
		srv := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "v0"}, nil)
		mcp.AddTool(srv, &mcp.Tool{Name: "ping", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil, nil
			})
		if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestSubprocess(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "HINDCAST_FAKE_MCP=1")
	ctx := context.Background()
	box, err := Connect(ctx, "test", []Server{{Name: "fake", Transport: &mcp.CommandTransport{Command: cmd}}})
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	res, err := box.Call(ctx, "fake__ping", nil)
	if err != nil || res.Text != "pong" {
		t.Errorf("result = %+v, err = %v", res, err)
	}
}
