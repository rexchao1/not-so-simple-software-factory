ALTER TABLE workflows RENAME COLUMN current_name_key TO current_title_key;
ALTER TABLE workflow_revisions RENAME COLUMN name TO title;
ALTER TABLE tasks RENAME COLUMN workflow_name TO workflow_title;
ALTER TABLE automations RENAME COLUMN name TO title;
ALTER TABLE automations RENAME COLUMN name_key TO title_key;
ALTER TABLE automation_occurrences RENAME COLUMN automation_name TO automation_title;
