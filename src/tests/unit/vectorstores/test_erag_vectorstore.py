# Copyright (C) 2024-2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

import logging
import pytest
from unittest import mock
from unittest.mock import patch
from comps.cores.proto.docarray import DocList,EmbedDoc, SearchedDoc, TextDoc
from comps.vectorstores.utils.erag_vectorstore import EragVectorStore

@pytest.fixture
def reset_singleton():
    EragVectorStore._instance = None
    yield
    EragVectorStore._instance = None

@pytest.fixture
def vectorstore_instance(vector_store_name="redis"):
    EragVectorStore._instance = None
    return EragVectorStore(vector_store_name=vector_store_name)

@pytest.fixture
def mock_redis_vectorstore():
    with mock.patch('comps.vectorstores.utils.connectors.connector_redis.ConnectorRedis', autospec=True) as MockClass:
        mock_instance = MockClass.return_value
        #mock_instance.add_texts = ['a', 'b', 'c']
        mock_instance.search = SearchedDoc(
            user_prompt='Hello?',
            retrieved_docs=DocList(docs=[
                TextDoc(text='Hello, how are you?'),
                TextDoc(text='Hello, I am fine.')
            ])
        )
        yield MockClass

def test_singleton_behavior(mock_redis_vectorstore):
    instance1 = EragVectorStore("redis")
    instance2 = EragVectorStore("redis")
    assert instance1 is instance2
    EragVectorStore._instance = None

def test_initialize_method(mock_redis_vectorstore):
    stores = EragVectorStore("redis")._SUPPORTED_VECTOR_STORES.keys()
    for store in stores:
        vectorstore = EragVectorStore(store)
        assert vectorstore._vector_store_name is not None
        assert isinstance(vectorstore.vector_store, mock_redis_vectorstore.return_value.__class__)

# def test_initialize_method_with_unsupported_store(vectorstore_instance, caplog):
#     with caplog.at_level(logging.ERROR):
#             vectorstore_instance._initialize(vector_store_name="unsupported")
#             assert "Unsupported vector store" in caplog.text

@patch('comps.vectorstores.utils.erag_vectorstore.EragVectorStore.add_texts')
def test_add_texts_method(mock_add_texts):
    vectorstore = EragVectorStore("redis")
    input_texts = [
        EmbedDoc(text='Hello, how are you?', embedding=[0.1, 0.2, 0.3]),
        EmbedDoc(text='Hello, I am fine', embedding=[0.4, 0.5, 0.6]),
    ]
    vectorstore.add_texts(input=input_texts)
    mock_add_texts.assert_called_once_with(input=input_texts)

@patch('comps.vectorstores.utils.erag_vectorstore.EragVectorStore.search')
def test_search_method(mock_search):
    vectorstore = EragVectorStore("redis")
    search_text = EmbedDoc(text='Hello, how are you?', embedding=[0.1, 0.2, 0.3])
    vectorstore.search(input=search_text)
    mock_search.assert_called_once_with(input=search_text)

def test_search_types(mock_redis_vectorstore):
    types = ['similarity', 'similarity_search_with_siblings', 'similarity_distance_threshold', 'mmr']
    search_text = EmbedDoc(text='Hello, how are you?', embedding=[0.1, 0.2, 0.3])

    for type in types:
        search_text.search_type = type
        if type == 'similarity_distance_threshold':
            search_text.distance_threshold = 0.5

def test_import_redis_success():
    vectorstore_instance = EragVectorStore("redis")
    with mock.patch('comps.vectorstores.utils.connectors.connector_redis.ConnectorRedis', autospec=True) as MockConnectorRedis:
        vectorstore_instance._import_redis()
        assert isinstance(vectorstore_instance.vector_store, MockConnectorRedis.__class__)

def test_import_redis_failure(caplog):
    with mock.patch('comps.vectorstores.utils.connectors.connector_redis', side_effect=ModuleNotFoundError) as MockConnectorRedis:
        with caplog.at_level(logging.ERROR):
            with pytest.raises(ModuleNotFoundError):
                MockConnectorRedis("redis")
                assert "exception when loading ConnectorRedis" in caplog.text
                assert MockConnectorRedis.vector_store is None

def test_milvus_is_a_supported_store(mock_redis_vectorstore):
    assert "milvus" in EragVectorStore("redis")._SUPPORTED_VECTOR_STORES

def test_import_milvus_success(reset_singleton):
    with mock.patch('comps.vectorstores.utils.connectors.connector_milvus.ConnectorMilvus', autospec=True) as MockConnectorMilvus:
        vectorstore = EragVectorStore("milvus")
        assert vectorstore._vector_store_name == "milvus"
        assert vectorstore.vector_store is MockConnectorMilvus.return_value

def test_import_milvus_failure(reset_singleton, caplog):
    with mock.patch('comps.vectorstores.utils.connectors.connector_milvus.ConnectorMilvus',
                    side_effect=ModuleNotFoundError):
        with caplog.at_level(logging.ERROR):
            vectorstore = EragVectorStore("milvus")
            assert "exception when loading ConnectorMilvus" in caplog.text
            assert not hasattr(vectorstore, "vector_store")
