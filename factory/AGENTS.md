# factory

The control plane and workers are Go. The cockpit is React and TypeScript under `web/`.
[CONTRIBUTING.md](CONTRIBUTING.md) is the full guide; this file carries only what a change here will break if you do not know it.

## What reaches GitHub

Pull request bodies and commit messages here are public.
Write `the user`. Never a person's name or a machine's, including one picked up from a home instructions file on whichever machine you happen to be running on.

## The committed UI bundle

`web/dist` is committed and embedded into `factory-server` by `web/embed.go`.
A change under `web/src` does not reach anyone's browser until the bundle is rebuilt and committed alongside it.

```sh
cd web && npm ci && npm run build   # then commit web/dist with the source change
```

Rebuild it in the same change, never as a follow-up: a merge that carries source without the bundle passes every check and ships nothing.
An operator build must not run Node or npm, which is the reason the bundle is committed at all.

## The critique prompt

`internal/controlplane/critique_prompt.md` is embedded into the server and is a copy of the `checkpoint-critic` skill's body with its frontmatter removed.
Change one and the other drifts, so change both, and keep them identical below the frontmatter.

## Shipped npm packages

The release inventory treats every non-development package in `web/package-lock.json` as shipped code.
When that set changes, update the license mapping and the committed license text under `third_party/npm`, then run `just test-release`.

## Checks

`just` is the entry point for all of them, and CONTRIBUTING.md lists the full set.
The ones a change usually needs:

```sh
just format-check
just vet
just test
just ui-check      # lint, typecheck, and component tests
```

`format-check` uses the gofmt bundled with the Go release named in `go.mod`, so a local run and CI agree.
Run the checks that cover what you touched and read their output; a stage that reports success is not the same as a check that passed.
