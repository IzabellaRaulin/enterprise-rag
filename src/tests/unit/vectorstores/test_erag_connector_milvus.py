# Copyright (C) 2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

import asyncio
import json
from unittest import mock

import pytest

from comps.vectorstores.utils.connectors.connector_milvus import ConnectorMilvus


@pytest.fixture(autouse=True)
def reset_connector_singleton():
    """VectorStoreConnector memoizes itself on the class, so it leaks across tests."""
    if hasattr(ConnectorMilvus, "instance"):
        del ConnectorMilvus.instance
    yield
    if hasattr(ConnectorMilvus, "instance"):
        del ConnectorMilvus.instance


@pytest.fixture
def milvus_env(monkeypatch):
    monkeypatch.setenv("MILVUS_URL", "http://milvus.vdb.svc:19530")
    monkeypatch.setenv("MILVUS_USERNAME", "root")
    monkeypatch.setenv("MILVUS_PASSWORD", "s3cret")
    monkeypatch.setenv("MILVUS_DB", "default")
    monkeypatch.setenv("MILVUS_VECTOR_COLLECTION", "vdb")
    monkeypatch.delenv("VECTOR_ALGORITHM", raising=False)
    monkeypatch.delenv("VECTOR_DISTANCE_METRIC", raising=False)
    monkeypatch.delenv("VECTOR_DIMS", raising=False)
    for name in ("VECTOR_HNSW_M", "VECTOR_HNSW_EF_CONSTRUCTION", "VECTOR_HNSW_EF_RUNTIME", "VECTOR_HNSW_EPSILON"):
        monkeypatch.delenv(name, raising=False)


class Seq(list):
    """Marks a list as successive responses rather than one list-shaped payload."""


class FakeHttp:
    """Stands in for urllib3.PoolManager, replaying a payload per endpoint."""

    def __init__(self, payloads=None):
        self.payloads = payloads or {}
        self.calls = []

    def request(self, method, url, json=None, headers=None, timeout=None):
        path = url.split("/v2/vectordb", 1)[1]
        self.calls.append({"method": method, "path": path, "body": json, "headers": headers})
        payload = self.payloads.get(path)
        if isinstance(payload, Seq):
            data = payload.pop(0) if payload else {}
        elif payload is None:
            data = {}
        else:
            data = payload
        return FakeResponse({"code": 0, "data": data})

    def bodies(self, path):
        return [c["body"] for c in self.calls if c["path"] == path]


class FakeResponse:
    def __init__(self, payload, status=200):
        self.status = status
        self.data = json.dumps(payload).encode()


@pytest.fixture
def http():
    fake = FakeHttp({"/collections/has": {"has": True}})
    with mock.patch(
        "comps.vectorstores.utils.connectors.connector_milvus.urllib3.PoolManager",
        return_value=fake,
    ):
        yield fake


def test_requests_carry_auth_and_scope(milvus_env, http):
    ConnectorMilvus()
    call = http.calls[0]
    assert call["path"] == "/collections/has"
    assert call["headers"]["Authorization"] == "Bearer root:s3cret"
    assert call["body"] == {"dbName": "default", "collectionName": "vdb"}


def test_missing_url_falls_back_to_host_and_port(milvus_env, http, monkeypatch):
    monkeypatch.delenv("MILVUS_URL", raising=False)
    monkeypatch.setenv("MILVUS_HOST", "milvus.vdb.svc")
    monkeypatch.setenv("MILVUS_PORT", "19531")
    connector = ConnectorMilvus()
    assert connector._base_url() == "http://milvus.vdb.svc:19531/v2/vectordb"


def test_milvus_error_code_raises(milvus_env, monkeypatch):
    fake = FakeHttp()
    fake.request = lambda *a, **k: FakeResponse({"code": 1100, "message": "boom"})
    with mock.patch(
        "comps.vectorstores.utils.connectors.connector_milvus.urllib3.PoolManager",
        return_value=fake,
    ):
        with pytest.raises(RuntimeError, match="code 1100"):
            ConnectorMilvus()


@pytest.mark.parametrize(
    "algorithm, expected_index",
    [
        ("FLAT", "FLAT"),
        ("HNSW", "HNSW"),
        ("IVF_FLAT", "IVF_FLAT"),
        ("hnsw", "HNSW"),
        # Shared across all vector stores; Milvus has no SVS-VAMANA index.
        ("SVS-VAMANA", "AUTOINDEX"),
        ("nonsense", "AUTOINDEX"),
    ],
)
def test_index_type_mapping(milvus_env, http, monkeypatch, algorithm, expected_index):
    monkeypatch.setenv("VECTOR_ALGORITHM", algorithm)
    assert ConnectorMilvus().index_type == expected_index


