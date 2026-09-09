# Set up the issue tracker

Set up a focused GitHub issue tracker for the repository in the current working
directory. Configure useful issue intake, a compact label taxonomy, and, when
the repository maintains a backlog, one GitHub Project that shows delivery
flow. Perform the work, verify it, and report the result. Do not merely return
instructions.

This prompt requires a configured GitHub remote with an initialized default
branch. If either is missing, stop and run the repository best-practices setup
first. This prompt does not authorize changing repository visibility, source
code, CI, branch rules, security features, releases, deployments, or production
access.

## Inspect before changing

Resolve the repository, its default branch, and the local name of the remote
that targets it. Prefer the GitHub remote selected by current tracking or
default-branch metadata; otherwise use the sole GitHub remote. If multiple
GitHub remotes remain plausible, ask once which repository is intended before
making mutations. Record the chosen name as `GITHUB_REMOTE` and use it for
every GitHub fetch, push, tracking branch, and URL verification. Verify GitHub
CLI authentication and repository access before making mutations.

Fetch and inspect the default branch from `GITHUB_REMOTE`. If committed
issue-form changes are needed, create their focused branch from
`GITHUB_REMOTE/DEFAULT_BRANCH`, not an arbitrary current branch. If local work
prevents a clean switch, use an isolated worktree or stop and explain the
conflict.

Inspect:

- whether Issues are enabled;
- issue forms and issue-template configuration on the default branch;
- every label and its use on open and closed issues and pull requests;
- repository and external automation that depends on exact label names;
- contribution and security files defining support or vulnerability routes.

Build a concise table with: area, current state, proposed change, reason, and
blocked decision. Preserve useful structures. Never delete, rename, or merge
labels until all references and automation are understood.

If there is no continuing backlog, continue with issue forms and labels but
skip all Project inspection, authorization, setup, and verification, then
report the board as not applicable. If there is a continuing backlog, ensure
the token has the `project` scope, using `gh auth refresh -s project` when
needed, and inspect whether Projects are enabled plus any linked Projects,
fields, views, workflows, and current items. If a new Project's owner cannot be
inferred from the repository, ask once for it. Do not guess Project visibility
when it could expose private work.

## Create issue intake

Enable Issues only after the supported forms are live on the default branch.
If Issues are disabled and this work only proposes the forms in an unmerged
pull request, leave Issues disabled and report enabling them as an explicit
post-merge action. If Issues are already enabled, do not disable them. Create
or improve YAML issue forms under `.github/ISSUE_TEMPLATE/`:

- `bug_report.yml`: problem, expected behavior, minimal reproduction, version,
  environment, sanitized evidence, duplicate search, and, only when one exists,
  a reminder to use the verified private security route;
- `feature_request.yml`: user or operational problem, observable outcome,
  constraints, possible approach, alternatives, and sanitized context;
- `task.yml`: outcome, context, acceptance criteria, proof or checks,
  dependencies, and out of scope;
- `config.yml`: disable blank issues when the forms cover supported intake and
  link real security or support routes when they exist.

Do not invent a security email or support site. If no private reporting route
exists, omit the link and report a repository-setup gap. Forms are not active
until merged into the default branch, so distinguish committed files from live
forms.

## Create a compact label taxonomy

Prefer Project fields for workflow state, priority, and effort. Use labels for
work type, routing, and responsibility. Start with:

| Label | Color | Purpose |
| --- | --- | --- |
| `bug` | `d73a4a` | Existing behavior is broken or unsafe |
| `enhancement` | `a2eeef` | New or improved behavior |
| `documentation` | `0075ca` | Documentation-only work |
| `dependencies` | `0366d6` | Dependency updates |
| `needs-agent` | `0E8A16` | The next action belongs to an agent |
| `needs-human` | `FBCA04` | Human review or a decision is required |
| `blocked` | `B60205` | Work cannot progress because of a dependency |

