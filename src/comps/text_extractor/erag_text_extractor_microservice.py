# Copyright (C) 2024-2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

import asyncio
import io
import os
import time
import uuid
from concurrent.futures.process import BrokenProcessPool
from pathlib import Path

from dotenv import load_dotenv
from fastapi import File, Form, HTTPException, UploadFile
from pathvalidate import is_valid_filename
import pymupdf
import requests
from requests.exceptions import ConnectionError, HTTPError, ProxyError
from urllib3.exceptions import MaxRetryError
from utils.erag_text_extractor import EragTextExtractor

from comps.cores.mega.base_statistics import register_statistics, statistics_dict
from comps.cores.mega.constants import MegaServiceEndpoint, ServiceType
from comps.cores.mega.logger import change_erag_logger_level, get_erag_logger
from comps.cores.mega.micro_service import erag_microservices, register_microservice
from comps.cores.proto.docarray import TextDoc, TextExtractorInput, TextSplitterInput
from comps.cores.utils.process_pool import new_process_pool, replace_if_broken
from comps.text_extractor.utils.file_loaders.load_pdf import LoadPdf, _process_single_page_from_file


# Define the unique service name for the microservice
USVC_NAME='erag_service@text_extractor'

# Load environment variables from .env file
load_dotenv(os.path.join(os.path.dirname(__file__), "impl/microservice/.env"))

# Initialize the logger for the microservice
logger = get_erag_logger(f"{__file__.split('comps/')[1].split('/', 1)[0]}_microservice")
change_erag_logger_level(logger, log_level=os.getenv("ERAG_LOGGER_LEVEL", "INFO"))

# Load ASR endpoint configuration
ASR_MODEL_SERVER_ENDPOINT = os.getenv('ASR_MODEL_SERVER_ENDPOINT')
def validate_asr_endpoint(asr_endpoint):
    """Validate that the ASR endpoint is responsive."""
    if not asr_endpoint or asr_endpoint.strip() == "":
        logger.info("ASR_MODEL_SERVER_ENDPOINT is not configured. Audio file support will be disabled.")
        return

    try:
        url = f"{asr_endpoint.rstrip('/')}/v1/health_check"
        response = requests.get(url, timeout=10)
        response.raise_for_status()
        logger.info(f"ASR endpoint validation successful: {asr_endpoint}")
    except Exception as e:
        logger.error(f"Unexpected error validating ASR endpoint: {asr_endpoint}. Error: {str(e)}")
        raise

# These functions are top-level (picklable) so Python's multiprocessing can submit them
# to the shared ProcessPoolExecutor. Each handles one unit of work:
#   - run_single_file: one non-PDF file
#   - run_single_link: one URL
# PDF pages are submitted via _process_single_page_from_file (imported from load_pdf).
# This design gives the shared pool all work at page granularity — no nested pools.
def run_single_file(filename: str, file_bytes: bytes, asr_endpoint):
    """Process a single non-PDF file in a pool worker. Accepts raw bytes to stay picklable."""
    binary_file = io.BytesIO(file_bytes)
    upload_file = UploadFile(filename=filename, file=binary_file)
    text_extractor = EragTextExtractor(asr_endpoint=asr_endpoint)
    return text_extractor._load_files([upload_file])


def run_single_link(link: str, asr_endpoint):
    """Process a single link in a pool worker."""
    text_extractor = EragTextExtractor(asr_endpoint=asr_endpoint)
    return text_extractor._load_links([link])


# Process Pool Executor was introduced due to API being unresponsive while computing non-io tasks.
# Originally, async functions are run in event loop which means that they block the server thread
# thus making it unresponsive for other requests - mainly health_check - which then might cause
# some issues on k8s deployments, showing the pod as not ready by failing the liveness probe.
# While moving it to sync function fixed that issue this meant that it would run in a separate
# thread but was instead slower due to not using async io calls. The resolution is to run it
# as an async function for io improvement, but spawn a separate process for heavy CPU usage calls.
# https://github.com/fastapi/fastapi/issues/3725#issuecomment-902629033
# https://luis-sena.medium.com/how-to-optimize-fastapi-for-ml-model-serving-6f75fb9e040d

