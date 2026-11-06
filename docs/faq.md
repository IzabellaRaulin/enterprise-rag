# Frequently Asked Questions

[← Docs Index](README.md)

Short answers with links to the full detail. New here? Read [Meet Intel® Chatbot Toolkit](meet/meet.md) first.

---

## Getting started

### What is Intel® Chatbot Toolkit?

A complete retrieval-augmented generation application you deploy on your own hardware: document ingestion, vector search, retrieval pipelines, guardrails, chat UI, single sign-on, and dashboards. [Toolkit Manager](https://github.com/intel/enterprise-ai-solutions) installs and operates it. The install target is `erag`. See [Meet](meet/meet.md).

### Which repository do I clone?

Clone `github.com/intel/enterprise-ai-solutions` and run every command from its root. `init erag` clones this repository into `ext/enterprise.ai-erag` at a pinned revision. You never clone this repository directly to deploy it.

### Do I have to install the platform before Intel® Chatbot Toolkit?

No. `install erag` pulls its dependencies automatically, in order: infrastructure, the platform services, Intel® Inference Toolkit, then Intel® Chatbot Toolkit. Installing the platform first is optional and useful only when you want to confirm model serving works before adding the chatbot. See [Quick Start](quickstart/quickstart.md).

### How long does a deployment take?

Intel® Chatbot Toolkit itself takes 20 to 30 minutes once the platform is up. A full deployment from bare metal, including Kubernetes provisioning and model downloads, takes longer and depends on your network.

### What hardware do I need?

On top of the platform: 60 logical cores, 128 GB RAM, and 200 GB disk for a multi-user workload. A limited single-user configuration runs on 32 logical cores and 64 GB RAM for evaluation only, and requires tuning. See [Prerequisites](quickstart/prerequisites.md).

### Do I need a GPU or an accelerator?

No. Model serving is tuned for Intel® Xeon® CPUs, including NUMA-aware CPU pinning.

### Do I need internet access?

Yes, for container images and model weights. Proxy settings are configured in the platform's `global_config.yaml`. To deploy from your own registry instead, see [Building Images](customize/images.md).

### Do I need a Hugging Face token?

Only for gated models such as Llama, Mistral, or Gemma. Export `HF_TOKEN` before installing. Open models need nothing.

---

## Pipelines and configuration

### What is the difference between a flavour, a pipeline type, and a variant?

A **flavour** is the preset that `init erag --flavour <name>` seeds your configuration from. A **pipeline type** is the service graph that gets composed (`chatqna`, `docsum`, `translation`). A **variant** modifies that graph, for example inserting a query-rewrite step. See [Pipelines](deploy/pipelines.md).

### Which flavours are available?

`chatqna` (default, conversational retrieval), `docsum` (document summarization), `translation`, `audioqna` (voice), and `pl_chatqna` (Polish-language retrieval).

### Can I run more than one pipeline at the same time?

No. One pipeline is deployed at a time. Switching re-composes the service graph and tears down the previous pipeline; the vector store lives in its own namespace and is preserved. See [Pipelines](deploy/pipelines.md#switching-pipelines).

### How do I switch pipelines after installing?

Edit `pipeline_type` and `pipeline_variant` in `env/<name>/config.erag.yaml`, then re-run `install erag`. You do not re-run `init`.

### Why does `init erag --flavour` refuse to run on an environment I already created?

To protect edits you have already made to the configuration. Either use a fresh `--env`, or delete `env/<name>/config.erag.yaml` to reseed it from the flavour.

### Which vector databases are supported?

`redis-cluster` (default), `mssql`, `pgvector` and `milvus`, selected with `vector_databases_vector_store`. See [Configuration](customize/configuration.md).

### How do I change the LLM, embedding, or reranking model?

List the models under `inference_models` in `env/<name>/config.erag.yaml`. Each entry must exist in `env/<name>/models-rag.yaml`. See [Models](customize/models.md).

### Do I need to deploy models manually with `model-manager`?

No. `install erag` deploys the models declared in `inference_models` through its own step, sizes them, and undeploys models that are no longer needed when you switch pipelines.

### What happens if I change the embedding model after ingesting documents?

Embedding dimensions change and the existing index is not rebuilt automatically, so retrieval will return nothing useful. Re-ingest your documents after the change. See [Pipelines - best practices](deploy/pipelines.md#best-practices).

### Where do my settings live, and why do nested keys not work?

Everything is under `env/<name>/`: `config.erag.yaml` for chatbot settings, `global_config.yaml` for platform settings, `models-rag.yaml` for the model catalog. The override surface is flat because files are merged shallowly; a nested dictionary replaces the role default instead of merging into it. See [Configuration](customize/configuration.md).

### Can I keep several deployments side by side?

Yes. Each `--env <name>` has its own configuration, credentials, and kubeconfig. All environments share one `ext/` checkout, so they cannot be pinned to different revisions of this repository.

---

## Access, identity, and security

### Where are the credentials after installing?

In `env/<name>/logs/rag/default_credentials.yaml`. They are one-time passwords; change them after first login. A few service credentials live in Kubernetes secrets instead, listed in [Install](deploy/install_rag.md#credentials).

### How do I open the UI?

The gateway binds ports 80 and 443 on the cluster node. Add the base domain and its subdomains to `/etc/hosts` on the machine with the browser, forwarding with `ssh -L 443:localhost:443` if that machine is not the cluster node. See [Install - access the UI](deploy/install_rag.md#access-the-ui).

### Why does the browser warn about the certificate?

Self-signed certificates are the default. Visit `https://s3.<base_domain_name>` and accept the warning before ingesting documents, otherwise uploads fail silently in the browser.

### Can I use our corporate identity provider?

Yes. Keycloak federates to Microsoft Entra ID, with multi-factor authentication and Active Directory federation. See [Authentication](customize/auth.md). The same Entra ID app registration also enables SharePoint ingestion.

### Can it ingest from SharePoint or an object store?

Yes. SharePoint Online with scheduled sync is covered in [SharePoint](customize/sharepoint.md); S3-compatible and NetApp ONTAP S3 backends are covered in [Object Store](customize/object_store.md).

### Is traffic between services encrypted?

Chatbot namespaces join the platform's Istio ambient mesh, which provides mutual TLS without sidecars. The current posture and its limits are documented in [Mesh](reference/mesh.md).

### Can AI agents use the retrieval and ingestion capabilities?

Yes, through the Model Context Protocol gateway. It is off by default; set `mcp_enabled: true`. See [MCP](customize/mcp.md).

---

## Running it

### How do I check that the deployment is healthy?

`./es_auto_installer.sh status --env <name>` for namespaces, pods, and endpoints, and `./es_auto_installer.sh validate erag --env <name>` for health checks. The pipeline test scripts under `ext/enterprise.ai-erag/deployment/scripts/` send a real request.

### Where are the logs?

Installer and Ansible logs are in `env/<name>/logs/`, for example `install-erag-*.log`. Service logs are in Loki and browsable from Grafana. See [Telemetry](operate/telemetry.md).

### What can I see in Grafana?

Dashboards for per-service traffic, the ingestion pipeline, autoscaling, and LLM inference, alongside the platform dashboards. See [Telemetry](operate/telemetry.md).

### An install failed halfway. Can I just re-run it?

Yes. Installs are idempotent, so re-running continues rather than duplicating work. Check the log in `env/<name>/logs/` first to fix the underlying cause.

### How does it scale under load?

Horizontal Pod Autoscaling is enabled by default (`hpa_enabled: true`) and disabled for minimum-hardware deployments. Per-step resources, replica sizing, and vector-database tuning are in [Performance](operate/performance.md).

### How do I remove it?

`teardown erag` removes Intel® Chatbot Toolkit in reverse order and undeploys its models, leaving the cluster, the platform, and Intel® Inference Toolkit running. `teardown infrastructure` removes the cluster and everything on it. Teardown is environment-scoped: use the same `--env` you installed with.

### Will switching models or pipelines re-download the weights?

No. Weights stay on the `model-store` persistent volume, so a model that comes back is re-placed rather than re-downloaded.

### Can I use my own container registry?

Yes. Build the component images and point `registry` and `tag` in `config.erag.yaml` at your registry. See [Building Images](customize/images.md).

### Something is broken and none of this covers it.

Start with [Troubleshooting](operate/troubleshooting.md), which maps symptoms to causes and includes the debug collection tool. If you need further support, access supporttickets.intel.com
