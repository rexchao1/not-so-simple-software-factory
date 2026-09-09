---
name: triage
description: >-
  Decide what a request for software work gets: done here in the conversation, a spec sent to the dark factory, or a planning session in the light factory.
  Use at the start of every request for work, before reading code for it.
user-invocable: false
---

# triage

Three outcomes.
Pick one before doing anything else, and say which.

| Outcome | Means |
|---|---|
| **here** | You do it now, in the real checkout, with your normal tools. |
| **dark** | You write a spec and send it through `/dark-factory`. A worker builds it and opens a pull request. |
| **light** | It is bigger than one spec. `/light-factory` plans it with the human before anything is built. |

Treating every request the same is how a factory becomes slower than a terminal.
The factory is for work that benefits from running without you, not for everything.

## The rule

Start from the five tests.
A request passes a test or it does not; no partial credit.

1. **One file.** The change is confined to a single source file, plus that file's existing test file.
2. **No new public interface.** No new exported function, type, endpoint, flag, config key, schema column, migration, or event. Changing the body of an existing one is fine.
3. **No unresolved choice.** Nothing in the request leaves a choice open that would change behavior, interfaces, data, security, scale, performance, compatibility, operations, cost, or proof.
4. **An existing check proves it.** The repository's test command already covers the changed behavior, or the request names the command that will.
5. **An end state, not a goal.** "Rename X to Y" is an end state. "Make login less confusing" is a goal.

Then:

- **All five pass, and the human is here with you:** `here`. Do it, run the check, show the diff. They are the human in the loop.
- **All five pass, but it is one of several, or they said to queue it:** `dark`, with `--assurance fast`.
- **Any test fails, and one spec of 250 to 500 words can hold the whole thing:** `dark`. Write the spec, show it, submit on approval.
- **Any test fails, and it needs more than one spec, or test 3 fails on a question only the human can settle:** `light`.

Ties break upward: `here` to `dark`, `dark` to `light`.
Skipping a gate is the expensive mistake; a minute of planning is the cheap one.

## Worked examples

| Request | Tests | Outcome |
|---|---|---|
| "the error message says Helo, fix the typo" | all pass | here |
| "fix that typo, and the three others in the same file, while I go to lunch" | all pass, queued | dark, fast |
| "add a farewell function next to greet" | fails 2 | dark |
| "greet should reject invalid input" | fails 3, "invalid" undefined | dark, the spec names the choice; light if they cannot say |
| "make the dashboard faster" | fails 1, 3, 5 | light |
| "the planning state should live in the server" | fails everything | light |

## When you are wrong

When something comes back wrong in a way triage should have caught, say so and propose the change to this rule.
The rule is meant to be revised from real misclassifications, not defended.