# TEXT_EXTRACTOR_MAX_WORKERS is injected by the Kubernetes Downward API (limits.cpu)
# in the pod spec, so this always reflects the actual pod CPU limit.
_max_workers = int(os.getenv('TEXT_EXTRACTOR_MAX_WORKERS', 4))
# Recycle the whole (fork) pool once it has processed this many tasks and goes idle, so the
# OS reclaims memory held by long-lived workers (loaded docling/RapidOCR/torch models).
# Per-worker recycling (max_tasks_per_child) needs spawn/forkserver, which crash in this
# container with a SemLock rebuild error — recycling the fork pool when idle is the safe
# equivalent. 0 disables.
_recycle_after = int(os.getenv('TEXT_EXTRACTOR_MAX_TASKS_PER_CHILD', 100))
pool = new_process_pool(_max_workers)
logger.info(
    f"ProcessPoolExecutor initialized with max_workers={_max_workers}, "
    f"recycle_after={_recycle_after or 'disabled'} tasks "
    f"(set TEXT_EXTRACTOR_MAX_WORKERS / TEXT_EXTRACTOR_MAX_TASKS_PER_CHILD to override)"
)

# Pool-recycling accounting. Safe as plain ints: all updates happen on the single asyncio loop.
_inflight = 0
_completed_since_recycle = 0


def _recycle_pool_if_idle():
    """Replace the pool once it has processed _recycle_after tasks and is idle. Only swaps when
    nothing is in flight, so no running future references the pool being shut down."""
    global pool, _completed_since_recycle
    if _recycle_after <= 0 or _inflight != 0 or _completed_since_recycle < _recycle_after:
        return
    old = pool
    pool = new_process_pool(_max_workers)
    _completed_since_recycle = 0
    old.shutdown(wait=False)
    logger.info("Recycled process pool to release worker memory")


def _replace_broken_pool(broken):
    """A worker that died (OOM kill, native crash) breaks the whole pool: every later task
    fails until the pod restarts, and the health check does not notice. Start a new pool."""
    global pool, _completed_since_recycle
    replacement = replace_if_broken(pool, broken, _max_workers)
    if replacement is not pool:
        pool = replacement
        _completed_since_recycle = 0
        logger.warning("Process pool broken (a worker died); started a new one")


def _submit(func, *args):
    """Submit a task to the pool, tracking in-flight/completed counts to drive recycling."""
    global _inflight
    loop = asyncio.get_event_loop()
    for retry in (False, True):
        current = pool
        try:
            future = loop.run_in_executor(current, func, *args)
            break
        except BrokenProcessPool:
            if retry:
                raise
            _replace_broken_pool(current)
    _inflight += 1
    task = asyncio.ensure_future(future)

    def _on_done(done):
        global _inflight, _completed_since_recycle
        _inflight -= 1
        _completed_since_recycle += 1
        if not done.cancelled() and isinstance(done.exception(), BrokenProcessPool):
            _replace_broken_pool(current)
        _recycle_pool_if_idle()

    task.add_done_callback(_on_done)
    return task


# Temporary PDFs and the page tasks still reading them. A request that fails cannot stop the
# pages its workers are running, so their file is removed once the last of them is done.
_pdf_readers: dict[str, int] = {}
_pdf_remove_when_read: set[str] = set()


def _hold_pdf(pdf_path: str, task) -> None:
    """Keep pdf_path until the page task is done."""
    _pdf_readers[pdf_path] = _pdf_readers.get(pdf_path, 0) + 1

    def _release(_):
        _pdf_readers[pdf_path] -= 1
        if not _pdf_readers[pdf_path]:
            del _pdf_readers[pdf_path]
            if pdf_path in _pdf_remove_when_read:
                _pdf_remove_when_read.discard(pdf_path)
                _remove_pdf(pdf_path)

    task.add_done_callback(_release)


