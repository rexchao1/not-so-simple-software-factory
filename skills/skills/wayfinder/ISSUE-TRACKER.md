# Issue tracker: GitHub

Maps and their tickets live as GitHub issues.
Use the `gh` CLI for every operation; it infers the repo from `git remote -v` when run inside a clone.

## Conventions

- **Create**: `gh issue create --title "..." --body "..."`, using a heredoc for multi-line bodies.
- **Read**: `gh issue view <number> --comments`.
- **List**: `gh issue list --state open --json number,title,body,labels,assignees`.
- **Comment**: `gh issue comment <number> --body "..."`
- **Label**: `gh issue edit <number> --add-label "..."` or `--remove-label "..."`
- **Close**: `gh issue close <number> --comment "..."`

GitHub shares one number space across issues and pull requests, so a bare `#42` may be either.
Resolve it with `gh issue view 42` and fall back to `gh pr view 42`.

## Wayfinding operations

- **Map**: a single issue labelled `wayfinder:map`, holding the Destination / Notes / Decisions-so-far / Not-yet-specified / Out-of-scope body.
  Create it with `gh issue create --label wayfinder:map`.
- **Child ticket**: an issue linked to the map as a GitHub sub-issue, via `gh api` on the sub-issues endpoint.
  Where sub-issues are not enabled, add the child to a task list in the map body and put `Part of #<map>` at the top of the child body.
  Label it `wayfinder:<type>`, one of `research`, `prototype`, `grilling`, `task`.
- **Blocking**: GitHub's native issue dependencies, which is the canonical and UI-visible representation.
  Add an edge with `gh api --method POST repos/<owner>/<repo>/issues/<child>/dependencies/blocked_by -F issue_id=<blocker-db-id>`.
  `<blocker-db-id>` is the blocker's numeric database id from `gh api repos/<owner>/<repo>/issues/<n> --jq .id`, not its `#number` and not its `node_id`.
  GitHub then reports open blockers under `issue_dependencies_summary.blocked_by`, which is the live gate.
  Where dependencies are unavailable, fall back to a `Blocked by: #<n>, #<n>` line at the top of the child body.
  A ticket is unblocked when every blocker is closed.
- **Frontier query**: list the map's open children, drop any with an open blocker or an assignee, and take the first in map order.
- **Claim**: `gh issue edit <n> --add-assignee @me`, which is the session's first write.
- **Resolve**: `gh issue comment <n> --body "<answer>"`, then `gh issue close <n>`, then append a context pointer to the map's Decisions-so-far.
