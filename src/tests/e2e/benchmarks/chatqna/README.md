# Intel® Chatbot Toolkit E2E benchmark
### Deploy Intel® Chatbot Toolkit and Adjust the parameters
Before running the E2E benchmark, ensure that Intel® Chatbot Toolkit is deployed and configured with the recommended parameters as outlined in the [Performance tuning tips](../../../../../docs/operate/performance.md) guide.

### Test prerequisites
In order to be able to run the e2e performance benchmark you need to:
* install necessary python packages
```bash
pip3 install -r requirements.txt
```
* bash scripts assumes that Intel® Chatbot Toolkit is deployed under `solutions.ai` domain:
  * if you are using the domain different than `solutions.ai` one you can overwrite it with `ERAG_DOMAIN_NAME` environmental variable
  * this variable has the impact on all the bash helpers
  * for python based benchmark see the relevant section to specify your domain
* benchmark and the bash helpers rely only on https protocol and can be run from any machine with https connection to the RAG deployment, so you need to ensure that you have network (https) access to `solutions.ai` (or your own domain):
  * configure `/etc/hosts`, so `solutions.ai` domain and subdomains points to your RAG deployment IP, for example: `127.0.0.1 minio.solutions.ai grafana.solutions.ai s3.solutions.ai solutions.ai auth.solutions.ai`
  * export both `no_proxy` and `NO_PROXY` env variables, so they contain  `solutions.ai` and `.solutions.ai` domains
* export UI username and password of the account with administrative rights using `KEYCLOAK_ERAG_ADMIN_USERNAME` and `KEYCLOAK_ERAG_ADMIN_PASSWORD` env variables. You can read them directly from the `erag-credentials` secret in the `keycloak` namespace:
```bash
export KEYCLOAK_ERAG_ADMIN_USERNAME=$(kubectl get secret -n keycloak erag-credentials -o jsonpath='{.data.KEYCLOAK_ERAG_ADMIN_USERNAME}' | base64 -d)
export KEYCLOAK_ERAG_ADMIN_PASSWORD=$(kubectl get secret -n keycloak erag-credentials -o jsonpath='{.data.KEYCLOAK_ERAG_ADMIN_PASSWORD}' | base64 -d)
```
* export Realm username and password using `KEYCLOAK_REALM_ADMIN_USERNAME` and `KEYCLOAK_REALM_ADMIN_PASSWORD` env variables. You can read them directly from the `keycloak-admin-secret` in the `keycloak` namespace:
```bash
export KEYCLOAK_REALM_ADMIN_USERNAME=$(kubectl get secret -n keycloak keycloak-admin-secret -o jsonpath='{.data.username}' | base64 -d)
export KEYCLOAK_REALM_ADMIN_PASSWORD=$(kubectl get secret -n keycloak keycloak-admin-secret -o jsonpath='{.data.password}' | base64 -d)
```
* if you are using a gated huggingface LLM model:
  * export your HF token `export HF_TOKEN="my-huggingface-token"`
  * this is required for the python based benchmark, so it would be able to obtain proper tokenizer from huggingface

### Ingesting data into RAG
Before running the test, it is advised to ingest some data into the RAG deployment to ensure that vector database contains some real context data. To do so there is the script which will do that. It can take few hours to execute that part.

```bash
./prepare_1M_vectors.sh
```
Script will load around 55 000 vectors based on real documents which are context related with the queries executed by the test benchmark. Additionally, it will fill the database with the context of Simple English Wikipedia dump to achieve totally 1 000 000 of vectors. When testing using such a database, we can simulate the production size databases and test the impact of data retrieval in the RAG pipeline.

