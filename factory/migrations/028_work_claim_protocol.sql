ALTER TABLE workers
ADD COLUMN work_claim_protocol_version INTEGER NOT NULL DEFAULT 0
    CHECK (work_claim_protocol_version >= 0);
