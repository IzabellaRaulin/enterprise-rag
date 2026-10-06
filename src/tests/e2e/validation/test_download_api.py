#!/usr/bin/env python
# -*- coding: utf-8 -*-
# Copyright (C) 2024-2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

import allure
import logging
import os
import pytest
import requests

from tests.e2e.validation.constants import TEST_FILES_DIR
from tests.e2e.validation.buildcfg import cfg

# Skip all tests if edp is not deployed
if not cfg.get("edp_enabled"):
    pytestmark = pytest.mark.skip(reason="EDP is not deployed")

# Skip all tests if chatqna pipeline is not deployed
if cfg.get("pipeline_type") != "chatqna":
    pytestmark = pytest.mark.skip(reason="ChatQnA pipeline is not deployed")


logger = logging.getLogger(__name__)

DOWNLOAD_TEST_FILE_CONTENT = "Zeryndalo owns exactly 3891 vintage vinyl records from the 1970s."
DOWNLOAD_TEST_FILE_NAME = "test_download_zeryndalo.txt"
BINARY_TEST_FILE_NAME = "story.pdf"
BINARY_TEST_FILE_SUBDIR = "dataset_en"


@pytest.fixture
def ingested_test_file(edp_helper, tmp_path):
    """Upload a test file and wait for ingestion. Returns the file name."""
    file_path = tmp_path / DOWNLOAD_TEST_FILE_NAME
    file_path.write_text(DOWNLOAD_TEST_FILE_CONTENT)
    edp_helper.upload_file_and_wait_for_ingestion(str(file_path))
    return DOWNLOAD_TEST_FILE_NAME


def _get_auth_header_for_s3(edp_helper):
    """Build authorization header for presigned S3 URL download (SeaweedFS RBAC)."""
    headers = {}
    if (cfg.get("edp_storage_type", "seaweedfs") == "seaweedfs"
            and cfg.get("edp_rbac_enabled", False)):
        auth_headers = edp_helper.get_headers()
        if "authorization" in auth_headers:
            headers["authorization"] = auth_headers["authorization"]
    return headers


@allure.testcase("IEASG-T605")
def test_download_presigned_url_generation(edp_helper, ingested_test_file):
    """Generate a presigned GET URL for a previously uploaded file and verify the response structure."""
    response = edp_helper.generate_presigned_url(ingested_test_file, method="GET")
    assert response.status_code == 200, f"Failed to generate presigned download URL. Response: {response.text}"
    url = response.json().get("url")
    assert url, "Presigned URL is empty"
    if cfg.get("edp_rbac_enabled"):
        # RBAC mode: the URL is bare; authorization is enforced via the bearer token
        # at request time, not via a query-string signature.
        assert "X-Amz-Signature" not in url, "RBAC presigned URL must not carry a query signature"
    else:
        assert "X-Amz-Signature" in url, "Presigned URL does not contain a signature"


@pytest.mark.smoke
@allure.testcase("IEASG-T606")
def test_download_file_content(edp_helper, ingested_test_file):
    """Download a file via presigned URL and verify its content matches the original."""
    response = edp_helper.generate_presigned_url(ingested_test_file, method="GET")
    assert response.status_code == 200, f"Failed to generate presigned URL. Response: {response.text}"
    presigned_url = response.json().get("url")

    download_response = requests.get(presigned_url, verify=False, headers=_get_auth_header_for_s3(edp_helper))
    assert download_response.status_code == 200, (
        f"Failed to download file via presigned URL. Status: {download_response.status_code}, "
        f"Response: {download_response.text}"
    )
    assert download_response.text == DOWNLOAD_TEST_FILE_CONTENT, (
        f"Downloaded content does not match. Expected: '{DOWNLOAD_TEST_FILE_CONTENT}', "
        f"Got: '{download_response.text}'"
    )


