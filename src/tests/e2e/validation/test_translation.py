#!/usr/bin/env python
# -*- coding: utf-8 -*-
# Copyright (C) 2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

import allure
import logging
import re

import pytest

from tests.e2e.validation.buildcfg import cfg

if cfg.get("pipeline_type") != "translation":
    pytestmark = pytest.mark.skip(reason="Translation pipeline is not deployed")

logger = logging.getLogger(__name__)
BAD_REQUEST_STATUS_CODES = [400, 422]

# Checked by script rather than wording, so a different but valid translation still passes.
LANGUAGE_PAIRS = [
    ("Elephants may be the largest land animals. They can be found in India and Africa.",
     "Chinese", re.compile(r"[一-鿿]")),
    ("Elephants may be the largest land animals. They can be found in India and Africa.",
     "Russian", re.compile(r"[Ѐ-ӿ]")),
    ("Слоны могут быть самыми крупными наземными животными.",
     "English", re.compile(r"^[^Ѐ-ӿ]*[A-Za-z][^Ѐ-ӿ]*$")),
]


@allure.testcase("DSPTV-T432")
@pytest.mark.smoke
@pytest.mark.parametrize("text, target_language, target_script", LANGUAGE_PAIRS,
                         ids=[pair[1] for pair in LANGUAGE_PAIRS])
def test_translation_language_pairs(translation_helper, text, target_language, target_script):
    response = translation_helper.call(text, target_language)
    assert response.status_code == 200, f"Unexpected status code: {response.status_code}, {response.text}"

    translation = translation_helper.get_text(response).strip()
    logger.info(f"Translation to {target_language}: '{translation}'")
    assert translation, f"Empty translation to {target_language}"
    assert target_script.search(translation), f"Translation is not in {target_language}: '{translation}'"


@allure.testcase("DSPTV-T433")
def test_translation_streams_response(translation_helper):
    response = translation_helper.call("Good morning, how are you?", "German")
    assert response.status_code == 200, f"Unexpected status code: {response.status_code}, {response.text}"
    assert response.headers.get("Content-Type") == "text/event-stream"

    data_lines = [line for line in response.text.splitlines() if line.startswith("data: ")]
    assert len(data_lines) > 1, f"Expected the translation in several chunks, got: {response.text}"
    assert data_lines[-1] == "data: [DONE]", f"Stream did not end with [DONE]: {data_lines[-1]}"


@allure.testcase("DSPTV-T434")
def test_translation_honours_stream_false(translation_helper):
    response = translation_helper.call("Good morning, how are you?", "German", stream=False)
    assert response.status_code == 200, f"Unexpected status code: {response.status_code}, {response.text}"
    assert response.headers.get("Content-Type") == "application/json", (
        f"Expected a single JSON document for stream=false, got {response.headers.get('Content-Type')}")
    assert translation_helper.get_text(response).strip(), f"Empty translation: {response.text}"


@allure.testcase("DSPTV-T435")
def test_translation_requires_authentication(translation_helper):
    response = translation_helper.call_with_payload(
        {"text": "Hello", "target_language": "German"}, headers={"Content-Type": "application/json"})
    assert response.status_code in [401, 403], (
        f"Expected 401/403 without a token, got {response.status_code}: {response.text}")


@allure.testcase("DSPTV-T436")
@pytest.mark.parametrize("payload", [
    {"text": "Hello"},
    {"target_language": "German"},
    {"text": "", "target_language": "German"},
    {"text": 123, "target_language": "German"},
    '{"text": "Hello", "target_language":',
], ids=["no_target_language", "no_text", "empty_text", "non_string_text", "malformed_json"])
def test_translation_rejects_bad_request(translation_helper, payload):
    response = translation_helper.call_with_payload(payload)
    assert response.status_code in BAD_REQUEST_STATUS_CODES, (
        f"Expected {BAD_REQUEST_STATUS_CODES} for {payload}, got {response.status_code}: {response.text}")


@allure.testcase("DSPTV-T437")
def test_translation_keeps_language_detection_prompt_template(translation_helper, k8s_helper):
    """
    A paramsKind on the PromptTemplate step makes the router replace the template that
    language detection sends with the fingerprint's ChatQnA one, and every request fails with 400.
    """
    gmc = k8s_helper.get_gmconnector("translation")
    assert gmc, "No 'translation' GMConnector found"
    steps = {step["name"]: step for step in gmc.raw["spec"]["nodes"]["root"]["steps"]}
    assert "paramsKind" not in steps["PromptTemplate"]["internalService"]["config"]

    response = translation_helper.call("Good night.", "German")
    assert response.status_code == 200, f"Unexpected status code: {response.status_code}, {response.text}"
    assert translation_helper.get_text(response).strip(), "Empty translation"
