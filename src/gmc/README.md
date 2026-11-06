# genai-microservices-connector(GMC)

This repo defines the GenAI Microservice Connector(GMC) for Intel® Chatbot Toolkit. GMC can be used to compose and adjust GenAI pipelines dynamically
on Kubernetes. It can leverage the microservices provided by [GenAIComps](https://github.com/opea-project/GenAIComps) and external services to compose GenAI pipelines. External services might be running in a public cloud or on-prem by providing a URL and access details such as an API key and ensuring there is network connectivity. It also allows users to adjust the pipeline on the fly like switching to a different Large Language Model(LLM), adding new functions into the chain(like adding guardrails), etc. GMC supports different types of steps in the pipeline, like sequential, parallel, and conditional.

Please refer to this [usage_guide](./usage_guide.md) for sample use cases.

## Description

The GenAI Microservice Connector(GMC) contains the CustomResourceDefinition(CRD) and its controller to bring up the services needed for a GenAI application.
Istio Service Mesh can also be leveraged to facilitate communication between microservices in the GenAI application.


## Getting Started

- **CRD** defines are at [deployment/components/gmc/crds/](../../deployment/components/gmc/crds/)
- **API** is [api/v1alpha3/](api/v1alpha3/)
- **Controller** is at [internal/controller/](internal/controller/)

## Fingerprint parameter groups

A pipeline step opts into fingerprint parameter injection through two entries in
its internal-service `config`:

- **`paramsKind`** categorises the step's parameter group. It must be one of the
  kinds the fingerprint microservice serves from its
  `/v1/system_fingerprint/kinds` catalog (today `llm`, `retriever`, `reranker`,
  `query_rewrite`, `prompt_template`, `input_guard`, `output_guard`,
  `dataprep_guard`). The validating webhook fetches that catalog and rejects any
  other value at admission time, so a misconfigured `GMConnector` never reaches
  the controller, and a kind added in the fingerprint becomes valid with no
  webhook change. The catalog is cached and refreshed on a TTL; if the
  fingerprint cannot be reached the webhook keeps the last catalog it fetched, or
  admits the step when it has never fetched one, so a fingerprint outage does not
  block pipeline writes.
- **`paramsKey`** names the group's row, so two steps of the same kind (for
  example two `llm` steps) can be addressed independently. It is optional and
  defaults to the lowercased step name when unset; set it explicitly only when
  a step needs a key that differs from its name.

A step that declares neither entry does not take part in parameter injection,
so pipelines that predate this mechanism are unaffected.

### Ensure-keys registration

On each reconcile the controller collects the `(paramsKey, paramsKind)` pairs
its pipeline declares and posts them to the fingerprint service's
`/v1/system_fingerprint/ensure_keys` endpoint, so the default row for each group
exists before the router starts reading it. The call is idempotent (the
fingerprint service inserts only missing rows) and best-effort: a fingerprint
service that is slow or unavailable is logged and retried on the next reconcile,
and never blocks a pipeline from deploying.

| Variable | Default | Purpose |
| --- | --- | --- |
| `FINGERPRINT_SERVICE_URL` | `http://fingerprint-svc.fingerprint.svc:6012` | Base URL of the fingerprint microservice the controller registers keys with and the webhook fetches the paramsKind catalog from. |

## Fingerprint KV bridge

The controller propagates pipeline parameter groups from the system fingerprint
Postgres store to a NATS JetStream key/value bucket that the router reads. The
bridge is the only writer of that bucket and runs on the elected leader, so
there is a single writer regardless of how many controller replicas are up.

- **Change notifications.** The bridge holds a Postgres `LISTEN` connection on
  the `fingerprint_config_changed` channel. The fingerprint table's trigger
  emits a notification carrying `{pipeline, tenant, params_key}` on every write.
  On each notification the bridge reads the row and writes its `values` to KV.
- **Reconcile.** Every `KV_RECONCILE_INTERVAL_SECONDS` (default 45) the bridge
  compares all Postgres rows against the bucket: it inserts keys that are
  missing, rewrites keys whose value has drifted, and deletes keys that no
  longer have a matching row. Deletion is limited to keys whose pipeline still
  exists in Postgres. Three consecutive reconcile failures mark the bridge
  unhealthy; a success clears it.
