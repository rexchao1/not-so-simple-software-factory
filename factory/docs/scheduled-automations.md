# Scheduled Automations

A scheduled Automation runs one shared Definition across one or more configured
repositories. It uses the same Run and Job lifecycle as **Run once**. The only
difference is `source_kind: "schedule"` and the durable schedule occurrence that
records why the Run was admitted.

## Create and operate a schedule

1. Create a Definition with the prompt, runtime, tools, timeout, and optional
   inputs the team wants to share.
2. Add the target repositories and make sure at least one local or remote Worker
   can access them.
3. Open **Automations**, choose **Schedule**, select the Definition and target
   repositories, then choose a frequency, local time, and IANA timezone.
4. Create the Automation. New Automations are disabled.
5. Use **Test trigger** to preview the next UTC instant without creating work.
6. Enable the Automation, or use **Run now** after enabling it.
7. Open the resulting Run to inspect each repository Job and retry failures.

Editing is available while an Automation is disabled. Disabling it prevents new
scheduled Runs but does not cancel work already admitted.

## Schedule rules

Factory accepts five-field cron expressions: minute, hour, day of month, month,
and day of week. Seconds fields and aliases such as `@daily` are rejected. The
timezone must be an IANA name such as `Europe/London`.

Factory calculates due instants in the chosen timezone and stores the exact UTC
instant. Daylight-saving overlaps can produce two distinct UTC instants. A local
time that does not exist during a daylight-saving transition is skipped.

## Delivery and replay guarantees

Each Automation and due UTC instant has one durable occurrence and one stable
Run request key. Replaying evaluation or dispatch returns the same Run instead
of creating duplicate Runs or Jobs. The occurrence snapshots the Definition,
repository IDs, parameters, concurrency limit, cron expression, and timezone
before dispatch.

An occurrence can commit before its Run is created. The dispatcher safely
resumes it after restart. If Run creation commits but linking the occurrence
fails, replay uses the same request key and links the existing Run. Dependency
errors block the Automation with a diagnostic; transient storage failures remain
retryable.

## Current boundary

Schedules are evaluated by the Factory control plane. Agents perform repository
and GitHub work with their installed tools. Factory does not contain a second
deterministic GitHub action layer. GitHub webhook triggers are a later trigger
type and Kubernetes Workers are outside the current local and remote-VM scope.
