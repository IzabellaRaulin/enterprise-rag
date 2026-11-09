# Copyright (C) 2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

import json
import os
from typing import Any, Dict, Iterable, List, Optional, Union # noqa: F401

import urllib3

from comps.cores.mega.logger import change_erag_logger_level, get_erag_logger
from comps.cores.proto.docarray import SearchedDoc, TextDoc # noqa: F401
from comps.cores.utils.utils import get_boolean_env_var, sanitize_env # noqa: F401
from comps.vectorstores.utils.connectors.connector import VectorStoreConnector


logger = get_erag_logger(f"{__file__.split('comps/')[1].split('/', 1)[0]}")
change_erag_logger_level(logger, log_level=os.getenv("ERAG_LOGGER_LEVEL", "INFO"))

# VECTOR_ALGORITHM is shared across all vector stores; anything Milvus does not
# implement falls back to AUTOINDEX rather than failing the ingestion pipeline.
_INDEX_TYPES = {
    "FLAT": "FLAT",
    "HNSW": "HNSW",
    "IVF_FLAT": "IVF_FLAT",
    "AUTOINDEX": "AUTOINDEX",
}
_METRIC_TYPES = {"COSINE", "L2", "IP"}

# The REST insert path sends vectors as JSON floats, so only FloatVector is usable here.
_SUPPORTED_DATATYPE = "FLOAT32"

_API_PREFIX = "/v2/vectordb"
_OUTPUT_FIELDS = ["text", "metadata"]

_DATE_FIELDS = ("creation_date", "ingestion_date", "last_update_date")

# Mirrors ConnectorRedis._FILENAME_EXTENSIONS so title filters behave the same on both stores.
_FILENAME_EXTENSIONS = frozenset({
    "pdf", "docx", "xlsx", "pptx", "doc", "xls", "ppt",
    "txt", "md", "json", "yaml", "yml", "csv", "html", "xml", "log",
})


def _quote(value: Any) -> str:
    """Renders a value as a Milvus string literal, neutralising expression injection."""
    escaped = str(value).replace("\\", "\\\\").replace('"', '\\"')
    return f'"{escaped}"'


def _looks_like_filename(value: str) -> bool:
    dot = value.rfind(".")
    return dot > 0 and value[dot + 1:].lower() in _FILENAME_EXTENSIONS


class MilvusFilter:
    """A Milvus boolean expression that composes with & and | like redisvl's FilterExpression."""

    def __init__(self, expression: str):
        self.expression = expression

    def _combine(self, other: "MilvusFilter", operator: str) -> "MilvusFilter":
        if not isinstance(other, MilvusFilter):
            return NotImplemented
        return MilvusFilter(f"({self.expression}) {operator} ({other.expression})")

    def __and__(self, other):
        return self._combine(other, "and")

    def __or__(self, other):
        return self._combine(other, "or")

    def __invert__(self):
        return MilvusFilter(f"not ({self.expression})")

    def __bool__(self) -> bool:
        return bool(self.expression)

    def __str__(self) -> str:
        return self.expression

    def __repr__(self) -> str:
        return f"MilvusFilter({self.expression!r})"


