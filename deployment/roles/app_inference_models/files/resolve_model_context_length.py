#!/usr/bin/env python3
# Copyright (C) 2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

"""
Resolve the context length (max_model_len) of a deployed LLM.

DocSum pre-validates 'stuff' summaries against the model's context length. It reads
that number from the LLM's /v1/models, but in this deployment the LLM is reached
through the AI gateway, which serves its own model list without max_model_len. Only
the model server itself reports it, so this script asks it from inside a KServe
workload pod (kubectl exec, then localhost). The apiserver proxy is no route there: the
host-network apiserver speaks plaintext, which mesh-wide STRICT mTLS rejects whenever
the model pod runs on another node than the apiserver.

Outputs a single JSON object to stdout:
  {"model_context_length": <int|null>, "source": "<source>", "reason": "<why null>"}

Exits 0 even when the value cannot be resolved: DocSum then skips pre-validation, so
an unreported context length (OVMS, no reachable pod) must not fail an install.
"""

import argparse
import json
import re
import subprocess  # nosec B404
import sys

NAME_PATTERN = re.compile(r"^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$")

WORKLOAD_SVC_SUFFIX = "-kserve-workload-svc"
WORKLOAD_CONTAINER = "main"
LOCAL_MODELS_URL = "http://127.0.0.1:8000/v1/models"


def build_exec_argv(kubectl, namespace, model_name, timeout):
    """Build the kubectl exec that prints /v1/models from inside a workload pod."""
    fetch = (
        "import sys, urllib.request; "
        f"sys.stdout.write(urllib.request.urlopen('{LOCAL_MODELS_URL}', timeout={timeout}).read().decode())"
    )
    return [kubectl, "exec", "-n", namespace, f"svc/{model_name}{WORKLOAD_SVC_SUFFIX}", "-c", WORKLOAD_CONTAINER, "--", "python3", "-c", fetch]


def fetch_models(kubectl, namespace, model_name, timeout):
    """Return the parsed /v1/models listing, or a reason why it is unavailable."""
    argv = build_exec_argv(kubectl, namespace, model_name, timeout)
    try:
        result = subprocess.run(argv, capture_output=True, text=True, timeout=timeout, check=False)  # nosec B603
    except (OSError, subprocess.TimeoutExpired) as error:
        return None, f"could not run kubectl: {error}"
    if result.returncode != 0:
        last_line = (result.stderr.strip().splitlines() or [""])[-1]
        return None, f"model server not reachable from its workload pod: {last_line}"
    try:
        return json.loads(result.stdout), None
    except json.JSONDecodeError as error:
        return None, f"model server returned no JSON model list: {error}"


def extract_context_length(models, model_name):
    """Pick max_model_len for the named model, or a reason why it is not there."""
    entries = models.get("data") or []
    named = [entry for entry in entries if entry.get("id") == model_name]
    # Fall back to the whole list: a model server may publish the model under a
    # different id than the catalog name it was deployed with.
    for entry in named or entries:
        if "max_model_len" in entry:
            return int(entry["max_model_len"]), None
    return None, f"model server does not report max_model_len for '{model_name}'"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model-name", required=True, help="Deployed model name (as in the catalog)")
    parser.add_argument("--namespace", required=True, help="Namespace the model is deployed in")
    parser.add_argument("--kubectl", default="kubectl", help="kubectl binary to use")
    parser.add_argument("--timeout", type=int, default=30, help="Seconds to wait for the model server")
    args = parser.parse_args()

    for value, label in ((args.model_name, "model name"), (args.namespace, "namespace")):
        if not NAME_PATTERN.match(value):
            json.dump({"model_context_length": None, "source": None,
                       "reason": f"invalid {label}: '{value}'"}, sys.stdout)
            return 0

    models, reason = fetch_models(args.kubectl, args.namespace, args.model_name, args.timeout)
    if models is None:
        json.dump({"model_context_length": None, "source": None, "reason": reason}, sys.stdout)
        return 0

    context_length, reason = extract_context_length(models, args.model_name)
    json.dump({
        "model_context_length": context_length,
        "source": "model-server" if context_length else None,
        "reason": reason,
    }, sys.stdout)
    return 0


if __name__ == "__main__":
    sys.exit(main())
