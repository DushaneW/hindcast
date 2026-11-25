# Contributing

## Setup

Go 1.25 or newer.

```sh
git clone https://github.com/DushaneW/Hindcast
cd Hindcast
make lint test
```

`make build` produces `./hindcast`. `make dist` cross-compiles the release
archives into `dist/`.

## What helps most

1. **Recorded incidents.** A trace of a real investigation plus a
   `scenario.yaml` with the true root cause is worth more than any feature.
   Run `hindcast investigate` against the incident, check the trace for
   anything you would not publish, and open a pull request adding it under
   `scenarios/`. The scenario issue template lists what to include.
2. **Eval results.** Run `hindcast eval scenarios/` with a model you have
   access to and post the output with the model name and Hindcast version.
3. **Prefetch recipes** for common alert types and MCP servers, tested
   against a real server.

## Changes to code

- Keep packages small and their boundaries as described in
  [docs/design.md](docs/design.md). The investigator depends only on
  `tool.Box` and `llm.Model`; keep it that way.
- Every behaviour change comes with a test. Tests use fakes for models and
  MCP servers; see `internal/investigate` and `internal/mcpbox` for the
  patterns.
- Changes to the system prompt, the loop or grading need `hindcast eval`
  output before and after, with the model used. Do not claim an improvement
  from one run.
- No new dependencies without a reason in the pull request.

## Commits

Short imperative subject line; the body says why, not what.