def test_metric_type_is_always_cosine(milvus_env, http, monkeypatch):
    monkeypatch.setenv("VECTOR_DISTANCE_METRIC", "L2")
    assert ConnectorMilvus().metric_type == "COSINE"


def test_vector_schema_from_env(milvus_env, http, monkeypatch):
    monkeypatch.setenv("VECTOR_ALGORITHM", "hnsw")
    monkeypatch.setenv("VECTOR_DIMS", "1024")
    monkeypatch.setenv("VECTOR_DATATYPE", "FLOAT16")
    monkeypatch.setenv("VECTOR_DISTANCE_METRIC", "L2")

    assert ConnectorMilvus()._vector_schema_from_env() == {
        "algorithm": "HNSW",
        "dims": 1024,
        "datatype": "FLOAT32",
        "distance_metric": "COSINE",
    }


def test_existing_collection_is_not_recreated(milvus_env, http):
    ConnectorMilvus()
    assert http.bodies("/collections/create") == []


def test_hnsw_index_and_search_params(milvus_env, monkeypatch):
    monkeypatch.setenv("VECTOR_ALGORITHM", "HNSW")
    monkeypatch.setenv("VECTOR_HNSW_M", "24")
    monkeypatch.setenv("VECTOR_HNSW_EF_CONSTRUCTION", "120")
    monkeypatch.setenv("VECTOR_HNSW_EF_RUNTIME", "12")
    fake = FakeHttp({"/collections/has": {"has": False}})
    with mock.patch(
        "comps.vectorstores.utils.connectors.connector_milvus.urllib3.PoolManager",
        return_value=fake,
    ):
        connector = ConnectorMilvus()
        asyncio.run(connector.similarity_search_by_vector("q", [0.1], k=20))

    assert fake.bodies("/collections/create")[0]["indexParams"][0]["params"] == {"M": 24, "efConstruction": 120}
    assert fake.bodies("/entities/search")[0]["searchParams"] == {"metricType": "COSINE", "params": {"ef": 20}}


def test_hnsw_runtime_above_limit_and_unsupported_epsilon(milvus_env, http, monkeypatch, caplog):
    monkeypatch.setenv("VECTOR_ALGORITHM", "HNSW")
    monkeypatch.setenv("VECTOR_HNSW_EF_RUNTIME", "40")
    monkeypatch.setenv("VECTOR_HNSW_EPSILON", "0.01")
    connector = ConnectorMilvus()

    asyncio.run(connector.similarity_search_by_vector("q", [0.1], k=10))

    assert http.bodies("/entities/search")[0]["searchParams"]["params"] == {"ef": 40}
    assert "VECTOR_HNSW_EPSILON" in caplog.text


def test_hnsw_settings_are_not_used_for_flat(milvus_env, http, monkeypatch):
    monkeypatch.setenv("VECTOR_HNSW_EF_RUNTIME", "40")
    connector = ConnectorMilvus()

    asyncio.run(connector.similarity_search_by_vector("q", [0.1], k=10))

    assert http.bodies("/entities/search")[0]["searchParams"] == {"metricType": "COSINE"}


@pytest.mark.parametrize("name,value", [("VECTOR_HNSW_M", "1"), ("VECTOR_HNSW_EF_CONSTRUCTION", "0"),
                                       ("VECTOR_HNSW_EF_RUNTIME", "0")])
def test_invalid_hnsw_params(milvus_env, http, monkeypatch, name, value):
    monkeypatch.setenv("VECTOR_ALGORITHM", "HNSW")
    monkeypatch.setenv(name, value)
    with pytest.raises(ValueError, match=name):
        ConnectorMilvus()


def test_collection_created_when_absent(milvus_env, monkeypatch):
    monkeypatch.setenv("VECTOR_DIMS", "1024")
    monkeypatch.setenv("VECTOR_ALGORITHM", "HNSW")
    fake = FakeHttp({"/collections/has": {"has": False}})
    with mock.patch(
        "comps.vectorstores.utils.connectors.connector_milvus.urllib3.PoolManager",
        return_value=fake,
    ):
        ConnectorMilvus()

    body = fake.bodies("/collections/create")[0]
    fields = {f["fieldName"]: f for f in body["schema"]["fields"]}
    assert fields["embedding"]["elementTypeParams"]["dim"] == 1024
    assert fields["embedding"]["dataType"] == "FloatVector"
    assert fields["id"]["isPrimary"] is True
    assert body["indexParams"] == [{
        "fieldName": "embedding",
        "indexName": "embedding",
        "indexType": "HNSW",
        "metricType": "COSINE",
    }]
    assert fake.bodies("/collections/load")[0]["collectionName"] == "vdb"


