CREATE TABLE product_model_upgrades (
    id TEXT PRIMARY KEY CHECK (id = 'definitions-runs-v1'),
    state TEXT NOT NULL CHECK (state IN ('draining', 'completed')),
    cancel_active INTEGER NOT NULL DEFAULT 0 CHECK (cancel_active IN (0, 1)),
    preview_json BLOB NOT NULL
        CHECK (json_valid(preview_json) AND json_type(preview_json) = 'object'),
    validation_json BLOB
        CHECK (validation_json IS NULL OR (
            json_valid(validation_json) AND json_type(validation_json) = 'object'
        )),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    completed_at INTEGER
);

CREATE TABLE product_upgrade_schedule_update_replays (
    automation_id TEXT PRIMARY KEY
        REFERENCES automations(id) ON DELETE CASCADE,
    expected_version INTEGER NOT NULL CHECK (expected_version >= 1),
    request_digest BLOB NOT NULL
);