def _remove_pdf(pdf_path: str) -> None:
    """Remove a temporary PDF now, or once the page tasks still reading it are done."""
    if pdf_path in _pdf_readers:
        _pdf_remove_when_read.add(pdf_path)
    elif os.path.exists(pdf_path):
        os.remove(pdf_path)
        logger.info(f"Removed temporary PDF {pdf_path}")


async def _process_pdf_windowed(pdf_path: str, filename: str, page_count: int, metadata: dict) -> TextDoc:
    """
    Process all pages of a PDF using a sliding window of at most _max_workers
    concurrent pool tasks.

    Why windowed instead of submitting all pages upfront:
    - ProcessPoolExecutor has a FIFO queue. Submitting all N pages at once fills
      the queue, starving later-arriving requests until this file is done.
    - By keeping only _max_workers tasks in-flight at once and awaiting
      FIRST_COMPLETED before submitting the next page, we yield to the asyncio
      event loop after each completion. This lets other concurrent requests'
      _process_pdf_windowed coroutines also submit their next pages.
    - Result: pages from all concurrent requests interleave naturally in the pool.
    """
    results: list = [None] * page_count
    inflight: dict = {}  # task -> page_num
    next_page = 0
    completed = 0
    start_time = time.time()

    try:
        while next_page < page_count or inflight:
            # Fill window up to _max_workers in-flight tasks
            while next_page < page_count and len(inflight) < _max_workers:
                page_num = next_page
                task = _submit(_process_single_page_from_file, pdf_path, page_num)
                _hold_pdf(pdf_path, task)
                inflight[task] = page_num
                next_page += 1

            # Wait for at least one to finish, then yield so other requests can submit
            done, _ = await asyncio.wait(set(inflight.keys()), return_when=asyncio.FIRST_COMPLETED)
            for t in done:
                pnum = inflight.pop(t)
                results[pnum] = t.result()
                completed += 1
                logger.info(
                    f"[{filename}] Page {completed}/{page_count} processed | "
                    f"elapsed {time.time() - start_time:.1f}s"
                )
            # Yield to event loop — lets other requests' coroutines submit their next pages
            await asyncio.sleep(0)

        logger.info(f"[{filename}] All {page_count} pages done in {time.time() - start_time:.1f}s")

        pages_text = []
        for i, page_result in enumerate(results):
            if not page_result or not page_result.get('success'):
                raise Exception(
                    f"{filename} page {i+1} failed: "
                    f"{page_result.get('error', 'unknown') if page_result else 'no result'}"
                )
            pages_text.append(page_result['text'])

        clean_metadata = {k: v for k, v in metadata.items() if k != '_pdf_path'}
        return TextDoc(text=" ".join(pages_text), metadata=clean_metadata)

    finally:
        pdf_path_to_remove = metadata.get('_pdf_path')
        if pdf_path_to_remove:
            _remove_pdf(pdf_path_to_remove)

# Register the multipart file-upload endpoint. Used by EDP for file ingestion and
# by the DocSum pipeline, whose uploads the GMC router forwards here (it sends a
# multipart entry request to a step's multipartEndpoint and JSON to its endpoint).
# Note: input_datatype is informational only — FastAPI derives the actual multipart
# schema from the handler signature.
@register_microservice(
    name=USVC_NAME,
    service_type=ServiceType.TEXT_EXTRACTOR,
    endpoint=str(MegaServiceEndpoint.TEXT_EXTRACTOR) + "/upload",
    host="0.0.0.0",
    port=int(os.getenv('TEXT_EXTRACTOR_USVC_PORT', default=9398)),
    input_datatype=list[UploadFile],
    output_datatype=TextSplitterInput,
)
@register_statistics(names=[USVC_NAME])
async def process_multipart(
    files: list[UploadFile] = File(default=[]),
    links: list[str] = Form(default=[]),
    texts: list[str] = Form(default=[]),
) -> TextSplitterInput:
    start = time.time()

    decoded_files: list[tuple[str, bytes]] = []
    try:
        for fidx, f in enumerate(files):
            if not f.filename:
                raise ValueError(f"File #{fidx} filename was empty.")
            if not is_valid_filename(f.filename):
                raise ValueError(f"File {f.filename} has an invalid filename.")
            file_data = await f.read()
            if not file_data:
                raise ValueError(f"File {f.filename} was empty.")
            decoded_files.append((f.filename, file_data))
    except ValueError as e:
        logger.error(e)
        raise HTTPException(status_code=400, detail=str(e))

    if not decoded_files and not links and not texts:
        raise HTTPException(status_code=400, detail="No files, links or texts provided.")

    async def load() -> list[TextDoc]:
        loaded_docs: list[TextDoc] = []
        if decoded_files:
            loaded_docs.extend((await _process_files(decoded_files)).loaded_docs)
        if links or texts:
            loaded_docs.extend((await _process_links_and_texts(link_list=links, texts=texts)).loaded_docs)
        return loaded_docs

    result = TextSplitterInput(loaded_docs=await _http_errors_only(load()))
    statistics_dict[USVC_NAME].append_latency(time.time() - start, None)
    return result


