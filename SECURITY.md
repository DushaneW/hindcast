# Security

## Reporting a vulnerability

Report privately through GitHub: **Security → Report a vulnerability** on
this repository. Do not open a public issue.

Include what an attacker can do, how to reproduce it, and the version
(`hindcast version`). You will get an acknowledgement within a week.

## What is in scope

- A way for the model, a tool result or an alert to make Hindcast perform a
  write through an MCP tool the read-only policy should have hidden.
- Credentials reaching the model, a trace or Slack despite matching a
  built-in redaction rule.
- Bypassing the bearer token on `hindcast serve`.
- The installers fetching or running anything other than the release
  archive whose checksum they verified.

## What Hindcast cannot protect against

- An MCP server that marks a writing tool `readOnlyHint`. Run servers with
  credentials that cannot write.
- Credentials with no recognisable shape in tool output. Do not give
  Hindcast access to secrets it does not need.
- Your model provider. Tool output, after redaction, is sent to the model
  you configure.
