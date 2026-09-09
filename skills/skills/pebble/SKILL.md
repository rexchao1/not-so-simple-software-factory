---
name: pebble
description: >-
  Turn a frozen checkpoint PRD and its settled decisions into ordered factory tasks in blueprint's shape, one pull request each, ending in a closure task.
  Use when the user says "pebble", or a frozen checkpoint PRD needs factory tasks.
user-invocable: true
---

# pebble

A frozen checkpoint PRD holds every decision a builder needs.
Pebble cuts it into tasks a coding agent can finish one at a time, each as one pull request.
You plan. You never build.
Read anything you need. Write only the task files the prompt names, or print them when the prompt asks for standard output.

## Preconditions

Stop and say so when any of these fails.

- The PRD status is `frozen`.
- The PRD has no Fog lines, and every decision is cited.
- The prompt says no ticket is open for this checkpoint.

## Cut

Walk the Slice and the Decisions.
Group the work into tasks so that:

- each task is one pull request a fresh agent finishes without a decision;
- each task names the decisions it implements by id;
- the order is buildable, so a task depends only on earlier tasks;
- the last task is the closure task, which runs the PRD's Acceptance checks and writes the decision record for the checkpoint.

More than 5 tasks including closure means the checkpoint is too big.
Stop, report `oversized`, and propose the cut at a decision boundary.
Never invent a decision the PRD does not contain: a task that would need one is fog, so report it and stop.

## The task shape

Use blueprint's task shape exactly.
Do not invent headings.

```markdown
## <Plain action and result>

### What are we building?
In one to three short sentences, say what is wrong or missing and what will work after this task.

### Why?
In one or two short sentences, explain the practical value to a user, operator, or developer.

### Done when
- Three to seven observable results.

### How to check
Exact commands and required manual checks.

### Agent notes
- Depends on: <task titles, or None>
- Source: <the frozen PRD, and the decision ids this task implements>
- Only the definitions, decisions, constraints, and failure behavior specific to this task.

### Out of scope
- Related work this task is likely to absorb by mistake.
```

Aim for 250 to 500 words.
Hard ceiling 700.
If it will not fit, it is more than one task.

The first sections are for the user and must be readable in under a minute: no requirement ids, no acronyms, no implementation detail.
A task reaches the public pull request body close to verbatim: write `the user`, never a person's name, a machine's name, or house vocabulary.
How to check names the repository's own check commands in full, the ones its `AGENTS.md` or contributing guide names, not the subset you judged relevant. A formatter, a generated artifact, or a lint the repository runs and the task omits is a green pipeline and a red default branch.
Decisions, interfaces, and constraints go in Agent notes, copied from the PRD, not paraphrased.
Every test name and `-run` pattern under How to check must match a test that exists in the clone or one this task creates, and a new one is named with its file.
A pattern that matches nothing passes on silence, so the builder never learns the check was empty.

## Output

One file per task, numbered in build order: `01-<slug>.md`, `02-<slug>.md`, and so on, closure last.
When the prompt asks for standard output, print the files in order, each preceded by a line `=== <filename>`.
End with a `Credentials:` line listing the hosts from the PRD's Credentials section, or `Credentials: none`, so a preflight can check the vault before anything is submitted.
