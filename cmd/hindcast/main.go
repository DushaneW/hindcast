// Command hindcast investigates alerts with an LLM and read-only tools.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hindcast/hindcast/internal/config"
	"github.com/hindcast/hindcast/internal/llm"
	"github.com/hindcast/hindcast/internal/mcpbox"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: hindcast <command> [flags]

commands:
  investigate  investigate an Alertmanager webhook payload
  replay       re-run a recorded investigation without live systems
  eval         replay a directory of scenarios and check the reports
  tools        list the tools the read-only policy offers and hides
  version      print the version

Run "hindcast <command> -h" for flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	commands := map[string]func(context.Context, []string) error{
		"investigate": cmdInvestigate,
		"replay":      cmdReplay,
		"eval":        cmdEval,
		"tools":       cmdTools,
		"version": func(context.Context, []string) error {
			fmt.Println(version)
			return nil
		},
	}
	cmd, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	err := cmd(ctx, os.Args[2:])
	var exit exitError
	switch {
	case err == nil:
	case errors.Is(err, flag.ErrHelp):
		os.Exit(2)
	case errors.As(err, &exit):
		os.Exit(int(exit))
	default:
		fmt.Fprintln(os.Stderr, "hindcast:", err)
		os.Exit(1)
	}
}

// exitError ends the process with a status but no message; the command has
// already printed what the user needs.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func newModel(m config.Model) (llm.Model, error) {
	key, err := m.APIKey()
	if err != nil {
		return nil, err
	}
	switch m.Provider {
	case "anthropic":
		return &llm.Anthropic{BaseURL: m.BaseURL, APIKey: key, Model: m.Name}, nil
	case "openai":
		return &llm.OpenAI{BaseURL: m.BaseURL, APIKey: key, Model: m.Name}, nil
	default:
		return nil, fmt.Errorf("unknown provider %q", m.Provider)
	}
}

// connect starts every configured MCP server. Server stderr goes to
// serverLog, which may be io.Discard.
func connect(ctx context.Context, cfg *config.Config, serverLog io.Writer) (*mcpbox.Box, error) {
	names := make([]string, 0, len(cfg.Servers))
	for name := range cfg.Servers {
		names = append(names, name)
	}
	sort.Strings(names)

	servers := make([]mcpbox.Server, 0, len(names))
	for _, name := range names {
		s := cfg.Servers[name]
		cmd := exec.Command(s.Command[0], s.Command[1:]...)
		cmd.Env = os.Environ()
		for k, v := range s.Env {
			cmd.Env = append(cmd.Env, k+"="+os.ExpandEnv(v))
		}
		cmd.Stderr = serverLog
		servers = append(servers, mcpbox.Server{Name: name, Transport: &mcp.CommandTransport{Command: cmd}, Allow: s.Allow})
	}
	return mcpbox.Connect(ctx, version, servers)
}
