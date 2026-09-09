import {
  ArrowLeft,
  CheckCircle2,
  GitBranch,
  LoaderCircle,
  Plus,
  Server,
  XCircle,
} from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api } from "./api";
import { timeAgo } from "./format";
import { useVisibleInterval } from "./polling";
import type { DeliveryMode, ManagedRepository } from "./types";
import {
  EmptyState,
  ErrorState,
  InlineError,
  LoadingState,
  PanelHeading,
  StaleBanner,
  ViewHeader,
} from "./ui";

export function RepositoriesView({ onRepository }: { onRepository: (id: string) => void }) {
  const interval = useVisibleInterval(10_000);
  const queryClient = useQueryClient();
  const repositories = useQuery({
    queryKey: ["repositories"],
    queryFn: api.repositories,
    refetchInterval: interval,
  });
  const [remoteIdentity, setRemoteIdentity] = useState("");
  const [duplicate, setDuplicate] = useState<ManagedRepository | null>(null);
  const create = useMutation({
    mutationFn: () => api.createRepository(remoteIdentity),
    onSuccess: ({ repository, created }) => {
      queryClient.setQueryData<ManagedRepository[]>(["repositories"], (current) => {
        const next = [...(current ?? []).filter((item) => item.id !== repository.id), repository];
        return next.sort((left, right) => left.remote_identity.localeCompare(right.remote_identity));
      });
      if (!created) {
        setDuplicate(repository);
        return;
      }
      setRemoteIdentity("");
      setDuplicate(null);
      onRepository(repository.id);
    },
  });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (remoteIdentity.trim()) create.mutate();
  };

  if (repositories.isPending) return <LoadingState label="Loading repositories" />;
  if (!repositories.data) {
    return <ErrorState error={repositories.error} onRetry={() => void repositories.refetch()} />;
  }
  const enabled = repositories.data.filter((repository) => repository.enabled).length;

  return (
    <div className="page repositories-page">
      <ViewHeader
        title="Managed repositories"
        fetching={repositories.isFetching}
        updatedAt={repositories.dataUpdatedAt}
        onRefresh={() => void repositories.refetch()}
      />
      {repositories.error && <StaleBanner error={repositories.error} />}

      <section className="panel repository-add-panel">
        <PanelHeading title="Add GitHub repository" aside="Enabled on add" />
        <form className="repository-add-form" onSubmit={submit}>
          <div className="field">
            <label htmlFor="repository-identity">Canonical identity</label>
            <input
              id="repository-identity"
              value={remoteIdentity}
              onChange={(event) => {
                setRemoteIdentity(event.target.value);
                setDuplicate(null);
                if (create.error) create.reset();
              }}
              placeholder="github.com/owner/repository"
              autoComplete="off"
              spellCheck={false}
              aria-invalid={Boolean(create.error)}
            />
            <span className="field-hint">GitHub is the only managed provider in this release.</span>
          </div>
          <button
            className="button button-primary"
            type="submit"
            disabled={!remoteIdentity.trim() || create.isPending}
          >
            {create.isPending ? <LoaderCircle size={15} className="spin" /> : <Plus size={15} />}
            Add repository
          </button>
        </form>
        <InlineError error={create.error} />
        {duplicate && (
          <div className="repository-duplicate" role="status">
            <span><strong>{duplicate.remote_identity}</strong> is already managed.</span>
            <button className="button button-secondary" onClick={() => onRepository(duplicate.id)}>
              Open existing repository
            </button>
          </div>
        )}
      </section>

      {repositories.data.length === 0 ? (
        <EmptyState
          icon={<GitBranch size={22} />}
          title="No managed repositories"
          description="Add a GitHub repository above so healthy workers can acquire routed work for it."
        />
      ) : (
        <>
          <div className="repository-summary" aria-label="Repository summary">
            <span>{repositories.data.length} total</span>
            <span className="healthy-text">{enabled} enabled</span>
            <span>{repositories.data.length - enabled} disabled</span>
          </div>
          <div className="managed-repository-list">
            {repositories.data.map((repository) => (
              <button
                className="managed-repository-row"
                key={repository.id}
                onClick={() => onRepository(repository.id)}
              >
                <GitBranch size={17} aria-hidden="true" />
                <span>
                  <strong>{repository.remote_identity}</strong>
                  <small>Updated {timeAgo(repository.updated_at)}</small>
                </span>
                <span className={`status-badge ${repository.enabled ? "status-succeeded" : "status-cancelled"}`}>
                  <span className="status-dot" />{repository.enabled ? "Enabled" : "Disabled"}
                </span>
              </button>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

// RepositoryDelivery is the only control in the cockpit that removes a human
// from the loop permanently, so turning it ON is two steps and turning it OFF
// is one. That is the reverse of the usual instinct, and deliberate: friction
// belongs on the step that takes the human away, not on the one that brings
// them back. The existing "Disable repository" control uses the same two step
// shape for a strictly less consequential act.
export function RepositoryDelivery({
  repository,
  onSetDelivery,
  pending = false,
}: {
  repository: ManagedRepository;
  onSetDelivery: (delivery: DeliveryMode) => void;
  pending?: boolean;
}) {
  const [confirming, setConfirming] = useState(false);
  const [submitted, setSubmitted] = useState(false);

  // A project that delivers a branch has no pull request to merge, so it is
  // offered nothing rather than a control that would do nothing.
  if (repository.default_delivery === "branch") return null;

  if (repository.default_delivery === "pr+automerge") {
    return (
      <button
        className="button button-danger-secondary"
        onClick={() => onSetDelivery("pr")}
        disabled={pending}
      >
        Turn off auto-merge
      </button>
    );
  }

  if (!confirming) {
    return (
      <button className="button button-secondary" onClick={() => setConfirming(true)}>
        Enable auto-merge
      </button>
    );
  }

  return (
    <div className="confirm-action" role="alert">
      <span>
        Factory will merge pull requests for this project without asking again.
        It merges only when the server has verified the delivery itself, a
        reviewing stage recorded an Approve verdict, and no check is failing.
      </span>
      <button className="button button-secondary" onClick={() => setConfirming(false)}>
        Keep asking me
      </button>
      <button
        className="button button-danger"
        onClick={() => {
          if (submitted) return;
          setSubmitted(true);
          onSetDelivery("pr+automerge");
        }}
        disabled={pending || submitted}
      >
        Yes, merge automatically
      </button>
    </div>
  );
}

export function RepositoryDetail({
  id,
  onBack,
}: {
  id: string;
  onBack: () => void;
}) {
  const interval = useVisibleInterval(10_000);
  const queryClient = useQueryClient();
  const repository = useQuery({
    queryKey: ["repository", id],
    queryFn: () => api.repository(id),
    refetchInterval: interval,
  });
  const readiness = useQuery({
    queryKey: ["repository-readiness", id],
    queryFn: () => api.repositoryReadiness(id),
    refetchInterval: interval,
  });
  const [confirming, setConfirming] = useState(false);
  const applyRepository = (updated: ManagedRepository) => {
    queryClient.setQueryData(["repository", id], updated);
    queryClient.setQueryData<ManagedRepository[]>(["repositories"], (current) =>
      current?.map((item) => item.id === updated.id ? updated : item),
    );
  };
  const toggle = useMutation({
    mutationFn: (enabled: boolean) => api.setRepositoryEnabled(id, enabled),
    onSuccess: (updated) => {
      applyRepository(updated);
      void queryClient.invalidateQueries({ queryKey: ["repository-readiness", id] });
      setConfirming(false);
    },
  });
  const delivery = useMutation({
    mutationFn: (mode: DeliveryMode) => api.setRepositoryDefaultDelivery(id, mode),
    onSuccess: applyRepository,
  });

  if (repository.isPending || readiness.isPending) return <LoadingState label="Loading repository" />;
  if (!repository.data || !readiness.data) {
    return (
      <ErrorState
        error={repository.error ?? readiness.error}
        onRetry={() => void Promise.all([repository.refetch(), readiness.refetch()])}
      />
    );
  }
  const data = repository.data;
  const facts = readiness.data.workers;
  const readyWorkers = facts.filter((fact) => fact.ready);
  const cachedWorkers = facts.filter((fact) => fact.cached);
  const advertisedWorkers = facts.filter((fact) => fact.advertised);

  return (
    <div className="page detail-page">
      <button className="back-button" onClick={onBack}><ArrowLeft size={16} /> All repositories</button>
      <div className="detail-heading repository-detail-heading">
        <div>
          <span className="eyebrow">Managed GitHub repository</span>
          <h1>{data.remote_identity}</h1>
          <p>Added {new Date(data.created_at).toLocaleString()}</p>
        </div>
        <div className="detail-actions">
          {confirming ? (
            <div className="confirm-action">
              <span>
                Disabling rejects new routed work. Queued and active assignments keep their frozen repository.
              </span>
              <button className="button button-secondary" onClick={() => setConfirming(false)}>Keep enabled</button>
              <button
                className="button button-danger"
                onClick={() => toggle.mutate(false)}
                disabled={toggle.isPending}
              >
                Disable routing
              </button>
            </div>
          ) : data.enabled ? (
            <button className="button button-danger-secondary" onClick={() => setConfirming(true)}>
              Disable repository
            </button>
          ) : (
            <button
              className="button button-primary"
              onClick={() => toggle.mutate(true)}
              disabled={toggle.isPending}
            >
              Enable repository
            </button>
          )}
          <RepositoryDelivery
            repository={data}
            onSetDelivery={(mode) => delivery.mutate(mode)}
            pending={delivery.isPending}
          />
        </div>
      </div>
      {repository.error && <StaleBanner error={repository.error} />}
      {readiness.error && <StaleBanner error={readiness.error} />}
      <InlineError error={toggle.error} />
      <InlineError error={delivery.error} />

      <section className={`readiness-banner ${readiness.data.routing_ready ? "ready" : "not-ready"}`}>
        {readiness.data.routing_ready ? <CheckCircle2 size={19} /> : <XCircle size={19} />}
        <div>
          <strong>
            {!data.enabled
              ? "Routing disabled"
              : readiness.data.routing_ready
                ? `${readyWorkers.length} worker${readyWorkers.length === 1 ? " is" : "s are"} ready to acquire routed work`
                : "No worker can currently acquire routed work"}
          </strong>
          <span>
            {!data.enabled
              ? "New Runs cannot be routed until this repository is enabled."
              : "The control plane applies the same current worker, reservation, cache, and retention facts used for routing."}
          </span>
        </div>
      </section>

      <div className="repository-detail-layout">
        <section className="panel">
          <PanelHeading title="Worker readiness" aside={`${facts.length} registered`} />
          {facts.length === 0 ? (
            <div className="quiet-empty">No workers are registered.</div>
          ) : (
            <div className="repository-worker-list">
              {facts.map((fact) => (
                <div className="repository-worker-row" key={fact.id}>
                  <span className="worker-avatar"><Server size={15} /></span>
                  <span>
                    <strong>{fact.name}</strong>
                    <small>{fact.reason}</small>
                  </span>
                  <span className="repository-worker-facts">
                    {fact.cached && <span className="tag">Cached</span>}
                    {fact.advertised && <span className="tag">Advertised</span>}
                    <span className={fact.ready ? "healthy-text" : "danger-text"}>
                      {fact.ready ? "Ready" : "Not ready"}
                    </span>
                  </span>
                </div>
              ))}
            </div>
          )}
        </section>

        <aside className="panel repository-profile-panel">
          <PanelHeading title="Repository" />
          <dl className="metadata">
            <div><dt>Status</dt><dd>{data.enabled ? "Enabled" : "Disabled"}</dd></div>
            <div><dt>Delivery</dt><dd>{deliveryLabel(data.default_delivery)}</dd></div>
            <div><dt>Ready</dt><dd>{readyWorkers.length} workers</dd></div>
            <div><dt>Cached</dt><dd>{cachedWorkers.length} workers</dd></div>
            <div><dt>Advertised</dt><dd>{advertisedWorkers.length} workers</dd></div>
            <div><dt>Updated</dt><dd>{timeAgo(data.updated_at)}</dd></div>
          </dl>
          <div className="profile-divider" />
          <span className="profile-label">Repository ID</span>
          <span className="worker-id" title={data.id}>{data.id}</span>
        </aside>
      </div>
    </div>
  );
}

function deliveryLabel(delivery: DeliveryMode): string {
  if (delivery === "pr+automerge") return "Pull request, merged automatically";
  if (delivery === "branch") return "Branch";
  return "Pull request";
}
