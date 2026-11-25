# Design

## Goal

Given an alert, produce a root cause the on-call engineer can verify in a
minute, and make the quality of that answer measurable over time.

Most AI SRE tools can be demoed but not evaluated: an investigation touches
live systems, so it cannot be repeated, compared across models, or turned
into a regression test. Hindcast is built around fixing that. Everything else
is kept small.

## Flow

```
alert ──► prefetch (parallel) ──► model loop ──► submit_report ──► report
               │                      │
               └──────── tool.Box ────┘
                     │            │
               redact.Box    trace.Replayer
                     │       (recorded results)
                  mcpbox
               (live MCP)
                     │
                trace.Writer ──► trace.jsonl
```

1. **Prefetch.** Configured calls, templated from the alert's labels and
   start time, run concurrently. They appear to the model as one assistant
   turn with ordinary tool calls, so it can cite them like any other call.
   A call whose template refers to a missing label is skipped with a warning.
2. **Loop.** The model calls tools until it calls `submit_report`. Calls in
   one turn run concurrently. Each result is prefixed with a call ID
   (`[call c3]`) that the model cites. Results above `max_result_bytes` are
   clipped for the model; the trace keeps them whole.
3. **Report.** `submit_report` is validated: unknown call IDs are dropped
   with a warning, and medium or high confidence without valid evidence is
   lowered to low. An invalid submission is returned to the model as a tool
   error so it can correct it.

The loop ends with an error if the model exhausts `max_steps`, or stops
twice without calling a tool. The last step tells the model to submit.

## Packages

| Package       | Responsibility                                           |
|---------------|----------------------------------------------------------|
| `tool`        | `Box` interface: list tools, call one                     |
| `mcpbox`      | `Box` over MCP sessions; read-only policy                 |
| `trace`       | JSONL trace format; `Replayer` is a `Box` over a trace    |
| `llm`         | Neutral chat types; OpenAI-compatible and Anthropic HTTP  |
| `investigate` | Prefetch, the loop, report validation                     |
| `eval`        | Load scenarios, replay them, check reports                |
| `redact`      | Masks credentials in tool output; a `Box` wrapper         |
| `notify`      | Slack incoming webhook                                    |
| `server`      | Webhook endpoint, queue, per-group dedupe and cooldown    |
| `alert`       | Alertmanager webhook payloads                             |
| `config`      | `hindcast.yaml`                                           |

The investigator depends only on `tool.Box` and `llm.Model`. That is what
makes live runs, replays and tests the same code path.

## Decisions

**Go, one binary.** Fast start, trivial distribution, and the Kubernetes and
Prometheus ecosystems are Go. The cost is a thinner LLM library ecosystem;
two HTTP clients written against the documented APIs cover it.

**MCP instead of built-in integrations.** Maintaining dozens of vendor SDKs
is most of the work in comparable projects and most of their dependency
weight. MCP servers already exist for the systems that matter. The cost is
that tool quality, naming and read-only annotations vary by server, which is
why `hindcast tools` exists and the allow list is explicit.

**Read-only by annotation.** The policy trusts `readOnlyHint` from the
server. A server that lies about it defeats the policy; prefer servers that
also have their own read-only mode, and run them with credentials that
cannot write.

**Redact before the trace, not only before the model.** Traces are meant to
be shared as scenarios, so they must not hold what the model was not allowed
to see. The cost is that a replay cannot show what a redacted value was.

**Two graders.** Regexps are cheap, deterministic and crude. A judge model
grades meaning, but it is a model: its verdicts need spot checks by hand
before a pass rate means anything, and a judge from the same family as the
model under test may share its blind spots.

**One investigation per alert group.** Alertmanager re-sends firing groups
on every group and repeat interval. `serve` keys on `groupKey` and ignores
repeats while a group is queued, running or within the cooldown.

## Not yet decided

- How eval runs in CI for pull requests from forks, which do not get secrets.
  Likely: maintainers trigger it, results posted as a comment.
- Whether to support MCP over HTTP in addition to stdio.