def test_non_default_database_is_created(milvus_env, monkeypatch):
    monkeypatch.setenv("MILVUS_DB", "vdb")
    fake = FakeHttp({"/collections/has": {"has": True}, "/databases/list": ["default"]})
    with mock.patch(
        "comps.vectorstores.utils.connectors.connector_milvus.urllib3.PoolManager",
        return_value=fake,
    ):
        ConnectorMilvus()

    assert fake.bodies("/databases/create") == [{"dbName": "vdb"}]


def test_existing_database_is_not_recreated(milvus_env, monkeypatch):
    monkeypatch.setenv("MILVUS_DB", "vdb")
    fake = FakeHttp({"/collections/has": {"has": True}, "/databases/list": ["default", "vdb"]})
    with mock.patch(
        "comps.vectorstores.utils.connectors.connector_milvus.urllib3.PoolManager",
        return_value=fake,
    ):
        ConnectorMilvus()

    assert fake.bodies("/databases/create") == []


def test_default_database_is_never_listed(milvus_env, http):
    ConnectorMilvus()
    assert http.bodies("/databases/list") == []


def test_add_texts_respects_batch_size(milvus_env, http):
    connector = ConnectorMilvus()
    # VectorStoreConnector.__new__ takes no arguments, so batch_size cannot be
    # passed to the constructor on any connector in this package.
    connector.batch_size = 2
    texts = ["a", "b", "c", "d", "e"]
    embeddings = [[float(i)] for i in range(5)]

    asyncio.run(connector.add_texts(texts, embeddings, None))

    inserts = http.bodies("/entities/insert")
    assert [len(body["data"]) for body in inserts] == [2, 2, 1]
    assert inserts[0]["data"][0] == {"text": "a", "embedding": [0.0], "metadata": {}}


def test_delete_by_metadata_builds_filter(milvus_env, http):
    connector = ConnectorMilvus()

    asyncio.run(connector.search_and_delete_by_metadata("file_id", "abc-123"))

    body = http.bodies("/entities/delete")[0]
    assert body["filter"] == 'metadata["file_id"] == "abc-123"'
    assert body["collectionName"] == "vdb"


def test_similarity_search_maps_flat_hits(milvus_env, monkeypatch):
    fake = FakeHttp({
        "/collections/has": {"has": True},
        # REST flattens output fields onto the hit, unlike the gRPC SDK.
        "/entities/search": [{"id": 7, "distance": 0.25, "text": "hello",
                              "metadata": {"file_id": "f1"}}],
    })
    with mock.patch(
        "comps.vectorstores.utils.connectors.connector_milvus.urllib3.PoolManager",
        return_value=fake,
    ):
        connector = ConnectorMilvus()
        result = asyncio.run(
            connector.similarity_search_by_vector("hi?", [0.1, 0.2], k=3, filter_expression="x == 1")
        )

    assert result.user_prompt == "hi?"
    doc = result.retrieved_docs[0]
    assert doc.text == "hello"
    assert doc.metadata["file_id"] == "f1"
    assert doc.metadata["vector_distance"] == 0.75
    assert doc.metadata["id"] == "7"

    body = fake.bodies("/entities/search")[0]
    assert body["limit"] == 3
    assert body["filter"] == "x == 1"
    assert body["annsField"] == "embedding"
    assert body["data"] == [[0.1, 0.2]]
    assert body["outputFields"] == ["text", "metadata"]


def test_similarity_search_without_hits(milvus_env, http):
    connector = ConnectorMilvus()

    result = asyncio.run(connector.similarity_search_by_vector("hi?", [0.1], k=3))

    assert len(result.retrieved_docs) == 0


def test_filter_expressions(milvus_env, http):
    connector = ConnectorMilvus()

    assert str(connector.get_object_name_filter_expression("bucket", "obj")) == (
        'metadata["bucket_name"] == "bucket" and metadata["object_name"] == "obj"'
    )
    assert str(connector.get_bucket_name_filter_expression(["a", "b"])) == (
        'metadata["bucket_name"] in ["a", "b"]'
    )
    assert str(connector.get_site_name_filter_expression(["s1"])) == (
        'metadata["site_name"] in ["s1"]'
    )
    # Empty scopes must not silently widen an RBAC filter.
    with pytest.raises(ValueError):
        connector.get_bucket_name_filter_expression([])
    with pytest.raises(ValueError):
        connector.get_site_name_filter_expression([])


