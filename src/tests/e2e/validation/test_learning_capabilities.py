#!/usr/bin/env python
# -*- coding: utf-8 -*-
# Copyright (C) 2024-2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

import allure
from datetime import date, timedelta
import logging
import os
import pytest
from types import SimpleNamespace
import yaml

from tests.e2e.validation.constants import DATAPREP_UPLOAD_DIR, TEST_FILES_DIR
from tests.e2e.validation.buildcfg import cfg

# Skip all tests if chatqna pipeline is not deployed
if cfg.get("pipeline_type") != "chatqna":
    pytestmark = pytest.mark.skip(reason="ChatQnA pipeline is not deployed")


logger = logging.getLogger(__name__)
UNRELATED_RESPONSE_MSG = "Chatbot should return answer that is strictly related to the previously uploaded file"

# These tests change retriever/reranker parameters and assert the chatqna pipeline
# reflects them, so writes must target the scope the chatqna router reads
# (chatqna._global).
PIPELINE = "chatqna"
TENANT = "_global"

# The following test cases are meant to check if chatbot learns from uploaded files.
# In each test case, a file is uploaded and a question related to the file content is asked.


@pytest.fixture
def test_data(request, test_language):
    """Load scenarios and package them with language info"""
    data_path = os.path.join(TEST_FILES_DIR, "localized_dataset.yaml")

    with open(data_path, "r", encoding="utf-8") as f:
        scenarios = yaml.load(f, Loader=yaml.SafeLoader)

    current_scenario = scenarios.get(request.node.name, [])
    stages = [step[test_language] for step in current_scenario if test_language in step]

    return SimpleNamespace(stages=stages, language=test_language)


