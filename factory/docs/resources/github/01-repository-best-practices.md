# Set up repository best practices

Set up the project in the current working directory as a well-maintained GitHub
repository. Make it easy to understand, safe to change, ready for coding
agents, and cheap to keep current. Perform the work, verify it, and report the
result. Do not merely return instructions.

Assume this prompt is being pasted from the root of the target project. The
directory may be empty, may not be a Git repository yet, may have local code
but no remote, or may already be connected to GitHub. Discover its state before
acting. Repository provisioning is in scope. Product implementation is not.

This prompt authorizes creating a missing GitHub repository after its owner and
visibility are resolved. It does not authorize making an existing repository
public, changing ownership, buying a plan, merging a pull request, creating
production credentials, or setting up releases or deployment.

## Resolve the starting state

Inspect the current directory, including hidden files. Determine:

- Git initialization, commits, branches, worktrees, uncommitted changes, and
  ignored and untracked files;
- all configured remotes, which one targets the intended GitHub repository,
  and whether that GitHub repository exists;
- the remote default branch and whether local history has diverged from it;
- the authenticated GitHub account and scopes from `gh auth status`;
- project purpose, stack, manifests, lockfiles, task runners, generated files,
  tests, and supported platforms from local evidence;
- existing instructions, root docs, workflows, GitHub settings, rulesets,
  security features, and open pull requests.

If GitHub CLI is missing or unauthenticated, complete only safe local work, then
stop before remote work and give the exact install or login command.

Handle the discovered state as follows:

1. If Git is not initialized, run `git init -b main`.
2. Resolve the intended repository owner, name, and local GitHub remote before
   creation. Prefer an existing GitHub remote selected by current tracking or
   default-branch metadata; otherwise use the sole matching GitHub remote. Keep
   its exact `OWNER/REPO` target even when that repository does not exist. If
   multiple GitHub remotes remain plausible, ask which target is intended. If
   there is no GitHub remote, use the directory name as the proposed repository
   name and the authenticated GitHub user as the proposed owner. If GitHub must
   be added while `origin` points elsewhere, preserve `origin` and ask whether
   to use a distinct remote name or replace it. Never make either remote change
   without approval. Record the resolved local name as `GITHUB_REMOTE`; use it
   for every GitHub fetch, push, comparison, tracking branch, and remote URL
   verification.
3. If there is no project code or manifest, do not scaffold an application or
   invent commands and architecture.
4. If there is no local commit history and no remote default branch, the
   local unborn branch must be named `main` before the first commit. Run
   `git branch -M main` when necessary. Then the complete initial setup may be
   committed and pushed directly to `main` on `GITHUB_REMOTE` because no
   protected default branch exists. Inspect every file for secrets, local
   configuration, generated output, and large binaries before including it. If
   a remote default branch exists, follow step 6 instead.
5. If local history exists and there is no remote default branch, create the
   GitHub repository when needed, publish the existing default branch to
   `GITHUB_REMOTE` without rewriting it, then make setup changes on a focused
   branch. Rename an unpublished branch to `main` only when no configuration
   depends on its current name.
6. If the remote default branch exists, fetch it from `GITHUB_REMOTE` and compare
   histories. Base the setup branch on `GITHUB_REMOTE/DEFAULT_BRANCH`, not an
   arbitrary current branch. If local work prevents a clean switch, use an
   isolated worktree from the remote default branch or stop and explain the
   conflict. Before publication, confirm that the pull request contains no
   unrelated commits.

Infer facts before asking questions. Ask once, in one compact message, only for:

- a one-sentence purpose when neither code nor docs establish one;
- the intended GitHub target when multiple remotes remain plausible, or the
  owner and repository name when no existing remote or local evidence resolves
  it;
- the remote-name plan when `origin` points somewhere other than GitHub;
- public, private, or internal visibility before creating a GitHub repository;
  offer internal only when the resolved owner supports it;
- an open-source license when a public repository is intended to be open
  source;
- maintainers when code ownership or mandatory reviews depend on them.

Continue with independent work after the answers. If licensing or ownership is
undecided, mark it as `[NEEDS: decision]` and complete independent work. Never
guess legal terms or make a repository public.

