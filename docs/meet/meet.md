# Meet Intel® Chatbot Toolkit

[← Docs Index](../README.md)

The theory before the commands: what this is, why it exists, and when you would choose it.

---

## What it is

Intel® Chatbot Toolkit is a complete retrieval-augmented generation application that you deploy on your own hardware. It takes your documents, makes them searchable by meaning, and answers questions about them with citations, through a web UI, a REST API, or as a tool exposed to AI agents.

It is not a library or a reference notebook. It is a running system: document ingestion, a vector database, the retrieval pipeline, guardrails, single sign-on, dashboards, and autoscaling, all deployed together by one command.

[Toolkit Manager](https://github.com/intel/enterprise-ai-solutions) installs and operates it. The platform underneath provides Kubernetes, storage, service mesh, identity, and model serving through Intel® Inference Toolkit. This toolkit provides the RAG application on top. `erag` is the install target name.

## Why it exists

A working RAG system is not one model call. It is roughly twenty moving parts: an embedding model, a vector store, a retriever, a reranker, prompt templating, an LLM, input and output guardrails, a document ingestion pipeline, chat history, a UI, and an identity provider in front of all of it. Each part needs to be sized, secured, monitored, and kept compatible with the others.

Teams that assemble this themselves spend their first months on plumbing rather than on their use case, and typically stop short of the parts that production actually requires: mutual TLS between services, per-user access control, an upgrade path, and dashboards that show why a query was slow.

This project is that plumbing, already assembled and tuned for Intel® Xeon® processors, with the parts you are likely to change exposed as configuration rather than code.

## Why you would use it

- **You keep your data.** Documents, embeddings, and the models all stay on infrastructure you control. Nothing is sent to a hosted API.
- **It runs on CPUs you already own.** Serving is tuned for Xeon, with NUMA-aware CPU pinning and autoscaling. No accelerator is required.
- **It is production-shaped from the start.** Keycloak SSO, role-based access, mutual TLS between services, and a Grafana and Prometheus stack are part of the default install, not a later project.
- **It is modular where it matters.** Swap the LLM, the embedding model, or the vector database through configuration. Add or remove a pipeline step by selecting a different variant.
- **More than chat.** The same deployment covers conversational retrieval, document summarization, translation, and voice, and can expose retrieval to AI agents over Model Context Protocol.

## Where the pieces live

You clone **one** repository and run every command from its root. The installer clones the rest for you.

```
github.com/intel/enterprise-ai-solutions      <- start here; run all commands from this root
  |
  |-- es_auto_installer.sh      the single entry point (configure / init / install / teardown)
  |-- env/<name>/               your configuration, kubeconfig, credentials, and logs
  `-- ext/                      cloned automatically by `init erag`
        |-- enterprise.ai-inference    model serving: KServe, vLLM, OpenVINO Model Server
        `-- enterprise.ai-erag         this repo: RAG pipelines, ingestion, UI
```

> [!IMPORTANT]
> Do not clone this repository directly to deploy it. Clone the Toolkit Manager repository, run `./es_auto_installer.sh init erag`, and it will fetch this one into `ext/` at a pinned revision. Every command in this documentation is run from the Toolkit Manager repo root.

## What gets deployed

`install erag` brings up the installer targets below it first, then Intel® Chatbot Toolkit. `inference` is Intel® Inference Toolkit. `erag` is this toolkit:

```
infrastructure  ->  platform  ->  inference  ->  erag
Kubernetes          Istio          KServe        pipelines, ingestion (EDP),
storage             Keycloak       vLLM / OVMS   vector database, guardrails,
                    PostgreSQL     AI Gateway    chat history, MCP gateway, UI
                    observability
```

You choose what the application does by selecting a **flavour** at init time: conversational retrieval (`chatqna`, the default), document summarization (`docsum`), translation (`translation`), voice (`audioqna`), or Polish-language retrieval (`pl_chatqna`). Flavours and their variants are described in [Pipelines](../deploy/pipelines.md).

## Start here

| You want to | Go to |
|---|---|
| Check whether your hardware qualifies | [Prerequisites](../quickstart/prerequisites.md) |
| Deploy it now | [Quick Start](../quickstart/quickstart.md) |
| Understand the components before deploying | [Architecture](../reference/architecture.md) |
| Know what a term means | [Glossary](../glossary.md) |
| Skim common questions | [FAQ](../faq.md) |
