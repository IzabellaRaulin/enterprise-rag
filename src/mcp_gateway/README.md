# MCP Gateway Microservice

The MCP (Model Context Protocol) gateway exposes Intel® Chatbot Toolkit capabilities as tools that AI agents can discover and call over a persistent SSE connection. It implements the MCP SSE transport and acts as the sole authentication boundary for agent traffic.

## Table of Contents

1. [MCP Gateway Microservice](#mcp-gateway-microservice)
2. [Overview](#overview)
3. [Available Tools](#available-tools)
4. [Configuration Options](#configuration-options)
5. [Getting Started](#getting-started)
   - 5.1. [🚀 Start MCP Gateway with Python (Option 1)](#-start-mcp-gateway-with-python-option-1)
     - 5.1.1. [Install Requirements](#install-requirements)
     - 5.1.2. [Start Microservice](#start-microservice)
   - 5.2. [🚀 Start MCP Gateway with Docker (Option 2)](#-start-mcp-gateway-with-docker-option-2)
     - 5.2.1. [Build the Docker Image](#build-the-docker-image)
     - 5.2.2. [Run the Docker Container](#run-the-docker-container)
   - 5.3. [Verify the MCP Gateway](#verify-the-mcp-gateway)
     - 5.3.1. [Health Check](#health-check)
     - 5.3.2. [List MCP Tools](#list-mcp-tools)
6. [Authentication](#authentication)

---

## Overview

The gateway translates MCP tool calls from AI agents into HTTP requests to EDP, plus, for `ingest_file`, a PUT to the presigned S3 URL EDP issues (which points at the object-store endpoint on the external hostname). Which tools are registered at startup depends on which backend endpoints are configured:

| Condition | Registered tools |
|-----------|-----------------|
| `EDP_ENDPOINT` set | `retrieve_context`, `list_buckets`, `ingest_url`, `ingest_file`, `check_ingestion_status` |

Tools not backed by a running service are not registered - agents see only what is available on their deployment.

The gateway is an OAuth 2.0 resource server. Agents obtain their own Keycloak access token (`client_credentials` against their own client) and present it as a bearer token on every request; the gateway verifies it and forwards it to EDP. No client secret reaches the gateway, and a tool call can never exceed the privileges of the agent that made it.

---

## Available Tools

### `retrieve_context`
Retrieves ranked document chunks from the knowledge base without running the full LLM pipeline. Use when you want source passages or need to supply retrieved context to your own LLM.

**Requires:** `EDP_ENDPOINT`

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `query` | string | required | Natural-language search phrase |
| `top_n` | integer | `5` | Number of ranked chunks to return if reranker is enabled. Ignored if reranker is false. |
| `k` | integer | `32` | Number of candidates to retrieve from retriever. Must be >= top_n for correct retrieval. Higher k may improve recall but increases latency. |
| `reranker` | boolean | `true` | Apply reranking; `false` for faster, less precise results |
| `search_type` | string | `"similarity"` | `similarity`, `similarity_search_with_siblings`, or `similarity_distance_threshold` |

### `list_buckets`
Lists all available buckets (collections) in the knowledge base. Call this before ingesting files to discover valid bucket names.

**Requires:** `EDP_ENDPOINT`

No parameters.

Returns: `list[str]` - bucket name strings.

### `ingest_url`
Ingests a URL into the knowledge base for processing. The EDP pipeline fetches the content, extracts text, chunks it, and generates embeddings. Processing starts asynchronously - the tool returns as soon as the URL is queued.

**Requires:** `EDP_ENDPOINT`

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `url` | string | required | URL to ingest (e.g. `https://example.com/document.pdf`). Must be a valid http or https URL reachable by the EDP service. |

### `ingest_file`
Uploads base64-encoded file content directly into the knowledge base. Use for files you already have in memory; prefer `ingest_url` for URL-hosted files.

**Requires:** `EDP_ENDPOINT`

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `bucket` | string | required | Destination bucket name |
| `filename` | string | required | Object name in storage |
| `content_base64` | string | required | Base64-encoded file bytes (standard alphabet, padding required) |
| `content_type` | string | `"application/octet-stream"` | MIME type hint |

### `check_ingestion_status`
Check processing status of files and URLs in knowledge base. Query by bucket/filename or url. Fetches only relevant data - files if bucket/filename/id provided, links if url/id provided, both if all params None.

**Requires:** `EDP_ENDPOINT`

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `bucket` | string | `None` | Filter files by bucket name |
| `id` | string | `None` | Filter by unique ID (fetches both files and links when provided) |
| `filename` | string | `None` | Filter files by object name |
| `url` | string | `None` | Filter links by URL |

Returns: `list[dict]` - file entries contain `id`, `bucket_name`, `object_name`, `status`, `chunks_total`, `chunks_processed`, `job_message`, `created_at`, `size`. Link entries contain `id`, `uri`, `status`, `chunks_total`, `chunks_processed`, `job_message`, `created_at`.

Status values: `uploaded`, `processing`, `ingested`, `error`, `deleting`, `canceled`.

---

## Configuration Options

Settings are read from environment variables. The `impl/microservice/.env` file provides development defaults; in production all values are injected via Helm at deploy time.

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `MCP_JWT_ISSUER` | Yes | (none) | Realm issuer URL that caller tokens must carry in `iss`, e.g. `https://keycloak.<base_domain_name>/realms/EnterpriseRAG` (`https://<base_domain_name>/auth/realms/...` in `path` routing mode). This is the externally reachable URL, because that is what Keycloak stamps into tokens. |
| `MCP_JWT_AUDIENCE` | Yes | (none) | Audience caller tokens must contain, `mcp-gateway` by default. Tokens issued for another service are refused. |
| `MCP_JWKS_URI` | Yes | (none) | In-cluster JWKS endpoint used to verify token signatures, e.g. `http://keycloak-service.<keycloak-namespace>.svc:8080/realms/EnterpriseRAG/protocol/openid-connect/certs`. |
| `MCP_JWT_ALGORITHMS` | No | `RS256,RS384` | Comma-separated signing algorithms accepted. The realm signs with RS384. |
| `MCP_RESOURCE_URL` | No | (none) | Canonical URL of this MCP resource, e.g. `https://<base_domain_name>/api/v1/mcp`. Published in the protected-resource metadata document and in the `WWW-Authenticate` challenge. |
| `EDP_ENDPOINT` | Yes | (none) | Internal EDP backend URL. Required - MCP gateway exposes only EDP retrieval/ingestion tools. |
| `MCP_SERVER_HOST` | No | `0.0.0.0` | Host address the server binds to |
| `MCP_SERVER_PORT` | No | `8000` | Port the server listens on |
| `S3_TLS_VERIFY` | No | `true` | Set to `false` to disable TLS verification for S3 storage (dev only) |
| `MCP_ALLOWED_HOSTS` | No | (loopback only) | Comma-separated `Host` header values accepted on the MCP endpoints, appended to the always-allowed loopback entries. Exact match or a trailing `:*` port wildcard only - there is no `*.domain` glob, so list a hostname twice (`example.com,example.com:*`) to accept it with and without a port. Requests with any other `Host` get HTTP 421. |
| `MCP_MAX_SESSIONS` | No | `100` | Maximum concurrent SSE sessions (DoS protection) |
| `MCP_MAX_SESSIONS_PER_CLIENT` | No | `5` | Maximum sessions per client_id (per-client DoS protection) |
| `MCP_SESSION_INACTIVITY_TIMEOUT` | No | `600` | Seconds before idle sessions are closed (prevents orphaned sessions) |
| `SSL_CERT_FILE` | No | (certifi default) | Path to CA certificate bundle for TLS verification |
| `ERAG_LOGGER_LEVEL` | No | `INFO` | Log level (`DEBUG`, `INFO`, `WARNING`, `ERROR`) |

---

## Getting Started

There are 2 ways to run this microservice:
  - [via Python](#-start-mcp-gateway-with-python-option-1)
  - [via Docker](#-start-mcp-gateway-with-docker-option-2) **(recommended)**

### 🚀 Start MCP Gateway with Python (Option 1)

#### Install Requirements
To freeze the dependencies of a particular microservice, [uv](https://github.com/astral-sh/uv) project manager is utilized. So before installing the dependencies, installing uv is required.
Next, use `uv sync` to install the dependencies. This command will create a virtual environment.

```bash
pip install uv
uv sync --locked --no-cache --project impl/microservice/pyproject.toml --group security-overrides
source impl/microservice/.venv/bin/activate
```

#### Start Microservice

```bash
python mcp_gateway.py
```

### 🚀 Start MCP Gateway with Docker (Option 2)

#### Build the Docker Image
Navigate to the `src` directory and use the docker build command to create the image:

```bash
cd ../../
docker build -t erag/mcp-gateway:latest -f mcp_gateway/impl/microservice/Dockerfile .
```

#### Run the Docker Container

```bash
docker run -d --name="mcp-gateway" \
  --net=host \
  --ipc=host \
  erag/mcp-gateway
```

If the backend services are running at different endpoints than the default, update the environment variables accordingly. Here's an example of how to pass configuration using the docker run command:

```bash
docker run -d --name="mcp-gateway" \
  -e MCP_JWT_ISSUER=https://keycloak.<base-domain>/realms/EnterpriseRAG \
  -e MCP_JWT_AUDIENCE=mcp-gateway \
  -e MCP_JWKS_URI=http://<keycloak-host>/realms/EnterpriseRAG/protocol/openid-connect/certs \
  -e MCP_RESOURCE_URL=https://<base-domain>/api/v1/mcp \
  -e EDP_ENDPOINT=http://<edp-host>:5000 \
  --net=host \
  --ipc=host \
  erag/mcp-gateway
```

### Verify the MCP Gateway

#### Health Check

```bash
curl http://localhost:8000/api/v1/mcp/health \
  -X GET \
  -H 'Content-Type: application/json'
```

#### List MCP Tools

```bash
curl -N http://localhost:8000/api/v1/mcp/sse \
  -H "Authorization: Bearer <access-token>"
```

---

## Authentication

The gateway is an OAuth 2.0 resource server. Agents obtain their own Keycloak access token with the `client_credentials` grant against their own client, and present it as a bearer token on every request:

```
GET /api/v1/mcp/sse
Authorization: Bearer <access-token>
```

No client secret is ever sent to the gateway, and the gateway never mints a token of its own: the caller's token is what reaches EDP, so a tool call cannot exceed the privileges of the agent that made it.

Tokens are verified twice - by the Envoy Gateway `SecurityPolicy` on the MCP route (signature, issuer, audience, expiry) and again in the service against the realm's JWKS, so a request that arrives without passing through the gateway is held to the same standard.

Every request carries its own token, including each tool-call POST. Session state binds a session UUID to the identity that opened it, so a session UUID observed in a log is not usable on its own:

| Case | Response |
|------|----------|
| No or unparseable `Authorization` header | 401 `invalid_token` |
| Signature, issuer, audience or expiry rejected | 401 `invalid_token` |
| Tool call for a session opened by a different identity | 403 `session_mismatch` |
| Tool call for an unknown or expired session | 404 `unknown_session` |
| Session caps reached | 503 `too_many_sessions` / `too_many_sessions_per_client` |

Every 401 carries `WWW-Authenticate: Bearer ..., resource_metadata="<url>"` pointing at `/api/v1/mcp/.well-known/oauth-protected-resource` (RFC 9728), which lists the authorization server. A spec-compliant MCP client uses that to discover where to get a token and to refresh once the current one expires. `/api/v1/mcp/health` and the metadata document itself are the only unauthenticated paths.


See [`docs/mcp_integration.md`](../../docs/customize/mcp.md) for the full integration guide including deployment, routing and production client setup.
