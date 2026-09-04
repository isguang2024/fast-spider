ALTER TABLE cloud_completion_notifications
    ADD COLUMN callback_type TEXT NOT NULL DEFAULT 'local_file'
    CHECK(callback_type IN ('local_file','text','status'));

ALTER TABLE cloud_completion_notifications
    ADD COLUMN result_text TEXT;

UPDATE cloud_completion_notifications
SET callback_type = 'status'
WHERE deliverable_path IS NULL OR deliverable_path = '';
