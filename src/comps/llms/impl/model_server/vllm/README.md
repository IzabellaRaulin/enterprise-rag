# vLLM LLM Model Server

This document focuses on using the [vLLM](https://github.com/vllm-project/vllm) as a LLM.

vLLM is a fast and easy-to-use library for LLM inference and serving, it delivers state-of-the-art serving throughput with a set of advanced features such as PagedAttention, Continuous batching and etc. Besides GPUs, vLLM already supported [Intel CPUs](https://www.intel.com/content/www/us/en/products/overview.html). This guide provides an example on how to launch vLLM serving endpoint on CPU.

## Table of Contents

1. [vLLM LLM Model Server](#vllm-llm-model-server)
2. [Getting Started](#getting-started)
   - 2.1. [Prerequisite](#prerequisite)
   - 2.2. [🚀 Start the vLLM Service via script (Option 1)](#-start-the-vllm-service-via-script-option-1)
     - 2.2.1. [Run the script](#run-the-script)
     - 2.2.2. [Verify the vLLM Service](#verify-the-vllm-service)
   - 2.3. [🚀 Deploy vLLM Service with LLM Microservice using Docker Compose (Option 2)](#-deploy-vllm-service-with-llm-microservice-using-docker-compose-option-2)
     - 2.3.1. [Modify the environment configuration file to align it to your case](#modify-the-environment-configuration-file-to-align-it-to-your-case)
     - 2.3.2. [Start the Services using Docker Compose](#start-the-services-using-docker-compose)
     - 2.3.3. [Service Cleanup](#service-cleanup)
   - 2.4. [Verify the Services](#verify-the-services)
   - 2.5. [vLLM for Xeon based on Red Hat® Universal Base Image](#vllm-for-xeon-based-on-red-hat-universal-base-image)

## Getting Started

### Prerequisite
Provide your Hugging Face API key to enable access to Hugging Face models. Alternatively, you can set this in the dotenv configuration files.
```bash
export HF_TOKEN=${your_hf_api_token}
```

Also, create a folder to preserve model data on host and change the ownership to the id that would match user in the image:
```bash
mkdir -p docker/data/
sudo chown -R 1000:1000 ./docker/data
```

### 🚀 Start the vLLM Service via script (Option 1)
#### Run the script

```bash
chmod +x run_vllm.sh
./run_vllm.sh
```
The script initiates a Docker container with the vLLM model server running on port `LLM_VLLM_PORT` (default: **8008**). Configuration settings are specified in the environment configuration file [docker/cpu/.env](docker/cpu/.env). You can adjust these settings by modifying the appropriate dotenv file or by exporting environment variables.

#### Verify the vLLM Service

First, check the logs to confirm the service is operational:
```bash
docker logs -f llm-vllm-model-server
```

The following log messages indicate that the startup of model server is completed:
```bash

INFO:     Started server process [1]
INFO:     Waiting for application startup.
INFO:     Application startup complete.
INFO:     Uvicorn running on http://0.0.0.0:80 (Press CTRL+C to quit)
```

### 🚀 Deploy vLLM Service with LLM Microservice using Docker Compose (Option 2)

To launch vLLM Service along with the LLM Microservice, follow these steps:

#### Modify the environment configuration file to align it to your case

Modify the `./docker/cpu/.env` file:

```env
#HF_TOKEN=<your-hf-api-key>

## VLLM Model Server Settings ##
LLM_VLLM_MODEL_NAME="meta-llama/Llama-3.1-8B-Instruct"
LLM_VLLM_PORT=8008

## VLLM Settings ##
VLLM_CPU_KVCACHE_SPACE=10
VLLM_DTYPE=bfloat16
VLLM_MAX_NUM_SEQS=256
VLLM_SKIP_WARMUP=false

[...]
```

#### Start the Services using Docker Compose

To build and start the services using Docker Compose

```bash
# for CPU device
cd docker/cpu
mkdir -p data
export UID && docker compose --env-file=.env -f docker-compose.yaml up --build -d


```

Note: Due to secure container best practices, main process is started as non-privileged user.
Due to the fact it uses volume mounts, the volume directory `data/` must be created beforehand.

#### Service Cleanup

To cleanup the services using Docker Compose:

```bash
cd docker

docker compose -f docker-compose.yaml down
```

### Verify the Services

- Test the `llm-vllm-model-server` using the following command:
    ```bash
    curl http://localhost:8008/v1/completions \
        -X POST \
        -d '{"model": "Intel/neural-chat-7b-v3-3", "prompt": "What is Deep Learning?", "max_tokens": 32, "temperature": 0}' \
        -H "Content-Type: application/json"
    ```
    **NOTICE**: First ensure that the model server is operational. Warming up might take a while, and during this phase, the endpoint won't be ready to serve the query.

- Check the `llm-vllm-microservice` status:

    ```bash
    curl http://localhost:9000/v1/health_check \
        -X GET \
        -H 'Content-Type: application/json'
    ```

- Test the `llm-vllm-microservice` for **non-streaming mode** using the following command:
    ```bash
    curl http://localhost:9000/v1/chat/completions \
        -X POST \
        -d '{
                "messages": [
                    {
                        "role": "system",
                        "content": "### You are a helpful, respectful, and honest assistant to help the user with questions. Please refer to the search results obtained from the local knowledge base. Refer also to the conversation history if you think it is relevant to the current question. Ignore all information that you think is not relevant to the question. If you dont know the answer to a question, please dont share false information. ### Search results:  \n\n"
                    },
                    {
                        "role": "user",
                        "content": "### Question: What is Deep Learning? \n\n"
                    }
                ],
                "max_new_tokens":17,
                "top_p":0.95,
                "temperature":0.01,
                "stream":false
            }' \
        -H 'Content-Type: application/json'
    ```

- Test the `llm-vllm-microservice` for **streaming mode** using the following command:
    ```bash
    curl http://localhost:9000/v1/chat/completions \
        -X POST \
        -d '{
                "messages": [
                    {
                        "role": "system",
                        "content": "### You are a helpful, respectful, and honest assistant to help the user with questions. Please refer to the search results obtained from the local knowledge base. Refer also to the conversation history if you think it is relevant to the current question. Ignore all information that you think is not relevant to the question. If you dont know the answer to a question, please dont share false information. ### Search results:  \n\n"
                    },
                    {
                        "role": "user",
                        "content": "### Question: What is Deep Learning? \n\n"
                    }
                ],
                "max_new_tokens":32,
                "top_p":0.95,
                "temperature":0.01,
                "stream":true
            }' \
        -H 'Content-Type: application/json'
    ```

### vLLM for Xeon based on Red Hat® Universal Base Image dedicated for OpenShift
This image is based on Universal Base Image 9 (UBI) maintained by Red Hat®.

The image leverages `AMX` and `AVX-512` instruction extensions for improved inference on Sapphire Rapids and newer generations.

It runs vLLM in an unprivileged mode installed on top of UBI9 which makes it a perfect choice for OpenShift deployments on Xeon® as it works out-of-the-box.

The image is publicly available on Intel® Chatbot Toolkit Docker Hub. It is compatible with all Xeon® CPUs. The image is a different variant of the vLLM image for CPU built and published by the vLLM project, so for deeper insights you can take a look at the [official documentation](https://docs.vllm.ai/en/stable/getting_started/installation/cpu).

If you want, you can use it as a standalone vLLM in your project.

- Leverages `AMX` and `AVX-512` for optimized inference on SPR+.
  - Falls back to normal instructions on older generations.
- Compatible with any Intel® Xeon® CPUs.
- By default, `VLLM_CPU_KVCACHE_SPACE` is set to `40GiB`; however, you can change it using an environment variable during runtime.
- Docker Hub -> [docker.io/opea/vllm-cpu-ubi](https://hub.docker.com/r/opea/vllm-cpu-ubi)
- [Dockerfile](./docker/cpu_ubi/Dockerfile)


#### Building the Docker image
You can customize and build the image yourself if needed.
- Default build that uses AMX and AVX-512 on SPR and newer generations, but also works on older generations (falls back to normal instructions when instruction extensions for AMX are not present). Just run `docker build` with default values.
- **TL;DR**
  - Works on any Intel Xeon CPU out-of-the-box. Uses optimizations when present.
```bash
docker build -f cpu_ubi/Dockerfile -t vllm-cpu-ubi:v0.18.0-ubi9 .
```

- More details on build and runtime args in [Build image from source](https://docs.vllm.ai/en/stable/getting_started/installation/cpu.html#build-image-from-source)

#### Running the Docker container as a standalone server
- Specify `VLLM_CPU_KVCACHE_SPACE`; it must be lower than your system memory (default is 40GB). A larger KV Cache can support more concurrent requests and longer context lengths.
```bash
docker run -d \
  -p 8000:8000 \
  -e VLLM_CPU_KVCACHE_SPACE=20 \
  -e HF_TOKEN=<YOUR_HF_TOKEN> \
  docker.io/opea/vllm-cpu-ubi:v0.18.0-ubi9  \
    --model TinyLlama/TinyLlama-1.1B-Chat-v1.0 \
    --host 0.0.0.0 \
    --port 8000 \
    --dtype bfloat16
```
