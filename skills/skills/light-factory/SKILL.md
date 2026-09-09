---
name: light-factory
description: >-
  Plan an idea too big for one spec, with the user in the loop: research it in subagents, route it into checkpoints, write one checkpoint's PRD with every decision cited, run a fresh critic on it, review it in the browser, freeze it, cut it into pebbles for the dark factory.
  Use when triage says light, when the user says "light factory" or "boulder", or to move a checkpoint that sits in review, fog, or frozen.
user-invocable: true
---

# light-factory

Planning with the user beside you.
One conversation carries the whole chain; the other minds are the research subagents and the critic, every one a fresh context.
You plan. The dark factory builds, when the user says go.

Everything you write goes to the factory server through `~/.claude/skills/light-factory/scripts/`, called `$LF` below.
Read anything in the project. Leave the tree as you found it.

| Word | Means |
|---|---|
| boulder | an idea too big to reason about in one head |
| route | the ordered checkpoints that get there |
| checkpoint | the smallest vertical slice a user can see working |
| PRD | one checkpoint written so completely that a fresh agent builds it without deciding anything |
| stone | one chunk of a frozen checkpoint, the two to four pieces its pebbles group into |
| pebble | one task cut from a frozen PRD, one pull request's worth |

```
idea -> research -> route -> PRD -> critic -> review -> freeze -> pebbles -> /dark-factory
```

Say where you are in the chain whenever you report.

**Voice.** Every document here is read by a critic, then by a builder, and ends up in a public pull request body close to verbatim.
Write `the user`. A person's name, a machine's name, or house vocabulary in a route, a PRD, or a pebble ships to GitHub as written.

## Research

Anything the plan turns on that is not in this repository gets researched before you recommend, not after the user asks you to go look.
A field with a literature, a standard, a third-party API, a data source you plan to ship, a platform limit: all of it.

Research runs in subagents, never in this conversation.
One subagent per question, every one dispatched in a single message so they run at once, every one on Sonnet:

```
Agent(subagent_type: "general-purpose", model: "sonnet",
      prompt: "<one question>. Search the web and read the sources.
               Return at most 400 words: the answer, the strongest evidence for it,
               the strongest evidence against it, and a citation for each.
               Write no files. Do not edit any CLAUDE.md, AGENTS.md, or SKILL.md.")
```

Sonnet because this is reading and summarising, and it is cheap enough to ask five questions instead of two.
You do the deciding, here, from what comes back, and every decision a subagent settled cites its source.

A measurement you took yourself answers an engineering question.
It never settles a question a field's literature already owns; those two get confused exactly when the measurement is interesting.

## Questions

Fog you can settle by reading goes to Research.
Fog only the user holds goes to `grilling`: load it and run it on them before you write, rather than letting the critic find the hole two steps later.

Grill goals before mechanisms.
What they want to be able to do comes before what a screen looks like; one answer to the first can retire a whole round of the second.
Stop when the next answer would not change what you write.

## 1. Route

Read the project, then research, then write the route and save it:

```bash
$LF/planning-save-route <project> <route.md>
```

```markdown
# Route: <the boulder in five words>

## Boulder
The idea in the user's own words, then one paragraph restating it as an outcome. At most 120 words.

## Checkpoints
1. <name>: <what a user can do after it that they cannot today>. At most 30 words.

## Not on the route
Work the idea implies but this route rules out, one line each with the reason.
```

Order checkpoints so each is usable alone and teaches the next; the first is the smallest real thing.
A checkpoint that bundles a data pipeline, a screen, audio and a settings page is the oversized finding already baked in at step one; cut it here, where it is free.
Only the next unbuilt checkpoint gets a PRD, because building N changes what N+1 should be.
A route that exists is read, and its next planned line is where you start.

Done when the Planning page shows the project and one row per checkpoint.

## 2. PRD

Grill the open questions first. Write it in the conversation and save it every time it changes:

```bash
$LF/planning-save-prd <project> <n> <prd.md>
```

