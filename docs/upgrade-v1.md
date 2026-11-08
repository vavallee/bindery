# Upgrading to v1.0 (multi-user migration)

> **Migration 025 is a one-way door on SQLite.** There is no automated rollback. Take a verified backup before you start.

v1.0 runs migration `025_multiuser.sql`, which adds `owner_user_id` to every user-owned table and backfills all existing rows to `user_id=1`. The migration runs inside a transaction — if it fails, the database is not left in a half-migrated state and Bindery exits cleanly with a repair hint.

Single-user installs are unaffected in practice: all data remains owned by user 1 and behaviour is identical post-upgrade.

## Step 1 — Take a verified backup

### Docker / binary

```bash
# Via API (creates a timestamped .db copy under <BINDERY_DATA_DIR>/backups)
curl -X POST -H "X-Api-Key: <key>" http://bindery:8787/api/v1/backup

# Via UI
Settings → Logs → Backup & Restore → Create Backup

# Verify it was written
ls -lh /config/backups/bindery_*.db
```

### Kubernetes

The image is distroless: it has no shell and no `tar`, so `kubectl exec` and `kubectl cp` cannot reach files inside the Bindery pod. The API backup above lands in `/config/backups` on the PVC. To get a copy off the cluster, scale Bindery down and read the PVC from a helper pod (the chart always names the PVC `bindery-config`):

```bash
kubectl scale deployment/bindery --replicas=0
kubectl run bindery-copy --restart=Never --image=busybox:1.37 \
  --overrides='{"spec":{"volumes":[{"name":"config","persistentVolumeClaim":{"claimName":"bindery-config"}}],"containers":[{"name":"bindery-copy","image":"busybox:1.37","command":["sleep","3600"],"volumeMounts":[{"name":"config","mountPath":"/config"}]}]}}'
kubectl cp bindery-copy:/config/bindery.db ./bindery-pre-v1.db
kubectl delete pod bindery-copy
kubectl scale deployment/bindery --replicas=1

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

# Boot the new image against the copy. Schema migrations run at startup,
# so this is the rehearsal. There is no separate dry-run command: `bindery
# migrate` is the CSV and Readarr data importer, not the schema runner.
docker run --rm \
  -e BINDERY_DB_PATH=/tmp/bindery-dryrun.db \
  -e BINDERY_PORT=18787 \
  -v /tmp:/tmp \
  -p 18787:18787 \
  ghcr.io/vavallee/bindery:1.0.0
```

Check the logs for `applied migration version=25 file=025_multiuser.sql`. If the migration fails on the copy, Bindery logs the error and exits before starting.

### Kubernetes dry-run

The image has no shell, so a one-off pod cannot copy the database before starting Bindery. Use the copy from Step 1 instead: run the binary locally against it and verify integrity:

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
docker pull ghcr.io/vavallee/bindery:1.0.0

# 4. Recreate the container on the new image; the migration runs at startup.
#    (docker start would restart the old container on the old image.)
docker rm bindery && docker run ...   # or: docker compose up -d

# 5. Tail logs to confirm
docker logs -f bindery | grep -E "migration|error"
# Expect: applied migration version=25 file=025_multiuser.sql
```

### Kubernetes (Helm)

```bash
# 1. Copy DB out of PVC (Step 1, already done)

# 2. Update the image tag
helm upgrade bindery charts/bindery \
  --set image.tag=1.0.0 \
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

Open the **Users** page from the header: you should see your original admin account listed with role `admin`.

## Rollback

**There is no automated rollback for migration 025.** SQLite does not support `DROP COLUMN`, so the `owner_user_id` columns cannot be removed by reverting the binary.

To roll back: restore from the backup taken in Step 1.

### Docker / binary rollback

```bash
docker stop bindery
cp /config/backups/bindery_<timestamp>.db /config/bindery.db
# The database runs in WAL mode: drop the newer write-ahead files so they
# are not replayed onto the restored copy
rm -f /config/bindery.db-wal /config/bindery.db-shm
docker run ... ghcr.io/vavallee/bindery:0.22.0   # the last release before v1.0.0
```

### Kubernetes rollback

```bash
# Copy the backup back into the PVC through a helper pod, as in Step 1
kubectl scale deployment/bindery --replicas=0
kubectl run bindery-copy --restart=Never --image=busybox:1.37 \
  --overrides='{"spec":{"volumes":[{"name":"config","persistentVolumeClaim":{"claimName":"bindery-config"}}],"containers":[{"name":"bindery-copy","image":"busybox:1.37","command":["sh","-c","rm -f /config/bindery.db-wal /config/bindery.db-shm; sleep 3600"],"volumeMounts":[{"name":"config","mountPath":"/config"}]}]}}'
# The pod first removes the WAL mode files (bindery.db-wal, bindery.db-shm)
# so the newer write-ahead log is not replayed onto the restored copy
kubectl cp ./bindery-pre-v1.db bindery-copy:/config/bindery.db
kubectl delete pod bindery-copy

# Roll back the Helm release
helm rollback bindery
```

Data written to Bindery between the upgrade and the rollback will be lost — it was scoped to the new schema and is not recoverable from the pre-upgrade snapshot.

## See also

- [docs/troubleshooting-auth.md](troubleshooting-auth.md) — consolidated symptom→cause→fix table covering migration failures and all auth phases

## Troubleshooting migration failures

| Symptom | Cause | Fix |
|---------|-------|-----|
| Startup fails: `025_multiuser.sql pre-flight: table "authors" has N row(s) but users table is empty` | The database holds library data but no user account, so there is no user 1 to backfill ownership to | Create an account first. Boot the previous version, complete first-run setup, then upgrade. |
| Migration exits mid-run, DB appears corrupted | Should not happen — migration runs in a transaction | Confirm by opening the DB with `sqlite3 /config/bindery.db ".schema authors"`. If `owner_user_id` column is absent, the migration rolled back cleanly. Restore from backup and investigate the error in logs before retrying. |
| All data appears under the wrong user post-migration | Backfill wrote to a different DB file than expected | Confirm `BINDERY_DB_PATH` points to the correct file. Check `sqlite3 /config/bindery.db "SELECT count(*) FROM authors;"` against your pre-upgrade count. |
| Admin account missing after migration | User table had the account but role column was not set | `sqlite3 /config/bindery.db "UPDATE users SET role='admin' WHERE id=1;"` — this is safe to run on a live instance. |
