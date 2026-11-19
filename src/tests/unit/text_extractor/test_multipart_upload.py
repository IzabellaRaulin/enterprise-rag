# Copyright (C) 2024-2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

"""Tests for the multipart upload endpoint on text_extractor microservice.

The @register_microservice decorator calls run_until_complete() at import time,
which conflicts with pytest-asyncio's event loop. We therefore import the module
at the top level (outside async context) and use patch.object on it.
"""

import io
import os
import sys
import pytest
from unittest.mock import patch, AsyncMock, MagicMock

from comps.cores.proto.docarray import TextDoc, TextExtractorInput, TextSplitterInput

# The microservice uses `from utils.erag_text_extractor import ...` which requires
# comps/text_extractor/ on sys.path.
_te_dir = os.path.join(os.path.dirname(__file__), "..", "..", "..", "comps", "text_extractor")
_te_dir = os.path.normpath(_te_dir)
if _te_dir not in sys.path:
    sys.path.insert(0, _te_dir)

# Import at module level (synchronous context) so @register_microservice succeeds.
import comps.text_extractor.erag_text_extractor_microservice as te_mod  # noqa: E402


@pytest.fixture
def sample_pdf_bytes():
    """Minimal valid-ish PDF bytes for testing (not a real PDF, but enough for decoding tests)."""
    return b"%PDF-1.4 fake content for testing purposes " + b"\x00" * 100


@pytest.fixture
def sample_txt_bytes():
    return b"This is a plain text document for testing."


class TestMultipartEndpoint:
    """Verify the multipart endpoint reads file bytes and delegates to shared processing logic."""

    @pytest.mark.asyncio
    async def test_multipart_endpoint_reads_and_calls_shared_logic(self, sample_txt_bytes):
        """The multipart endpoint should read file bytes and call _process_files."""
        from fastapi import UploadFile

        with patch.object(te_mod, "_process_files", new_callable=AsyncMock) as mock_process:
            mock_process.return_value = TextSplitterInput(loaded_docs=[TextDoc(text="extracted")])

            upload = UploadFile(filename="test.txt", file=io.BytesIO(sample_txt_bytes))
            await te_mod.process_multipart(files=[upload], links=[], texts=[])

            mock_process.assert_called_once()
            call_args = mock_process.call_args
            decoded_files = call_args[0][0]
            assert len(decoded_files) == 1
            assert decoded_files[0][0] == "test.txt"
            assert decoded_files[0][1] == sample_txt_bytes

    @pytest.mark.asyncio
    async def test_multipart_rejects_empty_file(self):
        """Multipart endpoint should reject files with no content."""
        from fastapi import UploadFile, HTTPException

        with patch.object(te_mod, "_process_files", new_callable=AsyncMock):
            upload = UploadFile(filename="empty.txt", file=io.BytesIO(b""))

            with pytest.raises(HTTPException) as exc_info:
                await te_mod.process_multipart(files=[upload], links=[], texts=[])
            assert exc_info.value.status_code == 400

    @pytest.mark.asyncio
    async def test_multipart_rejects_invalid_filename(self, sample_txt_bytes):
        """Multipart endpoint should reject files with invalid filenames."""
        from fastapi import UploadFile, HTTPException

        with patch.object(te_mod, "_process_files", new_callable=AsyncMock):
            upload = UploadFile(filename="../../../etc/passwd", file=io.BytesIO(sample_txt_bytes))

            with pytest.raises(HTTPException) as exc_info:
                await te_mod.process_multipart(files=[upload], links=[], texts=[])
            assert exc_info.value.status_code == 400

    @pytest.mark.asyncio
    async def test_multipart_rejects_empty_request(self):
        """Multipart endpoint should reject a request with no files, links or texts."""
        from fastapi import HTTPException

        with pytest.raises(HTTPException) as exc_info:
            await te_mod.process_multipart(files=[], links=[], texts=[])
        assert exc_info.value.status_code == 400

    @pytest.mark.asyncio
    async def test_multipart_combines_files_and_texts(self, sample_txt_bytes):
        """A DocSum upload may carry texts alongside the files; both are extracted."""
        from fastapi import UploadFile

        with patch.object(te_mod, "_process_files", new_callable=AsyncMock) as mock_files, \
                patch.object(te_mod, "_process_links_and_texts", new_callable=AsyncMock) as mock_links:
            mock_files.return_value = TextSplitterInput(loaded_docs=[TextDoc(text="from file")])
            mock_links.return_value = TextSplitterInput(loaded_docs=[TextDoc(text="from text")])

            upload = UploadFile(filename="test.txt", file=io.BytesIO(sample_txt_bytes))
            result = await te_mod.process_multipart(files=[upload], links=[], texts=["pasted"])

            mock_links.assert_called_once_with(link_list=[], texts=["pasted"])
            assert [doc.text for doc in result.loaded_docs] == ["from file", "from text"]


