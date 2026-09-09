# Global reusable phases for Factory

> **Status:** Superseded by the
> [Reusable Workflows and typed Automations design](../workflows/design.md),
> which is itself superseded for future work by the
> [Software Factory target architecture](../software-factory/design.md).

The Phase proposal was never implemented and is no longer an active Factory
design. The accepted product term is **Workflow**: versioned Markdown
instructions that can be selected for an ordinary Task. A Workflow is not a
pipeline stage and does not imply ordering, transitions, or approval state.
The Workflow library, Task snapshot contract, and typed Automations are
implemented.

Typed Automations are defined in the replacement design. One Automation binds
one Workflow to one managed repository and exactly one `github_issue`,
`github_pull_request`, or `schedule` Trigger. One durable Occurrence links
directly to at most one ordinary Task.

There is no Phase API, Phase schema, generic Queue, multi-repository Run, Run
target, runtime provider plugin, DAG, workflow chain, or approval engine to
implement or migrate. Historical discussion remains available in Git history.