# Register the JSON endpoint for link/text extraction (used by EDP for link
# ingestion and by DocSum for pasted text).
@register_microservice(
    name=USVC_NAME,
    service_type=ServiceType.TEXT_EXTRACTOR,
    endpoint=str(MegaServiceEndpoint.TEXT_EXTRACTOR),
    host="0.0.0.0",
    port=int(os.getenv('TEXT_EXTRACTOR_USVC_PORT', default=9398)),
    input_datatype=TextExtractorInput,
    output_datatype=TextSplitterInput,
)
@register_statistics(names=[USVC_NAME])
async def process(input: TextExtractorInput) -> TextSplitterInput:
    start = time.time()

    link_list = input.links
    texts = input.texts

    if not link_list and not texts:
        raise HTTPException(status_code=400, detail="No links or texts provided.")

    result = await _http_errors_only(_process_links_and_texts(link_list=link_list, texts=texts))
    statistics_dict[USVC_NAME].append_latency(time.time() - start, None)
    return result


async def _http_errors_only(coro):
    """Await coro, turning an unexpected error into a 500: unhandled, uvicorn resets the
    connection, and a keep-alive client (a sidecar) sends its next request into it."""
    try:
        return await coro
    except HTTPException:
        raise
    except Exception as e:
        logger.exception(e)
        raise HTTPException(status_code=500, detail=f"An error occurred while processing: {str(e)}")


