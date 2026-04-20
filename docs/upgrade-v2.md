# Upgrading to v1.0 (multi-user migration)

> **Migration 019 is a one-way door on SQLite.** There is no automated rollback. Take a verified backup before you start.

v1.0 runs migration `019_multiuser.sql`, which adds `owner_user_id` to every user-owned table and backfills all existing rows to `user_id=1`. The migration runs inside a transaction — if it fails, the database is not left in a half-migrated state and Bindery exits cleanly with a repair hint.

Single-user installs are unaffected in practice: all data remains owned by user 1 and behaviour is identical post-upgrade.

## Step 1 — Take a verified backup

### Docker / binary

```bash
# Via API (creates a timestamped .db copy in BINDERY_DATA_DIR)
curl -X POST -H "X-Api-Key: <key>" http://bindery:8787/api/v1/backup

# Via UI
Settings → General → Backup → Create backup

# Verify it was written
ls -lh /config/bindery_backup_*.db
```

### Kubernetes

Copy the database file out of the PVC **before** stopping Bindery. With the pod still running:

```bash
# Find the pod name
kubectl get pods -l app=bindery

# Copy bindery.db from the PVC to your local machine
kubectl cp bindery-0:/config/bindery.db ./bindery-pre-v1.db

# Confirm the copy is non-zero and readable
sqlite3 ./bindery-pre-v1.db "SELECT count(*) FROM books;"
```

Store `bindery-pre-v1.db` somewhere safe. This is your rollback point.

## Step 2 — Dry-run the migration on a copy

Run the new binary against a copy of your production database before touching the real one. This confirms the migration completes cleanly on your data.

### Docker dry-run

```bash
# Copy the live DB
cp /config/bindery.db /tmp/bindery-dryrun.db

# Run the new image against the copy (read-write, but isolated)
docker run --rm \
  -e BINDERY_DB_PATH=/tmp/bindery-dryrun.db \
  -v /tmp:/tmp \
  ghcr.io/vavallee/bindery:v1.0.0 \
  bindery migrate --dry-run

# Or just boot it — migration runs at startup, Bindery will exit after if no port is exposed
docker run --rm \
  -e BINDERY_DB_PATH=/tmp/bindery-dryrun.db \
  -e BINDERY_PORT=18787 \
  -v /tmp:/tmp \
  -p 18787:18787 \
  ghcr.io/vavallee/bindery:v1.0.0
```

Check the logs for `migration 019 complete; all rows backfilled to user_id=1`. If the migration fails on the copy, Bindery logs the error and the specific repair query needed.

### Kubernetes dry-run

```bash
# Spin up a one-off pod against the backup copy (copy it into a temp PVC or emptyDir)
kubectl run bindery-dryrun --rm -it --restart=Never \
  --image=ghcr.io/vavallee/bindery:v1.0.0 \
  --env="BINDERY_DB_PATH=/tmp/bindery-dryrun.db" \
  --overrides='{"spec":{"volumes":[{"name":"tmp","emptyDir":{}}],"containers":[{"name":"bindery-dryrun","volumeMounts":[{"name":"tmp","mountPath":"/tmp"}]}]}}' \
  -- sh -c "cp /config/bindery.db /tmp/bindery-dryrun.db && bindery"
```

Or, more practically: copy the DB locally, run the binary locally, verify integrity:

```bash
# Pull and run the new binary locally against the backup
BINDERY_DB_PATH=./bindery-pre-v1.db ./bindery-v1.0.0 &
sleep 3
curl -s http://localhost:8787/api/v1/author | jq 'length'   # should match pre-upgrade count
curl -s http://localhost:8787/api/v1/book  | jq 'length'
kill %1
```

### Integrity check queries

After the dry-run completes, verify data integrity directly:

```bash
DB=./bindery-pre-v1.db   # or /config/bindery.db post-upgrade

# All rows should have owner_user_id = 1
sqlite3 $DB "SELECT count(*) FROM authors WHERE owner_user_id IS NULL;"   # expect 0
sqlite3 $DB "SELECT count(*) FROM books   WHERE owner_user_id IS NULL;"   # expect 0
sqlite3 $DB "SELECT count(*) FROM downloads WHERE owner_user_id IS NULL;" # expect 0

# Row counts should match pre-upgrade
sqlite3 $DB "SELECT count(*) FROM authors;"
sqlite3 $DB "SELECT count(*) FROM books;"
```

## Step 3 — Upgrade

### Docker / binary

```bash
# 1. Stop Bindery
docker stop bindery   # or kill the process

# 2. Backup is already done (Step 1)

# 3. Pull the new image / replace the binary
docker pull ghcr.io/vavallee/bindery:v1.0.0

# 4. Start — migration runs automatically
docker start bindery

# 5. Tail logs to confirm
docker logs -f bindery | grep -E "migration|error"
# Expect: migration 019 complete; all rows backfilled to user_id=1
```

### Kubernetes (Helm)

```bash
# 1. Copy DB out of PVC (Step 1, already done)

# 2. Update the image tag
helm upgrade bindery charts/bindery \
  --set image.tag=v1.0.0 \
  --reuse-values

# 3. Watch the rollout
kubectl rollout status deployment/bindery

# 4. Confirm migration in logs
kubectl logs deployment/bindery | grep -E "migration|error"
```

## Step 4 — Verify

```bash
# UI: open Bindery, confirm library loads normally
# API: spot-check row counts match pre-upgrade
curl -s -H "X-Api-Key: <key>" http://bindery:8787/api/v1/author | jq 'length'
curl -s -H "X-Api-Key: <key>" http://bindery:8787/api/v1/book   | jq 'length'
```

Check **Settings → Users** — you should see your original admin account listed with role `admin`.

## Rollback

**There is no automated rollback for migration 019.** SQLite does not support `DROP COLUMN`, so the `owner_user_id` columns cannot be removed by reverting the binary.

To roll back: restore from the backup taken in Step 1.

### Docker / binary rollback

```bash
docker stop bindery
cp /config/bindery_backup_<timestamp>.db /config/bindery.db
docker run ... ghcr.io/vavallee/bindery:v0.24.0   # previous image
```

### Kubernetes rollback

```bash
# Copy the backup back into the PVC
kubectl cp ./bindery-pre-v1.db bindery-0:/config/bindery.db

# Roll back the Helm release
helm rollback bindery
```

Data written to Bindery between the upgrade and the rollback will be lost — it was scoped to the new schema and is not recoverable from the pre-upgrade snapshot.

## See also

- [docs/troubleshooting-auth.md](troubleshooting-auth.md) — consolidated symptom→cause→fix table covering migration failures and all auth phases

## Troubleshooting migration failures

| Symptom | Cause | Fix |
|---------|-------|-----|
| Startup fails: `orphaned rows detected in downloads` | Rows in `downloads` reference non-existent book or user IDs from earlier bugs | Follow the repair query printed in the log. Typically: `DELETE FROM downloads WHERE book_id NOT IN (SELECT id FROM books);` Re-run after fixing. |
| Startup fails: `migration 019: constraint violation` | A table has rows that conflict with the new `NOT NULL owner_user_id` constraint in a way the backfill didn't catch | Run the dry-run against a copy, examine the exact error, and apply the suggested fix. Always restore from backup before retrying on the live DB. |
| Migration exits mid-run, DB appears corrupted | Should not happen — migration runs in a transaction | Confirm by opening the DB with `sqlite3 /config/bindery.db ".schema authors"`. If `owner_user_id` column is absent, the migration rolled back cleanly. Restore from backup and investigate the error in logs before retrying. |
| All data appears under the wrong user post-migration | Backfill wrote to a different DB file than expected | Confirm `BINDERY_DB_PATH` points to the correct file. Check `sqlite3 /config/bindery.db "SELECT count(*) FROM authors;"` against your pre-upgrade count. |
| Admin account missing after migration | User table had the account but role column was not set | `sqlite3 /config/bindery.db "UPDATE users SET role='admin' WHERE id=1;"` — this is safe to run on a live instance. |
