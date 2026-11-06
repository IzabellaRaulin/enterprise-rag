# Backup and Restore

Intel® Chatbot Toolkit can back up and restore everything a deployment has accumulated —
ingested documents, embeddings, conversations, and the accounts and roles in the identity realm —
through the platform's Velero-based backup mechanism. Opt-in and off by default.

The platform provides the mechanism (Velero, CSI volume snapshots, the profile-driven engine). This
toolkit declares what its own data is and drives that engine against it. See
[Toolkit Manager](https://github.com/intel/enterprise-ai-solutions)'s
`docs/customize/configuration.md` for the platform settings (`velero_enabled`,
`storage_backend`).

## Enabling it

Two independent switches, both required:

```yaml
# env/<name>/global_config.yaml (platform)
storage_backend: nfs                      # or ceph — must support CSI snapshots
velero_enabled: true                      # deploys the mechanism

# env/<name>/config.erag.yaml (this toolkit)
backup_enabled: true                      # arms this toolkit's profile
```

Leaving either off is a safe no-op, not a silent partial backup: with `backup_enabled: false` the
trigger loads the profile, reports it is not enabled, and exits without touching the cluster; with
`velero_enabled: false` there is nothing for the trigger to call. Whichever is off, check both before
concluding a run "did nothing" is a defect.

## Running a backup or restore

```bash
export KUBECONFIG=env/<name>/kubeconfig.yaml
.venv/bin/ansible-playbook -i env/<name>/inventory/hosts.yaml \
  -e env_dir=$(pwd)/env/<name> \
  -e @env/<name>/global_config.yaml -e @env/<name>/config.erag.yaml \
  -e kubernetes_kubeconfig=$(pwd)/env/<name>/kubeconfig.yaml \
  ext/enterprise.ai-erag/deployment/backup.yaml -e backup_action=backup

# restore: same invocation with -e backup_action=restore -e backup_confirm=yes
# (without backup_confirm=yes a restore stops at a confirmation prompt, before
#  anything is deleted)
```

Run this from a workstation, not from a pod. `kubernetes.core` prefers an in-cluster
service-account token over `KUBECONFIG` whenever one exists, so invoking the engine from inside the
cluster (a CI agent, for instance) can silently talk to the agent's own cluster instead of the one
named by `KUBECONFIG`. Unset `http_proxy`/`https_proxy` too — the engine only ever calls the target
cluster's own API, so a proxied call comes back `403` for no reason related to the backup itself.

Check the result:

```bash
kubectl -n velero get backups.velero.io -l ai-solutions.io/backup-profile=erag \
  -o custom-columns=NAME:.metadata.name,PHASE:.status.phase,\
SNAPS:.status.csiVolumeSnapshotsCompleted
```

Always name Velero's resources with their full group — `backups.velero.io`, not `backup`. This
platform also runs a PostgreSQL operator that registers its own `backups` CRD, and on a cluster
running both, the bare name resolves to that one and returns an empty list instead of an error.

### Restoring from a specific backup, not the latest one

A restore with no further options replays the newest `Completed` backup that carries this profile's
label. To go back further — an N-2 backup, or any specific one — list the candidates and name one
explicitly:

```bash
kubectl -n velero get backups.velero.io -l ai-solutions.io/backup-profile=erag \
  -o custom-columns=NAME:.metadata.name,PHASE:.status.phase,COMPLETED:.status.completionTimestamp \
  --sort-by=.status.completionTimestamp

# add -e velero_restore_from=<name> to the restore invocation above, e.g.:
#   ... ext/enterprise.ai-erag/deployment/backup.yaml \
#       -e backup_action=restore -e backup_confirm=yes -e velero_restore_from=backup-erag-20260910t120000
```

Naming one this way is the only thing that changes: the cleanup, the restore order, and every
assertion behave exactly as they do for the newest backup. There is no per-namespace or per-object
restore — the unit is always this whole profile, as of whichever Backup you name.

Or drive the round trip through the three lifecycle stages, which assert what a green backup or
restore does not otherwise prove:

```bash
cd ext/enterprise.ai-erag/src
SCENARIO=backup-restore CLUSTER_STATE=before-backup tox -e e2e-scenario
#   … run the backup above …
SCENARIO=backup-restore CLUSTER_STATE=after-backup  tox -e e2e-scenario
#   … run the restore above …
SCENARIO=backup-restore CLUSTER_STATE=after-restore tox -e e2e-scenario
```

| Stage | Asserts |
|---|---|
| `before-backup` | The backup store is `Available` and a `VolumeSnapshotClass` exists — the two ways a backup can complete while capturing nothing. Uploads a document, saves a conversation, creates an account |
| `after-backup` | The newest Backup for this profile is `Completed`, carries the running version's label, covers the namespaces the test data lives in, and attempted at least as many volume snapshots as there are volumes in them. Creates a second document, conversation and account |
| `after-restore` | The newest Restore is `Completed` and replayed one of this profile's backups; every PVC is `Bound` and every pod `Running`; the first document, conversation, answer and account are back; the second set is gone |

The question answered in `after-restore` is deliberately one the vector store has to answer, not
just the object store — a restore that replays documents but leaves the vector index unusable would
otherwise look identical to a working one.

## What is and is not restored

| Captured | Where |
|---|---|
| Ingested documents and their extracted content | The EDP and object-store namespaces |
| Vector embeddings | The vector-database namespace |
| Conversation history | The chat-history namespace |
| The identity realm — accounts, roles, credential hashes | The platform's shared PostgreSQL cluster, as a database on that cluster's volume |
| Gateway and pipeline configuration, UI configuration | Their own namespaces |

| Not captured | Why |
|---|---|
| `llm-inference` | Model weights are not user data and are re-pulled from the model catalogue |
| Pods, ReplicaSets, webhooks, generated pipeline resources | Their controllers and operators recreate or reconcile them; replaying stale copies fights that |

## The identity realm

The realm is not exported and re-imported: it is a database inside the platform's shared PostgreSQL
cluster, and that cluster keeps its data on one PersistentVolumeClaim on a snapshot-capable
StorageClass — the same shape as every other store this profile captures. The `keycloak` namespace is
in the profile too, but holds no volume of its own; it is there for its configuration and its
database credentials, which have to agree with the ones inside the restored volume.

Two consequences follow from the volume being **shared**:

- The volume also holds every other database in the platform's cluster — on a stock deployment, the
  model proxy's and the tracing service's. A restore rolls those back to the same point in time along
  with the realm. One volume, one replay. A deployment that cannot accept that should give this toolkit
  a database of its own.
- The snapshot is crash-consistent, not transactional across volumes: a document ingested during a
  backup can end up present in one store and absent from another, because each store is snapshotted at
  its own moment. Take backups when the system is not actively ingesting.

A restore takes the shared database down and brings it back, so anything that authenticates against
it — every solution sharing that Keycloak instance, not only this one — fails to sign in for the few
minutes the replay takes. After the replay, an account that existed at backup time is present again; one
created after the backup is not. A `post_restore` hook rolls the identity service so it stops answering
from its own cache of the pre-restore realm.

## The vector store after a restore

A restore recreates the vector database's StatefulSet pods, and Kubernetes gives them new IPs —
ordinary behaviour, but the vector store's own cluster topology (persisted to disk before the backup)
still names the *old* ones. Left alone, that leaves part of the keyspace unreachable while the cluster
still reports itself healthy: ingestion accepts work and never finishes it, and questions that depend
on the restored index either fail or hang. A self-heal built into the vector store's StatefulSet
detects a restored (not first-install) cluster, resolves each peer's current address, and tells every
surviving member where the others actually are now — this runs automatically and needs no operator
step, but if a question answerable only from the restored index still fails after a restore, check the
vector store's cluster state before assuming the restore itself failed:

```bash
kubectl exec -n vdb <redis-pod> -c redis -- redis-cli -a "$REDIS_PASSWORD" cluster info | grep cluster_state
kubectl exec -n vdb <redis-pod> -c redis -- redis-cli -a "$REDIS_PASSWORD" cluster nodes
```

## The pre-upgrade gate

With `backup_enabled: true`, an upgrade refuses to start unless there is a `Completed` backup for the
running version, no older than `upgrade_backup_max_age_hours` (default 6):

```bash
./es_auto_installer.sh upgrade erag --env <name>
#   refuses without a fresh backup, and says so

./es_auto_installer.sh upgrade erag --env <name> -- -e allow_upgrade_without_backup=true
#   explicit override
```

The gate is skipped, not failed, when `backup_enabled` is false — demanding a restore point from a
deployment that was never configured to produce one would only teach operators to reach for the
override.

## Troubleshooting

| Symptom | Likely cause / fix |
|---------|--------------------|
| A backup or restore run reports success but nothing seems to have happened | Check both `backup_enabled` (this toolkit) and `velero_enabled` (platform) — either off makes the trigger a no-op, and only `backup_enabled` gates it here |
| `kubectl get backup` returns nothing, even though one clearly ran | The bare name resolves to the PostgreSQL operator's own `backups` CRD on this platform. Use `backups.velero.io` |
| Every test in the backup-restore suite fails, unrelated to backup itself | An unrelated aggregated API (commonly `custom.metrics.k8s.io`, if it has joined Istio ambient) can fail cluster-wide discovery. The suite binds the Velero/CSI kinds it needs explicitly so this cannot take it down — but a bare `kubectl api-resources` hanging or erroring is the same underlying symptom |
| A question answerable only from restored data fails or hangs after a restore, while every pod is `Running` | Check the vector store's cluster state (see above) before assuming the restore failed — a healthy-looking `Running` pod can still be part of a cluster with a chunk of its keyspace unreachable |
| Login fails with a bare `401` shortly into a test run, on a cluster that was never restored | The generated-credentials Secret was regenerated by a later plain re-install while the identity database (on its own persistent volume) kept the earlier password. Rotate to a new password — Keycloak's password-history policy refuses to reset a user back to one it has already held |
| An upgrade refuses even though a backup was just taken | Check the backup's version label matches the running version, and that it is newer than `upgrade_backup_max_age_hours` — a backup taken for a different release does not satisfy the gate |

## Known limitations

- **The shared database volume is shared.** A restore rolls back every database on it, not only this
  toolkit's realm.
- **No transaction boundary across stores.** Take backups when the system is idle for the most
  consistent result.
- **No point-in-time recovery.** This is a snapshot-and-replay mechanism, not continuous backup.
- **Restore is whole-profile.** There is no per-document or per-conversation restore.

## Related docs

[Configuration](../customize/configuration.md#backup-and-restore) (`backup_enabled` and the upgrade
gate settings), the platform's `docs/customize/configuration.md` (`velero_enabled`,
`storage_backend`), [Troubleshooting](troubleshooting.md) (issues unrelated to backup specifically).