class ConnectorMilvus(VectorStoreConnector):
    """Talks to Milvus over its RESTful v2 API, served on the same port as gRPC."""

    def __init__(self, batch_size: int = 32):
        self.batch_size = batch_size
        self.index_dict = {}
        self._pool = None
        self.collection_name = str(sanitize_env(os.getenv("MILVUS_VECTOR_COLLECTION", "vdb")))
        self.database_name = str(sanitize_env(os.getenv("MILVUS_DB", "default")))
        self.dims = int(sanitize_env(str(os.getenv("VECTOR_DIMS", 768))))
        self.timeout = float(sanitize_env(str(os.getenv("MILVUS_TIMEOUT", 30))))
        self.index_type = _INDEX_TYPES.get(
            str(sanitize_env(os.getenv("VECTOR_ALGORITHM", "FLAT"))).upper(), "AUTOINDEX"
        )
        metric = str(sanitize_env(os.getenv("VECTOR_DISTANCE_METRIC", "COSINE"))).upper()
        self.metric_type = metric if metric in _METRIC_TYPES else "COSINE"

        datatype = str(sanitize_env(os.getenv("VECTOR_DATATYPE", _SUPPORTED_DATATYPE))).upper()
        if datatype != _SUPPORTED_DATATYPE:
            logger.warning(
                f"VECTOR_DATATYPE={datatype} is ignored by the Milvus connector; "
                f"the collection is created as {_SUPPORTED_DATATYPE}."
            )

        if self.index_type == "HNSW" and any(os.getenv(v) for v in (
            "VECTOR_HNSW_M", "VECTOR_HNSW_EF_CONSTRUCTION", "VECTOR_HNSW_EF_RUNTIME", "VECTOR_HNSW_EPSILON"
        )):
            logger.warning(
                "VECTOR_HNSW_* settings are ignored by the Milvus connector; "
                "Milvus applies its own HNSW defaults."
            )

        try:
            self._ensure_database()
            self._ensure_collection()
            logger.info(f"Milvus collection '{self.collection_name}' is ready.")
        except Exception as e:
            logger.error(f"Error initializing Milvus collection: {e}")
            raise

    # --- transport -----------------------------------------------------------

    def get_client(self) -> urllib3.PoolManager:
        if self._pool is None:
            self._pool = urllib3.PoolManager()
        return self._pool

    def _base_url(self) -> str:
        uri = sanitize_env(os.getenv("MILVUS_URL"))
        if not uri:
            host = sanitize_env(os.getenv("MILVUS_HOST", "localhost"))
            port = int(sanitize_env(str(os.getenv("MILVUS_PORT", 19530))))
            schema = "https" if get_boolean_env_var("MILVUS_SSL", False) else "http"
            uri = f"{schema}://{host}:{port}"
        return uri.rstrip("/") + _API_PREFIX

    def _headers(self) -> Dict[str, str]:
        user = sanitize_env(os.getenv("MILVUS_USERNAME", "root"))
        password = sanitize_env(os.getenv("MILVUS_PASSWORD", None))
        return {"Authorization": f"Bearer {user}:{password}", "Accept": "application/json"}

    def _post(self, path: str, body: Dict[str, Any], tolerate: Optional[str] = None) -> Any:
        """Milvus answers HTTP 200 with a non-zero `code` in the body on failure."""
        response = self.get_client().request(
            "POST",
            self._base_url() + path,
            json=body,
            headers=self._headers(),
            timeout=self.timeout,
        )
        try:
            payload = json.loads(response.data)
        except ValueError:
            raise RuntimeError(
                f"Milvus {path} returned a non-JSON response (HTTP {response.status})"
            )

        if payload.get("code") == 0:
            return payload.get("data")

        message = str(payload.get("message", ""))
        if tolerate and tolerate in message:
            logger.debug(f"Milvus {path} skipped: {message}")
            return None
        raise RuntimeError(f"Milvus {path} failed (code {payload.get('code')}): {message}")

    def _scoped(self, **body: Any) -> Dict[str, Any]:
        return {"dbName": self.database_name, "collectionName": self.collection_name, **body}

    # --- bootstrap -----------------------------------------------------------

    def _ensure_database(self) -> None:
        """Milvus does not create databases implicitly, unlike collections."""
        if self.database_name == "default":
            return
        existing = self._post("/databases/list", {}) or []
        if self.database_name not in existing:
            self._post("/databases/create", {"dbName": self.database_name}, tolerate="already exist")

    def _ensure_collection(self) -> None:
        state = self._post("/collections/has", self._scoped()) or {}
        if state.get("has"):
            return

        schema = {
            "autoID": True,
            "enableDynamicField": False,
            "fields": [
                {"fieldName": "id", "dataType": "Int64", "isPrimary": True},
                {"fieldName": "text", "dataType": "VarChar",
                 "elementTypeParams": {"max_length": 65535}},
                {"fieldName": "embedding", "dataType": "FloatVector",
                 "elementTypeParams": {"dim": self.dims}},
                {"fieldName": "metadata", "dataType": "JSON", "nullable": True},
            ],
        }
        index_params = [{
            "fieldName": "embedding",
            "indexName": "embedding",
            "indexType": self.index_type,
            "metricType": self.metric_type,
        }]

        self._post(
            "/collections/create",
            self._scoped(schema=schema, indexParams=index_params),
            tolerate="already exist",
        )
        self._post("/collections/load", self._scoped())

    # --- data ----------------------------------------------------------------

    async def add_texts(self, texts, embeddings, metadatas=None):
        try:
            rows = [
                {"text": text, "embedding": embedding, "metadata": metadata or {}}
                for text, embedding, metadata in zip(texts, embeddings, metadatas or [None] * len(texts))
            ]
            for start in range(0, len(rows), self.batch_size):
                self._post("/entities/insert", self._scoped(data=rows[start:start + self.batch_size]))
        except Exception as e:
            logger.error(f"Error inserting into Milvus: {e}")
            raise

    async def search_and_delete_by_metadata(self, field_name: str, field_value: str):
        try:
            expression = f'metadata[{_quote(field_name)}] == {_quote(field_value)}'
            self._post("/entities/delete", self._scoped(filter=expression))
            logger.debug(f"Deleted records from Milvus where {field_name} = {field_value}")
        except Exception as e:
            logger.error(f"Error deleting from Milvus: {e}")
            raise

    def _convert_to_text_doc(self, hit: dict) -> TextDoc:
        """REST returns output fields flattened onto the hit, next to id and distance."""
        raw_metadata = hit.get("metadata") or {}
        if isinstance(raw_metadata, str):
            # JSON fields come back serialised on some Milvus versions.
            try:
                raw_metadata = json.loads(raw_metadata) if raw_metadata else {}
            except ValueError:
                logger.warning("Could not decode Milvus metadata payload; falling back to empty metadata.")
                raw_metadata = {}
        metadata = dict(raw_metadata)
        metadata["vector_distance"] = hit.get("distance")
        metadata["id"] = str(hit.get("id"))
        return TextDoc(text=hit.get("text", ""), metadata=metadata)

    async def similarity_search_by_vector(self, input_text: str, embedding: List[float], k: int,
                                          distance_threshold: float = None, filter_expression=None,
                                          parse_result: bool = True) -> SearchedDoc:
        try:
            hits = self._post("/entities/search", self._scoped(
                data=[embedding],
                annsField="embedding",
                limit=k,
                filter=str(filter_expression) if filter_expression else "",
                outputFields=_OUTPUT_FIELDS,
                searchParams={"metricType": self.metric_type},
            )) or []

            if parse_result is False:
                return hits

            return SearchedDoc(
                retrieved_docs=[self._convert_to_text_doc(hit) for hit in hits],
                user_prompt=input_text,
            )
        except Exception as e:
            logger.error(f"Error searching in Milvus: {e}")
            raise

    async def similarity_search_with_siblings(self, input_text: str, embedding: List[float],
                                              k: int,
                                              distance_threshold: float = None,
                                              filter_expression=None) -> SearchedDoc:

        initial_result = await self.similarity_search_by_vector(
            input_text=input_text,
            embedding=embedding,
            k=k,
            distance_threshold=distance_threshold,
            filter_expression=filter_expression
        )

        if not initial_result.retrieved_docs:
            logger.info("No documents found in the initial similarity search.")
            return SearchedDoc(retrieved_docs=[], user_prompt=input_text)

        all_sibling_docs = {}

        for doc in initial_result.retrieved_docs:
            start_index = doc.metadata.get("start_index", None)
            file_id = doc.metadata.get("file_id", None)
            sibling_id = doc.metadata.get("id", None)
            if start_index is None or file_id is None:
                logger.warning(
                    f"Document {sibling_id} missing 'start_index' or 'file_id' in metadata. "
                    f"Skipping sibling retrieval for {sibling_id}."
                )
                continue

            try:
                all_sibling_docs[sibling_id] = self._fetch_siblings(file_id, int(start_index))
            except Exception as e:
                logger.error(f"Error retrieving siblings from Milvus: {e}")
                continue

        logger.debug(f"Final sibling docs: {all_sibling_docs}")
        return SearchedDoc(
            retrieved_docs=initial_result.retrieved_docs,
            sibling_docs=all_sibling_docs,
            user_prompt=input_text
        )

    def _fetch_siblings(self, file_id: str, start_index: int) -> List[TextDoc]:
        """Milvus cannot order by a JSON path, so the nearest chunk is picked here."""
        siblings = []
        for expression, pick_last in (
            ('metadata["start_index"] < {}'.format(start_index), True),
            ('metadata["start_index"] > {}'.format(start_index), False),
        ):
            rows = self._post("/entities/query", self._scoped(
                filter='metadata["file_id"] == {} and {}'.format(_quote(file_id), expression),
                outputFields=["id"] + _OUTPUT_FIELDS,
            )) or []
            if not rows:
                continue
            rows.sort(key=lambda row: int((row.get("metadata") or {}).get("start_index", 0)))
            siblings.append(self._convert_to_text_doc(rows[-1] if pick_last else rows[0]))
        return siblings

    def get_object_name_filter_expression(self, bucket_name: str, object_name: str) -> MilvusFilter:
        if not bucket_name or not object_name:
            raise ValueError("bucket_name and object_name cannot be empty")
        return MilvusFilter(
            f'metadata["bucket_name"] == {_quote(bucket_name)} '
            f'and metadata["object_name"] == {_quote(object_name)}'
        )

    def get_bucket_name_filter_expression(self, bucket_names: List[str]) -> MilvusFilter:
        if not bucket_names:
            raise ValueError("bucket_names cannot be empty")
        values = ", ".join(_quote(name) for name in bucket_names)
        return MilvusFilter(f'metadata["bucket_name"] in [{values}]')

    def get_site_name_filter_expression(self, site_names: List[str]) -> MilvusFilter:
        if not site_names:
            raise ValueError("site_names cannot be empty")
        values = ", ".join(_quote(name) for name in site_names)
        return MilvusFilter(f'metadata["site_name"] in [{values}]')

    def get_files_filter_expression(self) -> MilvusFilter:
        """Links carry no file_id, so presence of the key distinguishes files from links."""
        return MilvusFilter('not exists metadata["link_id"]')

    def get_links_filter_expression(self) -> MilvusFilter:
        return MilvusFilter('not exists metadata["file_id"]')

    def get_author_filter_expression(self, authors: Union[str, List[str]]) -> MilvusFilter:
        author_list = [authors] if isinstance(authors, str) else list(authors or [])
        author_list = [a.strip() for a in author_list if a and a.strip()]
        if not author_list:
            raise ValueError("Author(s) cannot be empty")

        expression = MilvusFilter(f'metadata["author"] like {_quote("%" + author_list[0] + "%")}')
        for author in author_list[1:]:
            expression |= MilvusFilter(f'metadata["author"] like {_quote("%" + author + "%")}')
        return expression

    def get_text_exclude_filter_expression(self, field: str, value: str) -> MilvusFilter:
        if not field or not value or not value.strip():
            raise ValueError("Field and value cannot be empty")
        return ~MilvusFilter(f'metadata[{_quote(field)}] like {_quote("%" + value.strip() + "%")}')

    def get_author_exclude_filter_expression(self, author: str) -> MilvusFilter:
        return self.get_text_exclude_filter_expression("author", author)

    def get_title_filter_expression(self, title_pattern: str, exact_match: bool = False) -> MilvusFilter:
        if not title_pattern or not title_pattern.strip():
            raise ValueError("Title pattern cannot be empty")
        title_pattern = title_pattern.strip()

        if exact_match:
            title_filter = MilvusFilter(f'metadata["file_title"] == {_quote(title_pattern)}')
        else:
            title_filter = MilvusFilter(f'metadata["file_title"] like {_quote("%" + title_pattern + "%")}')

        if _looks_like_filename(title_pattern):
            return title_filter | MilvusFilter(
                f'metadata["object_name"] like {_quote("%" + title_pattern + "%")}'
            )
        return title_filter

    def get_date_range_filter_expression(self, field: str, start: int = None, end: int = None) -> MilvusFilter:
        if field not in _DATE_FIELDS:
            raise ValueError(f"Invalid date field: {field}")
        if start is None and end is None:
            raise ValueError("At least one of start or end timestamp required")

        clauses = []
        if start is not None:
            clauses.append(f'metadata[{_quote(field)}] >= {int(start)}')
        if end is not None:
            clauses.append(f'metadata[{_quote(field)}] <= {int(end)}')
        return MilvusFilter(" and ".join(clauses))
