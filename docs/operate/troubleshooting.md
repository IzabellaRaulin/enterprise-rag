# Troubleshooting

Common symptoms, causes, and fixes for Intel® Chatbot Toolkit deployments.

## Deployment Issues

| Symptom | Likely cause / fix |
|---------|--------------------|
| `install erag` fails with "inference layer not found" | Intel® Inference Toolkit must be installed first. Run `./es_auto_installer.sh install inference --env <name>` before `install erag`. |
| Namespace stuck in `Terminating` during teardown | Finalizers or resources not cleaned up. Force-delete: `kubectl delete ns <namespace> --grace-period=0 --force`. |
| Helm install fails with "release already exists" | Previous install failed mid-way. Uninstall: `helm uninstall <release> -n <namespace>`, then retry. |
| Models not deployed after `install erag` | Models don't auto-deploy unless both `inference_models_enabled: true` AND the model has `autodeploy: true` in the catalog. Deploy manually: `./model-manager deploy <name> --wait`. |
| Component fails with "image pull backoff" | Registry rate limit or proxy issue. Configure a pull-through mirror, or pre-pull images: `docker pull <image>:<tag>` on each node. |
| PVC stays `Pending` | Storage class not ready or no available provisioner. Check: `kubectl get sc`, `kubectl get pv`, and storage backend logs. |

## Model and Inference Issues

