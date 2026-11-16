# Telemetry

Intel® Chatbot Toolkit services emit metrics, logs, and traces to the platform observability stack (Prometheus + Grafana + Loki + Tempo). This doc covers accessing Grafana and the chatbot dashboards.

## Login

Visit `https://grafana.<base_domain_name>` (default: `https://grafana.solutions.ai`).

![login screenshot](../images/telemetry/login.png)

Two sign-in methods:

### Grafana admin credentials

Retrieve the admin password from the `grafana-admin-credentials` secret in the `monitoring` namespace:

```bash
kubectl get secret -n monitoring grafana-admin-credentials \
  -o jsonpath='{.data.password}' | base64 -d; echo
# Username: admin
```

### Keycloak SSO

On the Grafana login page, select **Sign in with Keycloak** and enter your username and password. Only users in the `erag-admins` or `erag-maintainers` Keycloak group can access Grafana.

## Dashboards

Click **Dashboards** in the left pane. Dashboards prefixed with `Chatbot Toolkit` are tailored to Intel® Chatbot Toolkit services. The Grafana instance also provides standard Kubernetes, Node Exporter, and Prometheus Overview dashboards for cluster-level monitoring.

![dashboard](../images/telemetry/dashboards.png)

Eight dashboards ship with this toolkit, each provisioned by the component that owns it:

| Dashboard | Shipped by | Covers |
|---|---|---|
| `Chatbot Toolkit / Services / Details` | `app_pipeline` (gmc chart) | Per-service resource use, API traffic, and logs |
| `Chatbot Toolkit / Services / Top` | `app_pipeline` (gmc chart) | Cross-service summary of the busiest microservices |
| `Chatbot Toolkit / Services / GMConnector (router)` | `app_pipeline` (gmc chart) | Pipeline router throughput and per-step timing |
| `Chatbot Toolkit / Services / EDP` | `app_edp` | Ingestion counts, chunks, stages, and errors |
| `Chatbot Toolkit / Services / Audio` | `app_audio` | ASR and TTS activity (AudioQnA flavour only) |
| `Chatbot Toolkit / HPA` | `app_hpa` | Replica counts and scaling decisions |
| `Chatbot Toolkit / VectorDB / Redis` | `app_vector_databases` | Redis vector store health and query load |
| `Chatbot Toolkit / VectorDB / Microsoft SQL Server` | `app_vector_databases` | MSSQL vector store health and query load |

A dashboard only appears if its component is enabled, so the Audio and VectorDB dashboards depend on the flavour and the `vector_databases_vector_store` you chose.

The three most-used ones are described below.

### Chatbot Toolkit / Services / Details

Overview of resource usage and service activity for selected namespace and services.

![Dashboard Chatbot Toolkit / Services / Details](../images/telemetry/dashboard_01_services_details.png)

Key panels:
- **System Resource Utilization**: CPU and memory usage per service
- **External API Traffic**: User-facing requests (service, method, endpoint, request rate, P95 duration)
- **Internal API Traffic**: Monitoring and health-check requests (independent of user activity)
- **Logs**: Recent log entries filterable by namespace or service. For deeper log analysis use Grafana's **Explore -> Logs** feature in the left panel.

### Chatbot Toolkit / Services / EDP

Metrics from the Enhanced Data Preparation (EDP) service: ingested documents, links, chunks, and processing statuses.

![Dashboard Chatbot Toolkit / Services / EDP](../images/telemetry/dashboard_02_services_edp.png)

Key panels:
- **General**: Total ingested files and links, chunks, blocked documents (unsafe content), files in error state
- **Details**: Trends for files, links, and chunks over time
- **Errors and Logs**: Registered errors over time and recent logs
- **Stage Overview**: Items in each processing stage (embedding, input guard scanning, etc.)

See [EDP documentation](../../src/edp/README.md) for service details.

### Chatbot Toolkit / HPA

Horizontal Pod Autoscaler activity: replica count, scaling actions over time, and the metrics used for scaling decisions (current value + threshold).

![Dashboard Chatbot Toolkit / HPA](../images/telemetry/dashboard_03_hpa.png)

The top panel shows current replica count for each HPA-managed service. Below, each service has a section with replica changes over time and the scaling metric.

See [Horizontal Pod Autoscaling](performance.md#horizontal-pod-autoscaling).

### Intel® Inference Toolkit Dashboards (tag `modelserving`)

Intel® Chatbot Toolkit relies on the **Intel® Inference Toolkit** component for LLM model serving. Models are served on vLLM or OpenVINO backends; Grafana provides dashboards for the backend in use. All AI Inference dashboards are tagged with `modelserving`, making them easy to find by filtering dashboards using that tag.

![EnterpriseAIInference / ModelServing / vLLM](../images/telemetry/dashboard_04_llm_inference.png)

Example dashboard: `EnterpriseAIInference / ModelServing / vLLM`

Typical panels:
- **Request throughput and queue**: Incoming requests per second, in-flight requests, queue depth
- **Latency**: End-to-end request latency and time-to-first-token distributions
- **Token metrics**: Prompt and generation token counts and rates (useful for capacity planning)
- **Engine resource usage**: CPU and memory consumption of model-serving pods, correlated with traffic panels

## Logs

In the left panel, select **Explore -> Logs** to access Grafana's integrated log exploration feature. View, search, and filter logs with advanced filtering, real-time streaming, and contextual log inspection.

![Logs Drilldown View](../images/telemetry/explore_logs.png)

For more on using this feature see [Grafana Logs Drilldown documentation](https://grafana.com/docs/grafana-cloud/visualizations/simplified-exploration/logs/get-started/).

## Platform Telemetry

Grafana itself, Prometheus, Loki, and Tempo are deployed by the **platform** (`roles/observability/`). For login setup, data retention, and platform dashboards see the Toolkit Manager observability documentation:

- [Toolkit Manager documentation](https://github.com/intel/enterprise-ai-solutions/blob/main/docs/README.md)
