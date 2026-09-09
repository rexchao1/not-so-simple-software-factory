ALTER TABLE workers
ADD COLUMN source_access_json TEXT NOT NULL DEFAULT '[]';
