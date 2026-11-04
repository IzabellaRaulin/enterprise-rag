#!/usr/bin/env python
# -*- coding: utf-8 -*-
# Copyright (C) 2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

import importlib.util
import json
import subprocess
import sys
from pathlib import Path
from unittest.mock import patch

import pytest

_SCRIPT_PATH = (
    Path(__file__).resolve().parents[4]
    / "deployment" / "roles" / "app_inference_models" / "files" / "resolve_model_context_length.py"
)


def _load_resolver():
    spec = importlib.util.spec_from_file_location("resolve_model_context_length", _SCRIPT_PATH)
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


resolver = _load_resolver()

VLLM_MODELS = {"object": "list", "data": [{"id": "llama3-8b-awq", "owned_by": "vllm", "max_model_len": 8192}]}


def _completed(returncode=0, stdout="", stderr=""):
    return subprocess.CompletedProcess(args=[], returncode=returncode, stdout=stdout, stderr=stderr)


def test_exec_targets_workload_service_without_apiserver_proxy():
    argv = resolver.build_exec_argv("kubectl", "llm-inference", "llama3-8b-awq", 30)

    assert argv[:7] == ["kubectl", "exec", "-n", "llm-inference", "svc/llama3-8b-awq-kserve-workload-svc",
                         "-c", "main"]
    assert resolver.LOCAL_MODELS_URL in argv[-1]


def test_fetch_models_parses_exec_output():
    with patch.object(resolver.subprocess, "run", return_value=_completed(stdout=json.dumps(VLLM_MODELS))):
        models, reason = resolver.fetch_models("kubectl", "llm-inference", "llama3-8b-awq", 30)

    assert models == VLLM_MODELS
    assert reason is None


@pytest.mark.parametrize("result, expected_reason", [
    (_completed(returncode=1, stderr="services not found"), "not reachable from its workload pod: services not found"),
    (_completed(returncode=1, stderr="Traceback (most recent call last):\n  ...\nURLError: refused\n"),
     "not reachable from its workload pod: URLError: refused"),
    (_completed(stdout="not json"), "no JSON model list"),
])
def test_fetch_models_reports_reason_on_failure(result, expected_reason):
    with patch.object(resolver.subprocess, "run", return_value=result):
        models, reason = resolver.fetch_models("kubectl", "llm-inference", "llama3-8b-awq", 30)

    assert models is None
    assert expected_reason in reason


def test_fetch_models_reports_timeout():
    with patch.object(resolver.subprocess, "run", side_effect=subprocess.TimeoutExpired("kubectl", 30)):
        models, reason = resolver.fetch_models("kubectl", "llm-inference", "llama3-8b-awq", 30)

    assert models is None
    assert "could not run kubectl" in reason


@pytest.mark.parametrize("models, expected", [
    (VLLM_MODELS, 8192),
    ({"data": [{"id": "/mnt/models", "max_model_len": 4096}]}, 4096),
    ({"data": [{"id": "llama3-8b-awq", "owned_by": "Envoy AI Gateway"}]}, None),
    ({}, None),
])
def test_extract_context_length(models, expected):
    context_length, reason = resolver.extract_context_length(models, "llama3-8b-awq")

    assert context_length == expected
    assert (reason is None) == (expected is not None)
