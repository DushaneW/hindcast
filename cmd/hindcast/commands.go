package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/hindcast/hindcast/internal/alert"
	"github.com/hindcast/hindcast/internal/config"
	"github.com/hindcast/hindcast/internal/eval"
	"github.com/hindcast/hindcast/internal/investigate"
	"github.com/hindcast/hindcast/internal/trace"
)

func cmdInvestigate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("investigate", flag.ContinueOnError)
	cfgPath := fs.String("c", "hindcast.yaml", "config file")
	alertPath := fs.String("alert", "-", "Alertmanager webhook payload, or - for stdin")
	traceDir := fs.String("traces", "hindcast-traces", "directory for the trace; empty disables recording")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	verbose := fs.Bool("v", false, "show MCP server stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	payload, err := readAlert(*alertPath)
	if err != nil {
		return err
	}
	l, err := openLive(ctx, cfg, *traceDir, serverLog(*verbose), os.Stderr)
	if err != nil {
		return err
	}
	defer l.Close()

	rep, err := l.investigate(ctx, payload)
	if rep != nil {
		if perr := printReport(rep, *asJSON); perr != nil {
			return perr
		}
	}
	return err
}

func cmdReplay(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	cfgPath := fs.String("c", "hindcast.yaml", "config file (only model and budget are used)")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: hindcast replay [flags] trace.jsonl")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return flag.ErrHelp
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	tr, err := trace.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	payload, err := alert.Parse(tr.Header.Alert)
	if err != nil {
		return err
	}
	box, err := trace.NewReplayer(tr)
	if err != nil {
		return err
	}
	model, err := newModel(cfg.Model)
	if err != nil {
		return err
	}
	prefetch, err := investigate.PrefetchFromTrace(tr)
	if err != nil {
		return err
	}
	inv := &investigate.Investigator{Model: model, Tools: box, Budget: cfg.Budget.Investigate(), Prefetch: prefetch}
	rep, err := inv.Run(ctx, payload)
	if err != nil {
		return err
	}
	return printReport(rep, *asJSON)
}

func cmdEval(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	cfgPath := fs.String("c", "hindcast.yaml", "config file (only model and budget are used)")
	asJSON := fs.Bool("json", false, "print results as JSON")
	traceDir := fs.String("traces", "", "directory to write one trace per scenario")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: hindcast eval [flags] scenarios-dir")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return flag.ErrHelp
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	scenarios, err := eval.Load(fs.Arg(0))
	if err != nil {
		return err
	}
	model, err := newModel(cfg.Model)
	if err != nil {
		return err
	}
	runner := eval.Runner{Model: model, Budget: cfg.Budget.Investigate()}
	judgeName := "none (regexp matching)"
	if cfg.Judge != nil {
		judgeModel, err := newModel(*cfg.Judge)
		if err != nil {
			return fmt.Errorf("judge: %w", err)
		}
		runner.Judge = &eval.Judge{Model: judgeModel}
		judgeName = cfg.Judge.Name
	}

	var results []eval.Result
	passed := 0
	for _, s := range scenarios {
		var w *trace.Writer
		if *traceDir != "" {
			f, _, err := createTrace(*traceDir, s.Name)
			if err != nil {
				return err
			}
			w = trace.NewWriter(f)
			defer f.Close()
		}
		res := runner.Run(ctx, s, w)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		results = append(results, res)
		if res.Pass {
			passed++
		}
		if !*asJSON {
			printResult(res)
		}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return err
		}
	} else {
		fmt.Printf("\n%d/%d passed (model %s, judge %s)\n", passed, len(results), cfg.Model.Name, judgeName)
	}
	if passed < len(results) {
		return exitError(1)
	}
	return nil
}

func cmdTools(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	cfgPath := fs.String("c", "hindcast.yaml", "config file")
	verbose := fs.Bool("v", false, "show MCP server stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	box, err := connect(ctx, cfg, serverLog(*verbose))
	if err != nil {
		return err
	}
	defer box.Close()

	fmt.Println("offered:")
	for _, t := range box.Tools() {
		fmt.Printf("  %s\n", t.Name)
	}
	if hidden := box.Hidden(); len(hidden) > 0 {
		fmt.Println("hidden (not marked read-only; add to the server's allow list to offer):")
		for _, name := range hidden {
			fmt.Printf("  %s\n", name)
		}
	}
	return nil
}

func printResult(r eval.Result) {
	switch {
	case r.Error != "":
		fmt.Printf("ERROR %s: %s\n", r.Scenario, r.Error)
	case r.Pass:
		fmt.Printf("PASS  %s (%d steps, %d tool calls)\n", r.Scenario, r.Report.Stats.Steps, r.Report.Stats.ToolCalls)
		if r.Verdict != nil {
			fmt.Printf("      judge: %s\n", r.Verdict.Reason)
		}
	default:
		fmt.Printf("FAIL  %s\n", r.Scenario)
		for _, f := range r.Failures {
			fmt.Printf("      %s\n", f)
		}
	}
}

func printReport(rep *investigate.Report, asJSON bool) error {
	if !asJSON {
		_, err := fmt.Print(rep.Markdown())
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

func readAlert(path string) (alert.Payload, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return alert.Payload{}, fmt.Errorf("read alert: %w", err)
	}
	return alert.Parse(data)
}

func serverLog(verbose bool) io.Writer {
	if verbose {
		return os.Stderr
	}
	return io.Discard
}
