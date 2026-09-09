# The candidate report

What goes on the page.
Lavish decides how the page is built and styled; this file decides what it says and what the diagrams show.

## Candidate card

The diagrams carry the weight.
Prose is sparse, plain, and uses the codebase-design glossary terms without ceremony.

Each candidate is one card:

- **Title** - short, names the deepening, e.g. "Collapse the Order intake pipeline".
- **Badge row** - recommendation strength (`Strong`, `Worth exploring`, `Speculative`), plus a tag for the dependency category (`in-process`, `local-substitutable`, `ports & adapters`, `mock`).
- **Files** - a monospaced list.
- **Before / After diagram** - the centrepiece, two columns side by side.
- **Problem** - one sentence. What hurts.
- **Solution** - one sentence. What changes.
- **Wins** - bullets, six words or fewer each, e.g. "Tests hit one interface", "Pricing logic stops leaking", "Delete 4 shallow wrappers".
- **Decision-record callout**, when one applies - one line in a warning-tinted box.

No paragraphs of explanation.
If the diagram needs a paragraph to be understood, redraw the diagram.

Close the report with a **Top recommendation**: one larger card naming the candidate, one sentence on why, and a link down to its card.

The report ends by asking which candidate to explore, and it asks on the page.
Give the user something to answer with rather than prose to reply to: a pick-one control per candidate, so their answer comes back through the poll already attached to the candidate it belongs to.

## Diagram patterns

Pick the pattern that fits the candidate.
Mix them.
Don't make every diagram look the same - variety is part of the point.

**Prefer Mermaid** for anything graph-shaped.
Lavish turns a rendered Mermaid diagram into an editable whiteboard, so a reviewer who disagrees with your "after" can redraw it and send the redraw back.
A hand-built SVG cannot be argued with; a Mermaid diagram can.

### Mermaid graph - the workhorse for dependencies and call flow

Use a `flowchart` or `graph` when the point is "X calls Y calls Z, and look at the mess."
Style with `classDef` to colour leakage edges red and the deep module dark.
Sequence diagrams work well for "before: 6 round-trips; after: 1."

```
flowchart LR
  A[OrderHandler] --> B[OrderValidator]
  B --> C[OrderRepo]
  C -.leak.-> D[PricingClient]
  classDef leak stroke:#dc2626,stroke-width:2px;
  class C,D leak
```

### Hand-built boxes and arrows - when Mermaid's layout fights you

Modules as bordered boxes, arrows as inline SVG over a positioned container.
Reach for this when the "after" needs to read as one thick-bordered deep module with greyed-out internals - Mermaid won't render that with the right weight.

### Cross-section - good for layered shallowness

Stack horizontal bands to show the layers a call passes through.
Before: six thin layers each doing nothing.
After: one thick band labelled with the consolidated responsibility.

### Mass diagram - good for "interface as wide as implementation"

Two rectangles per module, one for interface surface area and one for implementation.
Before: the interface rectangle is nearly as tall as the implementation rectangle (shallow).
After: the interface rectangle is short and the implementation rectangle is tall (deep).

### Call-graph collapse

Before: a tree of function calls as nested boxes.
After: the same tree collapsed into one box, the now-internal calls faded inside it.

## Diagram conventions

Hold these constant across every card so the reader learns them once, and state them in a legend at the top of the report:

- Solid box: a module.
- Dashed edge: a seam.
- Red edge: leakage across a seam.
- Thick dark box: a deep module.

Keep before and after at the same scale, so the shrinking interface is visible rather than asserted.
Label modules schematically, not as UI chrome.

## Tone

Plain English, concise - but the architectural nouns and verbs come straight from the codebase-design glossary.
Concision is not an excuse to drift.

**Use exactly:** module, interface, implementation, depth, deep, shallow, seam, adapter, leverage, locality.

**Never substitute:** component, service, unit (for module); API, signature (for interface); boundary (for seam); layer, wrapper (for module, when you mean module).

Phrasings that fit:

- "Order intake module is shallow - interface nearly matches the implementation."
- "Pricing leaks across the seam."
- "Deepen: one interface, one place to test."
- "Two adapters justify the seam: HTTP in prod, in-memory in tests."

**Wins** bullets name the gain in glossary terms: *"locality: bugs concentrate in one module"*, *"leverage: one interface, N call sites"*, *"interface shrinks; implementation absorbs the wrappers"*.
Don't write *"easier to maintain"* or *"cleaner code"* - those terms aren't in the glossary and don't earn their place.

No hedging, no throat-clearing, no "it's worth noting that".
If a sentence could be a bullet, make it a bullet.
If a bullet could be cut, cut it.
If a term isn't in the codebase-design glossary, reach for one that is before inventing a new one.
