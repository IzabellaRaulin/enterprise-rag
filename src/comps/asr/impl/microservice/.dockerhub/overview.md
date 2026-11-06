# Intel® Chatbot Toolkit ASR Microservice

Part of the Intel® Chatbot Toolkit ecosystem.

## 🔍 Overview

The Intel® Chatbot Toolkit ASR (Automatic Speech Recognition) microservice converts audio files to text using Whisper or compatible ASR models. It interfaces with ASR model servers to process audio and return transcriptions.

### Support Matrix

| Model server name |  Status   |
| ------------------| --------- |
| VLLM              | &#x2713;   |

### Features

- Converts audio files to text using Whisper ASR models
- Supports multiple audio formats (WAV, MP3)
- OpenAI-compatible API interface
- Configurable language detection and model selection

## 🔗 Related Components

This service integrates with other Intel® Chatbot Toolkit components:
- Intel® Chatbot Toolkit Text Splitter can divide the transcribed text into chunks for data ingestion
- Intel® Chatbot Toolkit Retriever & Reranker microservices can use transcriptions for document retrieval
- It triggers transcription requests with vLLM Model Server running on Intel® Xeon® Processors

## License

Intel® Chatbot Toolkit is licensed under the Apache License, Version 2.0.

Copyright © 2024–2026 Intel Corporation. All rights reserved.