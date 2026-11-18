# Docker Images

Published Intel® Chatbot Toolkit Docker images, organized by component type.

Default registry: `docker.io/intel`
Default tag: matches the solution version (`3.0.0`)
Full image map: `deployment/images.yaml`, version config: `deployment/version.yaml`

## Microservices

| Image | Dockerfile | Description | DockerHub |
|-------|------------|-------------|-----------|
| `intel/chatbot-toolkit-ingestion` | `src/comps/ingestion/impl/microservice/Dockerfile` | Document ingestion service | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-ingestion) |
| `intel/chatbot-toolkit-enhanced-dataprep` | `src/edp/Dockerfile` | Enhanced Data Prep (EDP) for advanced document processing | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-enhanced-dataprep) |
| `intel/chatbot-toolkit-embedding` | `src/comps/embeddings/impl/microservice/Dockerfile` | Embedding microservice for interacting with embedding endpoint | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-embedding) |
| `intel/chatbot-toolkit-retriever` | `src/comps/retrievers/impl/microservice/Dockerfile` | Retrieval service for retrieving relevant documents and context | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-retriever) |
| `intel/chatbot-toolkit-reranking` | `src/comps/reranks/impl/microservice/Dockerfile` | Reranking microservice for interacting with reranking endpoint | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-reranking) |
| `intel/chatbot-toolkit-prompt_template` | `src/comps/prompt_template/impl/microservice/Dockerfile` | Prompt template service for managing and customizing prompt templates | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-prompt_template) |
| `intel/chatbot-toolkit-in-guard` | `src/comps/guardrails/llm_guard_input_guardrail/impl/microservice/Dockerfile` | Input guardrail service | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-in-guard) |
| `intel/chatbot-toolkit-out-guard` | `src/comps/guardrails/llm_guard_output_guardrail/impl/microservice/Dockerfile` | Output guardrail service | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-out-guard) |
| `intel/chatbot-toolkit-dpguard` | `src/comps/guardrails/llm_guard_dataprep_guardrail/impl/microservice/Dockerfile` | Dataprep guardrail service | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-dpguard) |
| `intel/chatbot-toolkit-llm` | `src/comps/llms/impl/microservice/Dockerfile` | LLM microservice for interacting with LLM model server endpoint | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-llm) |
| `intel/chatbot-toolkit-system-fingerprint` | `src/comps/system_fingerprint/impl/microservice/Dockerfile` | Fingerprint service to ensure configuration integrity | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-system-fingerprint) |
| `intel/chatbot-toolkit-language-detection` | `src/comps/language_detection/impl/microservice/Dockerfile` | Language detection service | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-language-detection) |
| `intel/chatbot-toolkit-text-splitter` | `src/comps/text_splitter/impl/microservice/Dockerfile` | Text splitter service | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-text-splitter) |
| `intel/chatbot-toolkit-text-compression` | `src/comps/text_compression/impl/microservice/Dockerfile` | Text compression microservice for compressing text from documents | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-text-compression) |
| `intel/chatbot-toolkit-text-extractor` | `src/comps/text_extractor/impl/microservice/Dockerfile` | Text extractor service | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-text-extractor) |
| `intel/chatbot-toolkit-chat-history` | `src/comps/chat_history/impl/microservice/Dockerfile` | Chat history service | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-chat-history) |
| `intel/chatbot-toolkit-query-rewrite` | `src/comps/query_rewrite/impl/microservice/Dockerfile` | Query rewrite service | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-query-rewrite) |
| `intel/chatbot-toolkit-namespace-status-watcher` | `src/comps/namespace_status_watcher/impl/microservice/Dockerfile` | Namespace status watcher service | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-namespace-status-watcher) |
| `intel/chatbot-toolkit-asr` | `src/comps/asr/impl/microservice/Dockerfile` | Automatic speech recognition service | Not published |
| `intel/chatbot-toolkit-tts` | `src/comps/tts/impl/microservice/Dockerfile` | Text-to-speech service | Not published |
| `intel/chatbot-toolkit-docsum` | `src/comps/docsum/impl/microservice/Dockerfile` | Document summarization service | Not published |

## Orchestration

| Image | Dockerfile | Description | DockerHub |
|-------|------------|-------------|-----------|
| `intel/chatbot-toolkit-gmcrouter` | `src/gmc/router/Dockerfile` | Routing service for RAG service communication | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-gmcrouter) |
| `intel/chatbot-toolkit-gmcmanager` | `src/gmc/manager/Dockerfile` | Management service for RAG services orchestration | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-gmcmanager) |
| `intel/chatbot-toolkit-mcp-gateway` | `src/mcp_gateway/impl/microservice/Dockerfile` | Model Context Protocol gateway | Not published |

## UI

| Image | Dockerfile | Description | DockerHub |
|-------|------------|-------------|-----------|
| `intel/chatbot-toolkit-chatqna-conversation-ui` | `src/ui/apps/chatqna/Dockerfile` | ChatQnA user interface | [Link](https://hub.docker.com/r/intel/chatbot-toolkit-chatqna-conversation-ui) |
| `intel/chatbot-toolkit-docsum-ui` | `src/ui/apps/docsum/Dockerfile` | DocSum user interface | Not published |
| `intel/chatbot-toolkit-audioqna-ui` | `src/ui/apps/audioqna/Dockerfile` | AudioQnA user interface | Not published |

## Utilities

| Image | Dockerfile | Description |
|-------|------------|-------------|
| `intel/chatbot-toolkit-init-container` | `deployment/components/utils/init-container/Dockerfile` | Init container for setup tasks |
| `intel/chatbot-toolkit-ovms_ner` | `src/comps/retrievers/impl/model_server/ovms/docker/Dockerfile` | OpenVINO Model Server for NER |
| `intel/chatbot-toolkit-tts-fastapi-model-server` | `src/comps/tts/impl/model_server/fastapi/docker/Dockerfile` | TTS FastAPI model server |
| `intel/chatbot-toolkit-vllm-audio` | `src/comps/asr/impl/model_server/vllm/docker/Dockerfile` | vLLM audio model server |

## Building Custom Images

To build and push a custom image:

```bash
# From repo root
export REGISTRY=docker.io/myorg
export TAG=3.0.0-custom

# Build a microservice image
docker build -t ${REGISTRY}/chatbot-toolkit-embedding:${TAG} \
  -f src/comps/embeddings/impl/microservice/Dockerfile \
  src

docker push ${REGISTRY}/chatbot-toolkit-embedding:${TAG}
```

Override the image in `env/<name>/config.erag.yaml`:

```yaml
registry: docker.io/myorg
tag: 3.0.0-custom
```

For bulk image updates, use `deployment/update_images.sh`.

## Related

- [Image registry map](../../deployment/images.yaml) - complete mapping of image names to Dockerfiles
- [Version config](../../deployment/version.yaml) - solution version and default tag
