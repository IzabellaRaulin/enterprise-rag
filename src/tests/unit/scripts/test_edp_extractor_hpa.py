#!/usr/bin/env python
# -*- coding: utf-8 -*-
# Copyright (C) 2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

"""
Render the edp chart's text extractor autoscaling with `helm template`: with hpa_enabled,
the HPA (hpaProvider prometheus-adapter) or KEDA's ScaledObject (hpaProvider keda), never
both, with the same targets and behavior. Skipped where helm is not installed, except in
CI, where the tox env installs it.
"""

import os
import shutil
import subprocess
from pathlib import Path

import pytest
import yaml

_CHART = Path(__file__).resolve().parents[4] / "deployment" / "components" / "edp"
_SERVER = "keda.prometheus.serverAddress=https://thanos:9092"
_KEDA = {
    ("ServiceAccount", "edp-text-extractor-keda"),
    ("Role", "edp-text-extractor-keda-metrics"), ("RoleBinding", "edp-text-extractor-keda-metrics"),
    ("Role", "edp-text-extractor-keda-token"), ("RoleBinding", "edp-text-extractor-keda-token"),
    ("ConfigMap", "edp-text-extractor-keda-ca"),
    ("TriggerAuthentication", "edp-text-extractor-prometheus"),
    ("ScaledObject", "edp-text-extractor"),
}

pytestmark = pytest.mark.skipif(shutil.which("helm") is None and not os.environ.get("CI"),
                                reason="helm is not installed")


@pytest.fixture(scope="module", autouse=True)
def _dependencies():
    # charts/*.tgz is not in git; both dependencies are local (file://)
    subprocess.run(["helm", "dependency", "build", "--skip-refresh", str(_CHART)], check=True, capture_output=True)


def _render(*values):
    args = ["helm", "template", "t", str(_CHART), "-n", "edp"]
    for value in values:
        args += ["--set", value]
    out = subprocess.run(args, check=True, capture_output=True, text=True).stdout
    return [d for d in yaml.safe_load_all(out) if d]


def _keda(docs):
    return {(d["kind"], d["metadata"]["name"]): d for d in docs if (d["kind"], d["metadata"]["name"]) in _KEDA}


def _thresholds(*values):
    scaled = _keda(_render("hpa_enabled=true", "hpaProvider=keda", _SERVER, *values))[("ScaledObject", "edp-text-extractor")]
    return [float(t["metadata"]["threshold"]) for t in scaled["spec"]["triggers"]]


def _quantity(value):
    value = str(value)
    return float(value[:-1]) / 1000 if value.endswith("m") else float(value)


def _hpa(docs):
    return [d for d in docs if d["kind"] == "HorizontalPodAutoscaler"
            and d["metadata"]["name"] == "edp-text-extractor"]


def test_default_renders_neither():
    docs = _render()
    assert not _hpa(docs) and not _keda(docs)


def test_keda_provider_without_hpa_enabled_renders_neither():
    docs = _render("hpaProvider=keda")
    assert not _hpa(docs) and not _keda(docs)


def test_prometheus_adapter_provider_renders_the_hpa_only():
    docs = _render("hpa_enabled=true")
    assert len(_hpa(docs)) == 1 and not _keda(docs)


def test_keda_objects():
    objects = _keda(_render("hpa_enabled=true", "hpaProvider=keda", _SERVER, "keda.operatorNamespace=openshift-keda"))
    assert set(objects) == _KEDA
    token = objects[("RoleBinding", "edp-text-extractor-keda-token")]
    assert token["subjects"] == [{"kind": "ServiceAccount", "name": "keda-operator", "namespace": "openshift-keda"}]
    auth = objects[("TriggerAuthentication", "edp-text-extractor-prometheus")]["spec"]
    assert auth["configMapTargetRef"] == [{"parameter": "ca", "name": "edp-text-extractor-keda-ca", "key": "service-ca.crt"}]
    triggers = objects[("ScaledObject", "edp-text-extractor")]["spec"]["triggers"]
    assert [t["name"] for t in triggers] == ["files-to-upload", "files-active"]
    assert {t["metadata"]["serverAddress"] for t in triggers} == {"https://thanos:9092"}
    assert {t["metadata"]["namespace"] for t in triggers} == {"edp"}


def test_keda_thresholds_are_the_hpa_targets():
    targets = [m["object"]["target"]["value"] for m in _hpa(_render("hpa_enabled=true"))[0]["spec"]["metrics"]]
    assert _thresholds() == [_quantity(t) for t in targets]


def test_keda_thresholds_follow_the_values():
    assert _thresholds("textExtractor.hpa.scaleUpTargetValue=3", "textExtractor.hpa.scaleDownTargetValue=500m") == [3, 0.5]


def test_keda_without_a_server_address_fails():
    with pytest.raises(subprocess.CalledProcessError) as e:
        _render("hpa_enabled=true", "hpaProvider=keda")
    assert "keda.prometheus.serverAddress is required" in e.value.stderr


@pytest.mark.parametrize("values, window", [
    ([], 60),
    (["textExtractor.hpa.behavior.scaleDown.stabilizationWindowSeconds=120"], 120),
    (["textExtractor.hpa.behavior=null"], 60),  # the chart's built-in behavior
], ids=["values", "override", "unset"])
def test_keda_behaves_like_the_hpa(values, window):
    hpa = _hpa(_render("hpa_enabled=true", *values))[0]["spec"]
    scaled = _keda(_render("hpa_enabled=true", "hpaProvider=keda", _SERVER, *values))[("ScaledObject", "edp-text-extractor")]["spec"]
    assert scaled["advanced"]["horizontalPodAutoscalerConfig"]["behavior"] == hpa["behavior"]
    assert hpa["behavior"]["scaleDown"]["stabilizationWindowSeconds"] == window
    assert (scaled["minReplicaCount"], scaled["maxReplicaCount"]) == (hpa["minReplicas"], hpa["maxReplicas"])


@pytest.mark.parametrize("values, grace", [
    ([], 450),
    (["textExtractor.terminationGracePeriodSeconds=900"], 900),
    (["textExtractor.terminationGracePeriodSeconds=null"], None),  # the Kubernetes default
], ids=["values", "override", "unset"])
def test_scale_down_lets_parts_in_flight_finish(values, grace):
    deployment = next(d for d in _render(*values)
                      if d["kind"] == "Deployment" and d["metadata"]["name"] == "edp-text-extractor")
    assert deployment["spec"]["template"]["spec"].get("terminationGracePeriodSeconds") == grace