When the GitHub repository is missing, create the exact resolved `OWNER/REPO`
with `gh repo create` after resolving visibility. If an existing remote already
matches that target, preserve it, record its name as `GITHUB_REMOTE`, and create
the repository without `--source` or `--remote`; then push through the preserved
remote. If no matching remote or `origin` exists, create from the local source
with `--source . --remote origin` and record `origin`. If no matching remote
exists and a non-GitHub `origin` does, follow the approved remote-name plan. To
preserve it, pass the chosen distinct name with
`--source . --remote <chosen-name>`. To replace it after explicit approval,
create the GitHub repository without `--source` or `--remote`, run
`git remote set-url origin <resolved-github-url>`, and record `origin` as
`GITHUB_REMOTE`. Never overwrite `origin` implicitly. Do not rely on the source
directory name when an existing remote specifies a different repository name.
Do not ask GitHub to generate a README, license, or `.gitignore` that could
conflict with local files. Review local files and verify `GITHUB_REMOTE` before
the first push.

## Operating rules

1. Preserve useful existing material. Never replace a non-empty file with a
   generic template. Update it in place or report the conflict.
2. Treat pre-existing changes in a repository with history as user work. Do not
   stage or commit them unless required for this setup and explicitly included
   in the reported scope.
3. Treat committed files as the source of truth. Avoid duplicating maintained
   instructions across the README, architecture, contribution guide, agent
   files, and wiki.
4. Create the smallest complete setup. Do not add badges, folders, workflows,
   examples, or community files that cannot be accurate today.
5. Use Conventional Commit subjects. Never rewrite shared history or merge a
   pull request unless explicitly asked.
6. Resolve exact repository IDs, branch names, check names, capabilities, and
   plan availability before changing remote settings.
7. Never expose tokens, secrets, private URLs, vulnerability details, customer
   data, or local machine paths in files, logs, commits, or pull requests.
8. Run relevant checks, inspect the final diff, and report exact evidence.

## Audit before changing

Build a concise table with: area, current state, proposed change, reason, and
blocked decision. Cover:

- repository identity, visibility, description, homepage, and topics;
- default branch, merge methods, branch cleanup, rulesets, and access;
- root documentation and agent instructions;
- pull request template, CI, dependency maintenance, and security features.

Detect real commands from manifests, task runners, contribution docs, and
existing automation. Do not infer commands merely from the language ecosystem.

Classify the repository before choosing optional files: private application,
public product, reusable library or CLI, docs or content, or template. Keep the
setup proportional to that classification.

## Establish repository files

Create or improve only applicable files.

Required when accurate:

- `README.md`: project purpose, intended user, status, quickest working path,
  prerequisites, exact setup and test commands, safe configuration guidance,
  and deeper-document links. For an empty project, state what is undecided.
- `.gitignore`: derive it from the real stack and local tooling. Keep example
  configuration trackable and ignore real local configuration.
- `AGENTS.md`: canonical coding-agent policy. Include project purpose, verified
  source map, architectural boundaries, exact build, format, lint, test, and
  generated-file commands, security constraints, change workflow,
  documentation triggers, and definition of done. Omit unverified sections.
- `CLAUDE.md`: keep shared policy in `AGENTS.md`. Default to:

  ```md
  # Claude Code

  @AGENTS.md

  This repository keeps shared agent instructions in `AGENTS.md`. Add only
  Claude Code-specific setup here.
  ```

  Preserve real Claude-specific instructions already present.
- `.github/PULL_REQUEST_TEMPLATE.md`: include `Problem`, `Changes`,
  `Verification`, and `Risks`, then truthful checks for focus, tests, docs,
  independent review, and removal of sensitive data.

Add only when applicable:

- `.editorconfig` when formatting is not fully owned by project tools;
- `ARCHITECTURE.md` when implemented software has meaningful components,
  persistence, protocols, trust boundaries, or deployment topology. Describe
  the verified current system, not a proposal;
- `docs/README.md` when more than one supporting document needs an index;
- `LICENSE` for a public open-source repository, using the owner's choice;
- `CONTRIBUTING.md` when anyone beyond the owner may contribute, with exact
  local setup, checks, and pull request expectations;
- `SECURITY.md` for public projects and projects handling credentials, personal
  data, network access, or privileged execution. Include a private reporting
  route and never invent an email address;
- `CODE_OF_CONDUCT.md` for a public project accepting participation;
- `SUPPORT.md` when support needs a different route from bug reports;
- `.github/CODEOWNERS` when real users or teams own distinct areas;
- `.gitattributes` when line endings, generated files, linguist classification,
  or release archives need explicit behavior.

Keep volatile implementation detail out of `AGENTS.md`. Link to
`ARCHITECTURE.md` for system facts and `CONTRIBUTING.md` for long setup steps.

## Add proportionate CI and dependency maintenance

