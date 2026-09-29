#!/bin/bash

# Copyright (C) 2024-2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

ENV_FILE=docker/.env
echo "Reading configuration from $ENV_FILE..."

# Detect container runtime: docker -> nerdctl -> error
if command -v docker &> /dev/null; then
    CONTAINER_CLI="docker"
elif command -v nerdctl &> /dev/null; then
    CONTAINER_CLI="nerdctl"
else
    echo "Error: No container runtime found. Install docker or nerdctl (containerd)."
    echo "After kubespray localhost deployment, docker is not available."
    echo "Install nerdctl: https://github.com/containerd/nerdctl"
    exit 1
fi
echo "Info: Using container runtime: $CONTAINER_CLI"

# Both docker and nerdctl expose 'compose' as a subcommand.
if ! $CONTAINER_CLI compose version &> /dev/null; then
  echo "Error: '$CONTAINER_CLI compose' is not available. Install the compose plugin/subcommand."
  exit 1
fi

# Read configuration - priority is given to environment variables already set in the OS, then variables from the .env file.
if [ -f "$ENV_FILE" ]; then
    while IFS='=' read -r key value; do
        # Ignore comments and empty lines
        if [[ -z "$key" || "$key" == \#* ]]; then
            continue
        fi

        key=$(echo "$key" | xargs)
        value=$(echo "$value" | xargs)

        # Ignore comments after the value
        value=$(echo "$value" | cut -d'#' -f1 | xargs)

        # Remove surrounding quotes from the value (if any)
        value=$(echo "$value" | sed 's/^["'\'']//;s/["'\'']$//')

        # Check if the variable is already exported
        if ! printenv | grep -q "^$key="; then
            export "$key=$value"
        else
            echo "$key is already set; skipping"
        fi
    done < "$ENV_FILE"
else
    echo "Error: $ENV_FILE not available. Please create the file with the required environment variables."
    exit 1
fi

if [ "$CONTAINER_CLI" = "nerdctl" ]; then
    # nerdctl compose logs the underlying 'nerdctl run' argv at INFO level,
    # which includes secret -e KEY=VALUE pairs (HF_TOKEN, proxy creds). Drop it.
    $CONTAINER_CLI compose -f docker/docker-compose.yaml up --build -d embedding-vllm-model-server \
        2> >(grep -v -E '^INFO\[[0-9]+\][[:space:]]+Running \[' >&2)
else
    $CONTAINER_CLI compose -f docker/docker-compose.yaml up --build -d embedding-vllm-model-server
fi
