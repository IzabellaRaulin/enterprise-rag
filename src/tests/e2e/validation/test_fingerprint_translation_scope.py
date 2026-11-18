#!/usr/bin/env python
# -*- coding: utf-8 -*-
# Copyright (C) 2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

"""
Prove that a translation deployment gets its llm parameter row seeded and that it
is readable and writable under translation's own scope. Only the Llm step declares
paramsKind; PromptTemplate must not, so llm is the only group seeded here.

Needs a running translation deployment; skipped on other pipeline types.
"""

import allure
import logging

import pytest

from tests.e2e.validation.buildcfg import cfg

if cfg.get("pipeline_type") != "translation":
    pytestmark = pytest.mark.skip(
        reason=f"Requires a translation deployment; pipeline_type is {cfg.get('pipeline_type')!r}")

logger = logging.getLogger(__name__)

PIPELINE = "translation"
# The tenant the GMC controller seeds every pipeline's rows under.
TENANT = "_global"
PARAMS_KEY = "llm"
REQUIRED_FIELDS = {"max_new_tokens", "stream"}


@allure.testcase("DSPTV-T438")
@pytest.mark.smoke
def test_translation_llm_group_is_seeded(fingerprint_api_helper):
    response = fingerprint_api_helper.read_config(PARAMS_KEY, pipeline=PIPELINE, tenant=TENANT)
    assert response.status_code == 200, (
        f"No fingerprint row for translation's '{PARAMS_KEY}' group "
        f"(HTTP {response.status_code}). {response.text[:200]}")

    body = response.json()
    assert body["params_kind"] == "llm", f"'{PARAMS_KEY}' was seeded as kind '{body['params_kind']}'"
    missing = REQUIRED_FIELDS - set(body.get("values", {}))
    assert not missing, f"'{PARAMS_KEY}' is missing seeded field(s) {sorted(missing)}"


@allure.testcase("DSPTV-T439")
def test_translation_scope_write_is_read_back(fingerprint_api_helper):
    before = fingerprint_api_helper.read_config(PARAMS_KEY, pipeline=PIPELINE, tenant=TENANT)
    assert before.status_code == 200, (
        f"Cannot read translation's '{PARAMS_KEY}' baseline (HTTP {before.status_code})")
    baseline = before.json()["values"]["max_new_tokens"]
    target = baseline + 7

    try:
        write = fingerprint_api_helper.set_component_parameters(
            PARAMS_KEY, pipeline=PIPELINE, tenant=TENANT, max_new_tokens=target)
        assert write.status_code == 200, (
            f"Write to translation's '{PARAMS_KEY}' scope failed with "
            f"{write.status_code}: {write.text[:200]}")

        after = fingerprint_api_helper.read_config(PARAMS_KEY, pipeline=PIPELINE, tenant=TENANT)
        assert after.status_code == 200, "Cannot read back after the write"
        assert after.json()["values"]["max_new_tokens"] == target, (
            f"Write did not land in translation's scope: expected {target}, "
            f"got {after.json()['values']['max_new_tokens']}")
    finally:
        # The store is insert/update-only, so the row is reset rather than removed.
        restore = fingerprint_api_helper.set_component_parameters(
            PARAMS_KEY, pipeline=PIPELINE, tenant=TENANT, max_new_tokens=baseline)
        restore_error = (
            f"Failed to restore translation '{PARAMS_KEY}' max_new_tokens to {baseline} "
            f"(HTTP {restore.status_code}); later runs will see the test's value. "
            f"{restore.text[:200]}")
        if restore.status_code == 200:
            logger.info(f"Restored translation '{PARAMS_KEY}' max_new_tokens to {baseline}")
        else:
            logger.error(restore_error)
    assert restore.status_code == 200, restore_error