async def _process_files(decoded_files: list[tuple[str, bytes]]) -> TextSplitterInput:
    upload_folder = os.getenv('UPLOAD_PATH', '/tmp/erag_upload')

    # Collect async tasks: one windowed coroutine per PDF, one future per non-PDF.
    all_tasks: list = []
    pdf_paths: list[str] = []

    # Non-PDF files — one pool task each
    for filename, file_bytes in decoded_files:
        if not filename.lower().endswith('.pdf'):
            all_tasks.append(_submit(run_single_file, filename, file_bytes, ASR_MODEL_SERVER_ENDPOINT))

    # PDFs — each file gets its own _process_pdf_windowed coroutine.
    # The windowed approach submits only _max_workers pages at a time and yields
    # to the event loop after each completion. This ensures concurrent requests
    # interleave their pages in the pool instead of one request monopolising all workers.
    loaded_docs: list[TextDoc] = []
    try:
        for filename, file_bytes in decoded_files:
            if not filename.lower().endswith('.pdf'):
                continue
            Path(upload_folder).mkdir(parents=True, exist_ok=True)
            pdf_path = os.path.join(upload_folder, f"{uuid.uuid4()}_{filename}")
            # Recorded before the write, so a failed write or chmod is cleaned up too.
            pdf_paths.append(pdf_path)
            with open(pdf_path, 'wb') as fout:
                fout.write(file_bytes)
            os.chmod(pdf_path, 0o600)

            try:
                with pymupdf.open(pdf_path) as doc:
                    page_count = doc.page_count
            except pymupdf.FileDataError as e:
                logger.error(f"{filename} is not a readable PDF: {e}")
                raise HTTPException(status_code=400, detail=f"File {filename} is not a readable PDF.") from e
            logger.info(f"{filename}: {page_count} pages — starting windowed processing ({_max_workers} workers)")

            # Extract document metadata (title, author, dates) using LoadPdf
            try:
                metadata = LoadPdf(pdf_path).extract_metadata()
                # extract_metadata() derives filename from the temp path which
                # carries a uuid prefix for uniqueness. here we override it with the original filename for clarity in the metadata
                metadata['filename'] = filename
            except Exception as e:
                logger.warning(f"PDF metadata extraction failed for {filename}: {e}. Using basic metadata.")
                metadata = {'filename': filename}
            metadata['_pdf_path'] = pdf_path
            all_tasks.append(asyncio.ensure_future(_process_pdf_windowed(pdf_path, filename, page_count, metadata)))

        if all_tasks:
            try:
                results = await asyncio.gather(*all_tasks)
            except HTTPError as e:
                logger.exception(e)
                raise HTTPException(status_code=400, detail=f"A HTTP Error occurred while processing files: {str(e)}")
            except (ConnectionError, ProxyError) as e:
                logger.exception(e)
                raise HTTPException(status_code=400, detail=f"Could not connect to remote server: {str(e)}")
            except ValueError as e:
                logger.exception(e)
                raise HTTPException(status_code=400, detail=f"A Value Error occurred while processing: {str(e)}")
            except MaxRetryError as e:
                logger.exception(e)
                raise HTTPException(status_code=503, detail=f"Could not connect to remote server: {str(e)}")

            for result in results:
                if isinstance(result, TextDoc):
                    loaded_docs.append(result)      # PDF windowed coroutine result
                elif isinstance(result, list):
                    loaded_docs.extend(result)      # non-PDF file result list
    finally:
        # On success every task is done and has removed its own file. On a failure, here or in
        # another task, drop this request's remaining work; a PDF task cancelled before it starts
        # does not reach its finally, so remove what is left of the files here.
        for task in all_tasks:
            if not task.done():
                task.cancel()
        for path in pdf_paths:
            _remove_pdf(path)

    if not loaded_docs:
        raise HTTPException(status_code=400, detail="No documents were extracted from the provided input.")

    return TextSplitterInput(loaded_docs=loaded_docs)


async def _process_links_and_texts(
    link_list: list[str] | None,
    texts: list[str] | None,
) -> TextSplitterInput:
    # Links — one pool task each
    all_tasks: list = []
    for link in (link_list or []):
        all_tasks.append(_submit(run_single_link, link, ASR_MODEL_SERVER_ENDPOINT))

    # Plain texts — no CPU work, handle inline
    loaded_docs: list[TextDoc] = []
    if texts:
        for text in texts:
            if text.strip() == "":
                logger.warning("Empty text found, skipping...")
                continue
            loaded_docs.append(TextDoc(text=text, metadata={'timestamp': time.time()}))

    if all_tasks:
        try:
            results = await asyncio.gather(*all_tasks)
        except HTTPError as e:
            logger.exception(e)
            raise HTTPException(status_code=400, detail=f"A HTTP Error occurred while processing links: {str(e)}")
        except (ConnectionError, ProxyError) as e:
            logger.exception(e)
            raise HTTPException(status_code=400, detail=f"Could not connect to remote server: {str(e)}")
        except ValueError as e:
            logger.exception(e)
            raise HTTPException(status_code=400, detail=f"A Value Error occurred while processing: {str(e)}")
        except MaxRetryError as e:
            logger.exception(e)
            raise HTTPException(status_code=503, detail=f"Could not connect to remote server: {str(e)}")

        for result in results:
            if isinstance(result, list):
                loaded_docs.extend(result)      # link result list

    if not loaded_docs:
        raise HTTPException(status_code=400, detail="No documents were extracted from the provided input.")

    return TextSplitterInput(loaded_docs=loaded_docs)


if __name__ == "__main__":
    validate_asr_endpoint(ASR_MODEL_SERVER_ENDPOINT)
    erag_microservices[USVC_NAME].start()
    logger.info(f"Started ERAG Microservice: {USVC_NAME}")