- **Health endpoint.** Bridge health is exposed at `/readyz-bridge` on the
  metrics server (`--metrics-bind-address`, `:8080` by default) for manual and
  monitoring inspection. It is deliberately kept off the pod's `/readyz`
  readiness probe: the controller shares its pod with the validating webhook,
  so an unhealthy bridge (for example while Postgres or NATS is unreachable)
  must not pull the pod out of the Service and block GMConnector admission.
  Only `healthz.Ping` backs `/readyz`.
- **Bucket.** On startup the bridge opens the bucket `fingerprint` if it
  already exists and otherwise creates it with memory storage and a single
  history entry; an existing bucket is used as-is and its configuration is not
  re-applied. Its contents are always rebuildable from Postgres, so a NATS
  restart loses nothing durable.
- **Reconnect.** If the `LISTEN` connection drops the bridge backs off, opens a
  new connection and runs a full resync. A NATS reconnection triggers the same
  resync, so no change is lost while a connection is down.

### KV schema

- **Key:** `<pipeline>.<tenant>.<params_key>` - the three Postgres column
  values joined by dots, used verbatim (for example `chatqna._global.llm` or
  `chatqna._global.prompt_template`). Rows whose segments contain a dot,
  whitespace or a subject wildcard (`*`, `>`) are skipped, since they would
  produce an ambiguous JetStream key.
- **Value:** the raw JSON stored in the row's `values` column. The router
  deserializes it directly.

### Bridge environment

| Variable | Default | Purpose |
| --- | --- | --- |
| `NATS_URL` | _(unset - bridge stays idle)_ | JetStream server URL. |
| `POSTGRES_HOST` | `localhost` | Fingerprint Postgres host. |
| `POSTGRES_PORT` | `5432` | Fingerprint Postgres port. |
| `POSTGRES_USER` | `postgres` | Postgres user. |
| `POSTGRES_PASSWORD` | `postgres` | Postgres password. |
| `POSTGRES_DB` | `system_fingerprint` | Postgres database name. |
| `POSTGRES_SSLMODE` | `prefer` | libpq `sslmode` for the connection. The default `prefer` falls back to plaintext when the server declines TLS and never verifies the server, so the shipped default carries the credentials and parameter values unauthenticated - it is chosen because the in-repo postgresql chart serves no TLS. `require` encrypts but still does not verify the server; use `verify-full` with a configured server to get both. |
| `KV_RECONCILE_INTERVAL_SECONDS` | `45` | Reconcile interval in seconds. |

