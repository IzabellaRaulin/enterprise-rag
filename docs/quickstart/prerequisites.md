# Prerequisites

[← Docs Index](../README.md)

Everything you need before Toolkit Manager installs Intel® Chatbot Toolkit on the platform.

---

## Platform prerequisites

Toolkit Manager installs and operates Intel® Chatbot Toolkit on the platform. The install target is `erag`. The platform provides Kubernetes, storage, observability, model serving, and authentication before the chatbot can be deployed.

> [!NOTE]
> The platform prerequisites (OS, storage backends, node configuration, and SSH setup) are owned by Toolkit Manager. See:
> - [Platform Prerequisites](https://github.com/intel/enterprise-ai-solutions/blob/main/docs/quickstart/prerequisites.md) for base requirements (OS, hardware, tooling, credentials)
> - [Multi-Node & BYO Cluster](https://github.com/intel/enterprise-ai-solutions/blob/main/docs/deploy/topologies.md) for multi-node and bring-your-own-Kubernetes setups

---

## Hardware requirements for RAG

These are **additional** requirements on top of the platform. Sizing below assumes the platform and Intel® Inference Toolkit are already running.

### Xeon-only deployment

To deploy Intel® Chatbot Toolkit using Xeon only:

| Configuration | CPU cores | RAM | Disk | Notes |
|---|---|---|---|---|
| Standard minimum | 60 logical cores | 128 GB | 200 GB | Multi-user production workload |
| Limited single-user | 32 logical cores | 64 GB | 200 GB | Evaluation or dev use only; requires config tuning |

> [!NOTE]
> Logical cores = hardware threads (vCPUs), not physical cores.

The **limited single-user deployment** (32 cores / 64 GB RAM) is suitable only for evaluation, development, or single-user testing. It requires tuning model resource limits and disabling autoscaling. See [Deploying on Minimum Hardware](#deploying-on-minimum-hardware) below.

---

## Software prerequisites

> [!IMPORTANT]
> Gated models fail to deploy with an authorization error, not a clear message about the token. If you plan to serve Llama, Mistral, or Gemma, export `HF_TOKEN` **before** running `install erag`.

- **Hugging Face model access:** Ensure you have the necessary access to download and use the models your deployment will serve. Gated models (Llama, Mistral, Gemma) require a [Hugging Face token](https://huggingface.co/settings/tokens) exported as `HF_TOKEN` before install.

- **Multi-node clusters require ReadWriteMany (RWX) storage:** If deploying across multiple nodes, a CSI driver supporting `ReadWriteMany` access mode is required. Toolkit Manager can provision NFS (`storage_backend: nfs`) or NetApp Trident (`storage_backend: netapp-trident`). Single-node deployments use `local-path` by default. See [Platform Storage Documentation](https://github.com/intel/enterprise-ai-solutions/blob/main/docs/customize/configuration.md#storage) for configuration.

---

## Cluster provisioning and storage

Cluster provisioning, operating system requirements, Kubernetes versions, storage backends, and node topology are owned by **Toolkit Manager**.

For those topics, see:
- [Platform Prerequisites](https://github.com/intel/enterprise-ai-solutions/blob/main/docs/quickstart/prerequisites.md) - OS, tooling, hardware minimums for the platform
- [Platform Quickstart](https://github.com/intel/enterprise-ai-solutions/blob/main/docs/quickstart/quickstart.md) - installing the cluster
- [Multi-Node & BYO Cluster](https://github.com/intel/enterprise-ai-solutions/blob/main/docs/deploy/topologies.md) - multi-node and bring-your-own-Kubernetes setups
- [Storage Configuration](https://github.com/intel/enterprise-ai-solutions/blob/main/docs/customize/configuration.md#storage) - NFS, Ceph, NetApp Trident

---

## Deploying on Minimum Hardware

When deploying on the minimum hardware configuration (60 logical cores / 128 GB RAM) or the limited single-user configuration (32 logical cores / 64 GB RAM), additional tuning is required.

### KV cache reduction (required)

Model servers cache key-value pairs during inference. The default cache size (`10` GB) must be reduced to `1` GB for minimum-hardware deployments.

Edit the model catalog **after** running `init erag` (it creates the catalog at `env/<name>/models-rag.yaml`):

```bash
nano env/<name>/models-rag.yaml
```

Find the models you will deploy and reduce their `VLLM_CPU_KVCACHE_SPACE` setting from `"10"` to `"1"`:

```yaml
  - name: llama3-8b-awq
    model_id: casperhansen/llama-3-8b-instruct-awq
    category: llm
    runtime: vllm
    server_version: "0.24.0"
    cpu: 32
    memory: 24Gi
    env:
      VLLM_CPU_KVCACHE_SPACE: "1"    # reduced from "10"
    args: [--max-num-seqs, "32", ...]
```

Repeat for every model in `inference_models` (LLM, embedding, reranking).

> [!IMPORTANT]
> Reducing the KV cache from `10` GB to `1` GB significantly lowers vLLM memory usage but reduces the number of concurrent requests the model can cache. This trade-off is acceptable for evaluation and single-user deployments.

### Limited single-user (32 cores / 64 GB) additional steps

For the **32 logical cores / 64 GB RAM** configuration, also set the following in `env/<name>/config.erag.yaml` before running `install erag`:

```yaml
hpa_enabled: false                  # disable Horizontal Pod Autoscaling
kubernetes_cpu_policy: best-effort  # disable NRI CPU pinning
```

Additionally, reduce the model server CPU and memory limits in `env/<name>/models-rag.yaml`:

```yaml
  - name: llama3-8b-awq
    cpu: 16                          # reduced from 32
    memory: 16Gi                     # reduced from 24Gi
    replicas: 1                      # single replica only
    env:
      VLLM_CPU_KVCACHE_SPACE: "1"
```

---

## Next steps

Once prerequisites are met, proceed to [Quickstart](quickstart.md) to deploy Intel® Chatbot Toolkit.
