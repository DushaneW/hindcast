# Hindcast

Hindcast investigates production alerts with an LLM and read-only tools, and
records every investigation so it can be replayed later without access to the
systems it queried.

```
hindcast investigate -c hindcast.yaml -alert alert.json
```

It takes an Alertmanager webhook payload, gathers context through MCP servers
(Kubernetes, Prometheus, GitHub, whatever you run), and prints a report with a
root cause, a confidence level, and the tool calls that support it.

Status: early. Recording, replay, eval and the read-only policy have been run
against a live k3s cluster through
[kubernetes-mcp-server](https://github.com/containers/kubernetes-mcp-server),
with a scripted stand-in for the model. No real model has been evaluated yet,
so there are no quality numbers. See [What is missing](#what-is-missing).

## How it differs from other AI SRE agents

- **Replayable.** Each investigation is written to a JSON Lines trace: the
  alert, the tools offered, every call and its full result, and the report.
  `hindcast replay` re-runs it against the recorded results, so you can try a
  different model or prompt on a real incident without touching production.
- **Measured.** `hindcast eval` replays a directory of recorded incidents and
  checks each report. The plan is to publish those results for every change.
- **Local and read-only by default.** No account, no hosted service, no
  telemetry. Credentials in tool output are masked before the model or the
  trace sees them. A tool is offered to the model only if its MCP server marks it
  `readOnlyHint`, or you list it explicitly.
- **Small core.** One Go binary. Integrations are MCP servers you already
  have, not SDKs compiled in.
- **Citations are checked.** The model must cite call IDs for its findings.
  Citations of calls that never happened are dropped, and a confident answer
  with no valid citation is downgraded to low confidence.

## Install

```
go install github.com/hindcast/hindcast/cmd/hindcast@latest
```

Requires Go 1.25.

## Configure

Copy [hindcast.example.yaml](hindcast.example.yaml) to `hindcast.yaml`.

- `model`: `openai` covers any OpenAI-compatible endpoint: OpenAI, Ollama,
  vLLM, Gemini, OpenRouter, Groq and so on. `anthropic` uses Anthropic's
  native API. The example file has a block for each. The key is read from
  the environment variable named in `api_key_env` (the example uses
  `HINDCAST_API_KEY`); it never goes in the file.
- `servers`: MCP servers to start over stdio. Run `hindcast tools` to see
  which tools the read-only policy offers and which it hides. Add a hidden
  tool to `allow` only if you have checked that it does not change anything.
- `prefetch`: tool calls made for every alert before the model starts, so
  the obvious context arrives in one parallel round trip instead of several
  model turns. String arguments are Go templates over the alert:
  `{{.Labels.namespace}}`, `{{rfc3339 (before .StartsAt "30m")}}`.
- `redact`: tool output is masked before the model and the trace see it.
  Built-in rules cover private keys, AWS, GitHub, Slack and OpenAI-style
  keys, JWTs, bearer tokens, passwords in URLs, and `KEY=value` or
  `name:/value:` pairs whose key ends in password, secret, token or api key.
  Add your own regexps under `patterns`. Values with no recognisable shape
  are not caught, for example Secret data under a neutral key name, so do
  not give Hindcast credentials that can read Secrets.
- `notify.slack`: posts each report to a Slack incoming webhook whose URL is
  in the named environment variable.
- `judge`: a model that grades eval results against scenario references.

## Use

```
# Investigate; the trace and the report go to ./hindcast-traces/
hindcast investigate -alert examples/alert.json

# The payload can also come from stdin
hindcast investigate < examples/alert.json

# Receive Alertmanager webhooks on :9095/alerts
HINDCAST_WEBHOOK_TOKEN=... hindcast serve

# Re-run a recorded investigation with whatever model hindcast.yaml names
hindcast replay hindcast-traces/20261120T030012Z-KubePodCrashLooping.jsonl

# Replay every scenario and check the reports; exits 1 if any fail
hindcast eval scenarios/
```

### Alertmanager

```yaml
receivers:
  - name: hindcast
    webhook_configs:
      - url: http://hindcast:9095/alerts
        send_resolved: false
        http_config:
          authorization:
            credentials_file: /etc/alertmanager/hindcast-token
```

`serve` investigates each alert group once. Deliveries for a group that is
queued, running, or was investigated less than `-cooldown` ago (default 1h)
are acknowledged and dropped, so Alertmanager's repeats do not start new
investigations. At most `-workers` investigations run at a time; when the
queue is full the server answers 503 and Alertmanager retries. Without a
token in `HINDCAST_WEBHOOK_TOKEN` anyone who can reach the port can start an
investigation, and the server says so at startup.

## Scenarios

A scenario is a directory with a `trace.jsonl` and a `scenario.yaml` listing
what a correct report must contain:

```yaml
description: Pods OOMKilled after a deploy lowered the memory limit
expect:
  reference: A commit cut the memory limit from 512Mi to 256Mi; the service needs ~400Mi.
  root_cause: ["memory limit", "512Mi.*256Mi"]   # case-insensitive regexps; one must match
  cites: [k8s__events_list]                      # tools the evidence must cite
```

With a `judge` configured, the root cause is graded against `reference` by
that model; without one, against the regexps. Regexps reject correct answers
phrased differently and accept wrong ones that use the right words. A judge
is a model with its own errors: check a sample of its verdicts by hand, use
a different model from the one under test, and report which model judged.

Three scenarios in [scenarios/](scenarios/) are hand-written and synthetic.
`k3s-sandbox-failure` was recorded from a live cluster. All of them check that
the pipeline works end to end; they say nothing about how well any model does
on real incidents. Real, anonymised traces are the most useful contribution
this project can get.

When a replayed model calls a tool with arguments that were not recorded, it
gets an error listing the argument sets that were. That keeps replay usable
across models, but it also tells the model what the original investigation
looked at, which makes eval easier than the live task.

## What is missing

- Results from a real model on real incidents. Nothing here claims a quality
  or speed number, because none has been measured.
- Output to GitHub issues or PagerDuty; reports go to stdout, files and Slack.
- MCP servers over HTTP; only stdio is supported.

## Develop

```
go test -race ./...
```

Design notes are in [docs/design.md](docs/design.md).

## License

Apache 2.0.
