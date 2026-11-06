# Intel® Chatbot Toolkit OVMS NER Model Server

Part of the Intel® Chatbot Toolkit ecosystem.

## 🔍 Overview

The Intel® Chatbot Toolkit OVMS NER Model Server hosts Named Entity Recognition (NER) models using OpenVINO™ Model Server (OVMS), providing a scalable and efficient endpoint for extracting metadata entities (authors, dates, titles) from user queries. It serves as the backend for the Intel® Chatbot Toolkit Retriever Microservice's metadata filtering pipeline.

[OVMS](https://github.com/openvinotoolkit/model_server) is an open-source model server that supports efficient inference on Intel hardware using the OpenVINO™ toolkit. A lightweight NER gateway translates KServe v2 inference requests into structured entity annotations used for query-time metadata filtering.

## 🔗 Related Components
- Intel® Chatbot Toolkit Retriever Microservice sends NER inference requests to this model server for metadata-aware query filtering
- Embedding and Reranker Microservices work alongside the retriever to deliver relevant search results

## Disclaimer

This container image is intended for demo purposes only and not intended for production use.
To receive expanded security maintenance from Canonical on the Ubuntu base layer,
you may follow the [how-to guide to enable Ubuntu Pro in a Dockerfile](https://documentation.ubuntu.com/pro-client/en/docs/howtoguides/enable_in_dockerfile/)
which will require the image to be rebuilt.

## License
Intel® Chatbot Toolkit is licensed under the Apache License, Version 2.0.
Copyright © 2026 Intel Corporation. All rights reserved.