Deployment supplies these values; when `NATS_URL` is unset the bridge stays
idle so the controller still starts in environments without NATS. These are the
controller-side bridge variables; the router reads its own set, listed under
[Router environment](#router-environment).

## Router parameter injection

The router reads the same `fingerprint` KV bucket to inject parameter groups
into pipeline steps. It is a read-only consumer of the bucket; only the bridge
above writes to it.

- **KV watch.** On startup the router opens the `fingerprint` bucket and watches
  the keys for its own pipeline and tenant (`<pipeline>.<tenant>.*`, tenant
  defaulting to `_global`). Each update is cached in RAM under a
  `(tenant, params_key)` scope; the projected value already carries the wire
  shape downstream services read, so the router stores it as delivered, apart
  from a numeric top-level `version` field, which is reserved metadata for the
  version metric and is dropped rather than injected. A
  full resync runs on connect and on every NATS reconnection, so revisions
  missed while a connection was down are picked up rather than assumed to arrive
  on the watch. When `NATS_URL` is unset the router does not connect and serves
  entirely from the HTTP fallback. When `NATS_URL` is set but NATS or the bucket
  cannot be reached, the router logs and serves from the HTTP fallback while
  retrying the connection with a fixed backoff, so it never fails to start.
- **Selective injection.** For each step the router reads
  `internalService.config.paramsKey` (defaulting to the step name lowercased),
  looks up that one group, and merges its fragment onto the top level of the
  step's request. Only the named group is injected, so two steps of the same
  kind - for example two `Llm` steps with `paramsKey: llm_primary` and
  `paramsKey: llm_secondary` - receive different parameters. The router is
  shape-agnostic: the KV fragment is already in the wire shape downstream reads
  (flat kinds bare, nested kinds wrapped under their `*_params` key), so it is
  merged verbatim and the router holds no per-group layout knowledge. A new
  parameter kind therefore needs no router change.
- **Fallback chain.** The precedence is KV-fed RAM hit → legacy HTTP →
  last-known-good → empty. A lookup is served from RAM only for a value the
  watch actually delivered while connected. Otherwise - a miss, a value only
  ever fetched over HTTP, or the watch being down - the router calls the
  fingerprint microservice's scope-aware `config` endpoint for that one group,
  resolved under the router's own `(pipeline, tenant)`, and caches the result.
  A 404 (no group for the key, e.g. an unmanaged step) is treated as empty, so
  the legacy path injects the same single group the KV path would. If that call
  also fails it returns the last value it cached for the scope, or an empty
  group when nothing was ever cached, so downstream services fall back to their
  own defaults.
- **Fingerprint step.** With parameters injected per step, the standalone
  `Fingerprint` step is obsolete. The router skips a step named `Fingerprint`
  rather than calling it, so pipelines work whether or not the node is still
  present in the graph.
- **Scope.** Injection and the fingerprint-step skip apply to every leaf step
  regardless of the node type it is reached through. Each leaf step is injected
  once in `executeStep`, the shared send for `Sequence`, `Switch` and `Ensemble`
  nodes, so two same-kind steps in a parallel `Ensemble` receive different groups
  just as they do in a `Sequence`. A step routing to a sub-node is not injected;
  its own leaf steps are injected when the recursion reaches them.

## Router request handling

The router forwards each request through the graph and streams the terminal
node's response back to the client. Beyond parameter injection it enforces a
few safety and composition rules:

- **Composition semantics.** A `Sequence` stops and returns the last response
  when a step's condition does not match, and fails the request when a hard
  dependency answers unsuccessfully. A `Switch` selects the first branch whose
  condition matches, fails the request when a matched hard-dependency branch is
  unsuccessful, and returns `404` when no branch matches. An `Ensemble` runs its
  steps in parallel, propagates the initial request's parameters into each,
  merges the responses keyed by step name, and reports `207 Multi-Status` when a
  soft step did not succeed; a hard-dependency failure cancels the siblings and
  returns that step's response and status.
- **Recursion guard.** Routing into sub-nodes is capped; a graph cycle is
  rejected with `500` rather than recursing until the stack overflows.
- **Timeouts and limits.** Each downstream call and the overall request carry
  bounded deadlines; the server has a read-header timeout; the request body and
  multipart uploads are size-capped; and concurrent requests are bounded, with
  excess shed as `503`.
- **Health.** `/healthz` and `/readyz` on the router's `:8080` answer liveness
  and readiness with `200`. These are the router process's own endpoints and are
  distinct from the controller's `/readyz-bridge`, which also listens on `:8080`
  but in the separate controller process (see [Health endpoint](#fingerprint-kv-bridge)
  under the KV bridge). The KV watch degrades to the HTTP fallback rather than
  failing readiness, so it does not gate `/readyz`.

### Metrics

| Metric | Type | Purpose |
| --- | --- | --- |
| `router.config.source` | counter | Parameter fetches by serving source (`kv`, `legacy`, `lkg`), labelled by `pipeline` and `params_key`. |
| `router.config.version` | gauge | Version of the injected parameter group per `pipeline` and `params_key`. The projected value carries no version today, so this reports the KV revision as a monotonic stand-in (or a numeric `version` field if the value ever contains one); it is `0` for values served over the legacy HTTP fallback. |
| `router.pipeline.step` | histogram | Per-step latency for every step of every node type, labelled by `stepName`, `routerType` and `nodeName`. |
| `router.pipeline.ensemble.fanout` | histogram | An `Ensemble` node's fan-out width, labelled by `nodeName`, failure count and resulting status. |
| `router.pipeline.switch.decision` | counter | `Switch` routing decisions by chosen branch (or `no-match`), labelled by `nodeName`. |

### Router environment

| Variable | Default | Purpose |
| --- | --- | --- |
| `NATS_URL` | _(unset - HTTP fallback only)_ | JetStream server URL for the KV watch. |
| `PIPELINE_NAME` | _(graph name, then `default`)_ | Pipeline whose keys the router watches. |
| `TENANT_NAME` | `_global` | Tenant dimension of the cache scope. |
| `FINGERPRINT_SERVICE_URL` | `http://fingerprint-svc.fingerprint.svc:6012` | Base URL for the legacy HTTP fallback. |

### Router deployment and scaling

The router Deployment is not a standalone Helm resource. It is rendered from
`deployment/components/gmc/gmc-router.yaml.tpl`, which Helm bakes into the
`gmc-config` ConfigMap; the controller then applies it (with the PodDisruptionBudget
and, when enabled, the HorizontalPodAutoscaler emitted from the same template) into
each pipeline namespace. Scaling settings therefore live under `router` in the chart
values, not on a chart-level Deployment.

- **Health probes.** The container's liveness and readiness probes hit
  `:8080/healthz` and `:8080/readyz`; a hung process fails them, is restarted, and is
  taken out of Service rotation.
- **Replicas.** `router.replicaCount` sets a fixed count. The rollout uses
  `maxUnavailable: 0` so at least one pod keeps serving during an update, and pods
  carry a soft `kubernetes.io/hostname` anti-affinity to spread across nodes.
- **Resources.** `router.resources` sizes the router container. The controller's own
  requests and limits are separate, under `gmc.resources`, so raising one does not
  silently resize the other. A pipeline step file may still override the router
  through `services.gmc-router.resources`, which wins when set.
- **PodDisruptionBudget.** `router.pdb` (enabled by default, `minAvailable: 1`) keeps a
  pod serving through voluntary disruptions such as node drains.
- **Autoscaling.** Setting `router.hpa.enabled` emits a CPU-utilization HPA
  (`router.hpa.minReplicas`/`maxReplicas`/`targetCPUUtilizationPercentage`) targeting the
  router Deployment; the Deployment then omits `replicas` so the HPA owns the count.
  Custom scaling policies can be supplied through `router.hpa.behavior`.

## Monitoring

Each pipeline component manifest carries an optional `ServiceMonitor` document,
and the router manifest a `PodMonitor`. The controller applies them like any
other step resource, with an owner reference, so a monitor exists exactly for
the components a pipeline deploys and is garbage-collected with the
`GMConnector`. Rendering is gated only on `monitoring.enabled`; there is no
per-component switch, since a monitor ships with its pod.

### Where a monitor is deployed

**A monitor lives in the namespace of the workload it scrapes, not in a central
`monitoring` namespace.** For the `chatqna` pipeline the step `ServiceMonitor`s
and the router `PodMonitor` are all in `chatqna`, and likewise for every other
pipeline; the GMC controller's own `PodMonitor` is in the controller's namespace
(`system`). Two reasons: telemetry is now enabled and disabled with the service
rather than as a separate feature, so removing a namespace must remove its
monitoring with it; and there is then one predictable place to look for a given
monitor.

The rule is enforced by construction rather than by convention. No monitor
template writes a fixed namespace: a chart template lands in its release
namespace, and the documents the controller applies (the step manifests and
`gmc-router.yaml.tpl`) are namespaced by the controller as it applies them , 
`gmc-router.yaml.tpl` additionally interpolates the router namespace as
`{{.Namespace}}`, which resolves to the same value. That also keeps the owner
reference valid: a document left without a namespace would be cluster-scoped, and
Kubernetes rejects a cluster-scoped object owned by a namespaced `GMConnector`,
which puts the reconcile into a retry loop.
`src/gmc/internal/controller/monitoring_namespace_test.go` guards this.

Because the monitors are spread across pipeline namespaces, Prometheus must be
configured to look outside its own namespace - with prometheus-operator, through
`serviceMonitorNamespaceSelector` and `podMonitorNamespaceSelector` on the
`Prometheus` resource. This repo does not install Prometheus (see below), so that
selector is not ours to set; on a cluster where it is left at its default a
monitor in a pipeline namespace is created but not scraped.

These objects require the prometheus-operator CRDs (`monitoring.coreos.com`),
which this chart does not install - an external Prometheus is assumed. When the
CRD is absent the controller skips the monitor and records a Warning event
rather than failing the reconcile, so a pipeline deploys with or without
Prometheus present.

If prometheus-operator is installed *after* a pipeline is already running, the
controller does not watch the CRD, so it will not emit the monitors until its
next reconcile. Trigger one by restarting the controller
(`kubectl rollout restart deployment/<gmc-controller> -n system`) or by touching
any `GMConnector` - e.g. `kubectl annotate gmconnector <name> -n <ns>
reconcile-trigger="$(date +%s)" --overwrite`, which changes the object without
altering its pipeline and makes the operator re-run its reconcile.

Grafana dashboards ship as ConfigMaps labelled `grafana_dashboard: "1"` in the
namespace `monitoring.dashboardNamespace` watched by the Grafana sidecar.
