# Copyright (C) 2024-2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

from comps.vectorstores.utils.connectors.connector_redis import ConnectorRedis
from comps.vectorstores.utils.connectors.connector_milvus import ConnectorMilvus
import os
import pytest
from unittest import mock
from comps.cores.proto.docarray import EmbedDoc, SearchedDoc
from comps.retrievers.utils.erag_retriever import EragRetriever
from comps.vectorstores.utils.erag_vectorstore import EragVectorStore


@pytest.fixture(params=["redis", "milvus"])
def vector_store(request, monkeypatch):
    monkeypatch.setattr(EragRetriever, "_instance", None)
    monkeypatch.setattr(EragVectorStore, "_instance", None)
    monkeypatch.setattr(ConnectorMilvus, "_ensure_database", lambda self: None)
    monkeypatch.setattr(ConnectorMilvus, "_ensure_collection", lambda self: None)
    return request.param

@pytest.fixture
def mock_vectorstore():
    class SearchRes:
        def __init__(self, content=""):
            self.page_content = content
    class MockDbClient:
        def __init__(self, *args, **kwargs):
            pass
        def add_texts(texts, **kwargs):
            return texts
        def similarity_search_by_vector(**kwargs):
            return [SearchRes('a'), SearchRes('b')]
        def similarity_search_with_relevance_scores(self,  **kwargs):
            return []
        def max_marginal_relevance_search(self, **kwargs):
            return []
        def _check_embedding_index(**kwargs):
            return True
        def _create_index_if_not_exist(**kwargs):
            return True
        def get_files_filter_expression(self):
            return ConnectorRedis.get_files_filter_expression()
        def get_links_filter_expression(self):
            return ConnectorRedis.get_links_filter_expression()
        def get_object_name_filter_expression(self, bucket_name, object_name):
            return ConnectorRedis.get_object_name_filter_expression(bucket_name, object_name)
        def get_bucket_name_filter_expression(self, bucket_names):
            return ConnectorRedis.get_bucket_name_filter_expression(bucket_names)

    with mock.patch('comps.vectorstores.utils.connectors.connector_redis.ConnectorRedis', return_value=MockDbClient):
        yield