Also, there is a possibility to create smaller database (it will load a context of real documents anyway, so around 55 000 of vectors, but won't be loading the parts of the Wikipedia dumps when `$TARGET_VECTORS` value is reached), then you need to specify the desired number of vectors as the parameter to the script:
```bash
./prepare_1M_vectors.sh $TARGET_VECTORS
```
If you have some other vectors and you want to delete them before filling the database using above script, there is also the helper to do so. Be careful with calling this helper, because it will delete all the entries in your vector database. Since vectors removing process is asynchronous, it is suggested to wait a while after running the script before filling the database with the new vectors.
```bash
./prepare_cleanup_vectors.sh
```

### Test execution
To run the test, you need to generate a file with valid User Access Tokens. There is a script to do that. Those tokens are valid for 10800 seconds (after tokens expiration the RAG deployment is going to return 401 errors). You need to generate the number of token which is greater or equal to number of connections which you want to test, here is the example for 32 connections:
```bash
./generate_uat_to_file.sh /tmp/uat.txt 32
```
After that, you can run the benchmark. You need to specify the following parameters:
* input file with test questions `-f questions-pubmed.csv`
* length of the test `-d 30m`
* number of parallel connections(Concurrency levels) `-c 32`
* location of file with tokens `-b /tmp/uat.txt`
* the tokenizer model for benchmark, which would be the same as the deployed LLM model `-m meta-llama/Llama-3.1-8B-Instruct`
* (if you want to test with fixed number of input question tokens) specify the expected number of tokens `-x 512`
* (if you are running domain different than 'solutions.ai') specify the RAG url `-s "https://${ERAG_DOMAIN_NAME}/api/v1/chatqna"`
```bash
python3 benchmark.py -f questions-pubmed.csv -d 30m -c 32 -b /tmp/uat.txt -m meta-llama/Llama-3.1-8B-Instruct
```
When the test (or multiple tests for different parameters) are completed, detailed results for all the queries are saved into `bench_*.csv` files. You can parse them using the provided tool:
```bash
python3 parse.py .
```

### Regenerating questions from PubMed data
By default `questions-pubmed.csv` file contains 1024 randomly generated questions based on [pubmed23n0001](https://huggingface.co/datasets/MedRAG/pubmed/tree/main/chunk) data. If you want to generate different set of questions based on that dataset, there is a script to do so. More details are described in help of it.
```bash
python3 generate_pubmed_questions.py --help
```

### Helpers for configuring Intel® Chatbot Toolkit
Some of the RAG parameters have a significant impact on the performance, so in order to change them without logging into UI, there are some helpers to change those parameters using bash scripts:
* configuring `k` for retriever (number of documents retrieved from vector database)
```bash
./prepare_change_retriever.sh $K
```
* configuring `top_n` for reranker (number of output documents after reranking, which are appended into the prompt)
```bash
./prepare_change_reranker.sh $TOP_N
```
* configuring `max_new_tokens` for llm (number of output tokens in the RAG response)
```bash
./prepare_change_max_tokens.sh $MAX_NEW_TOKENS
```

### Automating a benchmark parameter sweep
Instead of running `benchmark.py` manually for every combination of model, concurrency, token length, `top_n` and `k`, you can use `run_benchmark_sweep.sh`, which wraps the helpers and `benchmark.py` in a single loop. It:
* verifies the prerequisites (required env vars, `no_proxy`, `/etc/hosts`, required tools, `model-manager`, `models-rag.yaml`)
* runs `prepare_1M_vectors.sh` and verifies the target vector count is met
* iterates over `SWEEP_MODELS`, switching the served LLM via `model-manager` before each model's sub-sweep. The full per-model switch is:
  1. `model-manager undeploy` the currently-served LLM (embed/rerank left in place)
  2. poll `model-manager cpu-collisions-check` until NRI balloons release the cores
  3. run `calculate_replicas.py` for the incoming LLM's `cpu`/`memory`, accounting for cores already held by embed/rerank
  4. `model-manager deploy <model> --cpu <adj> --memory <base> --replicas N --env OMP_NUM_THREADS=<adj> --wait`
  5. wait until **all N vLLM replicas** report `Ready=True` (`model-manager --wait` only gates on the first replica)
  6. `sync_rag_llm_model` - patch `cm/gmc-config[llm-usvc.yaml].LLM_MODEL_NAME` in the `system` namespace, restart `gmc-controller` and `llm-svc-deployment` so the RAG's LLM step targets the newly-served model instead of stale gateway routes
  7. `validate_rag_llm_config` - verify source-of-truth, GMC-derived ConfigMap, and running `llm-svc` pod env all agree on the new `LLM_MODEL_NAME`; a mismatch aborts before the benchmark starts
  * **A failed switch aborts the whole run**
* iterates over the remaining sweep arrays, calling `prepare_change_reranker.sh`, `prepare_change_retriever.sh` and `prepare_change_max_tokens.sh` between runs
* regenerates user access tokens before every run (tokens expire after ~3h)
* writes each run's raw `bench_*.csv` into `results_<timestamp>_<model>/run<N>_users<U>_in<I>_out<O>_topn<T>_k<K>_<qfile>/`, a `sweep_summary.txt`, and an aggregated `parsed_results.csv` per model directory

Run it:
```bash
./run_benchmark_sweep.sh                # execute the sweep with hard-coded defaults
./run_benchmark_sweep.sh --dry-run      # print what would run, without executing
./run_benchmark_sweep.sh --skip-vectors # skip prepare_1M_vectors.sh and the post-ingestion vector count check

# override the sweep dimensions inline - see "Customizing the sweep" below
SWEEP_MODELS="qwen3-0-6b,llama3-8b-awq" \
  SWEEP_USERS="1,4,8,16" \
  BENCHMARK_DURATION=2m \
  ./run_benchmark_sweep.sh
```

Required env vars (must be exported before running): `KEYCLOAK_ERAG_ADMIN_USERNAME`, `KEYCLOAK_ERAG_ADMIN_PASSWORD`, `KEYCLOAK_REALM_ADMIN_USERNAME`, `KEYCLOAK_REALM_ADMIN_PASSWORD`. See the [Test prerequisites](#test-prerequisites) section above for the exact `kubectl` commands that read them out of the `keycloak` namespace secrets.

Optional env vars (defaults shown):
* `ERAG_DOMAIN_NAME` – RAG deployment domain (default: `solutions.ai`)
* `HF_TOKEN` – HuggingFace token, required for gated models
* `BENCHMARK_DURATION` – duration per run (default: `10m`)
* `UAT_FILE` – path for the generated user access tokens (default: `/tmp/uat.txt`)
* `TARGET_VECTORS` – minimum vectors that must be in the DB before starting (default: `1000000`)
* `RESULTS_DIR` – output root; multi-model runs still get a `<model>/` subdirectory (default: `./results_<ts>_<model>/`)
* `ERAG_ENV_NAME` – env folder under the repo's top-level `env/` directory; also selects the default `MODELS_YAML` (default: `local`)
* `MODELS_YAML` – path to the model catalog (default: `env/<ERAG_ENV_NAME>/models-rag.yaml`, i.e. `env/local/models-rag.yaml`)
* `MODEL_SWITCH_TIMEOUT` – seconds to wait for the model to become Ready after deploy, also caps the "all replicas Ready" wait (default: `1800`)

The tokenizer passed to `benchmark.py` (`-m`) is resolved automatically from each model's `model_id` in `models.yaml`, so you do not need to set `HF_MODEL`.

Customizing the sweep - the four most-adjusted dimensions (`SWEEP_MODELS`, `SWEEP_USERS`, `SWEEP_INPUT_TOKENS`, `SWEEP_OUTPUT_TOKENS`) accept a comma-separated env-var override; the rest live as arrays near the top of the script. Defaults (when the env var is unset) are noted below:
* `SWEEP_MODELS` – catalog names from `models.yaml` to run through in order, e.g. `SWEEP_MODELS="llama3-8b-awq,qwen3-0-6b"` (default: `llama3-8b-awq`). Each is undeployed before the next is deployed. Every model runs through the same user/token/reranker/retriever sweep. Failure to switch a model aborts the whole run
* `SWEEP_USERS` – concurrency levels (number of parallel clients), e.g. `SWEEP_USERS="1,4,8"` (default: `1,2,4,8,16,32,64,128`)
* `SWEEP_INPUT_TOKENS` and `SWEEP_OUTPUT_TOKENS` – **paired by index**: element `i` of `SWEEP_INPUT_TOKENS` maps to element `i` of `SWEEP_OUTPUT_TOKENS` (defaults: `128,256,256,256` / `128,256,512,1024` - i.e. `128/128, 256/256, 256/512, 256/1024`). Set both to comma-separated lists of equal length; a length mismatch is caught during prerequisite checks and aborts the run. Input tokens are capped at 256 because the default embedding model (`BAAI/bge-base-en-v1.5`) has a 512-token architectural limit
* `SWEEP_TOP_N` – reranker `top_n` values (edited in-script; default: `(1)`)
* `SWEEP_K` – retriever `k` values (edited in-script; default: `(5)`)
* `SWEEP_Q_FILES` – base names of question CSVs to iterate over (edited in-script; default: `("questions-pubmed")`)

With no env vars set, the default sweep is: **1 model (`llama3-8b-awq`) × 8 concurrency levels × 4 token pairs × 10m per run** - 32 benchmark runs. Trim aggressively for smoke tests, or add more models via `SWEEP_MODELS="…,…"`:

```bash
# Smoke test: qwen only, 2 client levels, 1 token pair, 1 min per run
SWEEP_MODELS="qwen3-0-6b" \
  SWEEP_USERS="1,4" \
  SWEEP_INPUT_TOKENS="128" \
  SWEEP_OUTPUT_TOKENS="128" \
  BENCHMARK_DURATION=1m \
  ./run_benchmark_sweep.sh

# Reduced token grid, faster runs, both models
SWEEP_MODELS="llama3-8b-awq,qwen3-0-6b" \
  SWEEP_INPUT_TOKENS="128,256" \
  SWEEP_OUTPUT_TOKENS="128,256" \
  BENCHMARK_DURATION=2m \
  ./run_benchmark_sweep.sh

# Full default grid (llama3-8b-awq × 8 concurrency × 4 token pairs × 10m)
./run_benchmark_sweep.sh
```

### Performance testing best practices
* Adjusting resources:
  * If you are running configuration with HPA enabled, you need to first execute a warm-up run in order to allow HPA to scale the number of replicas of particular pods. Such a warm-up run should generally use the most stressing configuration you want to test and result of such a warm-up run should be voided.
  * If you are running configuration without HPA, it is suggested to adjust the particular nodes resources manually as described below:
    * vLLM pods scaling on Xeon:
      * if you are running baremetal system with multiple sockets you should generally create as many replicas of vLLM as many sockets you have
      * if you are running high core count systems (like 96 or 128 physical cores per socket) you can try to scale vLLM even more and create 2 or 3 vLLM instances per socket
      * scaling can be done using following command `kubectl scale --replicas=$VLLM_REPLICAS -n chatqna statefulset vllm-service-m-deployment`
    * TEI rereranking pod scaling:
      * if you are running on Xeon and there are still some free cores available it is suggested to scale TEI reranking pod to more than one replica (for example: 2)
      * scaling can be done using following command `kubectl scale --replicas=$TEI_REPLICAS -n chatqna deployment tei-reranking-svc-deployment`
    * Other pods typically do not cause a noticeable performance bottlenecks, but it is advised to observe the telemetry to notice any potential slowness of particular pods
* Test parameters:
  * Some of the previously mentioned RAG configuration parameters, such as `k`, `top_n` and `max_new_tokens` are the main factors impacting the overall performance. Generally, you should use the parameters which match your expected configuration, but here are some of the scenarios which are commonly used to track the performance and compare it against the typical LLM benchmarks:
    * `k=10, top_n=7, max_new_tokens=1024` simulates 1024 input and 1024 output for LLM
    * `k=4, top_n=2, max_new_tokens=128` simulates 512 input and 128 output for LLM
    * `k=20, top_n=15, max_new_tokens=128` simulates 2048 input and 128 output for LLM
  * Number of parallel connections simulates the number of concurrent users using the RAG system. It should be configured to match the expected numbers of user in the system. Also, it is advised to swipe over the lower numbers of connections to understand how system will behave with a lower utilization, for example:
    * `1, 4, 8, 16, 32` for Xeon pipeline
* Results file analysis:
  * For the ChatQA application with streaming enabled there are two main metrics which should be analyzed to understand the system performance:
    * `first_token_lat_mean` reported in seconds and expected to be in range of few seconds, it describes how long user will wait for the first token to pop up as the response
    * `next_token_lat_mean` reported in seconds and expected to be in range of tens of milliseconds, it describes how long user will wait for the next tokens to be returned
