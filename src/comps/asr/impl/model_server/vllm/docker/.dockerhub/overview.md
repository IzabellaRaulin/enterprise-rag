# VLLM Audio model server

Part of the Intel® Chatbot Toolkit ecosystem.

## 🔍 Overview

The VLLM Audio model server converts audio files to text using Whisper or compatible ASR models. It extends base vllm image to include audio related packages.

### Features

- Converts audio files to text using Whisper ASR models
- Supports multiple audio formats (WAV, MP3)
- OpenAI-compatible API interface
- Configurable language detection and model selection

## 🔗 Related Components

This service integrates with other Intel® Chatbot Toolkit components:
- Intel® Chatbot Toolkit ASR Microservice sends the requests to it to convert audio files to text.

## Disclaimer

This container image is intended for demo purposes only and not intended for production use.
To receive expanded security maintenance from Canonical on the Ubuntu base layer,
you may follow the [how-to guide to enable Ubuntu Pro in a Dockerfile](https://documentation.ubuntu.com/pro-client/en/docs/howtoguides/enable_in_dockerfile/)
which will require the image to be rebuilt.

## License

Intel® Chatbot Toolkit is licensed under the Apache License, Version 2.0.

Copyright © 2026 Intel Corporation. All rights reserved.