| Symptom | Likely cause / fix |
|---------|--------------------|
| Model stuck not-Ready after deploy | Check logs: `kubectl logs -n llm-inference -l app.kubernetes.io/name=<name> -f`. CPU model load plus warmup takes minutes - often slow, not broken. |
| Model requests fail with "model not found" | The `model` field in the request must match the deploy **name**, not the Hugging Face `model_id`. List registered models: `kubectl get llminferenceservice -n llm-inference`. |
| Embedding/reranking requests return errors | Verify the model endpoint is healthy: `kubectl get llminferenceservice,inferenceservice -n llm-inference`. Check that `inference_models` in `config.erag.yaml` lists the right models with `role: embedding` and `role: reranking`. |
| Download Job fails with "permission denied" | The downloader runs non-root; ensure the PVC is writable. The Job sets `HOME` and cache paths to writable locations. |
| Model "gated" error | Set `HF_TOKEN` in the catalog and re-deploy: `./model-manager deploy <name> --wait`. See [Gated models](https://github.com/intel/enterprise-inference/blob/main/docs/deploy/deploy_models.md#gated-models-hugging-face-token). |
| Download fails behind a corporate proxy | Set `http_proxy` / `https_proxy` / `no_proxy` in the catalog's `network:` section, or via `MM_HTTP_PROXY` / `MM_HTTPS_PROXY` / `MM_NO_PROXY`. |

## Pipeline and GMC Issues

| Symptom | Likely cause / fix |
|---------|--------------------|
| GMConnector stuck not-Ready | Check GMC manager logs: `kubectl logs -n <pipeline-namespace> -l app.kubernetes.io/name=gmc-manager`. Verify all pipeline steps have matching Service and Deployment resources: `kubectl get svc,deploy -n <pipeline-namespace>`. |
| Router service returns 503 | One or more pipeline steps are unhealthy. Check: `kubectl get pods -n <pipeline-namespace>`. Inspect logs of unhealthy pods. |
| Request goes through but returns empty results | Vector DB mismatch. Verify retriever and ingestion are using the same `vector_algorithm` setting. See [Performance](performance.md#verifying-the-redis-vector-index). |
| Pipeline variant not applied | Variant is set via `pipeline_variant` in `config.erag.yaml`. Re-run `install erag` after changing it (GMConnector must be regenerated). |
| NATS connection errors | NATS uses NKey auth. Verify the `nats-auth` secret exists in the pipeline namespace: `kubectl get secret -n <pipeline-namespace> nats-auth`. |

## EDP Issues

| Symptom | Likely cause / fix |
|---------|--------------------|
| File upload fails with 500 error | Check EDP API logs: `kubectl logs -n edp -l app.kubernetes.io/name=edp`. Verify PostgreSQL is healthy: `kubectl get cluster -n edp`. |
| Files stuck in processing state | Celery workers may be overloaded or unhealthy. Check: `kubectl get pods -n edp -l app.kubernetes.io/component=celery`. Scale workers: `kubectl scale deploy -n edp edp-celery --replicas=<N>`. |
| "No space left on device" during ingestion | EDP blob storage (SeaweedFS / S3) is full. Increase PVC size or clean up old files. Check storage usage: `kubectl exec -n edp <edp-pod> -- df -h`. |
| Vector DB index has two entries (queries return no results) | Retriever and ingestion are using different `vector_algorithm` settings. Fix: set both to the same value in `config.erag.yaml`, redeploy, and re-upload files. See [Performance](performance.md#verifying-the-redis-vector-index). |

## UI and Gateway Issues

| Symptom | Likely cause / fix |
|---------|--------------------|
| UI doesn't load (blank page) | Check browser console for errors. Verify APISIX gateway is running: `kubectl get pods -n <pipeline-namespace> -l app.kubernetes.io/name=apisix`. |
| UI returns 401 Unauthorized | Missing or expired JWT. Keycloak token expires in 15 minutes by default. Re-login or generate a new token: `source .../get-keycloak-token.sh`. |
| UI returns 503 Service Unavailable | Router service or one of its backend steps is unhealthy. Check: `kubectl get pods -n <pipeline-namespace>`. |
| APISIX returns 404 | Route not configured. Verify ApisixRoute resources exist: `kubectl get apisixroute -n <pipeline-namespace>`. Check APISIX logs: `kubectl logs -n <pipeline-namespace> -l app.kubernetes.io/name=apisix`. |
| TLS certificate warnings from browser | Self-signed CA. Import the CA cert into your trust store: `env/<env>/logs/ai-solutions-ca.crt`. |

## Vector Database Issues

| Symptom | Likely cause / fix |
|---------|--------------------|
| Redis cluster pods crash-looping | Insufficient memory. Increase resource limits in `config.erag.yaml` under `vector_databases_stores.redis-cluster.resources`. |
| MSSQL pod fails to start | EULA not accepted. Re-run `install erag` and answer `Y`, or set `MSSQL_ACCEPT_EULA=Y` in the installer's environment. See [Configuration › Microsoft SQL Server 2025](../customize/configuration.md#microsoft-sql-server-2025). |
| Queries return no results after algorithm change | Old index still exists. Re-upload all files to trigger re-indexing with the new algorithm. Verify only one index exists: `kubectl exec -it <redis-pod> -c redis -- redis-cli FT._LIST`. |

## Istio Mesh Issues

| Symptom | Likely cause / fix |
|---------|--------------------|
| Service can't reach another service (connection timeout) | Not in the mesh, or Istio port 15008 blocked by NetworkPolicy. Verify pod has `ambient.istio.io/redirection: enabled` annotation. Check ztunnel logs: `kubectl logs -n istio-system -l app=ztunnel | grep <namespace>`. |
| Ztunnel logs show "policy rejection" | `STRICT` mTLS enforced but source isn't presenting a cert. Verify source pod is in the mesh. If the source can't join the mesh, consider adding a port-level `PERMISSIVE` exception in `PeerAuthentication`. |

See [Mesh](../reference/mesh.md) for troubleshooting Istio issues.

## Debug Tool

For comprehensive diagnostics, use the debug tool:

```bash
cd /home/.../applications.ai.enterprise.ai-solutions/ext/enterprise.ai-erag/deployment
python tools/debug_tool.py --config-dir /path/to/env/<name>/ [--output-dir <dir>]
```

This collects pod logs, resource descriptions, config files (redacted), and Helm state into a timestamped bundle.

## Useful Commands

```bash
# Status of all installed components
./es_auto_installer.sh status --env <name>

# Validate Intel® Chatbot Toolkit
./es_auto_installer.sh validate erag --env <name>

# Test ChatQnA pipeline
cd deployment && ./scripts/test_connection.sh

# Test DocSum pipeline
cd deployment && ./scripts/test_docsum.sh

# Test MCP gateway
cd deployment && python scripts/test_mcp.py

# List deployed models
kubectl get llminferenceservice,inferenceservice -n llm-inference

# View GMConnector status
kubectl get gmconnector -n <pipeline-namespace>
kubectl describe gmconnector <name> -n <pipeline-namespace>

# Check HPA status
kubectl get hpa -n <pipeline-namespace>

# View logs for a specific microservice
kubectl logs -n <pipeline-namespace> -l app.kubernetes.io/name=<service> -f

# Check EDP job status
kubectl get pods -n edp -l app.kubernetes.io/component=celery
kubectl logs -n edp -l app.kubernetes.io/name=edp -f

# Inspect vector DB index
kubectl exec -it <redis-cluster-pod> -c redis -- redis-cli -a <password> FT._LIST
kubectl exec -it <redis-cluster-pod> -c redis -- redis-cli -a <password> FT.INFO <index>
```

For platform-level problems (cluster, storage, certificates, DNS) see the [Toolkit Manager documentation](https://github.com/intel/enterprise-ai-solutions/blob/main/docs/README.md).
