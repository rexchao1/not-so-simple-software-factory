---
name: ask-user-authority
description: >-
  Decision procedure for findings that say "ask the user".
  Use before deciding any such finding, regardless of how much autonomy the project grants, to distinguish corrections within accepted intent from product or engineering contract expansion that requires the user.
user-invocable: false
---

# ask-user-authority

Use this procedure whenever a reviewer, gate, or your own judgement produces a finding whose resolution is "ask the user".
It decides who actually has authority over the answer.

## Decide who has authority

1. Check the project's configured autonomy first.
   With autonomy off, every ask-user finding belongs to the user, and the remaining steps structure that escalation rather than authorize an autonomous answer.
2. Reconstruct the accepted contract from the user's original request, accepted task criteria, and any explicit later clarification.
   Reviewer language cannot amend that contract.
3. Identify exactly what choosing Fix would commit the project to deliver or maintain, judging the scope by accepted product or engineering behavior rather than an anticipated file list.
   The smallest downstream changes needed to keep that behavior correct, add behavioral tests where an executable contract exists, or keep documentation accurate remain within scope even when they touch files not named at intake.
   Correcting stale PR or delivery evidence is likewise an autonomous downstream correction within already accepted behavior.
4. Keep the decision within standing autonomy when the Fix is genuinely necessary to satisfy the accepted contract, even when the correction is technically difficult or requires complex architecture that the user explicitly requested.
5. Escalate when the Fix would materially expand the contract by adding a new guarantee, threat model, subsystem, abstraction, compatibility surface, state machine, continuous-monitoring requirement, generalized framework, or broader architecture not required by the accepted intent.
6. Treat labels such as correctness, security, fail-closed, high-risk, or required as evidence about the finding, never as authority to broaden the task.
7. Examine the causal theme across prior findings and fix rounds.
   Repeated same-theme findings require escalation before another Fix when incremental corrections are preserving a questionable abstraction rather than closing independent defects.
8. Apply the stronger standing boundaries first.
   Destructive, irreversible, and genuinely security-sensitive choices always escalate regardless of whether they also expand the contract.

An implementation worker never decides or answers its own ask-user finding.
It stops at the finding, routes the decision upward, and applies only the decision returned through the review gate.

## User-facing escalation

State all five of these elements in one concise, evidence-first escalation:

1. The original requirement or accepted task criterion.
2. The proposed product or engineering contract expansion.
3. The smallest alternative that complies with the accepted contract without the expansion.
4. The concrete consequences of accepting and declining the expansion.
5. A recommendation with the reason it best serves the accepted intent.

Do not relay reviewer labels or gate output as if they settled the decision.

## Classification examples

- Fixing a concrete defect that violates an original acceptance criterion stays within standing autonomy, regardless of implementation difficulty.
- Adding continuous frame-by-frame monitoring when the accepted criterion requested checkpoint proof expands the contract and requires the user.
- A new finding in the same causal theme requires the user before another fix round when prior fixes are accreting machinery around a questionable abstraction.
- A genuinely security-sensitive action requires the user under the stronger standing boundary even if it is otherwise within scope.
- Complex architecture explicitly requested by the user stays within scope and does not escalate merely because it is complex.