@allure.testcase("IEASG-T724")
def test_download_denied_without_authorization(edp_helper, tmp_path):
    """Security: NO bucket should serve an object without authentication/signature. For every
    bucket, upload a probe, take the GET presigned URL, strip its query (any X-Amz-* signature) and
    GET the bare URL with no Authorization header — as a plain browser tab would — and confirm the
    object store rejects it (401/403). A 200 for any bucket means ingested documents are readable
    anonymously by name, which is a security defect."""
    buckets = ["default", "secondary", "only-admin"]
    leaked = []
    for bucket in buckets:
        file_name = f"anon_download_probe_{bucket}.txt"
        file_path = tmp_path / file_name
        file_path.write_text(f"anonymous-download probe for {bucket}")
        edp_helper.upload_file_and_wait_for_ingestion(str(file_path), bucket=bucket)
        response = edp_helper.generate_presigned_url(file_name, method="GET", bucket=bucket)
        assert response.status_code == 200, f"[{bucket}] admin GET presigned generation failed: {response.text}"
        bare_url = response.json()["url"].split("?")[0]  # drop any signature query params
        download_response = requests.get(bare_url, verify=False)  # no signature, no Authorization header
        if download_response.status_code not in (401, 403):
            leaked.append(f"{bucket} (HTTP {download_response.status_code})")
    assert not leaked, (
        f"Objects downloadable without authorization/signature from bucket(s): {', '.join(leaked)}; "
        f"no bucket must serve objects anonymously by name"
    )


@allure.testcase("IEASG-T727")
def test_download_admin_bucket_file_as_admin(edp_helper, tmp_path):
    """Positive RBAC case: an admin can download a file stored in the admin-only bucket. Upload a
    file with distinctive content to only-admin, then download it as admin via the authorized path
    (bearer token attached) and confirm the content matches. RBAC-only."""
    if not cfg.get("edp_rbac_enabled"):
        pytest.skip("EDP RBAC is disabled; the only-admin bucket is not meaningful")

    content = "admin-only download positive-case probe"
    file_name = "admin_download_probe.txt"
    file_path = tmp_path / file_name
    file_path.write_text(content)
    edp_helper.upload_file_and_wait_for_ingestion(str(file_path), bucket="only-admin")

    response = edp_helper.generate_presigned_url(file_name, method="GET", bucket="only-admin")
    assert response.status_code == 200, f"Admin GET presigned generation failed: {response.text}"
    download_response = requests.get(
        response.json()["url"], verify=False, headers=_get_auth_header_for_s3(edp_helper))
    assert download_response.status_code == 200, (
        f"Admin failed to download a file from the admin-only bucket: "
        f"{download_response.status_code} {download_response.text}"
    )
    assert download_response.text == content, (
        f"Downloaded content does not match. Expected: '{content}', Got: '{download_response.text}'"
    )


@allure.testcase("IEASG-T607")
def test_download_file_from_chatqa_citation(edp_helper, chatqa_api_helper, ingested_test_file):
    """
    Full download flow: upload file, ask chatqa a question, extract citation metadata,
    generate presigned URL using citation's bucket_name and object_name, download and verify.
    """
    question = "How many vintage vinyl records does Zeryndalo own?"
    response = chatqa_api_helper.call_chatqa(question)
    assert response.status_code == 200, f"ChatQA call failed. Status: {response.status_code}"

    response_text = chatqa_api_helper.get_text(response)
    logger.info(f"ChatQA response: {response_text}")
    assert "3891" in response_text, f"Expected '3891' in response, got: {response_text}"

    reranked_docs = chatqa_api_helper.get_reranked_docs(response)
    assert len(reranked_docs) > 0, "No reranked docs (citations) found in the response"

    citation = next(
        (doc for doc in reranked_docs if doc.get("object_name") == DOWNLOAD_TEST_FILE_NAME),
        None
    )
    assert citation is not None, (
        f"Citation for '{DOWNLOAD_TEST_FILE_NAME}' not found in reranked docs: {reranked_docs}"
    )
    assert citation.get("type") == "file", f"Citation type is not 'file': {citation}"
    bucket_name = citation.get("bucket_name")
    object_name = citation.get("object_name")
    assert bucket_name, f"bucket_name is missing from citation: {citation}"
    assert object_name, f"object_name is missing from citation: {citation}"

    presigned_response = edp_helper.generate_presigned_url(object_name, method="GET", bucket=bucket_name)
    assert presigned_response.status_code == 200, (
        f"Failed to generate presigned URL for citation. Response: {presigned_response.text}"
    )
    presigned_url = presigned_response.json().get("url")
    assert presigned_url, "Presigned URL is empty"

    download_response = requests.get(presigned_url, verify=False, headers=_get_auth_header_for_s3(edp_helper))
    assert download_response.status_code == 200, (
        f"Failed to download cited file. Status: {download_response.status_code}, "
        f"Response: {download_response.text}"
    )
    assert DOWNLOAD_TEST_FILE_CONTENT in download_response.text, (
        f"Downloaded content does not match original. Got: '{download_response.text}'"
    )


