-- Normalize legacy "queued" status to "grabbed" and add new import-pipeline states.
UPDATE downloads SET status = 'grabbed' WHERE status = 'queued';
UPDATE downloads SET status = 'grabbed' WHERE status = 'paused';
