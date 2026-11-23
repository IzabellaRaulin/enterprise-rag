#!/usr/bin/env python
# -*- coding: utf-8 -*-
# Copyright (C) 2025-2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

import logging
from pathlib import Path

import allure
import requests
import yaml

from tests.e2e.validation.buildcfg import cfg, keycloak_base_url
from tests.e2e.validation.constants import VITE_KEYCLOAK_REALM

logger = logging.getLogger(__name__)

_LAYER_MAP_PATH = Path(__file__).resolve().parent / "uninstall_layers.yaml"
_BASE_NAMESPACES = ["default", "kube-system", "kube-public", "kube-node-lease", "local-path-storage"]
# Secrets that legitimately exist in any namespace and are not application leftovers.
_INFRA_SECRET_TYPES = {"kubernetes.io/service-account-token", "helm.sh/release.v1"}
_INFRA_SECRET_PREFIXES = ("default-token", "sh.helm.release")
# Platform gateway (toolkit-manager roles/gateway): path-mode apex grants, one key
# per path ('_root' is '/', 'a.b' is '/a/b'), each mapped to the namespace that owns it.
_GATEWAY_NAMESPACE = "envoy-gateway-system"
_PATH_GRANTS_CONFIGMAP = "gateway-path-grants"


def _grant_path(key):
    return "/" if key == "_root" else "/" + key.replace(".", "/")


def _realm_status(url):
    try:
        return requests.get(url, verify=False, timeout=15).status_code, None
    except requests.RequestException as e:
        return None, e


def _realm_deleted_problems(base_url):
    """Problems if the erag realm is not provably gone from the Keycloak at base_url."""
    master_url = f"{base_url}/realms/master"
    status, err = _realm_status(master_url)
    if status != 200:
        return [f"Cannot verify realm deletion: {master_url} returned {status or err} (expected 200)"]
    realm_url = f"{base_url}/realms/{VITE_KEYCLOAK_REALM}"
    status, err = _realm_status(realm_url)
    if status == 404:
        return []
    if status == 200:
        return [f"Keycloak realm '{VITE_KEYCLOAK_REALM}' still exists ({realm_url})"]
    return [f"Cannot verify realm deletion: {realm_url} returned {status or err} (expected 404)"]


def _load_layer_map():
    with open(_LAYER_MAP_PATH) as f:
        return yaml.safe_load(f)["layers"]


def _application_secrets(k8s_helper, namespace):
    """Secrets in a namespace that are not k8s/helm infrastructure (i.e. app leftovers)."""
    leftovers = []
    for secret in k8s_helper.list_secrets(namespace):
        if secret.raw.get("type") in _INFRA_SECRET_TYPES:
            continue
        if secret.name.startswith(_INFRA_SECRET_PREFIXES):
            continue
        leftovers.append(secret.name)
    return leftovers


