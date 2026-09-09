# Contributing

Factory uses Go for the control plane and workers, and React with TypeScript for
the embedded UI.

Before starting a change, search existing issues, keep the scope focused, and
do not include credentials, private repository data, or sensitive task content.
Report vulnerabilities through [SECURITY.md](SECURITY.md), not a public issue.
Use [WORKFLOW.md](WORKFLOW.md) for project statuses and repository labels.

## Setup

Install:

- Go 1.25.13 or newer on the 1.25 release line, or Go 1.26.6 or newer
- Git
- just
- Node.js 22.22.2 or newer on the 22 release line, Node.js 24.15.0 or newer,
  or Node.js 26 or newer, plus npm, when changing the UI
- Codex CLI or Claude Code CLI for real worker tests

Build the Go binaries from the committed UI:

```sh
just build
```

Rebuild the UI only when its source changes:

```sh
just ui-install
just ui-build 0
```

## Checks

Run backend, tooling, and launcher checks:

```sh
just format-check
just vet
just vuln
just boundary
just test
just test-worker-race
just test-tooling
just test-launcher
```

`format-check` and `format` run the gofmt bundled with the Go release named in
`go.mod`.
Any local Go toolchain therefore gives the same answer as CI.

Run UI checks:

```sh
just ui-check
just test-browser
```

`web/dist` is committed because it is embedded in `factory-server`. If UI source
changes, rebuild it and commit the generated assets. An operator build must not
run Node or npm.

The release inventory treats every non-development package in
`web/package-lock.json` as shipped code. When that set changes, update the
matching license mapping and committed license text under `third_party/npm`,
then run `just test-release`. The check fails if a production package is
missing or if the mapping contains stale entries.

## Dependency updates

Dependabot checks Go and npm dependencies each week and GitHub Actions each
month. Minor and patch updates are grouped to reduce pull-request noise; major
updates remain separate so their compatibility impact is visible. Review the
upstream release notes and keep `go.mod`, `go.sum`, `web/package.json`, and
`web/package-lock.json` in sync with the change.

Workflow actions must use a full commit SHA followed by a version comment, for
example `owner/action@0123456789abcdef0123456789abcdef01234567 # v1.2.3`.
Dependabot updates both the SHA and comment. Do not replace a pinned SHA with a
mutable tag.

Before merging an npm update, run:

```sh
cd web
npm audit --omit=dev
```

The weekly dependency-audit workflow runs the same production audit. Treat a
failure as a security maintenance task; confirm exploitability and update or
mitigate the dependency rather than suppressing the audit without evidence.

## Pull requests

- Keep changes focused.
- Include tests for changed behavior.
- Update `ARCHITECTURE.md` when current boundaries or contracts change.
- Use Conventional Commit messages.
- Explain what was verified, including browser checks for UI work.
- Do not commit credentials, local worker configuration, databases, or retained
  worktrees.

Changes to `main` must go through a pull request and pass the required `check`
status. Maintainers should follow the [maintainer guide](docs/maintainers.md)
when changing repository policy or recovering from a broken required check.

By participating, you agree to follow the
[Code of Conduct](CODE_OF_CONDUCT.md). Contributions use the project's
[MIT License](LICENSE).
