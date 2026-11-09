#!/usr/bin/env python
# -*- coding: utf-8 -*-
# Copyright (C) 2025 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

import functools
import logging

import kr8s

logger = logging.getLogger(__name__)

cfg = {}  # dictionary to hold build configuration

# Served by OpenShift's aggregated apiservers, so a chart or a CRD cannot put
# them on upstream Kubernetes.
OPENSHIFT_API_GROUPS = ("security.openshift.io", "project.openshift.io")


def keycloak_base_url():
    """External Keycloak URL for the configured routing mode.

    Path mode serves Keycloak under the platform's keycloak_path (http-relative-path,
    default /keycloak) on the base domain; subdomain mode on keycloak.<base_domain_name>.
    """
    fqdn = cfg.get("base_domain_name")
    if cfg.get("routing_mode", "subdomain") == "path":
        prefix = cfg.get("erag_keycloak_default_path_prefix") or cfg.get("keycloak_path") or "/keycloak"
        return f"https://{fqdn}{prefix}"
    return f"https://keycloak.{fqdn}"


@functools.cache
def is_openshift():
    """True when the cluster under test is OpenShift.

    Asked once per session and read from discovery only, which every
    authenticated user may do, so a kubeconfig without cluster-scoped read
    access works too. The build config cannot answer this: ai-solutions
    resolves the platform at run time (playbooks/includes/preflight_platform.yaml)
    and never writes cluster_distribution to global_config.yaml.
    """
    try:
        groups = {api_version.split("/")[0] for api_version in kr8s.api().api_versions()}
    except Exception as error:
        logger.warning(f"Could not read the cluster's API groups, assuming Kubernetes: {error}")
        return False
    return not groups.isdisjoint(OPENSHIFT_API_GROUPS)
