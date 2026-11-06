# Intel® Chatbot Toolkit Namespace Status Watcher Microservice

Part of the Intel® Chatbot Toolkit ecosystem.

## 🔍 Overview

The Intel® Chatbot Toolkit Namespace Status Watcher microservice provides Kubernetes namespace monitoring functionality. It queries the Kubernetes API to retrieve and report the status of all resources deployed in a specified namespace.

### Features

- Real-time status monitoring of Kubernetes resources
- Generic namespace monitoring via configuration
- Comprehensive resource discovery and reporting
- GMC-compatible status format
- Health check endpoint

## 🔗 Related Components

This service integrates with other Intel® Chatbot Toolkit components:
- Provides status information for audio namespace
- Can be deployed alongside any Intel® Chatbot Toolkit microservice deployment
- Accessible through NGINX reverse proxy for centralized status monitoring

## License

Intel® Chatbot Toolkit is licensed under the Apache License, Version 2.0.

Copyright © 2026 Intel Corporation. All rights reserved.