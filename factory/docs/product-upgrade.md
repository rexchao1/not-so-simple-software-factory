# Upgrade existing Factory data

Factory keeps existing Task, Attempt, Runbook, Automation, and Occurrence history
when an installation moves to the Definition, Run, Job, and Worker product
model. The Overview page shows an upgrade preview only when legacy records are
present.

## Before starting

Back up the Factory data directory. Finish any active legacy poller import
first. The preview lists:

- compatible schedule Automations that become Definition-backed schedules;
- GitHub polling Automations that remain readable but are retired;
- Tasks, Attempts, Runbooks, revisions, and Occurrences retained as history;
- active executions that must drain or be explicitly cancelled.

Legacy schedules did not store a runtime. Their generated Definitions use
Codex and require Git. Each Definition prompt contains the current Runbook
instructions and Automation context. The Automation keeps its repository,
cron expression, time zone, enabled state, and exact next due instant.

GitHub polling is not converted into an invented action. Replace each retired
poller with a scheduled Definition that tells the agent to use `gh`, configure
a GitHub webhook, or leave it retired.

## Cut over

1. Select **Upgrade Factory** on Overview. If legacy executions are active,
   choose **Freeze and drain** or **Freeze and cancel active work**.
2. Factory freezes legacy Task, Runbook, and polling mutations before waiting.
   New Definitions and Runs remain available.
3. When active legacy work is terminal, continue the upgrade. Factory converts
   compatible schedules in one SQLite transaction.
4. Review old Tasks under **Work**, now labelled **Legacy task history**.

The freeze is durable. If Factory stops after freezing but before conversion,
restart it and continue from Overview. The stored validation report records
how many schedules and Definitions were converted, how much legacy history was
retained, and confirms that no synthetic Runs were created.