```markdown
# Checkpoint <n>: <name>

Project: <project>
Status: planned

## Slice
One paragraph, at most 200 words. What works after this that did not before, from the user's side.

## Decisions
D1. <statement>. Cited: <file:line, doc, research source, experiment, or review answer with date>
D2. <statement>. Not yet specified: <what is unknown, what would settle it>

## Failure modes
What breaks, what the user sees, what the system does. One line each.

## Acceptance checks
Exact commands, then the checks the user runs on the thing itself.

## Credentials
<host>: <purpose the build itself needs>. Never a value. Or the bare word: none

## Experiments
Ran: <what, result, where it lives>. Proposed: <what it settles, cost, time>. Or: none.

## Questions
Q1. <title>: <the question, with choices when there are choices>
    Recommended: <your answer>

## Critique log
Round 1, F1: accepted, <what changed> | rejected, <reason>
```

Six rules govern the text:

- **Certainty.** Every decision is cited or marked Not yet specified. Cited means a reader opens the citation and sees the decision follow from it. A guess in a decision's clothing is the defect the critic always finds. A bare number is the usual one: twenty cards, eight words, thirty seconds. Cite it or mark it.
- **Proof.** A check written from a decision can only catch a build that disagrees with the plan. It can never catch a plan that disagrees with the user, and that is the failure that reaches them. So the Acceptance checks carry at least one check written from the Slice in the user's own words, and at least one the user performs on the running thing. Data the product ships gets a check on whether a person can read it, not only on whether it is present and non-empty.
- **Split.** Estimate the pebbles: one pull request with a 250 to 500 word spec each. More than five means cut at a decision boundary, keep the smaller front half, and push the rest back to the route as a new line.
- **Fog.** A Not yet specified item the user can answer on the spot is a question with your recommendation. One that needs reading, a prototype, or a longer conversation is fog. More than a handful of fog items is the split rule again.
- **Experiment.** Run one unasked only when it takes minutes, spends nothing new, and changes a decision. Anything with a bill or an afternoon in it is proposed and waits for the review.
- **Rabbit hole.** Two revisions that change the approach at its root mean stop: both approaches go in Questions with a recommendation, and the review chooses.

Close the questions you can close before you send it to the critic; a PRD with four open questions spends a critic round rediscovering them.

Done when every decision is cited or marked and the split estimate is five or fewer.

## 3. Critic

```bash
$LF/critic-submit <project> <n> --round <r>
```

The PRD goes to the Critique pipeline on the mini as factory Work and the script waits, minutes usually.
Say so, and leave the PRD alone until the findings print: undecided dressed as decided, missing failure modes, uncited claims, oversized scope, as JSON.

Take each finding: change the PRD and log `accepted, <what changed>`, or log `rejected, <reason>`.
A decision a finding disproves is downgraded to Not yet specified, never deleted.
A finding still open after two rounds becomes a question with your recommendation.
Run another round only when the revision changed something a critic would see; two rounds is usual, a third needs a reason you can say.

Then:

```bash
$LF/planning-set-status <project> <n> review
```

Done when the last round's findings are all logged and the status is review.

## 4. Review, the user's gate

Load `lavish` and follow it.
One page: the slice, the decisions with citations, the questions with your recommendations as the thing to answer, and the cost so far from `$LF/planning-show <project> <n>`.
Write it to `~/.factory/light-factory/review/<project>-<n>.html`, open it, say the URL in your next message, then poll in the foreground.
One review at a time.

**A checkpoint with a screen in it gets a prototype on that page.** Not a mockup and not a description: the actual screens, clickable, with real data where you have it, built throwaway and thrown away. Every decision about what a user sees, touches, or hears is a guess until they have looked at one.
Sixty seconds with a running thing overturns decisions that survived four critic rounds, and it costs one cheap task here against a rebuild later.
Say plainly that it is a prototype and that nothing in it is the build.

Feedback lands in this conversation.
Apply it to the PRD, save the PRD, and save what they said:

```bash
$LF/planning-save-answers <project> <n> <answers.md>
```

Render again only when the change is worth re-reading; otherwise say what changed and ask if they want it rendered.

Done when every question has an answer or was sent to research.

## 5. Freeze

On the user's word.
Each answered question becomes a decision cited as `review answer <date>`.
Each question sent to research becomes a line under a `## Fog` heading, phrased as a question one session can answer.

Freezing is one way. The server refuses to edit a frozen plan and refuses to move a frozen checkpoint back to review, so everything below happens before the status changes, not after.

Read the Credentials section aloud to yourself: it is the bare word `none`, or one `host: purpose` line per host, and nothing else. A sentence explaining why there are no credentials is read as a hostname. Put that explanation in the Slice.

