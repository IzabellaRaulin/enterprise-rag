# Copyright (C) 2024-2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

"""A dead splitter worker fails only its request: the next one runs on a new pool."""

import os
import sys

import pytest
from fastapi import HTTPException

from comps.cores.proto.docarray import TextDoc, TextSplitterInput
from comps.cores.utils.process_pool import new_process_pool

# The microservice uses `from utils import erag_textsplitter`, which requires
# comps/text_splitter/ on sys.path.
_ts_dir = os.path.normpath(os.path.join(os.path.dirname(__file__), "..", "..", "..", "comps", "text_splitter"))
if _ts_dir not in sys.path:
    sys.path.insert(0, _ts_dir)

# Import at module level (synchronous context) so @register_microservice succeeds.
import comps.text_splitter.erag_text_splitter_microservice as ts_mod  # noqa: E402


def _die(*args):
    os._exit(1)


def _echo(loaded_docs, *args):
    return loaded_docs


def _request():
    return TextSplitterInput(loaded_docs=[TextDoc(text="t", metadata={})], chunk_size=100, chunk_overlap=10,
                             use_semantic_chunking=False)


@pytest.mark.asyncio
async def test_a_dead_worker_is_a_500_and_the_next_request_gets_a_new_pool(monkeypatch):
    monkeypatch.setattr(ts_mod, "pool", new_process_pool(1))
    broken = ts_mod.pool
    try:
        monkeypatch.setattr(ts_mod, "run_splitter", _die)
        with pytest.raises(HTTPException) as exc_info:
            await ts_mod.process(_request())
        assert exc_info.value.status_code == 500
        assert ts_mod.pool is not broken
        monkeypatch.setattr(ts_mod, "run_splitter", _echo)
        assert [d.text for d in (await ts_mod.process(_request())).docs] == ["t"]
    finally:
        ts_mod.pool.shutdown()


def test_a_request_that_saw_the_old_pool_keeps_the_new_one(monkeypatch):
    broken, current = new_process_pool(1), new_process_pool(1)
    monkeypatch.setattr(ts_mod, "pool", current)
    try:
        ts_mod._replace_broken_pool(broken)
        assert ts_mod.pool is current
    finally:
        broken.shutdown()
        current.shutdown()
