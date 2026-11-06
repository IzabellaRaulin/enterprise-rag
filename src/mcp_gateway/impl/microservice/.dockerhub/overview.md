# Intel® Chatbot Toolkit MCP Gateway Microservice

Part of the Intel® Chatbot Toolkit ecosystem.

## 🔍 Overview

The Intel® Chatbot Toolkit MCP Gateway Microservice exposes Intel® Chatbot Toolkit capabilities as tools that AI agents can discover and call over a persistent SSE connection. It implements the Model Context Protocol (MCP) SSE transport and acts as the authentication boundary for agent traffic, translating MCP tool calls into HTTP requests to internal services.

The gateway dynamically registers tools based on which backend services are configured and available. Agents see only the tools backed by running services in their deployment.

### Supported Tools

| Tool | Requires | Purpose |
| ---- | -------- | ------- |
| retrieve_context | EDP_ENDPOINT | Retrieves ranked document chunks from knowledge base without running full LLM pipeline |
| list_buckets | EDP_ENDPOINT | Lists available document collections (buckets) |
| ingest_url | EDP_ENDPOINT | Ingests documents from URLs |
| ingest_file | EDP_ENDPOINT | Ingests uploaded files |
| check_ingestion_status | EDP_ENDPOINT | Checks async ingestion job status |

## 🔗 Related Components

This service integrates with other Intel® Chatbot Toolkit components:
- EDP (Enhanced Data Preparation) provides ingestion and retrieval endpoints
- SeaweedFS stores document objects and metadata
- Keycloak handles authentication via client_credentials grant

## License

Intel® Chatbot Toolkit is licensed under the Apache License, Version 2.0.

Copyright © 2024–2026 Intel Corporation. All rights reserved.