Create `.github/workflows/ci.yml` only when the repository has a meaningful,
repeatable check. Derive it from verified project commands. Never add a
workflow that merely prints success.

When CI is applicable, it must:

- run on pull requests and pushes to the default branch;
- declare least-privilege permissions, normally `contents: read`;
- set timeouts and use lockfile-respecting installs;
- run the formatting, analysis, tests, and builds the project supports;
- test extra platforms only when they are a real support contract;
- cancel superseded pull request runs;
- expose one stable required job named `check`. Use that name for the sole job
  in a single-job workflow. When several jobs or a matrix sit behind
  protection, make `check` a final aggregator that depends on every required
  job, uses `if: always()`, and fails unless every `needs.*.result` is
  `success`;
- pin third-party actions to verified full commit SHAs with version comments;
- prevent untrusted pull request code from receiving write tokens or secrets.

Create `.github/dependabot.yml` for detected package ecosystems and
`github-actions` when workflows exist. Prefer weekly grouped minor and patch
updates with bounded open pull requests. Keep majors separate. Use Dependabot
or Renovate, not both. Skip this file when there are no dependencies or Actions.

## Configure GitHub settings and security

Use `main` as the default branch for a new project. Configure settings
deliberately:

- disable the wiki when versioned docs are the source of truth;
- enable squash merge and use pull request titles for squash commits;
- disable merge commits and rebase merge unless explicit history policy needs
  them;
- automatically delete merged head branches;
- enable auto-merge and branch updates when supported;
- give GitHub Actions read-only default permissions and prevent Actions from
  approving pull requests unless a reviewed workflow requires it.

Enable available security features when relevant:

- dependency graph, Dependabot alerts, and security updates for dependencies;
- secret scanning and push protection;
- private vulnerability reporting for a public repository;
- CodeQL default setup for a supported implemented language, unless accurate
  advanced setup already exists.

Do not call a feature complete if its first run fails, the plan does not support
it, or the repository has no relevant code.

## Protect the default branch

Create a repository ruleset targeting the default branch. Prefer it over a
legacy branch protection rule. Default rules:

- active enforcement with no routine bypass actor;
- prevent deletion and non-fast-forward pushes;
- require a pull request before merging;
- require all review conversations to be resolved;
- allow only the chosen merge method.

If CI exists, add its `check` status only after it has completed in the
repository, and require the branch to be up to date. If meaningful CI does not
exist yet, activate the other rules and record the status check as follow-up.

For a solo repository, require a pull request but zero mandatory human
approvals. With two or more active maintainers, require at least one approval
and require approval of the most recent reviewable push or dismiss stale
approvals. Require code-owner review only when valid owners exist.

Do not require signed commits, linear history, a merge queue, deployments, or
code-scanning results unless the project can support them. Inspect the ruleset
through the API and, when practical, open a test pull request.

## Verify and hand off

Run every applicable format, lint, test, build, and documentation check.
Validate created YAML, inspect the exact diff and commit range, commit intended
files, and publish them through the safe path above. Verify through GitHub:

- repository identity, visibility, metadata, default branch, and merge settings;
- Actions default permissions;
- latest CI run and exact required check when CI exists;
- ruleset target, enforcement, pull request rule, checks, deletion, and
  force-push behavior;
- enabled security features and their first successful or pending scans;
- the pull request and its checks when applicable.

Return:

1. `Created or changed`: committed files and GitHub settings.
2. `Verified`: exact commands, workflow links, and API-backed settings.
3. `Decisions`: choices made and why.
4. `Gaps`: unresolved decisions, unavailable features, or failed checks, each
   with the next action.
5. `Links`: repository and applicable pull request, Actions, and security pages.

Do not claim the repository is protected, secure, or ready until the relevant
rules and checks are active and verified.

## Boundaries

Do not configure issue forms, labels, a GitHub Project, releases, deployments,
environments, webhooks, GitHub Apps, cloud resources, package publication, or
production credentials. Report them only as optional next steps when relevant.

## References

- [GitHub repository best practices](https://docs.github.com/en/repositories/creating-and-managing-repositories/best-practices-for-repositories)
- [Available repository ruleset rules](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets)
- [Troubleshooting required status checks](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/collaborating-on-repositories-with-code-quality-features/troubleshooting-required-status-checks)
- [Secure use of GitHub Actions](https://docs.github.com/en/actions/reference/security/secure-use)
- [GitHub repository security quickstart](https://docs.github.com/en/code-security/getting-started/quickstart-for-securing-your-repository)
- [About code owners](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners)