def _verify_layer(k8s_helper, layer):
    """Assert that a layered teardown of `layer` removed exactly its resources and left lower
    layers intact. `layer` is one of erag/inference/platform/all (keys in uninstall_layers.yaml).

    Beyond namespaces, this checks the leftover classes a helm-based teardown commonly misses:
    Helm releases in removed namespaces, layer-owned CRD groups, and orphaned PVCs/secrets.
    """
    layer_map = _load_layer_map()
    assert layer in layer_map, f"Unknown layer '{layer}'. Known: {sorted(layer_map)}"
    spec = layer_map[layer] or {}

    current_ns = set(k8s_helper.list_namespaces())
    logger.info(f"Verifying uninstall of layer '{layer}'. Namespaces present: {sorted(current_ns)}")

    removed_namespaces = list(spec.get("removed_namespaces", []))
    problems = []

    # 1) Full teardown: only base namespaces may remain.
    if layer == "all":
        allowed = set(spec.get("allowed_namespaces", _BASE_NAMESPACES))
        leftover_ns = sorted(current_ns - allowed)
        if leftover_ns:
            problems.append(f"Namespaces still present after full teardown: {leftover_ns} "
                            f"(allowed: {sorted(allowed)})")
        # Everything not on the allowlist counts as removed for the release/secret checks.
        removed_namespaces = leftover_ns

    # 2) Removed namespaces must be gone (and not stuck Terminating).
    for ns in spec.get("removed_namespaces", []):
        if ns in current_ns:
            problems.append(f"Namespace '{ns}' still exists "
                            f"(phase={k8s_helper.get_namespace_phase(ns)}) — expected removed")

    # 3) Retained namespaces (lower layers) must still exist.
    for ns in spec.get("retained_namespaces", []):
        if ns not in current_ns:
            problems.append(f"Namespace '{ns}' is missing — lower layer should be retained")

    # 4) No Helm release may remain in a removed namespace.
    removed_ns_set = set(removed_namespaces)
    orphan_releases = sorted(
        f"{rns}/{rname}" for rns, rname in k8s_helper.list_helm_releases() if rns in removed_ns_set
    )
    if orphan_releases:
        problems.append(f"Helm releases still present in removed namespaces: {orphan_releases}")

    # 5) Layer-owned CRD groups must be gone (helm does not delete CRDs on uninstall).
    crd_groups = k8s_helper.list_crd_groups()
    leftover_crd_groups = sorted(g for g in spec.get("removed_crd_groups", []) if g in crd_groups)
    if leftover_crd_groups:
        problems.append(f"CRD groups still present (incomplete teardown): {leftover_crd_groups}")

    # 6) Orphaned PVCs / application secrets in namespaces that should have been deleted.
    for ns in removed_namespaces:
        if ns not in current_ns:
            continue
        pvcs = [p.name for p in k8s_helper.list_pvcs(ns)]
        if pvcs:
            problems.append(f"Orphaned PVCs in '{ns}': {pvcs}")
        secrets = _application_secrets(k8s_helper, ns)
        if secrets:
            problems.append(f"Orphaned secrets in '{ns}': {secrets}")

    # 7) No forbidden application pods linger in the default namespace.
    bad_pods = [p.name for p in k8s_helper.list_pods(namespace="default") if p.name.startswith("rag-")]
    if bad_pods:
        problems.append(f"Unexpected pods in 'default' namespace: {bad_pods}")

    # 8) Path mode: every apex path grant must belong to a namespace that still exists and was
    #    not torn down. The grants live in the gateway namespace, so deleting the owner's
    #    namespace does not release them. No ConfigMap (subdomain mode, gateway gone): skip.
    grants = k8s_helper.get_configmap_data(
        _PATH_GRANTS_CONFIGMAP,
        cfg.get("gateway_namespace", _GATEWAY_NAMESPACE),
    )
    if grants:
        stale = sorted(
            f"{_grant_path(k)} -> {ns}" for k, ns in grants.items() if ns not in current_ns or ns in removed_ns_set
        )
        if stale:
            problems.append(f"Path grants still owned by removed or missing namespaces: {stale}")

    # 9) erag teardown deletes its realm through the platform Keycloak, which stays installed.
    #    The realm endpoint needs no token, so this works after erag-credentials is gone. The
    #    master realm is checked first: a 404 only proves the realm is gone if the same base URL
    #    serves master (a wrong path prefix also answers 404).
    if layer == "erag":
        problems.extend(_realm_deleted_problems(keycloak_base_url()))

    assert not problems, (
        f"Uninstall verification failed for layer '{layer}':\n  - " + "\n  - ".join(problems)
    )


@allure.testcase("DSPTV-T217")
def test_uninstall_erag(k8s_helper):
    """After `es_auto_installer.sh teardown erag`, the erag application layer must be fully
    gone while the inference and platform layers below it stay intact."""
    _verify_layer(k8s_helper, "erag")


@allure.testcase("DSPTV-T218")
def test_uninstall_inference(k8s_helper):
    """After `es_auto_installer.sh teardown inference`, the inference layer must be fully gone
    while the platform layer below it stays intact."""
    _verify_layer(k8s_helper, "inference")


@allure.testcase("DSPTV-T219")
def test_uninstall_platform(k8s_helper):
    """After `es_auto_installer.sh teardown platform`, the platform layer must be fully gone,
    leaving only the bare Kubernetes cluster (infrastructure layer)."""
    _verify_layer(k8s_helper, "platform")


@allure.testcase("DSPTV-T202")
def test_uninstall(k8s_helper):
    """After a full teardown (`teardown --all`), only base Kubernetes/node namespaces may
    remain and no application resources (releases, CRDs, PVCs, secrets, pods) may linger."""
    _verify_layer(k8s_helper, "all")
