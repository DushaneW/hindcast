---
name: Incident scenario
about: Contribute a recorded incident for eval
labels: scenario
---

**What happened**
The real root cause, as the people who fixed it would describe it.

**Trace**
Attach `trace.jsonl`. Check it for anything you would not publish: hostnames,
customer data, credentials the redaction rules missed.

**Expectations**
Proposed `scenario.yaml`: `reference`, `root_cause` regexps, and the tools the
evidence must cite.