@pytest.mark.smoke
@allure.testcase("DSPTV-T190")
def test_txt(edp_helper, chatqa_api_helper, test_data):
    """*.txt file learning capabilities"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@pytest.mark.smoke
@allure.testcase("DSPTV-T193")
def test_docx_text_only(edp_helper, chatqa_api_helper, test_data):
    """*.docx file learning capabilities (pure text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@pytest.mark.smoke
@allure.testcase("DSPTV-T191")
def test_pdf_text_only(edp_helper, chatqa_api_helper, test_data):
    """*.pdf file learning capabilities (pure text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@pytest.mark.smoke
@allure.testcase("DSPTV-T192")
def test_pptx_text_only(edp_helper, chatqa_api_helper, test_data):
    """*.pptx file learning capabilities (pure text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


# https://jira.devtools.intel.com/browse/IEASG-2851
@pytest.mark.xfail(reason="SmartArt text extraction regression")
@allure.testcase("DSPTV-T199")
def test_pptx_complex(edp_helper, chatqa_api_helper, test_data):
    """*.pptx file learning capabilities (tables, charts, and SmartArt)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T156")
def test_json_text_only(edp_helper, chatqa_api_helper, test_data):
    """*.json file learning capabilities (pure text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T157")
def test_doc_text_only(edp_helper, chatqa_api_helper, test_data):
    """*.doc file learning capabilities (pure text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T158")
def test_ppt_text_only(edp_helper, chatqa_api_helper, test_data):
    """*.ppt file learning capabilities (pure text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T159")
def test_md_text_only(edp_helper, chatqa_api_helper, test_data):
    """*.md file learning capabilities (pure text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T160")
def test_html_text_only(edp_helper, chatqa_api_helper, test_data):
    """*.html file learning capabilities (pure text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T161")
def test_tiff_text_only(edp_helper, chatqa_api_helper, test_data):
    """*.tiff file learning capabilities (pure text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T162")
def test_jpg_text_only(edp_helper, chatqa_api_helper, test_data):
    """*.jpg file learning capabilities (pure text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T163")
def test_jpeg_text_only(edp_helper, chatqa_api_helper, test_data):
    """*.jpeg file learning capabilities (pure text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T164")
def test_png_text_only(edp_helper, chatqa_api_helper, test_data):
    """*.png file learning capabilities (pure text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T165")
def test_svg_text_only(edp_helper, chatqa_api_helper, test_data):
    """*.svg file learning capabilities (pure text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T166")
def test_xlsx_text_only(edp_helper, chatqa_api_helper, test_data):
    """*.xlsx file learning capabilities (pure text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T167")
def test_xls_text_only(edp_helper, chatqa_api_helper, test_data):
    """*.xls file learning capabilities (pure text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T168")
def test_csv_text_only(edp_helper, chatqa_api_helper, test_data):
    """*.csv file learning capabilities (pure text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T169")
def test_jsonl_text_only(edp_helper, chatqa_api_helper, test_data):
    """*.jsonl file learning capabilities (pure text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T170")
def test_xml_text_only(edp_helper, chatqa_api_helper, test_data):
    """*.xml file learning capabilities (pure text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T171")
def test_docx_multi_paragraph(edp_helper, chatqa_api_helper, test_data):
    """*.docx file learning capabilities (multiple paragraphs inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T172")
def test_docx_with_images(edp_helper, chatqa_api_helper, test_data):
    """*.docx file learning capabilities (with images and text surrounding the images)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T173")
def test_get_context_from_filename(edp_helper, chatqa_api_helper):
    """Upload a file whose name contains unique info not present in the file body.
    Verify that the system can answer questions using context from the filename alone."""
    file = "Veldrix Tormanheim - Project Nexus.txt"
    person_question = "Who is associated with Project Nexus?"
    project_question = "What project is Veldrix Tormanheim working on?"

    baseline = ask_question(chatqa_api_helper, person_question)
    assert not chatqa_api_helper.words_in_response(
        ["veldrix", "tormanheim"], baseline
    ), "System should not know about Veldrix Tormanheim before file upload"

    edp_helper.upload_file_and_wait_for_ingestion(
        os.path.join(DATAPREP_UPLOAD_DIR, file)
    )

    response = ask_question(chatqa_api_helper, person_question)
    assert chatqa_api_helper.words_in_response(
        ["veldrix", "tormanheim"], response
    ), "System should associate Veldrix Tormanheim with Project Nexus from the filename"

    response = ask_question(chatqa_api_helper, project_question)
    assert chatqa_api_helper.words_in_response(
        ["nexus"], response
    ), "System should know Veldrix Tormanheim is associated with Project Nexus from the filename"

@allure.testcase("DSPTV-T392")
def test_pdf_table(edp_helper, chatqa_api_helper, test_data):
    """*.pdf file learning capabilities (with tables inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T175")
def test_docx_tables(edp_helper, chatqa_api_helper, test_data):
    """*.docx file learning capabilities (with tables inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T174")
def test_docx_formatted_text(edp_helper, chatqa_api_helper, test_data):
    """*.docx file learning capabilities (with formatted text inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T176")
def test_docx_with_hyperlink(edp_helper, chatqa_api_helper, test_data):
    """*.docx file learning capabilities (with hyperlinks inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T177")
def test_docx_numbered_and_bulleted_lists(edp_helper, chatqa_api_helper, test_data):
    """*.docx file learning capabilities (with numbered and bulleted lists inside the file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@pytest.mark.smoke
@allure.testcase("DSPTV-T178")
def test_content_is_forgotten_after_file_deletion(edp_helper, chatqa_api_helper, test_data):
    """Verify if the content is actually forgotten after file deletion"""
    stage = test_data.stages[0]
    file_name = stage["files"][0]
    question = stage["questions"][0]["question"]
    expected = stage["questions"][0]["expected_any"]

    file_path = os.path.join(TEST_FILES_DIR, f"dataset_{test_data.language}", file_name)
    logger.info(f"Ingesting file: {file_name}")
    edp_helper.upload_file_and_wait_for_ingestion(file_path)
    response = ask_question(chatqa_api_helper, question)
    assert chatqa_api_helper.words_in_response(expected, response), UNRELATED_RESPONSE_MSG

    response = delete_file(edp_helper, file_name)
    assert response.status_code == 204, f"Failed to delete file. Response: {response.text}"
    logger.debug("Waiting for file to be deleted from the system")
    edp_helper.wait_for_file_deletion(file_name)
    logger.debug("Asking question once again after file deletion")
    response = ask_question(chatqa_api_helper, question)
    assert not chatqa_api_helper.words_in_response(expected, response), UNRELATED_RESPONSE_MSG


@allure.testcase("DSPTV-T179")
def test_adoc_basic(edp_helper, chatqa_api_helper, test_data):
    """Validate basic *.adoc files support and learning capabilities"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T180")
def test_adoc_recognized_as_troff_mime_type(edp_helper, chatqa_api_helper, test_data):
    """
    Upload a file with 'text/troff' mime type.
    Check if the file is recognized as adoc.
    Ask a question related to the content of the file.
    """
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T181")
def test_adoc_links(edp_helper, chatqa_api_helper, test_data):
    """Check if links are properly extracted from the adoc file"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T182")
def test_adoc_tables(edp_helper, chatqa_api_helper, test_data):
    """Validate if tables are recognized in the adoc file"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T183")
def test_adoc_source_code(edp_helper, chatqa_api_helper, test_data):
    """Put a code block in the adoc file and check if it is recognized"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T184")
def test_adoc_substitutions(edp_helper, chatqa_api_helper, test_data):
    """Check if substitutions are recognized in the text (*.adoc file)"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T188")
def test_logs_parsing_capability(edp_helper, chatqa_api_helper, test_data):
    """Check if chatbot can extract information from log files"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


def test_docx_comprehensive_pl(edp_helper, chatqa_api_helper, test_data):
    """*.docx file with tables, lists, hyperlinks, and Polish character encoding"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


def _ingestion_date_candidates():
    """Return date strings for today ± 1 day to account for timezone differences between host and test runner."""
    candidates = []
    for delta in (-1, 0, 1):
        d = date.today() + timedelta(days=delta)
        candidates.append(d.strftime("%Y-%m-%d"))
        candidates.append(d.strftime("%B %-d, %Y"))
        candidates.append(d.strftime("%-d %B %Y"))
        candidates.append(d.strftime("%B %Y"))
    return candidates


def run_metadata_validation(edp_helper, chatqa_api_helper, test_data):
    if not test_data.stages:
        pytest.skip("No available stages for this language in the test data.")

    failures = []
    for stage in test_data.stages:
        stage_files = stage.get("files", [])
        for file_name in stage_files:
            path = os.path.join(TEST_FILES_DIR, f"dataset_{test_data.language}", file_name)
            logger.info(f"Ingesting file: {file_name}")
            edp_helper.upload_file_and_wait_for_ingestion(path)

        try:
            questions = stage.get("questions", [])
            consolidated_q = stage.get("consolidated_question")
            pending_indices = list(range(len(questions)))

            if consolidated_q:
                response = ask_question(chatqa_api_helper, consolidated_q)
                still_pending = []
                for idx in pending_indices:
                    item = questions[idx]
                    expected = _ingestion_date_candidates() if item.get("check_ingestion_date") else item.get("expected_any", [])
                    if expected and chatqa_api_helper.words_in_response(expected, response):
                        logger.info(f"Consolidated response matched: {item['question'][:80]}...")
                    else:
                        still_pending.append(idx)
                pending_indices = still_pending
                if not pending_indices:
                    logger.info("All metadata found in consolidated response")

            for idx in pending_indices:
                item = questions[idx]
                response = ask_question(chatqa_api_helper, item["question"])
                expected = _ingestion_date_candidates() if item.get("check_ingestion_date") else item.get("expected_any", [])
                if expected and not chatqa_api_helper.words_in_response(expected, response):
                    failures.append(f"None of {expected[:5]} found in response to: '{item['question']}'\n  Response: {response}")
        finally:
            for file_name in stage_files:
                logger.info(f"Deleting file after test: {file_name}")
                delete_file(edp_helper, file_name)

    assert not failures, f"Metadata failures ({len(failures)}):\n" + "\n".join(f"  [{i+1}] {f}" for i, f in enumerate(failures))


@allure.testcase("DSPTV-T194")
def test_metadata_pdf(edp_helper, chatqa_api_helper, test_data):
    """Verify that all metadata (author, title, creation/modification/ingestion dates) is extracted from PDF"""
    run_metadata_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T200")
def test_pdf_metadata_survives_split(edp_helper, chatqa_api_helper, test_data):
    """Verify PDF metadata survives EDP's >100-page split.

    A PDF larger than MAX_PAGES_PER_SPLIT (default 100 pages) is split into parts
    by qpdf in EDP before extraction. qpdf parts do not carry the original /Info
    dictionary, so author/creation/modification dates must be re-injected onto every
    part; regressing that drops them. The fixture
    (test_pdf_metadata_survives_split.pdf) is 105 pages but only ~50 KB - text sits on
    page 1 and page 101 so both split parts carry a retrievable chunk while the file
    stays tiny.

    Also spot-checks content integrity across the split: one question targets a fact
    that lives only on page 101 (the second split part), guarding against content-loss
    regressions on the split path where a part is dropped, mis-ordered or truncated.
    """
    run_metadata_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T195")
def test_metadata_docx(edp_helper, chatqa_api_helper, test_data):
    """Verify that all metadata (author, title, creation/modification/ingestion dates) is extracted from DOCX"""
    run_metadata_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T196")
def test_metadata_pptx(edp_helper, chatqa_api_helper, test_data):
    """Verify that all metadata (author, title, creation/modification/ingestion dates) is extracted from PPTX"""
    run_metadata_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T197")
def test_metadata_distinguishes_authors_across_documents(edp_helper, chatqa_api_helper, test_data):
    """Verify that the LLM correctly attributes authors when multiple documents are ingested"""
    run_standard_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T198")
def test_metadata_xlsx(edp_helper, chatqa_api_helper, test_data):
    """Verify that all metadata (author, title, creation/modification/ingestion dates) is extracted from XLSX"""
    run_metadata_validation(edp_helper, chatqa_api_helper, test_data)


@allure.testcase("DSPTV-T189")
def test_reupload(edp_helper, chatqa_api_helper, test_data):
    """Check if re-uploading the same file and updated file works as expected"""

    def modify_file(file__to_modify, old_value, new_value):
        with open(file__to_modify, "r+") as f:
            content = f.read().replace(old_value, new_value)
            f.seek(0)
            f.write(content)
            f.truncate()

    stage = test_data.stages[0]
    file_name = stage["files"][0]
    question = stage["questions"][0]["question"]
    expected_initial = stage["questions"][0]["expected_any"][0]
    expected_modified = stage["questions"][0]["expected_modified"]
    modify_old = stage["questions"][0]["modify_old"]
    modify_new = stage["questions"][0]["modify_new"]

    file_path = os.path.join(TEST_FILES_DIR, f"dataset_{test_data.language}", file_name)

    # Upload initial file
    edp_helper.upload_file_and_wait_for_ingestion(file_path)
    response = ask_question(chatqa_api_helper, question)
    assert expected_initial in response, UNRELATED_RESPONSE_MSG

    # Re-upload the same file
    edp_helper.upload_file_and_wait_for_ingestion(file_path)
    response = ask_question(chatqa_api_helper, question)
    assert expected_initial in response, UNRELATED_RESPONSE_MSG

    # Upload updated file
    try:
        modify_file(file_path, modify_old, modify_new)
        edp_helper.upload_file_and_wait_for_ingestion(file_path)
        response = ask_question(chatqa_api_helper, question)
        assert expected_modified in response, UNRELATED_RESPONSE_MSG
    finally:
        modify_file(file_path, modify_new, modify_old)  # restore original file content


@allure.testcase("DSPTV-T39")
def test_similarity_search_with_siblings(edp_helper, chatqa_api_helper, fingerprint_api_helper, test_data):
    """
    Upload a file with a list of 20 elements.
    Ask a question that requires the entire list to be included in the answer.
    With retriever's search type set to 'similarity', expect the answer to be incomplete.
    Change the retriever's search type to 'similarity_search_with_siblings' and expect the answer to be complete.
    """
    # Read the baseline from the same scope the test writes to, so the restore
    # cannot overwrite the chatqa retriever config with values from another scope.
    retriever_config = fingerprint_api_helper.read_config("retriever", pipeline=PIPELINE, tenant=TENANT)
    current_parameters = retriever_config.json().get("values", {}) if retriever_config.status_code == 200 else {}
    original_k = current_parameters.get("k")
    original_search_type = current_parameters.get("search_type")
    if not original_k or not original_search_type:
        pytest.skip("Failed to get current retriever's parameters")

    stage = test_data.stages[0]
    file_name = stage["files"][0]
    question = stage["questions"][0]["question"]
    expected_absent = stage["questions"][0]["expected_absent"]
    expected_present = stage["questions"][0]["expected_present"]

    file_path = os.path.join(TEST_FILES_DIR, f"dataset_{test_data.language}", file_name)
    edp_helper.upload_file_and_wait_for_ingestion(file_path)

    try:
        if original_search_type != "similarity":
            fingerprint_api_helper.set_chatqa_component_parameters(
                "retriever", search_type="similarity", k=original_k)

        response = ask_question(chatqa_api_helper, question)
        assert expected_absent not in response, (f"'{expected_absent}' is the last principle in the list. It should not be "
                                               "included in the answer when search_type='similarity'")

        # Change search type to similarity_search_with_siblings. Expect the response to be longer
        fingerprint_api_helper.set_chatqa_component_parameters("retriever", search_type="similarity_search_with_siblings",
                                                               k=original_k)
        response = ask_question(chatqa_api_helper, question)
        assert expected_present in response, \
            "With similarity_search_with_siblings search type, the answer should be longer"
    finally:
        # Restore default parameters
        fingerprint_api_helper.set_chatqa_component_parameters(
            "retriever", search_type=original_search_type, k=original_k)


@allure.testcase("DSPTV-T185")
def test_updated_file(edp_helper, chatqa_api_helper):
    question_1 = "Who is Alex?"
    expected_1_before = ["sloth"]
    expected_1_after = ["fox"]

    question_2 = "Where does Alex live?"
    expected_2_before = ["rainforest", "Amazon", "america", "south"]
    expected_2_after = ["cold", "tundra", "arctic", "north"]

    question_3 = "What does Alex eat?"
    expected_3_before = ["leaves", "shoots", "fruits"]
    expected_3_after = ["carnivor", "fish", "birds"]

    file = "test_updated_document.txt"
    edp_helper.upload_file_and_wait_for_ingestion(os.path.join(DATAPREP_UPLOAD_DIR, file))

    response_1_before = ask_question(chatqa_api_helper, question_1)
    assert chatqa_api_helper.words_in_response(expected_1_before, response_1_before), UNRELATED_RESPONSE_MSG

    response_2_before = ask_question(chatqa_api_helper, question_2)
    assert chatqa_api_helper.words_in_response(expected_2_before, response_2_before), UNRELATED_RESPONSE_MSG

    response_3_before = ask_question(chatqa_api_helper, question_3)
    assert chatqa_api_helper.words_in_response(expected_3_before, response_3_before), UNRELATED_RESPONSE_MSG

    with edp_helper.substitute_file("test_updated_document.txt", "test_updated_document-updated.txt"):
        edp_helper.upload_file_and_wait_for_ingestion(os.path.join(DATAPREP_UPLOAD_DIR, file))

        response_1_after = ask_question(chatqa_api_helper, question_1)
        assert chatqa_api_helper.words_in_response(expected_1_after, response_1_after), UNRELATED_RESPONSE_MSG

        response_2_after = ask_question(chatqa_api_helper, question_2)
        assert chatqa_api_helper.words_in_response(expected_2_after, response_2_after), UNRELATED_RESPONSE_MSG

        response_3_after = ask_question(chatqa_api_helper, question_3)
        assert chatqa_api_helper.words_in_response(expected_3_after, response_3_after), UNRELATED_RESPONSE_MSG


@allure.testcase("DSPTV-T187")
def test_multi_doc_reasoning(edp_helper, chatqa_api_helper):
    """
    Upload 2 documents:
    A: Marianooo has 20 balls for a game called Marianoball.
    B: One ball for the game called Marianoball costs 15$.
    Check if chatbot can combine information from both documents to answer the question:
    "What is the total value of all Marianooo's balls for the game called Marianoball?"
    """
    file1 = "multi_doc_retrieval_1.txt"
    file2 = "multi_doc_retrieval_2.txt"
    edp_helper.upload_file_and_wait_for_ingestion(os.path.join(DATAPREP_UPLOAD_DIR, file1))
    edp_helper.upload_file_and_wait_for_ingestion(os.path.join(DATAPREP_UPLOAD_DIR, file2))

    question = "What is the total value of all Marianooo's balls for the game called Marianoball?"
    response = ask_question(chatqa_api_helper, question)
    assert "300" in response.lower(), UNRELATED_RESPONSE_MSG


@allure.testcase("DSPTV-T186")
def test_top_n(edp_helper, chatqa_api_helper, fingerprint_api_helper, test_data):
    """
    Upload 2 documents:
    A: Emanueleee has got 4 RC cars.
    B: Raffaeleee has got 27 RC cars.
    Ask a question that requires information from both documents to be answered:
    "How many RC cars do Emanueleee and Raffaeleee have in total?"
    Change top_n parameter to 1 and check if the answer is wrong (only one document should be used to answer the question).
    Change top_n parameter to 3 and check if the answer is correct (both documents should be used to answer the question).
    """
    # Read the baseline from the same scope the test writes to, so the restore
    # cannot overwrite the chatqa reranker config with values from another scope.
    reranker_config = fingerprint_api_helper.read_config("reranker", pipeline=PIPELINE, tenant=TENANT)
    current_parameters = reranker_config.json().get("values", {}) if reranker_config.status_code == 200 else {}
    original_n = current_parameters.get("top_n")
    if original_n is None:
        pytest.skip("Failed to get current reranker's top_n")

    # Get the first (and only) stage for this test from the unified test_data object
    stage = test_data.stages[0]
    question_item = stage["questions"][0]
    question = question_item["question"]
    expected_value = question_item["expected_any"][0]

    # 1. Ingest files using the language defined in test_data
    for file_name in stage.get("files", []):
        path = os.path.join(TEST_FILES_DIR, f"dataset_{test_data.language}", file_name)
        edp_helper.upload_file_and_wait_for_ingestion(path)

    try:
        fingerprint_api_helper.set_chatqa_component_parameters("reranker", top_n=1)
        response = ask_question(chatqa_api_helper, question)
        assert expected_value not in response.lower(), "only a single document should be used to answer the question"

        fingerprint_api_helper.set_chatqa_component_parameters("reranker", top_n=3)
        response = ask_question(chatqa_api_helper, question)
        assert expected_value in response.lower(), "3 documents should be used to answer the question"
    finally:
        # revert top_n back to original value
        fingerprint_api_helper.set_chatqa_component_parameters("reranker", top_n=original_n)


def upload_and_ask_question(edp_helper, chatqa_api_helper, file, question=""):
    edp_helper.upload_file_and_wait_for_ingestion(os.path.join(DATAPREP_UPLOAD_DIR, file))
    return ask_question(chatqa_api_helper, question)


def ask_question(chatqa_api_helper, question=""):
    """Ask a question to the chatbot and return the response"""
    response = chatqa_api_helper.call_chatqa(question)
    assert response.status_code == 200, "Unexpected status code returned"
    response_text = chatqa_api_helper.get_text(response)
    logger.info(f"ChatQA response: {response_text}")
    return response_text


def delete_file(edp_helper, file):
    response = edp_helper.generate_presigned_url(file, "DELETE")
    return edp_helper.delete_file(response.json().get("url"))


def run_standard_validation(edp_helper, chatqa_api_helper, test_data):
    """
    For each stage in the test data:
    1. Ingest files specified in the stage.
    2. For each question in the stage, ask the question and validate the response against expected answers.
    3. Support both 'expected_any' and 'expected_all' validation methods.
    """
    if not test_data.stages:
        pytest.skip("No available stages for this language in the test data. Look at the dataset file.")

    for stage in test_data.stages:
        stage_files = stage.get("files", [])
        # 1. File ingestion
        for file_name in stage_files:
            path = os.path.join(TEST_FILES_DIR, f"dataset_{test_data.language}", file_name)
            logger.info(f"Ingesting file: {file_name}")
            edp_helper.upload_file_and_wait_for_ingestion(path)

        # 2. Question validation
        for item in stage.get("questions", []):
            question = item.get("question")
            response = ask_question(chatqa_api_helper, question)

            # expected_any: success if at least one string is found
            if "expected_any" in item:
                expected_list = item["expected_any"]
                assert chatqa_api_helper.words_in_response(expected_list, response), \
                    f"Assertion failed! None of {expected_list} found in response: {response}"

            # expected_all: success only if all strings are found
            if "expected_all" in item:
                expected_list = item["expected_all"]
                missing_words = [word for word in expected_list if word.lower() not in response.lower()]
                assert not missing_words, \
                    f"Assertion failed! Missing words: {missing_words} in response: {response}"
