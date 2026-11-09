#!/usr/bin/env python
# -*- coding: utf-8 -*-
# Copyright (C) 2024-2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0


import allure
import kr8s
import pytest

from tests.e2e.validation.buildcfg import is_openshift

OPENSHIFT_STATIC_POD_OWNER_PREFIX = "revision-status-"


def _runs_to_completion(pod) -> bool:
    """Return True for one-shot pods, which are not expected to stay Running.

    Besides Jobs, OpenShift rolls out its control-plane static pods with an
    installer and a pruner pod per revision, owned by that revision's
    revision-status-<n> ConfigMap. Superseded attempts are kept and can sit in
    Failed; the owning ClusterOperator reports whether the rollout is healthy.
    """
    for owner in pod.metadata.get("ownerReferences", []):
        kind = owner.get("kind")
        if kind == "Job":
            return True
        if (is_openshift() and kind == "ConfigMap"
                and owner.get("name", "").startswith(OPENSHIFT_STATIC_POD_OWNER_PREFIX)):
            return True
    return False


@pytest.mark.smoke
@allure.testcase("DSPTV-T152")
def test_pods_are_running():
    pods_not_running = []
    for pod in kr8s.get("pods", namespace=kr8s.ALL): # type: kr8s.api_resources.Pod
        if _runs_to_completion(pod):
            continue

        if not pod.status.phase == "Running" and not pod.status.phase == "Succeeded":
            pods_not_running.append(f"{pod.namespace}/{pod.name} is in {pod.status.phase}")
    assert pods_not_running == [], "Some of the pods are not in Running or Succeeded state"
