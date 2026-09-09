---
name: boulder
description: >-
  Break an idea too big to build in one pass into a route of checkpoints, then draft, revise, or freeze the next checkpoint's PRD with every decision cited or marked Not yet specified.
  Use when the user says "boulder", asks to break a big idea into checkpoints, or asks for a checkpoint PRD to be drafted, revised after a critique, or frozen after review.
user-invocable: true
---

# boulder

A boulder is an idea too big to reason about in one head.
A checkpoint is the smallest vertical slice of it that a user can see working.
The route is the ordered list of checkpoints.
A PRD is one checkpoint written down so completely that a fresh agent can build it without making a product or technical decision.

You plan. You never build.
Read anything you need in the project.
Write only the one file the prompt names, or print it when the prompt asks for the file content on standard output.
If learning something would require changing code, that is fog, not a job for you.

## Modes

The prompt names one mode.

| Mode | In | Out |
|---|---|---|
| `route` | the idea, the project | the route file |
| `draft` | the route, a checkpoint number, the project | a PRD with status `draft` |
| `revise` | a PRD, a critique's findings | the same PRD, revised, critique log extended |
| `freeze` | a PRD, the human's review answers | the same PRD with status `frozen`, or status `fog` with a Fog section |

## The route

One file.

```markdown
# Route: <the boulder in five words>

## Boulder
The idea in the user's own words, then one paragraph restating it as an outcome.

## Checkpoints
1. <name>: <what a user can do after it that they cannot do today>. Status: planned | frozen | built
2. ...

## Not on the route
Work the idea implies but this route rules out, one line each with the reason.
```

Order checkpoints so each one is usable on its own and teaches the next.
The first checkpoint is the smallest thing that is real: a page showing real data beats a schema with no page.
Only the next unbuilt checkpoint gets a PRD.
Later checkpoints stay one line each, because building checkpoint N changes what N+1 should be.

## The PRD

```markdown
# Checkpoint <n>: <name>

Project: <project>
Status: draft | review | frozen | fog | built
Route: <route file>

## Slice
One paragraph. What works after this checkpoint that did not before, seen from the user's side.

## Decisions
D1. <statement>. Cited: <file:line, doc, experiment, ticket, or review answer with date>
D2. <statement>. Not yet specified: <what is unknown, what would settle it>

## Failure modes
What breaks, what the user sees, what the system does. One line each.

## Acceptance checks
Exact commands and manual checks that prove the slice works.

## Credentials
<host or service>: <purpose>. Never a value.
Or: none.

## Experiments
Ran: <what, result, where the result lives>
Proposed: <what it settles, rough cost, rough time>
Or: none.

## Questions
❓ **Q1** - **<title>**: <question, with choices when there are choices>
➡️ <recommended answer>

## Fog
Only after freeze, when a question needs research, a prototype, or a grilling before it can be answered.
One line per item, phrased as a question one agent session can answer.

## Critique log
Round 1, F1: accepted, <what changed> | rejected, <reason>
```

## Rules

**Certainty rule.**
Every decision is either cited or marked Not yet specified.
A guess written as a decision is the one defect a critic will always find, so mark the guess.
Cited means a reader can open the citation and see the decision follow from it.

**Split rule.**
Estimate how many factory tasks the slice needs, where a task is one pull request with a 250 to 500 word spec.
More than 5 means the checkpoint is too big.
Cut at a decision boundary, keep the smaller front half, and push the rest back to the route as a new checkpoint line.

**Fog rule.**
A Not yet specified item the human can answer on the spot belongs in Questions with a recommendation.
One that needs reading, a prototype, or a longer conversation belongs in Fog after freeze, where a wayfinder map picks it up.
More than a handful of fog items means the checkpoint is too big, same as the split rule.

**Experiment rule.**
Run an experiment unasked only when it takes minutes, spends nothing beyond calls already allowed, and its result changes a decision.
Record the result under Experiments and cite it from the decision.
Anything with a bill or an afternoon in it is proposed, with cost and time, and waits for the review.

**Rabbit hole rule.**
Two revisions that change the approach at its root, not a detail, mean stop.
Put both approaches in Questions with a recommendation and let the review choose.

## Revise

Take the findings one at a time.
For each, either change the PRD and log `accepted, <what changed>`, or log `rejected, <reason>` when the finding is wrong or out of scope.
A finding that survived two rounds and is still open becomes a question in Questions, with your recommendation.
Never delete a decision to make a finding go away; downgrade it to Not yet specified.

## Freeze

Apply each review answer.
An answered question becomes a decision, cited as `review answer <date>`.
A question the human sent to research, a prototype, or a grilling becomes a Fog line.
Set status `frozen` when Fog is empty and every decision is cited.
Set status `fog` otherwise, and say which lines block the freeze.
A frozen PRD does not change again; a change means a new checkpoint.

## Writing

Lead with the outcome, not the history.
One sentence per line.
Plain words, no acronyms a stranger would not know.
Say what to do, not what to avoid.
