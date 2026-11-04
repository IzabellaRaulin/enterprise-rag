#!/usr/bin/env python
# -*- coding: utf-8 -*-
# Copyright (C) 2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

"""Guard tests skip the guards the deployed pipeline graph does not contain."""

from unittest.mock import MagicMock, patch

import kr8s
import pytest

from tests.e2e.helpers.guard_helper import GuardHelper, GuardType, deployed_guard_types
from tests.e2e.helpers.k8s_helper import K8sHelper


def _step(name, params_kind=None, node_name=None):
    if node_name:
        return {"name": name, "nodeName": node_name, "dependency": "Hard"}
    config = {"endpoint": f"/v1/{name.lower()}"}
    if params_kind:
        config |= {"paramsKey": params_kind, "paramsKind": params_kind}
    return {"name": name, "dependency": "Hard",
            "internalService": {"serviceName": f"{name.lower()}-svc", "config": config}}


def _k8s_helper_serving(nodes):
    k8s_helper = MagicMock()
    k8s_helper.get_gmconnector.return_value = MagicMock(raw={"spec": {"nodes": nodes}})
    return k8s_helper


_BASE = {"root": {"routerType": "Sequence", "steps": [
    _step("Embedding"), _step("Retriever", "retriever"), _step("Reranking", "reranker"),
    _step("PromptTemplate", "prompt_template"), _step("LLMGuardInput", "input_guard"),
    _step("Llm", "llm")]}}
_OUTPUT_GUARD = {"root": {"routerType": "Sequence",
                          "steps": _BASE["root"]["steps"] + [_step("LLMGuardOutput", "output_guard")]}}
_RETRIEVE_RERANK = {"root": {"routerType": "Sequence", "steps": [
    _step("Embedding"), _step("Retriever", "retriever"), _step("Reranking", "reranker")]}}
_GUARD_IN_REFERENCED_NODE = {
    "root": {"routerType": "Sequence", "steps": [
        _step("Embedding"), _step("Retriever", node_name="parallel-retrieval-node"), _step("Llm", "llm")]},
    "parallel-retrieval-node": {"routerType": "Ensemble", "steps": [
        _step("BM25", "retriever"), _step("LLMGuardInput", "input_guard")]},
}


@pytest.mark.parametrize("nodes, expected", [
    (_BASE, (GuardType.INPUT,)),
    (_OUTPUT_GUARD, (GuardType.INPUT, GuardType.OUTPUT)),
    (_RETRIEVE_RERANK, ()),
    (_GUARD_IN_REFERENCED_NODE, (GuardType.INPUT,)),
])
def test_deployed_guard_types_follow_the_deployed_graph(nodes, expected):
    assert deployed_guard_types(_k8s_helper_serving(nodes)) == expected


def test_deployed_guard_types_reads_the_graph_named_after_the_guard_scope():
    k8s_helper = _k8s_helper_serving(_BASE)

    deployed_guard_types(k8s_helper)

    k8s_helper.get_gmconnector.assert_called_once_with("chatqna")


def test_deployed_guard_types_without_a_deployed_graph_deploys_no_guards():
    k8s_helper = MagicMock()
    k8s_helper.get_gmconnector.return_value = None

    assert deployed_guard_types(k8s_helper) == ()


def test_get_gmconnector_without_a_deployed_graph_returns_nothing():
    with patch("tests.e2e.helpers.k8s_helper.kr8s.get", return_value=iter([])):
        assert K8sHelper().get_gmconnector("chatqna") is None


def test_get_gmconnector_with_an_unserved_kind_returns_nothing():
    """A cluster without the pipeline layer has no GMConnector CRD; that must not error the session."""
    error = kr8s.ServerError("404 page not found", response=MagicMock(status_code=404))

    with patch("tests.e2e.helpers.k8s_helper.kr8s.get", side_effect=error):
        assert K8sHelper().get_gmconnector("chatqna") is None


def test_get_gmconnector_propagates_other_api_errors():
    """A denied or failing read is a cluster problem to surface, not an absent pipeline."""
    error = kr8s.ServerError("403 Forbidden", response=MagicMock(status_code=403))

    with patch("tests.e2e.helpers.k8s_helper.kr8s.get", side_effect=error):
        with pytest.raises(kr8s.ServerError):
            K8sHelper().get_gmconnector("chatqna")


def test_setup_skips_a_guard_that_is_not_deployed():
    fingerprint = MagicMock()
    guard_helper = GuardHelper(MagicMock(), fingerprint, deployed_guards=(GuardType.INPUT,))

    with pytest.raises(pytest.skip.Exception, match="output_guard is not deployed"):
        guard_helper.setup(GuardType.OUTPUT, "regex", {"enabled": True})
    fingerprint.change_arguments.assert_not_called()


def test_disable_all_guards_touches_only_deployed_guards():
    fingerprint = MagicMock()
    fingerprint.read_config.return_value = MagicMock(
        status_code=200, json=lambda: {"values": {"input_guardrail_params": {}}})
    guard_helper = GuardHelper(MagicMock(), fingerprint, deployed_guards=(GuardType.INPUT,))

    with patch("tests.e2e.helpers.guard_helper.time.sleep"):
        guard_helper.disable_all_guards()

    read_keys = [call.args[0] for call in fingerprint.read_config.call_args_list]
    assert read_keys == [GuardType.INPUT.value]
