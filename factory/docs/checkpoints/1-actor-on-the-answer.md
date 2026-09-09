# Checkpoint 1: Actor on the answer

Source: frozen PRD `state/checkpoints/factory/1.md` in chao-orchestrator, frozen 2026-09-04.

## Slice
After this checkpoint, whoever answers a needs-input question is recorded by name.
The answer request at `POST /api/v1/work/{work_id}/answer` accepts an optional `actor`, and a request without one is recorded as `operator`, exactly as every answer is recorded today.
The answer response carries the actor back.
The Work record in the run detail shows `answered_by` next to `answer`, the way it already shows `approved_by` next to the approval.
The continuation history the next attempt receives names the actor on the answer row instead of always saying operator.
A Go test proves each of those, and a migration keeps every answer recorded before this change labelled operator.
Nothing about who may answer, who may approve, or what an answer does to the Work changes.
The orchestrator's `bin/factory-answer --actor` is a later checkpoint in chao-orchestrator and is the first client that will send a non-default actor.

## Decisions
D1. `WorkAnswerRequest` gains an optional `Actor string` field with the JSON name `actor`, placed after `message`.
D2. An absent or whitespace-only actor resolves to `operator` before validation, storage, and replay comparison.
D3. The actor is a free-form label, trimmed, 1 to 255 bytes of valid UTF-8, with no allowed-value list beyond the one reserved label in D3a, and no CHECK on `work_answers.actor` beyond the byte bounds in D5.
D3a. A label that equals `agent` after trimming, compared without regard to letter case, is rejected with status 400 and code `invalid_actor`, and nothing is stored. The comparison folds case because the guard is about how the row reads to the next attempt, which receives the actor as JSON per `resume.go:205-212`, not about the SQL comparison, which answer rows never pass through. The comparison is Go's `strings.EqualFold` on the trimmed label.
D4. An actor over 255 bytes or with invalid UTF-8 is rejected with status 400 and code `invalid_actor`, and nothing is stored.
D5. Migration `040_answer_actor.sql` adds `actor TEXT NOT NULL DEFAULT 'operator' CHECK (length(CAST(actor AS BLOB)) BETWEEN 1 AND 255)` to `work_answers` and `answered_by TEXT NOT NULL DEFAULT '' CHECK (length(CAST(answered_by AS BLOB)) <= 255)` to `sessions`, both with plain `ALTER TABLE ... ADD COLUMN` and no table rebuild.
D6. The same migration backfills `UPDATE sessions SET answered_by = 'operator' WHERE answer != ''`, so a Work answered before this change shows who answered without waiting for a new answer, and a blank `answered_by` has one meaning: not answered.
D7. `WorkAnswer` gains `Actor string` with the JSON name `actor`, and every query that builds a `WorkAnswer` reads the new column.
D8. `Session` gains `AnsweredBy string` with the JSON name `answered_by,omitempty`, placed directly after `Answer`.
D9. `AnswerWork` writes the resolved actor into `work_answers.actor` in the insert and into `sessions.answered_by` in the update that stores the answer, in the same transaction.
D10. The transition that moves a Work back to needs-input clears `answered_by` to the empty string where it clears `answer`.
D11. The run detail scan reads `session.answered_by` alongside `session.answer`.
D12. "Who answered" surfaces on the Work record as `answered_by`, not in a list of updates, because no route serves the updates table and `Session.Updates` is never populated.
D13. The continuation history query selects `answer.actor` in place of the literal `'operator'`, and the prospective row built during `AnswerWork` carries the resolved actor. The field that carries it is typed as decided in D24.
D14. Every answer row stays `trusted` regardless of actor.
D15. The continuation reserve's worst-case rows do not change, and a long actor never causes an answer to be rejected. The only cost of a long actor is history-row budget in the next continuation prompt, the same as a long message today.
D16. The fixed label `Trusted operator answer:` in the mandatory section of the continuation prompt does not change.
D17. A replay with the same `request_id` must match both the message and the resolved actor, and a differing actor returns 409 `answer_request_conflict`.
D18. No `work_updates` row is written for an answer.
D19. The cockpit does not change.
D20. Three documentation passages change, in the same task as the protocol change. `ARCHITECTURE.md:152-153` gains one sentence after "The current answer is also projected on Work.": the answer carries the actor that gave it, `operator` unless the request names one, and the Work projects it as `answered_by`. `docs/software-factory/design.md:515-517` gains one sentence after the operator answer contract: the answer request may name an actor of 1 to 255 bytes, not `agent` in any letter case, recorded on the answer and defaulting to `operator`. `docs/software-factory/design.md:190-191`, the sentence "The answer is stored as trusted operator context and requeues the same Work.", becomes: the answer is stored as trusted context labelled with the actor that gave it, `operator` unless the request names one, and requeues the same Work. `ARCHITECTURE.md:549`, which says the CLI and browser do not yet expose answer, stays true and does not change. The table list at `ARCHITECTURE.md:535` names tables and not columns and does not change.
D21. The change is three factory tasks: protocol, migration, store, the run detail, and the three documentation passages with their tests; the continuation history with its tests plus the HTTP test; and the closure task.
D22. Deployment is a merge followed by the human restarting the factory, and the migration applies at startup.
D23. The route's checkpoint 1 line is amended at freeze so the route and this PRD agree. The clause "the Work API's updates show who answered instead of always saying operator" becomes "the Work record's `answered_by` shows who answered instead of always saying operator".
D24. The answer actor is not a `protocol.WorkUpdateActor`. `continuationHistory.Actor` changes from `protocol.WorkUpdateActor` to `string`, keeping the JSON name `actor`, so one field carries the closed update actor and the free answer actor and the prompt JSON does not change shape. `SupportedWorkUpdateActor` stays the closed rule for `work_updates.actor` only, per D18, and no answer path calls it; the answer actor is validated by D3, D3a, and D4 alone. The places that assign the typed constants to that field convert them with `string(...)`: `internal/controlplane/resume.go:66`, `:70`, and `:443`, and the test rows at `internal/controlplane/resume_test.go:686`, `:753`, `:757`, `:797`, `:867`, and `:871`.
D25. `system` is a legal answer actor; `agent` in any letter case is the only reserved label. `TestAnswerWorkRejectsAgentActor` covers only the three spellings of `agent`, and no test asserts a rejection of `system`.