def test_filter_expression_from_rbac_by(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    assert retriever.vector_store._vector_store_name == vector_store
    rbac_by = {}
    rbac_filter_expression = retriever.filter_expression_from_rbac_by(rbac_by)
    assert rbac_filter_expression is None


def test_filter_expression_from_rbac_by_no_access(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    rbac_by = {"bucket_names": []}
    rbac_filter_expression = retriever.filter_expression_from_rbac_by(rbac_by)
    assert rbac_filter_expression is None

def test_filter_expression_from_rbac_by_valid_buckets(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    rbac_by = {"bucket_names": ["bucket1", "bucket2"]}
    rbac_filter_expression = retriever.filter_expression_from_rbac_by(rbac_by)
    assert rbac_filter_expression is not None
    assert "bucket1" in str(rbac_filter_expression)
    assert "bucket2" in str(rbac_filter_expression)

def test_filter_expression_from_search_by(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    search_by = {}
    filter_expression = retriever.filter_expression_from_search_by(search_by)
    assert filter_expression is None

def test_filter_expression_from_search_by_bucket_names_empty(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    search_by = {"bucket_names": []}
    filter_expression = retriever.filter_expression_from_search_by(search_by)
    assert filter_expression is None

def test_filter_expression_from_search_by_bucket_names_valid(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    search_by = {"bucket_names": ["bucket1", "bucket2"]}
    filter_expression = retriever.filter_expression_from_search_by(search_by)
    assert filter_expression is not None
    assert "bucket1" in str(filter_expression)
    assert "bucket2" in str(filter_expression)

def test_filter_expression_from_search_by_object_name_empty(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    search_by = {"bucket_name": "bucket1"}
    filter_expression = retriever.filter_expression_from_search_by(search_by)
    assert filter_expression is None

def test_filter_expression_from_search_by_bucket_name_empty(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    search_by = {"object_name": "bucket1"}
    filter_expression = retriever.filter_expression_from_search_by(search_by)
    assert filter_expression is None

def test_filter_expression_from_search_by_bucket_name_and_object_name_invalid(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    search_by = {"bucket_name": "", "object_name": ""}
    filter_expression = retriever.filter_expression_from_search_by(search_by)
    assert filter_expression is None

def test_filter_expression_from_search_by_bucket_name_and_object_name_valid(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    search_by = {"bucket_name": "bucket1", "object_name": "object1"}
    filter_expression = retriever.filter_expression_from_search_by(search_by)
    assert filter_expression is not None
    assert "bucket1" in str(filter_expression)
    assert "object1" in str(filter_expression)

# ======================================

def test_generate_retrieve_filter_expression_with_bucket_names_and_rbac_empty(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store, rbac_enabled=True)
    search_by = {"bucket_names": ["bucket1", "bucket2"] }
    rbac_by = {}
    filter_expression = retriever.filter_expression_from_search_by(search_by)
    rbac_filter_expression = retriever.filter_expression_from_rbac_by(rbac_by)
    result = retriever.generate_retrieve_filter_expression(filter_expression, rbac_filter_expression)
    assert result is not None
    assert str(retriever.vector_store.get_links_filter_expression()) == str(result)

def test_generate_retrieve_filter_expression_with_bucket_name_and_object_name_and_rbac_empty(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store, rbac_enabled=True)
    search_by = {"bucket_name": "bucket1", "object_name": "object1" }
    rbac_by = {}
    filter_expression = retriever.filter_expression_from_search_by(search_by)
    rbac_filter_expression = retriever.filter_expression_from_rbac_by(rbac_by)
    result = retriever.generate_retrieve_filter_expression(filter_expression, rbac_filter_expression)
    assert result is not None
    assert str(retriever.vector_store.get_links_filter_expression()) == str(result)

def test_generate_retrieve_filter_expression_with_bucket_name_and_object_name_invalid_and_rbac_empty(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store, rbac_enabled=True)
    search_by = {"bucket_name": "", "object_name": "" }
    rbac_by = {}
    filter_expression = retriever.filter_expression_from_search_by(search_by)
    rbac_filter_expression = retriever.filter_expression_from_rbac_by(rbac_by)
    result = retriever.generate_retrieve_filter_expression(filter_expression, rbac_filter_expression)
    assert result is not None
    assert str(retriever.vector_store.get_links_filter_expression()) == str(result)

def test_generate_retrieve_filter_expression_empty_and_rbac_filtered(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store, rbac_enabled=True)
    search_by = {}
    rbac_by = { "bucket_names": ["bucket1"] }
    filter_expression = retriever.filter_expression_from_search_by(search_by)
    rbac_filter_expression = retriever.filter_expression_from_rbac_by(rbac_by)
    result = retriever.generate_retrieve_filter_expression(filter_expression, rbac_filter_expression)
    assert result is not None
    assert "bucket1" in str(result)
    assert str(retriever.vector_store.get_links_filter_expression()) in str(result)

def test_generate_retrieve_filter_expression_with_bucket_names_and_rbac_missmatch(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store, rbac_enabled=True)
    search_by = {"bucket_names": ["bucket1", "bucket2"] }
    rbac_by = {"bucket_names": ["bucket3"]}
    filter_expression = retriever.filter_expression_from_search_by(search_by)
    rbac_filter_expression = retriever.filter_expression_from_rbac_by(rbac_by)
    result = retriever.generate_retrieve_filter_expression(filter_expression, rbac_filter_expression)
    assert result is not None
    assert str(retriever.vector_store.get_links_filter_expression()) in str(result)

def test_generate_retrieve_filter_expression_with_bucket_name_and_object_name_and_rbac_missmatch(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store, rbac_enabled=True)
    search_by = {"bucket_name": "bucket1", "object_name": "object1" }
    rbac_by = {"bucket_names": ["bucket3"]}
    filter_expression = retriever.filter_expression_from_search_by(search_by)
    rbac_filter_expression = retriever.filter_expression_from_rbac_by(rbac_by)
    result = retriever.generate_retrieve_filter_expression(filter_expression, rbac_filter_expression)
    assert result is not None
    assert str(retriever.vector_store.get_links_filter_expression()) in str(result)

def test_generate_retrieve_filter_expression_with_bucket_names_and_rbac_match(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store, rbac_enabled=True)
    search_by = {"bucket_names": ["bucket1", "bucket2"] }
    rbac_by = {"bucket_names": ["bucket1"]}
    filter_expression = retriever.filter_expression_from_search_by(search_by)
    rbac_filter_expression = retriever.filter_expression_from_rbac_by(rbac_by)
    result = retriever.generate_retrieve_filter_expression(filter_expression, rbac_filter_expression)
    assert result is not None
    assert "bucket1" in str(result)
    assert str(retriever.vector_store.get_links_filter_expression()) in str(result)

def test_generate_retrieve_filter_expression_with_bucket_name_and_object_name_and_rbac_match(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store, rbac_enabled=True)
    search_by = {"bucket_name": "bucket1", "object_name": "object1" }
    rbac_by = {"bucket_names": ["bucket1"]}
    filter_expression = retriever.filter_expression_from_search_by(search_by)
    rbac_filter_expression = retriever.filter_expression_from_rbac_by(rbac_by)
    result = retriever.generate_retrieve_filter_expression(filter_expression, rbac_filter_expression)
    assert result is not None
    assert "bucket1" in str(result)
    assert str(retriever.vector_store.get_links_filter_expression()) in str(result)

def test_generate_retrieve_filter_expression_with_bucket_name_and_object_name_and_rbac_disabled(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store, rbac_enabled=False)
    search_by = {"bucket_name": "bucket1", "object_name": "object1" }
    rbac_by = {}
    filter_expression = retriever.filter_expression_from_search_by(search_by)
    rbac_filter_expression = retriever.filter_expression_from_rbac_by(rbac_by)
    result = retriever.generate_retrieve_filter_expression(filter_expression, rbac_filter_expression)
    assert result is not None
    assert "bucket1" in str(result)
    assert "object1" in str(result)
    assert str(retriever.vector_store.get_links_filter_expression()) in str(result)

def test_generate_retrieve_filter_expression_empty_and_rbac_disabled(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store, rbac_enabled=False)
    search_by = {}
    rbac_by = {}
    filter_expression = retriever.filter_expression_from_search_by(search_by)
    rbac_filter_expression = retriever.filter_expression_from_rbac_by(rbac_by)
    result = retriever.generate_retrieve_filter_expression(filter_expression, rbac_filter_expression)
    assert result is None

# =============

def test_generate_search_by_empty(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    search_by = retriever.generate_search_by({})
    assert search_by == {}

def test_generate_search_by_bucket_names(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    search_by = retriever.generate_search_by({'search_by': {"bucket_names": ["123"]}})
    assert search_by == {"bucket_names": ["123"]}

def test_generate_search_by_bucket_name_and_object_name(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    search_by = retriever.generate_search_by({'search_by': {"bucket_name": "bucket1", "object_name": "object1"}})
    assert search_by == {"bucket_name": "bucket1", "object_name": "object1"}

# =================

def test_generate_rbac(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    return_bucket_names = { "buckets": ["bucket1", "bucket2"] }
    with mock.patch('comps.retrievers.utils.erag_retriever.retrieve_bucket_list', return_value=return_bucket_names):
        rbac_by = retriever.generate_rbac(None)
        assert rbac_by is not None
        assert rbac_by['bucket_names'] == return_bucket_names['buckets']

def test_generate_rbac_no_access(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    return_bucket_names = { "buckets": [] }
    with mock.patch('comps.retrievers.utils.erag_retriever.retrieve_bucket_list', return_value=return_bucket_names):
        rbac_by = retriever.generate_rbac(None)
        assert rbac_by is not None
        assert rbac_by['bucket_names'] == []

def test_generate_rbac_malformed(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    return_bucket_names = None
    with mock.patch('comps.retrievers.utils.erag_retriever.retrieve_bucket_list', return_value=return_bucket_names):
        rbac_by = retriever.generate_rbac(None)
        assert rbac_by is not None
        assert rbac_by['bucket_names'] == []

def test_generate_rbac_access_error(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    with mock.patch('comps.retrievers.utils.erag_retriever.retrieve_bucket_list', side_effect=ValueError("Authorization header is missing.")):
        rbac_by = retriever.generate_rbac(None)
        assert rbac_by is not None
        assert rbac_by['bucket_names'] == []

# ===================

@pytest.mark.asyncio
async def test_retrieve(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    input = EmbedDoc(text="test", embedding=[1, 2, 3])

    async def return_none():
        return SearchedDoc(retrieved_docs=[], user_prompt="")

    with mock.patch.object(retriever.vector_store, 'search', return_value=return_none()) as mock_retrieve:
        result = await retriever.retrieve(input=input, search_by={}, rbac_by=None)
        mock_retrieve.assert_called_once()
        assert result is not None

# ==================

def test_initialize_basic(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store)
    assert retriever.vector_store is not None
    assert retriever.rbac_enabled is False
    assert retriever._query_parsing_enabled is False
    assert retriever._query_parser is not None

def test_initialize_with_rbac_enabled(vector_store):
    EragRetriever._instance = None
    retriever = EragRetriever(vector_store=vector_store, rbac_enabled=True)
    assert retriever.vector_store is not None
    assert retriever.rbac_enabled is True
    assert retriever._query_parsing_enabled is False
    assert retriever._query_parser is not None

def test_initialize_with_metadata_extraction_off(vector_store):
    EragRetriever._instance = None
    with mock.patch.dict(os.environ, {"METADATA_EXTRACTION_MODE": "off"}):
        retriever = EragRetriever(vector_store=vector_store)
        assert retriever._query_parsing_enabled is False
        assert retriever._query_parser is not None

def test_initialize_with_metadata_extraction_regex_only(vector_store):
    EragRetriever._instance = None
    with mock.patch.dict(os.environ, {"METADATA_EXTRACTION_MODE": "regex_only"}):
        retriever = EragRetriever(vector_store=vector_store)
        assert retriever._query_parsing_enabled is True

def test_initialize_query_parser_initialization_failure(vector_store):
    EragRetriever._instance = None
    with mock.patch.dict(os.environ, {"METADATA_EXTRACTION_MODE": "regex_only"}):
        with mock.patch('comps.retrievers.utils.erag_retriever.QueryMetadataParser.from_env', side_effect=Exception("Parser init failed")):
            retriever = EragRetriever(vector_store=vector_store)
            assert retriever._query_parser is None

def test_initialize_singleton_pattern(vector_store):
    EragRetriever._instance = None
    retriever1 = EragRetriever(vector_store=vector_store)
    retriever2 = EragRetriever(vector_store=vector_store, rbac_enabled=True)
    assert retriever1 is retriever2
    assert retriever1.rbac_enabled is False