```bash
$LF/preflight <project> <n>
```

Every host the PRD names must already have a rule in the vault, by name only; no value passes through you. A MISSING line stops here until the user adds the rule.

Save the PRD, then:

- Fog empty, every decision cited, preflight green: `$LF/planning-set-status <project> <n> frozen`
- Otherwise: `$LF/planning-set-status <project> <n> fog`, read the Fog lines to the user, and stop here. Freeze again when the answers come back.

A frozen PRD is final; a change to it is a new checkpoint on the route.

## 6. Pebbles

From a frozen PRD only.
Cut tasks so that each is one pull request a fresh agent finishes without deciding anything, names the decisions it implements by id, depends only on earlier tasks, and the last is the closure task that runs the Acceptance checks and writes the decision record.
Group them into two to four stones when the checkpoint has natural chunks, one otherwise.
Six or more tasks is `oversized`: stop and propose the cut.
A task that needs a decision the PRD lacks is fog: stop and say which.

Task shape, exactly, 250 to 500 words, ceiling 700:

```markdown
## <Plain action and result>

### What are we building?
One to three short sentences: what is wrong or missing, and what works after this task.

### Why?
One or two short sentences on the practical value to a user, operator, or developer.

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

The first sections are for the user, readable in a minute, plain language.
Agent notes carry the decisions copied from the PRD, not paraphrased.
Every test named under How to check exists or is created by name in this task; a pattern that matches nothing passes on silence.
How to check names the repository's own check commands in full, the ones its `AGENTS.md` or contributing guide names, not the subset you judged relevant. A formatter, a generated artifact, or a lint the repository runs and your spec omits is a green pipeline and a red default branch.

Save the cut as one batch, files named `NN-<slug>.md` in build order, with an optional `stones.json` beside them naming each stone and the slugs it holds:

```bash
$LF/planning-save-pebbles <project> <n> <tasks-dir>
```

Done when the pebbles are saved.

## 7. Hand over

Load `dark-factory`.
Present the pebbles as the batch it describes; a frozen PRD approved the plan, and the batch presentation is where the user approves the specs.

**The factory has no dependencies, so submit in waves.** Admitted work runs in any order, and every pebble already names what it depends on. Read those lines as a graph, not a chain: a wave is every task whose dependencies are all merged, and the whole wave goes at once. Wait for the wave, merge what is green, then send the next wave.

A five task checkpoint is usually three waves, not five submissions. Serialising tasks that never touched each other costs a full pipeline round trip each for nothing.

**Watch it without being asked.** After every submit, start the wait in the background so its exit brings you back:

```bash
while :; do
  s=$($DF/factory-status <run_id> | awk '$1=="state"{print $2; exit}')
  case "$s" in blocked|queued|running) sleep 60 ;; *) break ;; esac
done
$DF/factory-status <run_id>
```

Run it with `run_in_background`, one wait per run in the wave, and report the moment each returns.
Verify a pull request yourself before merging: read the body, check the pushed range, read `checks` in the status output, and run the repository's own check commands when it says none were recorded. Merge what is green, then send the next wave.
The user should never have to ask what the factory is doing.

When the closure task's pull request merges, the checkpoint is built once its checks have actually been run, both halves:

1. Run every command under Acceptance checks yourself and read the output. A closure task reporting success is not the same as the checks passing.
2. Put the running thing in front of the user. Load `run`, get it going, and walk them through the manual checks one by one.

```bash
$LF/planning-set-status <project> <n> built
```

A pebble you built here rather than sending to the factory has no Work row, so record what carries it or it reads `planned` on the Planning page forever:

```bash
$LF/planning-set-pebble-built <project> <n> <slug> <commit sha or pull request url>
```

A checkpoint whose manual checks nobody ran is not built, however green the pipeline was.
What they say while watching it run is the most valuable feedback in the chain and is usually a decision to amend: record it as a new decision that names the one it amends, or as a new line on the route.

The next checkpoint starts at step 2.

## Where things stand

```bash
$LF/planning-show <project>       # every checkpoint: status, critic rounds, cost
$LF/planning-show <project> <n>   # one checkpoint: PRD body and answers
```

The cockpit's Planning page shows the same, so the user can look without asking.

## Writing

Lead with the outcome.
One sentence per line in the documents.
Plain words a stranger would know.
Cite or mark.