Add `good first issue` and `help wanted` only when a public project welcomes
outside contributors. Add area labels only when someone will use them. Do not
keep redundant pairs such as `bug` and `type:bug`.

When replacing a taxonomy, prepare an old-to-new mapping, migrate every open
issue and pull request, and update forms and automation. Preserve legacy labels
used only by closed work unless the owner explicitly accepts changing that
history. Remove a label only after confirming it has no remaining references.

## Create or update one GitHub Project

Create or link one Project owned by the repository's user or organization. Make
it the single delivery board for this repository and link the two.

Use these Status options in order:

1. `Todo`
2. `Ready`
3. `In Progress`
4. `Blocked`
5. `Review`
6. `Done`

Add `Priority` with `P0`, `P1`, `P2`, and `P3`. Add `Effort` with `XS`, `S`,
`M`, `L`, and `XL` only when the team will use estimates. Retain GitHub's
native assignee, label, milestone, repository, linked pull request, reviewer,
parent issue, and sub-issue progress fields.

Create these views:

- `Pipeline`: board grouped by Status;
- `Backlog`: table filtered to `Todo`, sorted by Priority;
- `Ready`: table filtered to work ready to start;
- `Review`: issues and pull requests awaiting human action.

Add the repository's existing in-scope open issues and pull requests to the
Project before relying on auto-add. Assign their initial Status deliberately.
Then enable built-in workflows to:

- add future matching issues and pull requests from this repository;
- set newly added items to `Todo` when they have no Status;
- set closed issues and merged pull requests to `Done`;
- archive completed items after a reasonable retention period.

Use one automation as the owner of each Status transition. If labels such as
`needs-agent` drive automation, inspect exact filters and prevent competing
workflows from changing Status.

A task is `Ready` only when a fresh agent can finish it without product or
technical questions. It must state the outcome, constraints, acceptance
criteria, checks, dependencies, and what is out of scope.

## Verify and hand off

Validate YAML and inspect the exact diff and commit range. When issue-form files
changed, commit them on the focused branch, push it, and open a pull request
without merging it. When the supported forms are already correct on the
default branch, do not create an empty commit, branch, or pull request; report
those steps as not applicable. Verify through GitHub:

- Issues settings;
- label names, colors, descriptions, and migrated references;
- issue-form syntax and whether each form is proposed or live;
- Project owner, visibility, repository link, fields, option order, existing
  items, views, filters, sorting, and workflows when a Project is applicable;
- a test issue's auto-add and Status behavior only when a disposable test is
  explicitly authorized. Otherwise inspect configuration without adding noise.

Return:

1. `Created or changed`: issue forms and labels, plus applicable Project fields,
   views, workflows, and imported items.
2. `Verified`: YAML checks and API-backed settings.
3. `Migrated`: old-to-new mappings, or `not applicable`.
4. `Gaps`: forms awaiting merge, unavailable features, missing decisions, or
   failed checks, each with the next action.
5. `Links`: repository Issues, pull request, relevant settings, and the Project
   when applicable.

For a repository with a continuing backlog, do not claim the tracker is ready
until the Project is linked and its fields, views, existing items, and workflows
are verified. Without a continuing backlog, verify forms and labels and report
the Project as not applicable. Do not claim issue forms are live while their
pull request remains unmerged.

## Boundaries

Do not change product code, README, architecture, agent policy, CI, Dependabot,
repository visibility, merge settings, branch protection, security scanning,
releases, deployments, environments, webhooks, GitHub Apps, cloud resources,
package publication, or production credentials. Report foundation gaps
separately.

## References

- [Issue and pull request templates](https://docs.github.com/en/communities/using-templates-to-encourage-useful-issues-and-pull-requests/about-issue-and-pull-request-templates)
- [GitHub Projects best practices](https://docs.github.com/en/issues/planning-and-tracking-with-projects/learning-about-projects/best-practices-for-projects)