class TestProcessLinksEndpoint:
    """Verify the JSON process() endpoint delegates link/text requests correctly."""

    @pytest.mark.asyncio
    async def test_process_delegates_links(self):
        """process() should pass links to _process_links_and_texts."""
        with patch.object(te_mod, "_process_links_and_texts", new_callable=AsyncMock) as mock:
            mock.return_value = TextSplitterInput(loaded_docs=[TextDoc(text="x")])
            await te_mod.process(TextExtractorInput(links=["http://example.com"]))
            mock.assert_called_once_with(link_list=["http://example.com"], texts=[])

    @pytest.mark.asyncio
    async def test_process_rejects_empty_input(self):
        """process() should reject requests with no links and no texts."""
        from fastapi import HTTPException

        with patch.object(te_mod, "_process_links_and_texts", new_callable=AsyncMock):
            with pytest.raises(HTTPException) as exc_info:
                await te_mod.process(TextExtractorInput(links=[], texts=[]))
            assert exc_info.value.status_code == 400


class TestErrors:
    """Errors become HTTP errors: an unhandled one makes uvicorn reset the connection."""

    @pytest.mark.asyncio
    async def test_unexpected_error_is_a_500(self, sample_txt_bytes):
        from fastapi import HTTPException, UploadFile

        with patch.object(te_mod, "_process_files", new_callable=AsyncMock, side_effect=OSError("disk full")):
            with pytest.raises(HTTPException) as exc_info:
                await te_mod.process_multipart(
                    files=[UploadFile(filename="test.txt", file=io.BytesIO(sample_txt_bytes))], links=[], texts=[])
            assert exc_info.value.status_code == 500

    @pytest.mark.asyncio
    async def test_unexpected_error_in_process_is_a_500(self):
        from fastapi import HTTPException

        with patch.object(te_mod, "_process_links_and_texts", new_callable=AsyncMock, side_effect=OSError("x")):
            with pytest.raises(HTTPException) as exc_info:
                await te_mod.process(TextExtractorInput(texts=["t"]))
            assert exc_info.value.status_code == 500

    @pytest.mark.asyncio
    async def test_unreadable_pdf_is_a_400_and_leaves_no_files(self, tmp_path, monkeypatch):
        import pymupdf
        from fastapi import HTTPException

        monkeypatch.setenv("UPLOAD_PATH", str(tmp_path))
        with pymupdf.open() as good:
            good.new_page().insert_text((72, 300), "good")
            good_bytes = good.tobytes()
        with patch.object(te_mod, "_process_pdf_windowed", new_callable=AsyncMock):
            with pytest.raises(HTTPException) as exc_info:
                await te_mod._process_files([("good.pdf", good_bytes), ("bad.pdf", b"%PDF-1.4 not really")])
        assert exc_info.value.status_code == 400 and "bad.pdf" in exc_info.value.detail
        assert list(tmp_path.iterdir()) == []

    @pytest.mark.asyncio
    async def test_any_error_in_the_pdf_loop_cancels_the_queued_work_and_leaves_no_files(self, tmp_path, monkeypatch):
        import pymupdf

        monkeypatch.setenv("UPLOAD_PATH", str(tmp_path))
        with pymupdf.open() as good:
            good.new_page().insert_text((72, 300), "good")
            good_bytes = good.tobytes()
        queued = []

        def _queue(coro):
            coro.close()
            task = MagicMock()
            task.done.return_value = False
            queued.append(task)
            return task

        with patch.object(te_mod, "_process_pdf_windowed", new_callable=AsyncMock), \
                patch.object(te_mod.asyncio, "ensure_future", side_effect=_queue), \
                patch.object(te_mod.os, "chmod", side_effect=[None, OSError("No space left on device")]):
            with pytest.raises(OSError):
                await te_mod._process_files([("a.pdf", good_bytes), ("b.pdf", good_bytes)])
        assert len(queued) == 1 and queued[0].cancel.called
        assert list(tmp_path.iterdir()) == []

    @pytest.mark.asyncio
    async def test_a_pdf_failing_in_gather_cancels_the_other_pdfs_and_leaves_no_files(self, tmp_path, monkeypatch):
        import asyncio
        import pymupdf
        from fastapi import HTTPException

        monkeypatch.setenv("UPLOAD_PATH", str(tmp_path))
        with pymupdf.open() as good:
            good.new_page().insert_text((72, 300), "good")
            good_bytes = good.tobytes()

        async def _windowed(pdf_path, filename, page_count, metadata):
            if filename == "a.pdf":
                raise ValueError("a.pdf page 1 failed")
            await asyncio.Event().wait()

        tasks = []
        ensure_future = asyncio.ensure_future

        def _track(coro):
            task = ensure_future(coro)
            tasks.append(task)
            return task

        with patch.object(te_mod, "_process_pdf_windowed", _windowed), \
                patch.object(te_mod.asyncio, "ensure_future", side_effect=_track):
            with pytest.raises(HTTPException) as exc_info:
                await te_mod._process_files([("a.pdf", good_bytes), ("b.pdf", good_bytes)])
            await asyncio.sleep(0)
        assert exc_info.value.status_code == 400
        assert len(tasks) == 2 and tasks[1].cancelled()
        assert list(tmp_path.iterdir()) == []

    @pytest.mark.asyncio
    async def test_a_cancelled_pdf_keeps_its_file_until_the_running_pages_are_done(self, tmp_path, monkeypatch):
        import asyncio

        pdf_path = tmp_path / "doc.pdf"
        pdf_path.write_bytes(b"%PDF-1.4")
        pages = []

        def _page(func, *args):
            pages.append(asyncio.get_running_loop().create_future())
            return pages[-1]

        monkeypatch.setattr(te_mod, "_max_workers", 2)
        with patch.object(te_mod, "_submit", side_effect=_page):
            task = asyncio.ensure_future(
                te_mod._process_pdf_windowed(str(pdf_path), "doc.pdf", 10, {"_pdf_path": str(pdf_path)}))
            await asyncio.sleep(0)
            task.cancel()
            with pytest.raises(asyncio.CancelledError):
                await task
        assert len(pages) == 2 and pdf_path.exists()
        pages[0].set_result({"success": True, "text": "one"})
        await asyncio.sleep(0)
        assert pdf_path.exists()
        pages[1].set_result({"success": True, "text": "two"})
        await asyncio.sleep(0)
        assert not pdf_path.exists() and te_mod._pdf_readers == {}


