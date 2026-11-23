#!/usr/bin/env python
# -*- coding: utf-8 -*-
# Copyright (C) 2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

"""
Guard the routing-mode defaults that several deployment roles share.

ui_domain_name, erag_keycloak_domain_name and erag_keycloak_path_prefix are role defaults,
not facts: teardown, the upgrade pre-steps and --only never run
app_pre_install/tasks/solution_config.yaml, so a value that only that file set was missing
there (path-mode teardown called Keycloak without /keycloak and failed). Each role that reads
one of them therefore declares its own copy, and the copies must not drift apart: a role whose
copy differs renders a different URL on exactly the actions that skip solution_config.

These tests read the roles from disk and render the expressions with Jinja2. No Ansible,
cluster or network is used.
"""

import re
from pathlib import Path

import jinja2
import pytest
import yaml

_ROLES_DIR = Path(__file__).resolve().parents[4] / "deployment" / "roles"

# The one accepted definition of each shared default.
CANONICAL = {
    "ui_domain_name": "{{ base_domain_name | default('solutions.ai') }}",
    "erag_keycloak_domain_name": (
        "{{ (base_domain_name | default('solutions.ai')) if (routing_mode | default('subdomain')) == 'path'"
        " else 'keycloak.' ~ (base_domain_name | default('solutions.ai')) }}"
    ),
    "erag_keycloak_default_path_prefix": "{{ keycloak_path | default('/keycloak') }}",
    "erag_keycloak_path_prefix": (
        "{{ erag_keycloak_default_path_prefix if (routing_mode | default('subdomain')) == 'path' else '' }}"
    ),
}
# erag_keycloak_path_prefix is defined in terms of this one, so a role that declares the
# former must declare it too.
_REQUIRES = {"erag_keycloak_path_prefix": "erag_keycloak_default_path_prefix"}


def _roles():
    return sorted(p for p in _ROLES_DIR.iterdir() if (p / "tasks").is_dir())


def _defaults(role):
    path = role / "defaults" / "main.yaml"
    return (yaml.safe_load(path.read_text()) or {}) if path.exists() else {}


def _references(role, var):
    """Files of a role, other than its defaults, that mention var."""
    pattern = re.compile(rf"\b{re.escape(var)}\b")
    defaults = role / "defaults" / "main.yaml"
    return [
        f for f in role.rglob("*")
        if f.is_file() and f != defaults and pattern.search(f.read_text(errors="ignore"))
    ]


def test_roles_found():
    assert len(_roles()) > 10, f"no deployment roles under {_ROLES_DIR}"


@pytest.mark.parametrize("var", sorted(CANONICAL))
def test_every_copy_is_canonical(var):
    drifted = {
        role.name: value
        for role in _roles()
        if var in (defaults := _defaults(role)) and (value := defaults[var]) != CANONICAL[var]
    }
    assert not drifted, f"{var} differs from the shared definition in: {drifted}"


@pytest.mark.parametrize("var", sorted(CANONICAL))
def test_every_reader_declares_it(var):
    missing = {
        role.name: [str(f.relative_to(role)) for f in refs]
        for role in _roles()
        if (refs := _references(role, var)) and var not in _defaults(role)
    }
    assert not missing, (
        f"{var} is read but not declared in defaults/main.yaml of: {missing}. "
        "Without the default, actions that skip solution_config.yaml render it empty."
    )


@pytest.mark.parametrize("var, needed", sorted(_REQUIRES.items()))
def test_dependencies_declared(var, needed):
    missing = [role.name for role in _roles() if var in (d := _defaults(role)) and needed not in d]
    assert not missing, f"{var} needs {needed} declared alongside it in: {missing}"


def test_solution_config_does_not_set_them():
    tasks = yaml.safe_load((_ROLES_DIR / "app_pre_install/tasks/solution_config.yaml").read_text())
    set_keys = {k for t in tasks for k in (t.get("ansible.builtin.set_fact") or {})}
    duplicated = set_keys & set(CANONICAL)
    assert not duplicated, f"solution_config.yaml sets {duplicated} again; keep the role defaults the only source"


def _render(extra_vars):
    env = jinja2.Environment(undefined=jinja2.Undefined)
    scope = dict(extra_vars)
    # Resolve in dependency order, as Ansible does lazily.
    for var in ("erag_keycloak_default_path_prefix", "erag_keycloak_path_prefix",
                "erag_keycloak_domain_name", "ui_domain_name"):
        scope[var] = env.from_string(CANONICAL[var]).render(**scope)
    return scope


@pytest.mark.parametrize(
    "extra_vars, domain, prefix",
    [
        ({"routing_mode": "path", "base_domain_name": "example.com"}, "example.com", "/keycloak"),
        ({"routing_mode": "subdomain", "base_domain_name": "example.com"}, "keycloak.example.com", ""),
        ({"base_domain_name": "example.com"}, "keycloak.example.com", ""),
        ({"routing_mode": "path", "base_domain_name": "example.com", "keycloak_path": "/kc"}, "example.com", "/kc"),
        ({"routing_mode": "subdomain", "base_domain_name": "example.com", "keycloak_path": "/kc"},
         "keycloak.example.com", ""),
    ],
)
def test_rendered_values_match_the_platform(extra_vars, domain, prefix):
    """Path mode: Keycloak on the base domain under keycloak_path (its http-relative-path);
    subdomain mode: keycloak.<base> with no prefix (toolkit-manager roles/keycloak)."""
    scope = _render(extra_vars)
    assert scope["erag_keycloak_domain_name"] == domain
    assert scope["erag_keycloak_path_prefix"] == prefix
    assert scope["ui_domain_name"] == "example.com"
