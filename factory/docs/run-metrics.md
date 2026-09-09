# Run health metrics

Factory's overview reports Task invocations, stored as Runs. Performance metrics
use one fixed cohort: Runs admitted during the previous 24 hours. Operational
status metrics such as active Runs and Worker availability cover all retained
records.

| Metric | Formula |
| --- | --- |
| Runs | Count of Runs admitted in the cohort |
| Completed runs | Count of cohort Runs with a terminal time |
| Completion rate | Completed runs divided by all runs in the cohort |
| Average queue time | Mean of first Session start minus Run admission for Runs that started |
| Average cycle time | Mean of terminal time minus Run admission for completed Runs |

The first start is the earliest stored Attempt start across the Run's Sessions.
The terminal time is set when every Session has reached a terminal state. A
Session retry stays inside the same Run record, so it does not inflate the Run
count or rewrite the first start. Empty cohorts show `No data` for rates and
durations instead of reporting a misleading zero.