## Failure modes
- Actor over 255 bytes or not UTF-8: the client gets 400 `invalid_actor`, nothing is stored, and the Work stays needs-input.
- Actor equal to `agent` in any letter case, such as `agent`, `Agent`, or `AGENT`: the client gets 400 `invalid_actor`, nothing is stored, and the Work stays needs-input.
- Same `request_id` replayed with a different actor: the client gets 409 `answer_request_conflict`, the first answer stands.
- Same `request_id` replayed with the same message and actor: the stored answer is returned unchanged with its actor, as today.
- Migration 040 fails at startup: the factory does not start, the error names the migration file, and the human sees it in the tmux session, per `internal/controlplane/store.go:544`.
- A Work answered before the change: the run detail shows `answered_by: operator` from the backfill, and its next continuation history row says operator.
- A Work asks a second question: `answer` and `answered_by` are both cleared, and the earlier answer with its actor stays in the history the next attempt receives.
- An answer whose mandatory prompt section would exceed the continuation limit: the answer is rejected before anything is stored, as today; the actor is not part of that section, so it cannot cause the rejection.
- An answer with a long actor: it is accepted; the actor costs history-row budget in the next continuation prompt and can push an older row out of the selected rows into the ones the prompt omits and digests, as any long message does today.
- An old client that sends no actor: recorded as operator, indistinguishable from today.

## Tests
- `TestAnswerWorkRecordsActor`: an answer with actor `overseer` returns `Actor == "overseer"`, and `store.Work` shows `AnsweredBy == "overseer"`.
- `TestAnswerWorkDefaultsActorToOperator`: an answer with no actor returns `operator` and the Work shows `answered_by` operator.
- `TestAnswerWorkRejectsActorOverByteLimit` and `TestAnswerWorkAcceptsActorAtByteLimit`: mirror `approval_test.go:94` and `:111`.
- `TestAnswerWorkRejectsAgentActor`: answers with actors `agent`, `Agent`, and `AGENT` each return 400 `invalid_actor`, no `work_answers` row exists, and the Work stays needs-input.
- `TestAnswerWorkReplayWithDifferentActorConflicts`: 409 `answer_request_conflict`.
- `TestContinuationHistoryCarriesAnswerActor`: after an answer as `overseer`, the next claim's prompt history contains `"actor":"overseer"` and `"trusted":true` on the answer row, following the pattern at `resume_test.go:352-356` and `:377-388`; this test also proves D24, because `overseer` is a label `SupportedWorkUpdateActor` rejects.
- `TestWorkAnswerHTTPCarriesActor`: the HTTP path returns the actor, mirroring `work_http_test.go:16-46`.
- `TestMigration040LabelsExistingAnswersOperator`: a Work answered before the migration shows `answered_by` operator and its `work_answers` row has actor operator, following `submitted_name_test.go:30-50`.
- `TestContinuationPreservesEveryTrustedAnswerAcrossQuestionRounds` extended to assert `paused.AnsweredBy == ""` at `resume_test.go:348-351`.
- The existing predicate test at `internal/protocol/work_test.go:15-30` still passes unchanged, proving the closed list for update actors did not widen.
