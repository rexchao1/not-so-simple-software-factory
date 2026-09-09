---
name: improve-codebase-architecture
description: Scan a codebase for deepening opportunities, present them as a reviewable artifact, then grill through whichever one you pick. Use when the user asks for an architecture review, wants to find refactors that make code more testable or navigable, or asks what to restructure.
user-invocable: true
disable-model-invocation: true
---

# Improve Codebase Architecture

Surface architectural friction and propose **deepening opportunities** - refactors that turn shallow modules into deep ones.
The aim is testability and AI-navigability.

Everything here is built on a shared design vocabulary:

- Call the Skill tool with "codebase-design" for the architecture vocabulary (**module**, **interface**, **depth**, **seam**, **adapter**, **leverage**, **locality**) and its principles (the deletion test, "the interface is the test surface", "one adapter = hypothetical seam, two = real").
  Use these terms exactly in every suggestion - don't drift into "component," "service," "API," or "boundary."
- If the repo keeps a domain glossary, its terms name the good seams; if it keeps decision records, those record decisions this skill should not re-litigate.
  Read both before scanning, and skip this quietly when the repo has neither.

## Process

### 1. Explore

**Scope before you scan - YAGNI.**
Deepening a module pays off by making future changes to it easier, so put extra weight on the parts of the codebase that have recently changed.
Decide *where* to look before you look:

- If the user named a direction - a module, a subsystem, a pain point - take it, and skip the inference below.
- Otherwise, walk back a good stretch of the commit history (`git log --oneline`) to find the codebase's hot spots - the files and areas that keep coming up - and let those paths pull your attention first.
  If the changes are scattered with no clear hot spot, widen the net.

Then walk the codebase.
Don't follow rigid heuristics - explore organically and note where you experience friction:

- Where does understanding one concept require bouncing between many small modules?
- Where are modules **shallow** - interface nearly as complex as the implementation?
- Where have pure functions been extracted just for testability, but the real bugs hide in how they're called (no **locality**)?
- Where do tightly-coupled modules leak across their seams?
- Which parts of the codebase are untested, or hard to test through their current interface?

Apply the **deletion test** to anything you suspect is shallow: would deleting it concentrate complexity, or just move it?
A "yes, concentrates" is the signal you want.

### 2. Present the candidates for review

Call the Skill tool with "lavish" and build the candidate set as one reviewable artifact.
Lavish owns the rendering, the design system, and the feedback loop; this skill owns what goes on the page.
Do not hand-roll an HTML file, a temp path, or a browser launch.

The artifact is a set of candidate cards plus a **Top recommendation** section naming which one you'd tackle first and why.
Each card carries:

- **Files** - which files/modules are involved
- **Problem** - one sentence on what hurts
- **Solution** - one sentence on what changes
- **Before / After diagram** - the centrepiece, side by side, showing the shallowness and the deepening
- **Wins** - short bullets in glossary terms (locality, leverage, interface shrinks)
- **Recommendation strength** - a badge reading `Strong`, `Worth exploring`, or `Speculative`

See [REPORT.md](REPORT.md) for the card anatomy, the diagram patterns, and the tone the artifact holds to.

**Use the repo's own vocabulary for the domain, and the codebase-design vocabulary for the architecture.**
If the domain calls it an "Order," talk about "the Order intake module" - not "the FooBarHandler," and not "the Order service."

**Decision-record conflicts**: if a candidate contradicts a recorded decision, only surface it when the friction is real enough to warrant reopening that decision.
Mark it on the card as a warning callout: *"contradicts ADR-0007 - but worth reopening because..."*.
Don't list every theoretical refactor a decision record forbids.

Do NOT propose interfaces yet.
Poll the artifact for the user's pick rather than asking in chat: the question "which of these would you like to explore?" belongs on the page, next to the cards it is about.

### 3. Grilling loop

Once the user picks a candidate, call the Skill tool with "grilling" to walk the decision tree with them - constraints, dependencies, the shape of the deepened module, what sits behind the seam, what tests survive.

Side effects happen inline as decisions crystallize:

- **Naming a deepened module after a concept the repo's glossary doesn't hold?**
  Add the term where that repo keeps its domain language, and say you did.
  Don't create a glossary file the repo never asked for.
- **User rejects the candidate with a load-bearing reason?**
  Offer to record it, framed as: *"want me to record this so a future architecture review doesn't re-suggest it?"*
  Only offer when the reason would actually be needed by a future explorer - skip ephemeral reasons ("not worth it right now") and self-evident ones.
- **Want to explore alternative interfaces for the deepened module?**
  Call the Skill tool with "codebase-design" and use its design-it-twice pattern.
