# Copyright (C) 2024-2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

import os
import time
from asyncio import TimeoutError

from aiohttp.client_exceptions import ClientResponseError
from dotenv import load_dotenv
from fastapi import HTTPException
from langsmith import traceable
from requests.exceptions import HTTPError, RequestException, Timeout

from comps.cores.mega.base_statistics import register_statistics, statistics_dict
from comps.cores.mega.constants import MegaServiceEndpoint, ServiceType
from comps.cores.mega.logger import change_erag_logger_level, get_erag_logger
from comps.cores.mega.micro_service import erag_microservices, register_microservice
from comps.cores.mega.utils import get_access_token
from comps.cores.proto.docarray import PromptTemplateInput, SearchedDoc
from comps.cores.utils.utils import sanitize_env
from comps.reranks.utils.erag_reranking import EragReranker


# Define the unique service name for the microservice
USVC_NAME='erag_service@reranking'

# Load environment variables from .env file
load_dotenv(os.path.join(os.path.dirname(__file__), "impl/microservice/.env"))

# Initialize the logger for the microservice
logger = get_erag_logger(f"{__file__.split('comps/')[1].split('/', 1)[0]}_microservice")
change_erag_logger_level(logger, log_level=os.getenv("ERAG_LOGGER_LEVEL", "INFO"))

# Get the token
access_token = get_access_token(sanitize_env(os.getenv('RERANKING_VLLM_TOKEN_URL')), sanitize_env(os.getenv('RERANKING_VLLM_CLIENT_ID')), sanitize_env(os.getenv('RERANKING_VLLM_CLIENT_SECRET'))) if sanitize_env(os.getenv('RERANKING_VLLM_TOKEN_URL')) and sanitize_env(os.getenv('RERANKING_VLLM_CLIENT_ID')) and sanitize_env(os.getenv('RERANKING_VLLM_CLIENT_SECRET')) else None
# If token is passed directly override it
if os.getenv('RERANKING_VLLM_API_KEY'):
    access_token = sanitize_env(os.getenv('RERANKING_VLLM_API_KEY'))

headers = {}
if access_token:
    headers = {"Authorization": f"Bearer {access_token}"}

# Initialize an instance of the EragReranker class with environment variables.
erag_reranker = EragReranker(
    service_endpoint=sanitize_env(os.getenv('RERANKING_SERVICE_ENDPOINT')),
    model_server=sanitize_env(os.getenv('RERANKING_MODEL_SERVER')),
    model_name=sanitize_env(os.getenv('RERANKING_MODEL_NAME')),
    headers=headers
)

# Register the microservice with the specified configuration.
@register_microservice(
    name=USVC_NAME,
    service_type=ServiceType.RERANK,
    endpoint=str(MegaServiceEndpoint.RERANKING),
    host='0.0.0.0',
    port=int(os.getenv('RERANKING_USVC_PORT', default=8000)),
    input_datatype=SearchedDoc,
    output_datatype=PromptTemplateInput,
)
@traceable(run_type="llm")
@register_statistics(names=[USVC_NAME])
# Define a function to handle processing of input for the microservice.
# Its input and output data types must comply with the registered ones above.
async def process(input: SearchedDoc) -> PromptTemplateInput:
    """
    Asynchronously process the input document using the EragReranker..

    Args:
        input (SearchedDoc): The input document to be processed.

    Returns:
        PromptTemplateInput: The result of the processing containing reranked top_n documents

    Raises:
        HTTPException: If a ValueError or any other exception occurs during processing.

    """

    start = time.time()
    try:
        # Pass the input to the 'run' method of the microservice instance
        res = await erag_reranker.run(input)
    except ValueError as e:
        error_message = f"A ValueError occurred while processing: {str(e)}"
        logger.exception(error_message)
        raise HTTPException(status_code=400, detail=error_message)
    except TimeoutError as e:
        error_message = f"A Timeout error occurred while processing: {str(e)}"
        logger.exception(error_message)
        raise HTTPException(status_code=408, detail=error_message)
    except Timeout as e:
        error_message = f"A Timeout error occurred while processing: {str(e)}"
        logger.exception(error_message)
        raise HTTPException(status_code=408, detail=error_message)
    except HTTPError as e:
        err_message = e.response.json()['error']
        if hasattr(e.response, "status_code") and e.response.status_code == 413:
            raise HTTPException(status_code=413, detail=f"Too many documents provided into reranker. Adjust 'k' parameter in retriever or consider changing reranking model. Error: {err_message}")
        elif hasattr(e.response, "status_code"):
            raise HTTPException(status_code=e.response.status_code, detail=err_message)
        else:
            raise HTTPException(status_code=500, detail=err_message)
    except ClientResponseError as e:
        if hasattr(e, "status") and e.status == 413:
            raise HTTPException(status_code=413, detail=f"Too many documents provided into reranker. Adjust 'k' parameter in retriever or consider changing reranking model. Error: {e.message}")
        elif hasattr(e, "status"):
            raise HTTPException(status_code=e.status, detail=e.message)
        else:
            raise HTTPException(status_code=500, detail=e.message)
    except RequestException as e:
        error_code = e.response.status_code if e.response else 503
        error_message = f"A RequestException occurred while processing: {str(e)}"
        logger.exception(error_message)
        raise HTTPException(status_code=error_code, detail=error_message)

    except Exception as e:
         logger.exception(f"An error occurred while processing: {str(e)}")
         raise HTTPException(status_code=500,
                             detail=f"An error occurred while processing: {str(e)}"
    )
    statistics_dict[USVC_NAME].append_latency(time.time() - start, None)
    res.history_id = input.history_id
    return res


if __name__ == "__main__":
    # Start the microservice
    erag_microservices[USVC_NAME].start()
    logger.info(f"Started ERAG Microservice: {USVC_NAME}")