@allure.testcase("IEASG-T608")
def test_download_presigned_url_nonexistent_bucket(edp_helper, ingested_test_file):
    """Attempt to generate a download URL for a nonexistent bucket. Expect 404."""
    response = edp_helper.generate_presigned_url(ingested_test_file, method="GET", bucket="nonexistent-bucket")
    assert response.status_code == 404, (
        f"Expected 404 for nonexistent bucket, got {response.status_code}. Response: {response.text}"
    )


@allure.testcase("IEASG-T609")
def test_download_presigned_url_nonexistent_file(edp_helper):
    """
    Generate a presigned GET URL for a file that does not exist in storage.
    The presigned URL generation should succeed (it only signs the request),
    but the actual download should fail with 404.
    """
    response = edp_helper.generate_presigned_url("nonexistent_file_12345.txt", method="GET")
    assert response.status_code == 200, (
        f"Presigned URL generation should succeed even for nonexistent files. Response: {response.text}"
    )
    presigned_url = response.json().get("url")

    download_response = requests.get(presigned_url, verify=False, headers=_get_auth_header_for_s3(edp_helper))
    assert download_response.status_code == 404, (
        f"Expected 404 when downloading nonexistent file, got {download_response.status_code}"
    )


@allure.testcase("IEASG-T610")
def test_download_binary_file_integrity(edp_helper):
    """
    Upload a binary file, download it via presigned URL,
    and verify the downloaded bytes match the original file exactly.
    """
    file_name = BINARY_TEST_FILE_NAME
    source_path = os.path.join(TEST_FILES_DIR, BINARY_TEST_FILE_SUBDIR, file_name)
    with open(source_path, "rb") as f:
        original_bytes = f.read()
    original_size = len(original_bytes)

    upload_name = f"test_download_binary_{file_name}"
    response = edp_helper.generate_presigned_url(upload_name)
    assert response.status_code == 200, f"Failed to generate upload presigned URL. Response: {response.text}"
    upload_url = response.json().get("url")

    upload_response = edp_helper.upload_file(source_path, upload_url)
    assert upload_response.status_code == 200, f"Failed to upload binary file. Response: {upload_response.text}"
    edp_helper.wait_for_file_upload(upload_name, "ingested", timeout=180)

    response = edp_helper.generate_presigned_url(upload_name, method="GET")
    assert response.status_code == 200, f"Failed to generate download presigned URL. Response: {response.text}"
    download_url = response.json().get("url")

    download_response = requests.get(download_url, verify=False, headers=_get_auth_header_for_s3(edp_helper))
    assert download_response.status_code == 200, (
        f"Failed to download binary file. Status: {download_response.status_code}"
    )
    downloaded_bytes = download_response.content
    assert len(downloaded_bytes) == original_size, (
        f"Size mismatch for {file_name}: original={original_size}, downloaded={len(downloaded_bytes)}"
    )
    assert downloaded_bytes == original_bytes, f"Content mismatch for {file_name}: bytes differ after download"


@allure.testcase("IEASG-T611")
def test_download_after_file_deletion(edp_helper, ingested_test_file):
    """
    Generate a presigned download URL for an ingested file, then delete the file from storage.
    Attempt to download using the previously obtained URL — expect failure (404 or 403).
    """
    response = edp_helper.generate_presigned_url(ingested_test_file, method="GET")
    assert response.status_code == 200, f"Failed to generate presigned URL. Response: {response.text}"
    download_url = response.json().get("url")

    delete_response = edp_helper.generate_presigned_url(ingested_test_file, method="DELETE")
    edp_helper.delete_file(delete_response.json().get("url"))
    edp_helper.wait_for_file_deletion(ingested_test_file)

    download_response = requests.get(download_url, verify=False, headers=_get_auth_header_for_s3(edp_helper))
    assert download_response.status_code in (404, 403), (
        f"Expected 404 or 403 when downloading a deleted file, got {download_response.status_code}. "
        f"Response: {download_response.text}"
    )