def _die():
    os._exit(1)


class TestBrokenPool:
    @pytest.mark.asyncio
    async def test_a_dead_worker_gets_a_new_pool(self, monkeypatch):
        from concurrent.futures.process import BrokenProcessPool
        from comps.cores.utils.process_pool import new_process_pool

        monkeypatch.setattr(te_mod, "pool", new_process_pool(1))
        monkeypatch.setattr(te_mod, "_inflight", 0)
        monkeypatch.setattr(te_mod, "_completed_since_recycle", 0)
        broken = te_mod.pool
        try:
            with pytest.raises(BrokenProcessPool):
                await te_mod._submit(_die)
            assert te_mod.pool is not broken
            assert await te_mod._submit(os.getpid) != os.getpid()
        finally:
            te_mod.pool.shutdown()

    @pytest.mark.asyncio
    async def test_a_pool_broken_while_idle_is_replaced_on_submit(self, monkeypatch):
        import signal
        import time
        from comps.cores.utils.process_pool import new_process_pool

        monkeypatch.setattr(te_mod, "pool", new_process_pool(1))
        monkeypatch.setattr(te_mod, "_inflight", 0)
        monkeypatch.setattr(te_mod, "_completed_since_recycle", 0)
        broken = te_mod.pool
        try:
            # A worker killed with nothing in flight: no task's callback replaces the pool,
            # so the next submit finds it broken.
            os.kill(broken.submit(os.getpid).result(timeout=30), signal.SIGKILL)
            deadline = time.monotonic() + 30
            while not broken._broken and time.monotonic() < deadline:
                time.sleep(0.05)
            assert broken._broken
            assert await te_mod._submit(os.getpid) != os.getpid()
            assert te_mod.pool is not broken
        finally:
            te_mod.pool.shutdown()
            broken.shutdown()
