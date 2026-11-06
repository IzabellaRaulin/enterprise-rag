# Mesh

Intel® Chatbot Toolkit namespaces join the Istio ambient mesh deployed by the platform. This doc covers how the chatbot services plug into the mesh, the current mTLS posture, and how to troubleshoot mesh issues.

## Mesh Integration

Istio ambient mesh is deployed by the **platform** (`roles/istio/` in the Toolkit Manager repo). It provides zero-trust mTLS without sidecars via ztunnel (one proxy per node).

RAG namespaces join the mesh by label. See `roles/app_pre_install/tasks/install.yaml` for the actual namespace creation and labeling logic.

### Plugging Workloads into the Mesh

When Istio is deployed, workloads need to be plugged into the mesh. The easiest way:

- **Label the namespace** with `istio.io/dataplane-mode: ambient` to add all current and future workloads into the mesh.
- **Label a specific pod** with `istio.io/dataplane-mode: ambient` to introduce only that workload.
- **Exclude a pod** from the mesh with `istio.io/dataplane-mode: none` (overrides the namespace setting).

Verify a pod joined the mesh by checking for the annotation:

```yaml
annotations:
  ambient.istio.io/redirection: enabled
```

This gets set as soon as Istio configures ztunnel correctly for the pod.

## Current mTLS Posture

The deployment enforces `STRICT` mTLS via `PeerAuthentication` resources applied to each RAG namespace. All inter-service traffic is encrypted; only mTLS connections are allowed.

**Authorization policies are not currently deployed.** The deployment ships zero `AuthorizationPolicy` resources. Only mutual TLS is enforced; service-to-service authorization (which services can talk to which) is not configured at this time. All services within the mesh can communicate with each other as long as they present a valid mTLS certificate.

For background on `PeerAuthentication` see [Istio PeerAuthentication reference](https://istio.io/latest/docs/reference/config/security/peer_authentication/).

## Introducing a New Service into the Mesh

To introduce a new service to the RAG mesh:

1. **Ensure the namespace is labeled** `istio.io/dataplane-mode: ambient` (this is done by `app_pre_install` for standard RAG namespaces).
2. **Ensure the workload has a well-defined `ServiceAccount`** (use `default` as a last resort).
3. **Verify communication** by watching ztunnel logs (see below).

If you need to configure authorization policies in the future, you would:
- Identify the `serviceIdentity` of the source and target (format: `cluster.local/ns/<namespace>/sa/<service-account>`)
- Create an `AuthorizationPolicy` resource targeting the destination workload with a selector
- List allowed source identities in the `principals` field

## Troubleshooting with Ztunnel Logs

Ztunnel is the ambient data plane component (one per node). It applies all authentication policies and logs all connection decisions.

### View ztunnel logs

```bash
kubectl logs -f -n istio-system -l app=ztunnel
```

Filter for a specific namespace:

```bash
kubectl logs -f -n istio-system -l app=ztunnel | grep edp
```

### Sample log entry (successful connection)

```log
23:25:37.932494Z  info  access  connection complete
src.addr=10.233.102.158:41772  src.workload="edp-celery-59bdb56886-fx4kt"  src.namespace="edp"
src.identity="spiffe://cluster.local/ns/edp/sa/edp-chart"
dst.addr=10.233.102.148:15008  dst.hbone_addr=10.233.102.148:6379
dst.service="edp-redis-master.edp.svc.cluster.local"  dst.workload="edp-redis-master-0"
dst.namespace="edp"  dst.identity="spiffe://cluster.local/ns/edp/sa/edp-redis-master"
direction="inbound" bytes_sent=22 bytes_recv=170 duration="164ms"
```

- `info` on success, `error` for connection issue
- `connection complete` - seen most often
- `src.identity` and `dst.identity` are the SPIFFE IDs (used by authorization policies)
- `dst.hbone_addr` - the real address requested by the source service

### Common errors

**Denial by `PeerAuthentication`:**
```log
error="connection closed due to policy rejection: explicitly denied by: istio-system/istio_converted_static_strict"
```
Cause: A service without mTLS or outside of the mesh attempted a plain text request to a service under `STRICT` mTLS policy.

**Port blocked by `NetworkPolicy`:**
```log
error="io error: deadline has elapsed" error="connection timed out maybe a NetworkPolicy is blocking HBONE port 15008"
```
Cause: A Kubernetes `NetworkPolicy` allows specific ports but doesn't include Istio port 15008.

**HTTP 503:**
```log
503 Service Unavailable
```
Cause: The actual target might be unhealthy or unavailable (e.g., 0 replicas). Review the health of the destination service.

## Useful Commands

List workload configuration in the mesh:

```bash
istioctl ztunnel-config workload
```

HBONE should be shown for every workload within the mesh. TCP is left for system or host-network pods.

View all service identities in the mesh (along with their certificates):

```bash
istioctl ztunnel-config services
# Example output:
# spiffe://cluster.local/ns/edp/sa/edp-redis-master
# spiffe://cluster.local/ns/fingerprint/sa/fingerprint
```

Set ztunnel log level:

```bash
istioctl zc log <ztunnel-pod-name> --level=info,access=debug
```

## Platform Istio Documentation

For Istio installation, version, and cluster-wide configuration see the Toolkit Manager Istio documentation:

- [Namespace Security Labels](https://github.com/intel/enterprise-ai-solutions/blob/main/docs/reference/labels.md)
- [Platform Architecture](https://github.com/intel/enterprise-ai-solutions/blob/main/docs/reference/architecture.md)
- [Network Architecture](https://github.com/intel/enterprise-ai-solutions/blob/main/docs/deploy/networking.md)

Istio troubleshooting guides:
- https://istio.io/latest/docs/ambient/usage/troubleshoot-ztunnel/
- https://github.com/istio/istio/wiki/Troubleshooting-Istio-Ambient
