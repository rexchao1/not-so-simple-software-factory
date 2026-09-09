import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, GitBranch } from "lucide-react";
import { api } from "./api";
import { taskDisplayName } from "./format";
import { useVisibleInterval } from "./polling";
import type { Run } from "./types";
import { EmptyState, ErrorState, InlineError, LoadingState, ViewHeader } from "./ui";

// The cockpit authenticates nobody: there is no operator identity anywhere in
// it. So an approval records the channel that made it rather than a person.
// Stamping a human name the UI never verified would falsify the approved_by
// audit record that the single approval gate exists to create.
const approvalActor = "cockpit";

export interface DraftRow {
  id: string;
  name: string;
  repository: string;
}

export function DraftsView() {
  const client = useQueryClient();
  const interval = useVisibleInterval(5_000);
  const query = useQuery({ queryKey: ["drafts"], queryFn: api.draftRuns, refetchInterval: interval });
  const approve = useMutation({
    mutationFn: (workID: string) => api.approveWork(workID, approvalActor),
    // Awaiting the refetch keeps the mutation pending until the approved row
    // is gone, so the disabled control below covers the whole window rather
    // than re-enabling over a row whose approval already landed.
    onSuccess: async () => { await query.refetch(); void client.invalidateQueries({ queryKey: ["runs"] }); },
  });
  if (query.isPending) return <LoadingState label="Loading Drafts" />;
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  return <div className="page">
    <ViewHeader title="Drafts" fetching={query.isFetching || approve.isPending} updatedAt={query.dataUpdatedAt} onRefresh={() => void query.refetch()} />
    <div className="view-toolbar"><p>Admitted Work waits here until a human approves it. Nothing below has been dispatched.</p></div>
    <InlineError error={approve.error} />
    <Drafts drafts={draftRows(query.data ?? [])} approving={approve.isPending} onApprove={(workID) => approve.mutate(workID)} />
  </div>;
}

// Admission creates one Run per submitted spec, with one target, so every
// target of a draft Run is a Work item waiting on this gate.
function draftRows(runs: Run[]): DraftRow[] {
  return runs.flatMap((run) => (run.targets ?? []).map((target) => ({
    id: target.id,
    // Resolved once here, so the row text and the button's accessible name
    // cannot drift apart.
    name: taskDisplayName(run.task),
    repository: target.repository_identity,
  })));
}

export function Drafts({ drafts, approving = false, onApprove }: {
  drafts: DraftRow[];
  approving?: boolean;
  onApprove: (workID: string) => void;
}) {
  if (!drafts.length) {
    return <EmptyState icon={<CheckCircle2 size={22} />} title="Nothing to approve" description="No drafts waiting for approval." />;
  }
  return <section className="panel">
    <div className="panel-heading"><h2>Waiting for approval</h2><span>{drafts.length}</span></div>
    <div className="draft-list">
      {drafts.map((draft) => <div className="draft-row" key={draft.id}>
        <GitBranch size={15} />
        <span><strong>{draft.name}</strong><small>{draft.repository}</small></span>
        <button className="button button-primary" aria-label={`Approve ${draft.name}`} disabled={approving} onClick={() => onApprove(draft.id)}>Approve</button>
      </div>)}
    </div>
  </section>;
}
