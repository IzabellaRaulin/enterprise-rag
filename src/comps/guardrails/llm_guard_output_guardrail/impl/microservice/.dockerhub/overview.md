# Intel® Chatbot Toolkit Output Guardrail Service

Part of the Intel® Chatbot Toolkit ecosystem.

## 🔍 Overview

The Intel® Chatbot Toolkit Output Guardrail service provides content filtering and privacy-related capabilities for LLM-generated responses in the Intel® Chatbot Toolkit system. This microservice ensures all output content adheres to content policies, preventing potentially harmful, inappropriate, or inaccurate responses from reaching end users.

This microservice implements [LLM Guard](https://protectai.github.io/llm-guard/) Output Scanners, including:
- [BanSubstrings](https://protectai.github.io/llm-guard/output_scanners/ban_substrings/)
- [BanTopics](https://protectai.github.io/llm-guard/output_scanners/ban_topics/)
- [Bias](https://protectai.github.io/llm-guard/output_scanners/bias/)
- [Code](https://protectai.github.io/llm-guard/output_scanners/code/)
- [Deanonymize](https://protectai.github.io/llm-guard/output_scanners/deanonymize/)
- [JSON](https://protectai.github.io/llm-guard/output_scanners/json/)
- [MaliciousURLs](https://protectai.github.io/llm-guard/output_scanners/malicious_urls/)
- [NoRefusal](https://protectai.github.io/llm-guard/output_scanners/no_refusal/)
- [NoRefusalLight](https://protectai.github.io/llm-guard/output_scanners/no_refusal/)
- [ReadingTime](https://protectai.github.io/llm-guard/output_scanners/reading_time/)
- [FactualConsistency](https://protectai.github.io/llm-guard/output_scanners/factual_consistency/)
- [Regex](https://protectai.github.io/llm-guard/output_scanners/regex/)
- [Relevance](https://protectai.github.io/llm-guard/output_scanners/relevance/)
- [Sensitive](https://protectai.github.io/llm-guard/output_scanners/sensitive/)
- [Sentiment](https://protectai.github.io/llm-guard/output_scanners/sentiment/)
- [Toxicity](https://protectai.github.io/llm-guard/output_scanners/toxicity/)
- [URLReachability](https://protectai.github.io/llm-guard/output_scanners/url_reachability/)

### Features

- Real-time filtering of LLM outputs before reaching end users
- Allows to upholds organizational policy compliance

## 🔗 Related Components

This service integrates with other Intel® Chatbot Toolkit components:
- Pairs with the Intel® Chatbot Toolkit Input Guardrail for complete input/output protection
- Guards Intel® Chatbot Toolkit LLM service against providing inappropriate responses

## License

Intel® Chatbot Toolkit is licensed under the Apache License, Version 2.0.

Copyright © 2024–2026 Intel Corporation. All rights reserved.