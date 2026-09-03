CREATE TABLE cloud_completion_notifications (
    notification_id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners(id),
    collaboration_id TEXT NOT NULL REFERENCES cloud_collaborations(collaboration_id) ON DELETE CASCADE,
    task_id TEXT NOT NULL,
    generation INTEGER NOT NULL CHECK(generation > 0),
    notification_kind TEXT NOT NULL CHECK(notification_kind = 'completion'),
    outcome TEXT NOT NULL CHECK(outcome IN ('completed','blocked','failed')),
    source_session_id TEXT NOT NULL,
    target_session_id TEXT NOT NULL,
    deliverable_path TEXT,
    state TEXT NOT NULL CHECK(state IN ('pending','claimed','acked')),
    claim_id TEXT,
    claimed_at INTEGER,
    acked_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE(owner_id, collaboration_id, task_id, generation, notification_kind)
);

CREATE INDEX idx_cloud_completion_notifications_claim
    ON cloud_completion_notifications(owner_id, target_session_id, state, created_at, notification_id);

CREATE INDEX idx_cloud_completion_notifications_claim_id
    ON cloud_completion_notifications(owner_id, target_session_id, claim_id, state);