def test_filter_expressions_compose(milvus_env, http):
    connector = ConnectorMilvus()

    bucket = connector.get_bucket_name_filter_expression(["a"])
    links = connector.get_links_filter_expression()

    assert str(links) == 'not exists metadata["file_id"]'
    assert str(connector.get_files_filter_expression()) == 'not exists metadata["link_id"]'
    assert str(bucket | links) == (
        '(metadata["bucket_name"] in ["a"]) or (not exists metadata["file_id"])'
    )
    assert str(connector.get_date_range_filter_expression("creation_date", 1, 2)) == (
        'metadata["creation_date"] >= 1 and metadata["creation_date"] <= 2'
    )
    assert str(connector.get_author_filter_expression("Ann")) == 'metadata["author"] like "%Ann%"'
    # Quotes in user input must not break out of the expression.
    assert str(connector.get_bucket_name_filter_expression(['a" or 1==1'])) == (
        'metadata["bucket_name"] in ["a\\" or 1==1"]'
    )


def _siblings_http(search_hit, query_pages):
    return FakeHttp({
        "/collections/has": {"has": True},
        "/entities/search": [search_hit],
        "/entities/query": Seq(query_pages),
    })


def test_siblings_pick_nearest_chunk_on_each_side(milvus_env):
    fake = _siblings_http(
        {"id": 7, "distance": 0.1, "text": "match",
         "metadata": {"file_id": "f1", "start_index": 100}},
        [
            [
                {"id": 1, "text": "far before", "metadata": {"file_id": "f1", "start_index": 10}},
                {"id": 2, "text": "just before", "metadata": {"file_id": "f1", "start_index": 90}},
            ],
            [
                {"id": 4, "text": "far after", "metadata": {"file_id": "f1", "start_index": 300}},
                {"id": 3, "text": "just after", "metadata": {"file_id": "f1", "start_index": 110}},
            ],
        ],
    )
    with mock.patch(
        "comps.vectorstores.utils.connectors.connector_milvus.urllib3.PoolManager",
        return_value=fake,
    ):
        connector = ConnectorMilvus()
        result = asyncio.run(connector.similarity_search_with_siblings("q", [0.1], k=1))

    assert [doc.text for doc in result.sibling_docs["7"]] == ["just before", "just after"]
    assert fake.bodies("/entities/query")[0]["filter"] == (
        'metadata["file_id"] == "f1" and metadata["start_index"] < 100'
    )


def test_siblings_preserve_retrieval_filter(milvus_env):
    fake = _siblings_http(
        {"id": 7, "distance": 0.1, "text": "match",
         "metadata": {"file_id": "f1", "start_index": 100}},
        [[], []],
    )
    with mock.patch(
        "comps.vectorstores.utils.connectors.connector_milvus.urllib3.PoolManager",
        return_value=fake,
    ):
        connector = ConnectorMilvus()
        allowed = connector.get_bucket_name_filter_expression(["allowed"])
        filter_expression = allowed | connector.get_links_filter_expression()
        asyncio.run(connector.similarity_search_with_siblings("q", [0.1], k=1, filter_expression=filter_expression))

    assert fake.bodies("/entities/search")[0]["filter"] == str(filter_expression)
    assert [body["filter"] for body in fake.bodies("/entities/query")] == [
        f'({filter_expression}) and (metadata["file_id"] == "f1" and metadata["start_index"] < 100)',
        f'({filter_expression}) and (metadata["file_id"] == "f1" and metadata["start_index"] > 100)',
    ]


def test_siblings_skipped_when_chunk_metadata_missing(milvus_env):
    fake = _siblings_http({"id": 7, "distance": 0.1, "text": "match", "metadata": {}}, [])
    with mock.patch(
        "comps.vectorstores.utils.connectors.connector_milvus.urllib3.PoolManager",
        return_value=fake,
    ):
        connector = ConnectorMilvus()
        result = asyncio.run(connector.similarity_search_with_siblings("q", [0.1], k=1))

    assert len(result.retrieved_docs) == 1
    assert not result.sibling_docs
    assert fake.bodies("/entities/query") == []


def test_siblings_with_no_initial_hits(milvus_env, http):
    connector = ConnectorMilvus()

    result = asyncio.run(connector.similarity_search_with_siblings("q", [0.1], k=1))

    assert len(result.retrieved_docs) == 0
    assert http.bodies("/entities/query") == []
