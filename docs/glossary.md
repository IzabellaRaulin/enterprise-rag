# Glossary

[← Docs Index](README.md)

Terms used across this documentation, in alphabetical order.

---

**Ambient mesh.** The sidecar-free mode of Istio used by the platform. Traffic between services is encrypted with mutual TLS by a per-node proxy (ztunnel) instead of a proxy container in every pod. See [Mesh](reference/mesh.md).

**APISIX.** The API gateway in front of the RAG services. It terminates client requests to the pipeline and applies the access rules tied to your Keycloak session.

**Chunking.** Splitting a document into passages small enough to embed and retrieve individually. Chunk size and overlap decide how much context a single retrieval hit carries.

**Component.** One unit the installer deploys, backed by an Ansible role. RAG components are named `app_*`, for example `app_pipeline`, `app_edp`, `app_ui`. Listed in [Architecture](reference/architecture.md).

**EDP (Enhanced Data Preparation).** The ingestion pipeline: it watches an object store or SharePoint, extracts text from PDF and Office documents, chunks it, embeds it, and writes the vectors to the vector database.

**Embedding.** A numeric vector representing the meaning of a passage. Retrieval works by comparing the embedding of your question against the embeddings of stored passages. Changing the embedding model changes the vector dimensions and invalidates an existing index.

**Environment (`env/<name>/`).** One deployment's state: configuration, model catalog, kubeconfig, credentials, and logs. Selected with `--env`; defaults to `local`. Environments are independent, and install and teardown are always scoped to one.

**Flavour.** The preset that `init erag --flavour <name>` seeds `config.erag.yaml` from: `chatqna`, `docsum`, `translation`, `audioqna`, or `pl_chatqna`. See [Pipelines](deploy/pipelines.md).

**GMC (GenAI Microservices Connector).** The Kubernetes operator, with its own custom resources, that composes individual microservices into a running pipeline according to a service-graph definition and routes requests through the steps in order.

**Guardrails.** Scanning steps around the LLM. The input guardrail inspects the prompt before generation, the output guardrail inspects the answer before it reaches the user.

**HPA (Horizontal Pod Autoscaler).** The Kubernetes controller that adds and removes replicas of a service under load. Enabled by default via `hpa_enabled`, and disabled for minimum-hardware deployments. See [Performance](operate/performance.md).

**Intel® Inference Toolkit.** Serves the models over an OpenAI-compatible endpoint using KServe with vLLM or OpenVINO™ Model Server. Toolkit Manager clones it into `ext/enterprise.ai-inference`. The installer target is `inference`.

**Ingestion.** Getting documents into the system so they can be retrieved: upload or sync, extract, chunk, embed, index. Handled by EDP.

**Keycloak.** The identity provider. It issues the tokens the gateway checks on every request, and can federate to Microsoft Entra ID or Active Directory. See [Authentication](customize/auth.md).

**KServe.** The Kubernetes model-serving framework used by Intel® Inference Toolkit to run model servers and expose them as cluster services.

**Installer target.** A group of components installed as a unit, in dependency order: `infrastructure`, `platform`, `inference`, `erag`. `inference` is Intel® Inference Toolkit. `erag` is Intel® Chatbot Toolkit. Installing one target pulls in the targets it depends on.

**MCP (Model Context Protocol).** A protocol that lets AI agents discover and call tools. The MCP gateway exposes retrieval and ingestion as such tools; off by default. See [MCP](customize/mcp.md).

**Model catalog (`models-rag.yaml`).** The per-environment list of servable models with their runtime, CPU and memory sizing, and server arguments. `inference_models` in `config.erag.yaml` selects entries from it by name.

**model-store.** The persistent volume that holds downloaded model weights. Because it survives undeploy, switching models or pipelines does not re-download weights.

**NATS.** NATS JetStream, deployed with NKey authentication, holds the GMC router state that the pipeline uses to route requests between steps.

**NRI CPU balloons.** The platform's NUMA-aware CPU pinning policy. It reserves groups of cores for model servers so inference threads are not scheduled across memory domains. Set `kubernetes_cpu_policy: best-effort` to disable it on small machines.

**Pipeline.** The ordered set of microservices a request passes through, for example embedding, retriever, reranking, prompt template, guardrail, LLM. One pipeline is deployed at a time.

**Pipeline type.** The service graph a pipeline is composed from: `chatqna`, `docsum`, or `translation`.

**Reranking.** A second-pass scoring model that reorders retrieved passages by relevance, so the prompt carries the best few rather than the first few.

**Retrieval-augmented generation (RAG).** Answering a question by first retrieving relevant passages from your own corpus and then asking the model to answer using only those passages. This grounds answers in your data and makes citation possible.

**Retriever.** The service that turns a query embedding into candidate passages from the vector database.

**System fingerprint.** The service that records the deployed configuration and checks its integrity, so a running system can be compared against the setup it was deployed with.

**Target.** What you pass to `install`, `teardown`, or `validate`: an installer target such as `erag`, or a single component such as `app_pipeline`. Dependencies are resolved automatically unless you pass `--only`.

**Variant.** A modification of a pipeline type: `base`, `query-rewrite`, `output_guard`, `retrieve-rerank`, or `upload`. Variants add or remove steps and change which models are needed. See [Pipelines](deploy/pipelines.md#pipeline-variants).

**Vector database.** The store that holds embeddings and answers similarity queries. Selectable with `vector_databases_vector_store`: `redis-cluster` (default), `mssql`, or `pgvector`.

**ztunnel.** The per-node proxy that carries mutual TLS traffic in an Istio ambient mesh.
