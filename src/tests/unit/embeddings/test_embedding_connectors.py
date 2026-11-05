# ruff: noqa: E711, E712
# Copyright (C) 2024-2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0
# test_embedding_connectors.py

from typing import List
from unittest.mock import MagicMock

import pytest
from docarray import BaseDoc

from comps.embeddings.utils.connectors.connector import EmbeddingConnector
from comps.embeddings.utils.connectors.ovms_connector import OVMSConnector, OVMSEndpointEmbeddings
from comps.embeddings.utils.connectors.vllm_connector import VLLMConnector

def clean_singleton():
    OVMSConnector._instance = None
    VLLMConnector._instance = None

class MockEmbeddingConnector(EmbeddingConnector):
    def embed_documents(self, texts: List[str]) -> List[List[float]]:
        return [[0.1, 0.2, 0.3] for _ in texts]

    def embed_query(self, input_text: str) -> BaseDoc:
        mock_doc = MagicMock(spec=BaseDoc)
        mock_doc.embedding = [0.1, 0.2, 0.3]
        return mock_doc

def test_embed_query_valid_input():
    connector = MockEmbeddingConnector("model", "endpoint")
    result = connector.embed_query("test query")
    assert isinstance(result, BaseDoc)
    assert result.embedding == [0.1, 0.2, 0.3]

def test_embed_query_empty_string():
    connector = MockEmbeddingConnector("model", "endpoint")
    result = connector.embed_query("")
    assert isinstance(result, BaseDoc)
    assert result.embedding == [0.1, 0.2, 0.3]

def test_EmbeddingConnector_not_implemented():
    with pytest.raises(TypeError):
        EmbeddingConnector("model", "endpoint")
