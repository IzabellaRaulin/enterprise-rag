#!/bin/bash
# Copyright (C) 2025 Intel Corporation
# SPDX-License-Identifier: Apache-2.0
#
# Chatbot Toolkit ChatQnA – Benchmark parameter sweep
#
# Usage:
#   ./run_benchmark_sweep.sh [--dry-run] [--skip-vectors]
#
# --skip-vectors skips prepare_1M_vectors.sh and the post-ingestion count
# check. Use when the vector DB already holds enough vectors from a prior run.
#
# Required environment variables (set before running):
#   KEYCLOAK_ERAG_ADMIN_PASSWORD   – UI admin password
#   KEYCLOAK_REALM_ADMIN_PASSWORD  – Realm admin password
#
# Optional environment variables (defaults shown):
#   ERAG_DOMAIN_NAME             – eRAG domain           (default: solutions.ai)
#   HF_TOKEN                     – HuggingFace token, needed for gated models
#   BENCHMARK_DURATION           – Duration per run      (default: 10m)
#   UAT_FILE                     – Path for token storage (default: /tmp/uat.txt)
#   TARGET_VECTORS               – Minimum vectors in DB  (default: 1000000)
#   RESULTS_DIR                  – Output root            (default: ./results_<ts>_<model>/)
#   ERAG_ENV_NAME                – Env folder name under env/  (default: local)
#   MODELS_YAML                  – Path to models catalog    (default: env/${ERAG_ENV_NAME}/models-rag.yaml)
#   MODEL_SWITCH_TIMEOUT         – Deploy --wait timeout, s  (default: 1800)
#   MODEL_UNDEPLOY_WAIT          – Balloon-release wait, s   (default: 600)
#   MAX_REPLICAS_PER_NODE        – calc --max-replicas-per-node (default: 10)
#   INFERENCE_THROUGHPUT_MODE    – calc --throughput-mode      (default: true)
#
# Sweep parameter arrays (comma-separated overrides, otherwise built-in defaults):
#   SWEEP_MODELS         – LLM catalog names to iterate  (default: all six catalog models)
#                          e.g. SWEEP_MODELS="qwen3-14b-awq,llama3-8b-awq"
#   SWEEP_USERS          – Concurrent clients per run                       (default: 1,2,4,8,16,32,64,128)
#                          e.g. SWEEP_USERS="1,4,32"
#   SWEEP_INPUT_TOKENS   – Input token lengths, paired with SWEEP_OUTPUT_TOKENS by index
#                                                                          (default: 128,256,256,256)
#   SWEEP_OUTPUT_TOKENS  – Output token lengths, paired with SWEEP_INPUT_TOKENS by index
#                                                                          (default: 128,256,512,1024)
#                          e.g. run only in128/out128:
#                            SWEEP_INPUT_TOKENS=128 SWEEP_OUTPUT_TOKENS=128
#   SWEEP_TOP_N, SWEEP_K, SWEEP_Q_FILES – edited in-script (top of file)
#
# Per LLM switch this script mirrors what `es_auto_installer.sh install application`
# does for the LLM role only:
#   1. undeploy the currently-served LLM via `model-manager undeploy --wait`
#      (MM_CONFIG points at ${ERAG_ENV_DIR}/models-rag.yaml so the same catalog
#      is used); --wait blocks until pods are gone and NRI balloons release cores.
#   2. read baseline cpu/memory for the incoming LLM from models-rag.yaml.
#   3. run calculate_replicas.py with the incoming LLM cpu + the already-deployed
#      embed/rerank cpu (auto-detected from cluster+catalog) so the calculator
#      accounts for cores held by embed/rerank.
#   4. `model-manager deploy <name> --cpu <adj> --memory <base> --replicas N
#          --env OMP_NUM_THREADS=<adj> --wait`.
# Embed/rerank models are left in place across the whole sweep.
# Per-model results land in results_<ts>_<model>/. The tokenizer passed to
# benchmark.py is auto-resolved from each model's HuggingFace model_id.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ─────────────────────────────────────────────────────────────────────────────
# CONFIGURABLE SWEEP PARAMETERS
# Edit these arrays to change the parameter sweep.
# token_params are PAIRED: SWEEP_INPUT_TOKENS[i] maps to SWEEP_OUTPUT_TOKENS[i]
# Scenarios:
#   128/128   – Short query, short response
#   256/256   – Medium query, medium response
#   256/512   – Medium query, extended response
#   256/1024  – Medium query, long response
# NOTE: input tokens are capped at 256 because the embedding model
# (BAAI/bge-base-en-v1.5) has a hard 512-token architectural limit. vLLM adds
# ~21 special tokens, so inputs padded to 512+ tokens cause a 400 from the
# Embedding step. To test larger inputs, replace the embedding model with one
# that supports longer sequences (e.g., BAAI/bge-m3, max 8192 tokens).
# ─────────────────────────────────────────────────────────────────────────────
# Full-grid defaults: 1..128 clients, 10m per run, four paired token scenarios
# (128/128, 256/256, 256/512, 256/1024). Override any of these via env vars
# below (all comma-separated). Quick smoke tests can trim on the command line,
# e.g. SWEEP_USERS="1,4" BENCHMARK_DURATION=1m ./run_benchmark_sweep.sh
#
# Override SWEEP_USERS at invocation with a comma-separated list, e.g.:
#   SWEEP_USERS="1,4,8,16" ./run_benchmark_sweep.sh
if [[ -n "${SWEEP_USERS:-}" ]]; then
    IFS=',' read -ra SWEEP_USERS <<< "$SWEEP_USERS"
    for i in "${!SWEEP_USERS[@]}"; do
        SWEEP_USERS[$i]="$(echo "${SWEEP_USERS[$i]}" | xargs)"
    done
else
    SWEEP_USERS=(1 2 4 8 16 32 64 128)
fi

# SWEEP_INPUT_TOKENS and SWEEP_OUTPUT_TOKENS are PAIRED by index — element i of
# one maps to element i of the other. Override with comma-separated lists of
# equal length; length parity is enforced later in check_prerequisites.
# Example:
#   SWEEP_INPUT_TOKENS="128,256,256"  SWEEP_OUTPUT_TOKENS="128,256,512" ./run_benchmark_sweep.sh
if [[ -n "${SWEEP_INPUT_TOKENS:-}" ]]; then
    IFS=',' read -ra SWEEP_INPUT_TOKENS <<< "$SWEEP_INPUT_TOKENS"
    for i in "${!SWEEP_INPUT_TOKENS[@]}"; do
        SWEEP_INPUT_TOKENS[$i]="$(echo "${SWEEP_INPUT_TOKENS[$i]}" | xargs)"
    done
else
    SWEEP_INPUT_TOKENS=(128 256 256 256)
fi
if [[ -n "${SWEEP_OUTPUT_TOKENS:-}" ]]; then
    IFS=',' read -ra SWEEP_OUTPUT_TOKENS <<< "$SWEEP_OUTPUT_TOKENS"
    for i in "${!SWEEP_OUTPUT_TOKENS[@]}"; do
        SWEEP_OUTPUT_TOKENS[$i]="$(echo "${SWEEP_OUTPUT_TOKENS[$i]}" | xargs)"
    done
else
    SWEEP_OUTPUT_TOKENS=(128 256 512 1024)
fi
SWEEP_TOP_N=(1)
SWEEP_K=(5)
SWEEP_Q_FILES=("questions-pubmed")

# Catalog names from your env folder's models-rag.yaml. Only the LLM role is swapped
# per iteration — the currently-deployed embed/rerank services are left in
# place, and their cpu is fed into calculate_replicas.py so the LLM sizing
# accounts for cores they already hold.
#
# Override at invocation time with a comma-separated list, e.g.:
#   SWEEP_MODELS="qwen3-0-6b,llama3-8b-awq" ./run_benchmark_sweep.sh
if [[ -n "${SWEEP_MODELS:-}" ]]; then
    IFS=',' read -ra SWEEP_MODELS <<< "$SWEEP_MODELS"
    # Trim surrounding whitespace on each entry
    for i in "${!SWEEP_MODELS[@]}"; do
        SWEEP_MODELS[$i]="$(echo "${SWEEP_MODELS[$i]}" | xargs)"
    done
else
    SWEEP_MODELS=(
        "llama3-8b-awq"                    # casperhansen/llama-3-8b-instruct-awq (default)
        "llama3-1-8b"                      # meta-llama/Llama-3.1-8B-Instruct (needs HF_TOKEN)
        "qwen3-14b"                        # Qwen/Qwen3-14B
        "qwen3-14b-awq"                    # Qwen/Qwen3-14B-AWQ
        "solidrust-mistral-7b-v0-3-awq"    # solidrust/Mistral-7B-Instruct-v0.3-AWQ
        "solidrust-llama3-13b-awq"         # solidrust/Llama-3-13B-Instruct-v0.1-AWQ
    )
fi

# ─────────────────────────────────────────────────────────────────────────────
# Runtime settings (override via env vars)
# ─────────────────────────────────────────────────────────────────────────────
ERAG_DOMAIN="${ERAG_DOMAIN_NAME:-solutions.ai}"
# Keep ERAG_DOMAIN_NAME exported so helper scripts (generate_uat_to_file.sh,
# prepare_*.sh, add_cert_to_ca.sh) pick up the same domain when this script
# only sets the default via ERAG_DOMAIN.
export ERAG_DOMAIN_NAME="${ERAG_DOMAIN}"
BENCHMARK_DURATION="${BENCHMARK_DURATION:-10m}"
UAT_FILE="${UAT_FILE:-/tmp/uat.txt}"
TARGET_VECTORS="${TARGET_VECTORS:-1000000}"
RESULTS_ROOT="${RESULTS_DIR:-}"

# Auto-locate model-manager relative to this script — this file lives at
# <parent-repo>/ext/enterprise.ai-erag/src/tests/e2e/benchmarks/chatqna/, so
# the top-level model-manager entry-point is 7 levels up. Falls back to the
# canonical checkout under ext/enterprise.ai-inference/model_manager/. The
# MODEL_MANAGER env var stays as an escape hatch for non-standard layouts
# but is not documented as user-facing configuration.
if [[ -z "${MODEL_MANAGER:-}" ]]; then
    if [[ -x "${SCRIPT_DIR}/../../../../../../../model-manager" ]]; then
        MODEL_MANAGER="${SCRIPT_DIR}/../../../../../../../model-manager"
    elif [[ -x "${SCRIPT_DIR}/../../../../../../enterprise.ai-inference/model_manager/model-manager" ]]; then
        MODEL_MANAGER="${SCRIPT_DIR}/../../../../../../enterprise.ai-inference/model_manager/model-manager"
    else
        MODEL_MANAGER="${SCRIPT_DIR}/../../../../../../../model-manager"   # keep for the error message below
    fi
fi
# Name of your env folder under the repo's top-level env/ directory (local,
# mytest, …). Its models-rag.yaml catalog — seeded/edited by the installer's
# `install application` path — is read here and passed to model-manager via
# MM_CONFIG below.
ENV_ROOT="${SCRIPT_DIR}/../../../../../../../env"
ERAG_ENV_NAME="${ERAG_ENV_NAME:-local}"
ERAG_ENV_DIR="${ENV_ROOT}/${ERAG_ENV_NAME}"
MODELS_YAML="${MODELS_YAML:-${ERAG_ENV_DIR}/models-rag.yaml}"
MODEL_SWITCH_TIMEOUT="${MODEL_SWITCH_TIMEOUT:-1800}"
MODEL_UNDEPLOY_WAIT="${MODEL_UNDEPLOY_WAIT:-600}"
MAX_REPLICAS_PER_NODE="${MAX_REPLICAS_PER_NODE:-10}"
INFERENCE_THROUGHPUT_MODE="${INFERENCE_THROUGHPUT_MODE:-true}"
INFERENCE_EDP_ENABLED="${INFERENCE_EDP_ENABLED:-false}"
INFERENCE_TELEMETRY_ENABLED="${INFERENCE_TELEMETRY_ENABLED:-false}"
INFERENCE_VECTOR_DBS_ENABLED="${INFERENCE_VECTOR_DBS_ENABLED:-false}"
# Emits one JSON blob per node — the role variant used by app_inference_models.
PROBE_TOPOLOGY_SCRIPT="${PROBE_TOPOLOGY_SCRIPT:-${SCRIPT_DIR}/../../../../../deployment/roles/app_inference_models/files/probe-node-topology.sh}"
CALCULATE_REPLICAS_SCRIPT="${CALCULATE_REPLICAS_SCRIPT:-${SCRIPT_DIR}/../../../../../deployment/scripts/calculate_replicas.py}"
DRY_RUN=0
SKIP_VECTORS=0

# ─────────────────────────────────────────────────────────────────────────────
# Colours & logging
# ─────────────────────────────────────────────────────────────────────────────
RED='\033[0;31m'; YELLOW='\033[0;33m'; GREEN='\033[0;32m'
CYAN='\033[0;36m'; BOLD='\033[1m'; RESET='\033[0m'

log_info()  { echo -e "${CYAN}[INFO]${RESET}  $*"; }
log_ok()    { echo -e "${GREEN}[OK]${RESET}    $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${RESET}  $*"; }
log_error() { echo -e "${RED}[ERROR]${RESET} $*" >&2; }
log_sep()   { echo -e "${BOLD}────────────────────────────────────────────────────${RESET}"; }

# ─────────────────────────────────────────────────────────────────────────────
# Argument parsing
# ─────────────────────────────────────────────────────────────────────────────
for arg in "$@"; do
    case "$arg" in
        --dry-run) DRY_RUN=1 ;;
        --skip-vectors) SKIP_VECTORS=1 ;;
        -h|--help)
            # Print the leading comment block (skipping shebang) — stops at
            # the first non-comment line so this stays honest as the header
            # grows or shrinks.
            awk '
                /^#!/ { next }
                /^#/  { sub(/^# ?/, ""); print; next }
                      { exit }
            ' "${BASH_SOURCE[0]}"
            exit 0 ;;
        *) log_error "Unknown argument: $arg"; exit 1 ;;
    esac
done

# ─────────────────────────────────────────────────────────────────────────────
# Prerequisites check
# ─────────────────────────────────────────────────────────────────────────────
check_prerequisites() {
    log_sep
    log_info "Checking prerequisites …"
    local errors=0

    # Required env vars — Keycloak credentials for the RAG user and the realm
    # admin. The source of truth is the erag-credentials / keycloak-admin-secret
    # in the keycloak namespace, NOT ansible-logs/default_credentials.txt (which
    # goes stale on re-install).
    local creds_set=1
    for var in KEYCLOAK_ERAG_ADMIN_USERNAME KEYCLOAK_ERAG_ADMIN_PASSWORD \
               KEYCLOAK_REALM_ADMIN_USERNAME KEYCLOAK_REALM_ADMIN_PASSWORD; do
        if [[ -z "${!var:-}" ]]; then
            log_error "Required env var '$var' is not set."
            log_error "  Export it from the keycloak namespace secret:"
            case "$var" in
                KEYCLOAK_ERAG_ADMIN_USERNAME)
                    log_error "    export KEYCLOAK_ERAG_ADMIN_USERNAME=\$(kubectl get secret -n keycloak erag-credentials -o jsonpath='{.data.KEYCLOAK_ERAG_ADMIN_USERNAME}' | base64 -d)" ;;
                KEYCLOAK_ERAG_ADMIN_PASSWORD)
                    log_error "    export KEYCLOAK_ERAG_ADMIN_PASSWORD=\$(kubectl get secret -n keycloak erag-credentials -o jsonpath='{.data.KEYCLOAK_ERAG_ADMIN_PASSWORD}' | base64 -d)" ;;
                KEYCLOAK_REALM_ADMIN_USERNAME)
                    log_error "    export KEYCLOAK_REALM_ADMIN_USERNAME=\$(kubectl get secret -n keycloak keycloak-admin-secret -o jsonpath='{.data.username}' | base64 -d)" ;;
                KEYCLOAK_REALM_ADMIN_PASSWORD)
                    log_error "    export KEYCLOAK_REALM_ADMIN_PASSWORD=\$(kubectl get secret -n keycloak keycloak-admin-secret -o jsonpath='{.data.password}' | base64 -d)" ;;
            esac
            errors=$((errors + 1))
            creds_set=0
        else
            log_ok "$var is set."
        fi
    done

    # Proxy bypass
    local proxy_ok=0
    for proxy_var in no_proxy NO_PROXY; do
        if echo "${!proxy_var:-}" | grep -q "${ERAG_DOMAIN}"; then
            proxy_ok=1
            break
        fi
    done
    if [[ $proxy_ok -eq 0 ]]; then
        log_error "Proxy bypass for '${ERAG_DOMAIN}' is NOT configured."
        log_error "  Run:"
        log_error "    export no_proxy=\"\${no_proxy:+\$no_proxy,}${ERAG_DOMAIN},.${ERAG_DOMAIN}\""
        log_error "    export NO_PROXY=\"\${NO_PROXY:+\$NO_PROXY,}${ERAG_DOMAIN},.${ERAG_DOMAIN}\""
        errors=$((errors + 1))
    else
        log_ok "Proxy bypass for ${ERAG_DOMAIN} is configured."
    fi

    # /etc/hosts entries — Keycloak now lives on its own subdomain
    # (keycloak.<domain>) in the inference stack, not on auth.<domain>.
    if grep -qE "(^|\s)${ERAG_DOMAIN//./\\.}(\s|$)" /etc/hosts 2>/dev/null; then
        log_ok "/etc/hosts contains an entry for ${ERAG_DOMAIN}."
    else
        log_error "/etc/hosts does NOT contain an entry for '${ERAG_DOMAIN}'."
        log_error "  Add a line such as:"
        log_error "    <GATEWAY_IP>  ${ERAG_DOMAIN} keycloak.${ERAG_DOMAIN} inference.${ERAG_DOMAIN} grafana.${ERAG_DOMAIN} minio.${ERAG_DOMAIN} s3.${ERAG_DOMAIN}"
        errors=$((errors + 1))
    fi

    # Required tools
    for tool in python3 curl jq wget kubectl; do
        if ! command -v "$tool" &>/dev/null; then
            log_error "Required tool '$tool' not found in PATH."
            errors=$((errors + 1))
        else
            log_ok "'$tool' is available."
        fi
    done

    # kubectl connectivity — hitting the api-server for real, not just checking
    # the CLI exists. Failure here typically means the corporate proxy is
    # intercepting the api-server URL because it is missing from no_proxy.
    local api_server api_host
    api_server=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}' 2>/dev/null || true)
    api_host="${api_server#*://}"; api_host="${api_host%%:*}"
    if kubectl cluster-info >/dev/null 2>&1; then
        log_ok "kubectl reaches the cluster (${api_server:-unknown})."
    else
        log_error "kubectl cannot reach the cluster at ${api_server:-<unknown>}."
        log_error "  Common cause: ${api_host:-<api-server host>} is not in no_proxy, so kubectl goes through the corporate proxy and gets 'Forbidden'."
        log_error "  Fix in this shell, then re-run:"
        log_error "    export no_proxy=\"\${no_proxy:+\$no_proxy,}${api_host:-<api-server-host>},.svc,.cluster.local\""
        log_error "    export NO_PROXY=\"\$no_proxy\""
        log_error "  Reproduce manually with:  kubectl cluster-info"
        errors=$((errors + 1))
    fi

    # model_manager is required for multi-model sweeps
    if [[ ! -x "$MODEL_MANAGER" ]]; then
        log_error "model-manager CLI not found. Auto-detection looked for:"
        log_error "  ${SCRIPT_DIR}/../../../../../../../model-manager"
        log_error "  ${SCRIPT_DIR}/../../../../../../enterprise.ai-inference/model_manager/model-manager"
        log_error "This script expects the enterprise.ai-solutions parent repo layout."
        errors=$((errors + 1))
    else
        log_ok "model-manager: $MODEL_MANAGER"
    fi

    if [[ -z "$ERAG_ENV_NAME" ]]; then
        log_error "ERAG_ENV_NAME is not set. Set it to your env folder name under ${ENV_ROOT}."
        log_error "  e.g. ERAG_ENV_NAME=local ./run_benchmark_sweep.sh"
        errors=$((errors + 1))
    elif [[ ! -f "$MODELS_YAML" ]]; then
        log_error "models.yaml not found at: $MODELS_YAML"
        log_error "  Check ERAG_ENV_NAME, or override with: MODELS_YAML=/path/to/models.yaml"
        log_error "  (default expects env/<ERAG_ENV_NAME>/models-rag.yaml seeded by 'es_auto_installer.sh install application')"
        errors=$((errors + 1))
    else
        log_ok "env: ${ERAG_ENV_NAME} (${ERAG_ENV_DIR})"
        log_ok "models.yaml: $MODELS_YAML"
    fi

    if [[ ! -f "$PROBE_TOPOLOGY_SCRIPT" ]]; then
        log_error "probe-node-topology.sh not found at: $PROBE_TOPOLOGY_SCRIPT"
        log_error "  Override with: PROBE_TOPOLOGY_SCRIPT=/path/to/probe-node-topology.sh"
        errors=$((errors + 1))
    else
        log_ok "probe-node-topology.sh: $PROBE_TOPOLOGY_SCRIPT"
    fi

    if [[ ! -f "$CALCULATE_REPLICAS_SCRIPT" ]]; then
        log_error "calculate_replicas.py not found at: $CALCULATE_REPLICAS_SCRIPT"
        log_error "  Override with: CALCULATE_REPLICAS_SCRIPT=/path/to/calculate_replicas.py"
        errors=$((errors + 1))
    else
        log_ok "calculate_replicas.py: $CALCULATE_REPLICAS_SCRIPT"
    fi

    if [[ ${#SWEEP_MODELS[@]} -eq 0 ]]; then
        log_error "SWEEP_MODELS is empty. Add at least one catalog name."
        errors=$((errors + 1))
    fi

    # Python benchmark.py
    if [[ ! -f "${SCRIPT_DIR}/benchmark.py" ]]; then
        log_error "benchmark.py not found in ${SCRIPT_DIR}."
        errors=$((errors + 1))
    else
        log_ok "benchmark.py found."
    fi

    # Question CSV files
    for qf in "${SWEEP_Q_FILES[@]}"; do
        if [[ ! -f "${SCRIPT_DIR}/${qf}.csv" ]]; then
            log_error "Question file '${qf}.csv' not found in ${SCRIPT_DIR}."
            errors=$((errors + 1))
        else
            log_ok "Question file '${qf}.csv' found."
        fi
    done

    # Input/output token array length parity
    if [[ ${#SWEEP_INPUT_TOKENS[@]} -ne ${#SWEEP_OUTPUT_TOKENS[@]} ]]; then
        log_error "SWEEP_INPUT_TOKENS and SWEEP_OUTPUT_TOKENS arrays must have the same length."
        errors=$((errors + 1))
    fi

    # Actually mint a user token with the exported creds. Catches a stale
    # password (re-installed cluster, rotated secret) before the sweep spends
    # 40 minutes switching models only to discover the token endpoint rejects
    # us. Reuses generate_uat_to_file.sh so this test is the exact code path
    # the sweep runs later (including the admin-side requiredActions reset
    # against the erag-admin user) — if it passes here, it will pass there.
    # Only runs when all four env vars are present; the presence check above
    # produces a clearer error otherwise.
    if [[ $creds_set -eq 1 ]]; then
        local uat_out uat_rc=0
        # `|| uat_rc=$?` so set -e doesn't abort here on a bad password — we
        # want the error branch below to report it.
        uat_out=$(bash "${SCRIPT_DIR}/generate_uat_to_file.sh" /dev/null 1 2>&1) || uat_rc=$?
        if [[ $uat_rc -ne 0 ]]; then
            log_error "Keycloak credential validation failed. generate_uat_to_file.sh output:"
            while IFS= read -r line; do log_error "    ${line}"; done <<< "$uat_out"
            log_error "  Re-read credentials from the cluster secrets, then re-run:"
            log_error "    export KEYCLOAK_REALM_ADMIN_USERNAME=\$(kubectl get secret -n keycloak keycloak-admin-secret -o jsonpath='{.data.username}' | base64 -d)"
            log_error "    export KEYCLOAK_REALM_ADMIN_PASSWORD=\$(kubectl get secret -n keycloak keycloak-admin-secret -o jsonpath='{.data.password}' | base64 -d)"
            log_error "    export KEYCLOAK_ERAG_ADMIN_USERNAME=\$(kubectl get secret -n keycloak erag-credentials -o jsonpath='{.data.KEYCLOAK_ERAG_ADMIN_USERNAME}' | base64 -d)"
            log_error "    export KEYCLOAK_ERAG_ADMIN_PASSWORD=\$(kubectl get secret -n keycloak erag-credentials -o jsonpath='{.data.KEYCLOAK_ERAG_ADMIN_PASSWORD}' | base64 -d)"
            errors=$((errors + 1))
        else
            log_ok "Keycloak credential validation passed (realm=${KEYCLOAK_REALM_ADMIN_USERNAME}, user=${KEYCLOAK_ERAG_ADMIN_USERNAME})."
        fi
    fi

    if [[ $errors -gt 0 ]]; then
        log_error "$errors prerequisite check(s) failed. Please fix the issues above and re-run."
        exit 1
    fi
    log_ok "All prerequisite checks passed."
}

# ─────────────────────────────────────────────────────────────────────────────
# Vector DB check & ingestion
# ─────────────────────────────────────────────────────────────────────────────
check_and_prepare_vectors() {
    log_sep
    log_info "Checking vector count and preparing database if needed …"
    log_info "prepare_1M_vectors.sh handles token generation and the vector count check internally."

    if [[ ! -f "${SCRIPT_DIR}/prepare_1M_vectors.sh" ]]; then
        log_error "prepare_1M_vectors.sh not found in ${SCRIPT_DIR}."
        exit 1
    fi

    if [[ $DRY_RUN -eq 1 ]]; then
        log_info "[DRY-RUN] Would run: bash ${SCRIPT_DIR}/prepare_1M_vectors.sh ${TARGET_VECTORS}"
    else
        bash "${SCRIPT_DIR}/prepare_1M_vectors.sh" "${TARGET_VECTORS}"
    fi

    # After the ingestion script returns, do a final authoritative vector count
    # to make absolutely sure everything is ingested and the target is met
    # before allowing any benchmark run to start.
    # A fresh token is needed here because prepare_1M_vectors.sh ran in a subshell.
    if [[ $DRY_RUN -eq 0 ]]; then
        log_info "Performing post-ingestion vector count verification …"
        source "${SCRIPT_DIR}/generate_uat_to_file.sh" "/dev/null" 1
        if [[ -z "${USER_ACCESS_TOKEN:-}" ]] || [[ "${USER_ACCESS_TOKEN}" == "null" ]]; then
            log_error "Failed to generate a user access token for post-ingestion verification."
            exit 1
        fi

        local edp_url="https://${ERAG_DOMAIN}/api/v1/edp"
        local verified_chunks=0 in_progress_count=0
        local verify_stat
        verify_stat=$(curl -k -s "${edp_url}/files" -H "Authorization: Bearer ${USER_ACCESS_TOKEN}")
        while read -r details; do
            local vstatus vchunks
            vstatus=$(echo "$details" | jq -r '.status')
            if [[ "$vstatus" == "ingested" ]]; then
                vchunks=$(echo "$details" | jq -r '.chunks_processed')
                verified_chunks=$((verified_chunks + vchunks))
            elif [[ "$vstatus" != "error" ]]; then
                in_progress_count=$((in_progress_count + 1))
            fi
        done < <(echo "$verify_stat" | jq -c '.[]' 2>/dev/null)

        if [[ $in_progress_count -gt 0 ]]; then
            log_error "Ingestion is still in progress (${in_progress_count} file(s) not yet ingested)."
            log_error "Wait for ingestion to complete, then re-run this script."
            exit 1
        fi

        if [[ "$verified_chunks" -lt "$TARGET_VECTORS" ]]; then
            log_error "Vector count after ingestion (${verified_chunks}) is still below the target (${TARGET_VECTORS})."
            log_error "Check the ingestion logs and re-run prepare_1M_vectors.sh manually."
            exit 1
        fi

        log_ok "Post-ingestion verification passed: ${verified_chunks} vectors in DB (target: ${TARGET_VECTORS})."
    fi
}

# ─────────────────────────────────────────────────────────────────────────────
# Token generation
# ─────────────────────────────────────────────────────────────────────────────
generate_tokens() {
    local n=$1
    log_info "Generating ${n} user access token(s) → ${UAT_FILE} …"
    if [[ $DRY_RUN -eq 1 ]]; then
        log_info "[DRY-RUN] Would run: source ${SCRIPT_DIR}/generate_uat_to_file.sh ${UAT_FILE} ${n}"
        return
    fi
    # Source so that USER_ACCESS_TOKEN is available in this shell too
    source "${SCRIPT_DIR}/generate_uat_to_file.sh" "${UAT_FILE}" "${n}"
    if [[ $? -ne 0 ]]; then
        log_error "Token generation failed."
        exit 1
    fi
    log_ok "${n} token(s) written to ${UAT_FILE}."
}

# ─────────────────────────────────────────────────────────────────────────────
# Model manager helpers
# ─────────────────────────────────────────────────────────────────────────────
# Read a scalar field for a named model entry from a models*.yaml catalog.
# Uses awk so no yq dependency is required. Prints the value (with quotes
# stripped) on stdout; prints nothing and returns 1 if not found.
#   resolve_model_field <model-name> <field>
# Supported top-level scalar fields under a `- name: X` entry: model_id,
# cpu, memory, category, runtime, server_version, replicas.
resolve_model_field() {
    local name="$1" field="$2"
    local val
    val=$(awk -v want="$name" -v key="${field}:" '
        /^models:/ {inm=1; next}
        inm && /^[^ ]/ {inm=0}
        inm && $1 == "-" && $2 == "name:" { current=$3; next }
        inm && current == want && $1 == key { print $2; exit }
    ' "$MODELS_YAML")
    # Strip a single pair of surrounding double quotes if present (e.g. server_version: "0.21.0")
    val="${val%\"}"; val="${val#\"}"
    if [[ -z "$val" ]]; then
        return 1
    fi
    echo "$val"
}

# Back-compat wrapper — everywhere else in the script called this by name.
resolve_model_id() {
    local id
    if ! id=$(resolve_model_field "$1" "model_id"); then
        log_error "Could not resolve model_id for '$1' in $MODELS_YAML"
        return 1
    fi
    echo "$id"
}

# Returns names of currently deployed LLM inference services.
list_deployed_llms() {
    kubectl get llminferenceservice -n llm-inference \
        -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null
}

# List names of deployed llminferenceservices whose catalog `category:` matches
# the given value (llm | embed | rerank). Requires the deployed service names to
# match catalog entries in $MODELS_YAML — anything unknown is skipped.
list_deployed_by_category() {
    local want_cat="$1" name cat
    while IFS= read -r name; do
        [[ -z "$name" ]] && continue
        cat=$(resolve_model_field "$name" "category" 2>/dev/null || true)
        [[ "$cat" == "$want_cat" ]] && echo "$name"
    done < <(list_deployed_llms)
}

# model-manager `deploy --wait` returns as soon as MainWorkloadReady flips
# (first replica up), not once all N replicas are Ready — lib/helpers.sh only
# inspects .items[0] on heartbeat. Poll until the pod-Ready count equals the
# target, so the benchmark doesn't start with warm-up traffic still hitting
# half-loaded replicas. Uses jq (already required by the sweep).
wait_all_replicas_ready() {
    local model="$1" target="$2" timeout="${MODEL_SWITCH_TIMEOUT:-1800}" waited=0
    local poll=10 ready_count=0

    if [[ "$target" -le 1 ]]; then
        return 0   # model-manager --wait already covers the single-replica case
    fi

    log_info "  Waiting up to ${timeout}s for all ${target} replicas of '${model}' to be Ready..."

    while (( waited < timeout )); do
        ready_count=$(kubectl get pods -n llm-inference \
            -l "app.kubernetes.io/name=${model}" -o json 2>/dev/null \
            | jq '[.items[] | select(.status.conditions[]?
                | select(.type=="Ready" and .status=="True"))] | length' 2>/dev/null || echo 0)

        if (( ready_count >= target )); then
            log_ok "  All ${target} replica(s) Ready."
            return 0
        fi

        log_info "    Ready: ${ready_count}/${target} (elapsed ${waited}s)"
        sleep "$poll"
        waited=$((waited + poll))
    done

    log_error "  Timed out (${timeout}s) waiting for replicas of '${model}': ${ready_count}/${target} Ready."
    kubectl get pods -n llm-inference -l "app.kubernetes.io/name=${model}" 2>&1 | sed 's/^/    /' >&2 || true
    return 1
}

# Point the RAG pipeline (llm-svc → ai-gateway) at the currently-served LLM.
#
# model-manager only deploys the vLLM pod + gateway route; it does not update
# the LLM microservice's LLM_MODEL_NAME. The truth-source is
# gmc-config.data['llm-usvc.yaml'] in the 'system' namespace, which GMC renders
# into cm/llm-usvc-config in the chatqna namespace. If this stays stale after
# a model swap, llm-svc asks the gateway for a model that no longer exists →
# router-service returns 500 with "No matching route found ... not configured
# in the Gateway", never reaching vLLM.
#
# Steps:
#   1. patch gmc-config.llm-usvc.yaml (LLM_MODEL_NAME) via kubectl apply
#   2. restart gmc-controller so it re-renders the derived ConfigMap
#   3. restart llm-svc-deployment so llm-svc reloads env
#   4. wait for llm-svc rollout to be Ready
sync_rag_llm_model() {
    local model="$1"
    log_info "Syncing RAG LLM_MODEL_NAME → '${model}' in gmc-config…"

    if [[ $DRY_RUN -eq 1 ]]; then
        log_info "[DRY-RUN] Would patch cm/gmc-config in 'system' ns, restart gmc-controller, restart llm-svc-deployment."
        return 0
    fi

    if ! kubectl get cm gmc-config -n system -o json 2>/dev/null | \
         python3 -c "
import json, sys, re
new_model = sys.argv[1]
cm = json.load(sys.stdin)
data = cm.get('data', {})
if 'llm-usvc.yaml' not in data:
    sys.exit('gmc-config has no llm-usvc.yaml key')
data['llm-usvc.yaml'] = re.sub(
    r'(LLM_MODEL_NAME:\s*\")[^\"]+(\")', r'\1' + new_model + r'\2',
    data['llm-usvc.yaml'])
cm['metadata'].pop('resourceVersion', None)
cm['metadata'].pop('uid', None)
cm['metadata'].pop('creationTimestamp', None)
json.dump(cm, sys.stdout)
" "$model" | kubectl apply -f - >/dev/null 2>&1; then
        log_error "  Failed to patch gmc-config.llm-usvc.yaml with LLM_MODEL_NAME='${model}'."
        return 1
    fi

    if ! kubectl rollout restart deployment/gmc-controller -n system >/dev/null 2>&1; then
        log_error "  Failed to restart gmc-controller."
        return 1
    fi
    if ! kubectl rollout status deployment/gmc-controller -n system --timeout=120s >/dev/null 2>&1; then
        log_warn "  gmc-controller rollout did not complete within 120s (continuing anyway)."
    fi

    if ! kubectl rollout restart deployment/llm-svc-deployment -n chatqna >/dev/null 2>&1; then
        log_error "  Failed to restart llm-svc-deployment."
        return 1
    fi
    if ! kubectl rollout status deployment/llm-svc-deployment -n chatqna --timeout=180s >/dev/null 2>&1; then
        log_error "  llm-svc-deployment did not become Ready within 180s."
        return 1
    fi

    # `rollout status` returns as soon as the new ReplicaSet is Ready and the
    # deployment's .status.updatedReplicas matches .spec.replicas — old pods
    # from the previous ReplicaSet can still be in Terminating state at that
    # point (running out their terminationGracePeriodSeconds). Wait for them
    # to actually leave `get pods` before we validate env, so the check does
    # not race against pods that are logically decommissioned but not yet
    # deleted from etcd. Best-effort — proceed even if the wait hits its own
    # timeout, since the env validation is retrying + filtering anyway.
    log_info "  Waiting for previous llm-svc pods to finish terminating…"
    kubectl wait --for=delete pod -n chatqna \
        -l "app.kubernetes.io/name=llm-usvc" \
        --field-selector="status.phase!=Running" \
        --timeout=120s >/dev/null 2>&1 || true

    log_ok "  RAG now targets '${model}'."
}

# Confirm the RAG's LLM_MODEL_NAME is coherent end-to-end before the benchmark
# runs. Fails fast (before wasting benchmark time on 500s) if any of these are
# out of sync:
#   1. gmc-config.data['llm-usvc.yaml'] in 'system' ns (source-of-truth)
#   2. cm/llm-usvc-config in 'chatqna' ns (derived by gmc-controller)
#   3. running llm-svc pods (env var reflects the ConfigMap they mounted)
# The derived-CM check is retried with backoff to give GMC time to reconcile.
validate_rag_llm_config() {
    local expected="$1"
    log_info "Validating RAG config → LLM_MODEL_NAME='${expected}'…"

    if [[ $DRY_RUN -eq 1 ]]; then
        log_info "[DRY-RUN] Would validate gmc-config, llm-usvc-config, and running llm-svc env."
        return 0
    fi

    # 1) Source-of-truth (system/gmc-config).
    local src_value
    src_value=$(kubectl get cm gmc-config -n system -o jsonpath='{.data.llm-usvc\.yaml}' 2>/dev/null \
                | grep -oE 'LLM_MODEL_NAME: *"[^"]+"' | head -1 | sed -E 's/.*"([^"]+)".*/\1/')
    if [[ "$src_value" != "$expected" ]]; then
        log_error "  gmc-config[llm-usvc.yaml].LLM_MODEL_NAME='${src_value:-<empty>}' — expected '${expected}'."
        return 1
    fi
    log_ok "  gmc-config[llm-usvc.yaml]: LLM_MODEL_NAME='${src_value}' ✓"

    # 2) Derived ConfigMap in chatqna (GMC reconciles this from gmc-config).
    #    Retry briefly to allow gmc-controller to catch up after the restart.
    local derived_value="" waited=0 timeout=90
    while (( waited < timeout )); do
        derived_value=$(kubectl get cm llm-usvc-config -n chatqna -o jsonpath='{.data.LLM_MODEL_NAME}' 2>/dev/null || true)
        [[ "$derived_value" == "$expected" ]] && break
        sleep 5
        waited=$((waited + 5))
    done
    if [[ "$derived_value" != "$expected" ]]; then
        log_error "  cm/llm-usvc-config[LLM_MODEL_NAME]='${derived_value:-<empty>}' after ${timeout}s — expected '${expected}'."
        log_error "  gmc-controller did not reconcile. Investigate: kubectl logs -n system deployment/gmc-controller"
        return 1
    fi
    log_ok "  cm/llm-usvc-config: LLM_MODEL_NAME='${derived_value}' ✓ (reconciled in ${waited}s)"

    # 3) Running llm-svc pods must see the reconciled value in their env.
    #    kubectl rollout status can return while the previous ReplicaSet's pods
    #    are still Terminating — those pods are visible in `get pods` output
    #    for a few seconds and `printenv` still shows the old env until the
    #    grace period elapses. Two guards:
    #      a) skip pods with a deletionTimestamp (Terminating),
    #      b) retry the whole scan with backoff until all live pods agree.
    local mismatched=0 pod_env total_pods=0 pod_line pod phase deletion_ts
    local pod_waited=0 pod_timeout=90
    while (( pod_waited < pod_timeout )); do
        mismatched=0; total_pods=0
        while IFS='|' read -r pod phase deletion_ts; do
            [[ -z "$pod" ]] && continue
            [[ -n "$deletion_ts" ]] && continue   # skip Terminating pods
            [[ "$phase" != "Running" ]] && continue
            total_pods=$((total_pods + 1))
            pod_env=$(kubectl exec -n chatqna "$pod" -c llm-usvc -- printenv LLM_MODEL_NAME 2>/dev/null || true)
            if [[ "$pod_env" != "$expected" ]]; then
                mismatched=$((mismatched + 1))
            fi
        done < <(kubectl get pods -n chatqna -l app.kubernetes.io/name=llm-usvc \
                    -o jsonpath='{range .items[*]}{.metadata.name}|{.status.phase}|{.metadata.deletionTimestamp}{"\n"}{end}' 2>/dev/null)
        if (( total_pods > 0 && mismatched == 0 )); then
            break
        fi
        sleep 5
        pod_waited=$((pod_waited + 5))
    done

    if [[ $total_pods -eq 0 ]]; then
        log_error "  No Running (non-Terminating) llm-usvc pods found in chatqna namespace after ${pod_timeout}s."
        return 1
    fi
    if [[ $mismatched -gt 0 ]]; then
        # Emit per-pod detail only on final failure so successful runs stay quiet.
        while IFS='|' read -r pod phase deletion_ts; do
            [[ -z "$pod" || -n "$deletion_ts" || "$phase" != "Running" ]] && continue
            pod_env=$(kubectl exec -n chatqna "$pod" -c llm-usvc -- printenv LLM_MODEL_NAME 2>/dev/null || true)
            [[ "$pod_env" != "$expected" ]] && log_error "    pod ${pod}: env LLM_MODEL_NAME='${pod_env:-<empty>}' — expected '${expected}'"
        done < <(kubectl get pods -n chatqna -l app.kubernetes.io/name=llm-usvc \
                    -o jsonpath='{range .items[*]}{.metadata.name}|{.status.phase}|{.metadata.deletionTimestamp}{"\n"}{end}' 2>/dev/null)
        log_error "  ${mismatched}/${total_pods} llm-svc pod(s) still have stale env after ${pod_timeout}s — rollout did not complete."
        return 1
    fi
    log_ok "  llm-svc pods (${total_pods}): env LLM_MODEL_NAME='${expected}' ✓ (settled in ${pod_waited}s)"

    log_ok "RAG config validation passed."
}

# Run the role's node-topology probe locally (bash-compatible sh) and wrap its
# single-line JSON in a {"<node>": <json>} dict shaped for calculate_replicas.py.
# For multi-node clusters this would need one probe per node — for now we assume
# the workload nodes share a topology and take the first Ready node name.
# Prints the JSON dict on stdout; exits 1 on failure.
probe_topology_json() {
    local node_name probe_json
    node_name=$(kubectl get nodes \
        -o jsonpath='{.items[?(@.status.conditions[-1].type=="Ready")].metadata.name}' 2>/dev/null \
        | awk '{print $1}')
    [[ -z "$node_name" ]] && node_name="localhost"

    if ! probe_json=$(sh "$PROBE_TOPOLOGY_SCRIPT" 2>/dev/null); then
        log_error "probe-node-topology.sh failed"
        return 1
    fi
    if [[ -z "$probe_json" ]]; then
        log_error "probe-node-topology.sh produced no output"
        return 1
    fi
    printf '{"%s": %s}\n' "$node_name" "$probe_json"
}

# Compute deploy-time cpu/memory/replicas for the incoming LLM by calling
# calculate_replicas.py with the LLM's baseline cpu and the already-deployed
# embed/rerank cpu (so the calculator accounts for cores those services hold).
# Sets globals CALC_CPU, CALC_MEMORY, CALC_REPLICAS. Returns 1 on failure.
compute_sizing() {
    local model="$1" nodes_json="$2" embed_cpu="$3" rerank_cpu="$4"
    local llm_cpu llm_memory
    llm_cpu=$(resolve_model_field "$model" "cpu") \
        || { log_error "Cannot read cpu for '$model' from $MODELS_YAML"; return 1; }
    llm_memory=$(resolve_model_field "$model" "memory") \
        || { log_error "Cannot read memory for '$model' from $MODELS_YAML"; return 1; }

    log_info "  Baseline (from catalog): cpu=${llm_cpu}, memory=${llm_memory}"
    log_info "  Deployed embed cpu=${embed_cpu}, rerank cpu=${rerank_cpu}"

    local raw
    if ! raw=$(python3 "$CALCULATE_REPLICAS_SCRIPT" \
            --nodes-dict "$nodes_json" \
            --vllm-size "$llm_cpu" \
            --embedding-size "$embed_cpu" \
            --reranking-size "$rerank_cpu" \
            --throughput-mode "$INFERENCE_THROUGHPUT_MODE" \
            --edp-enabled "$INFERENCE_EDP_ENABLED" \
            --telemetry-enabled "$INFERENCE_TELEMETRY_ENABLED" \
            --vector-databases-enabled "$INFERENCE_VECTOR_DBS_ENABLED" \
            --max-replicas-per-node "$MAX_REPLICAS_PER_NODE" 2>&1); then
        log_error "calculate_replicas.py failed:"
        echo "$raw" | sed 's/^/    /' >&2
        return 1
    fi

    # Prefer an AMX-supporting node (per-instance sizes are identical across
    # nodes; the role picks any AMX node). Fall back to the first non-total
    # entry so single-node dev clusters without AMX metadata still work.
    local pick
    pick=$(echo "$raw" | python3 -c '
import json, sys
data = json.load(sys.stdin)
node = None
for k, v in data.items():
    if k == "total_replicas" or not isinstance(v, dict):
        continue
    if v.get("amx_supported"):
        node = v; break
    if node is None:
        node = v
if node is None:
    sys.exit("no per-node entries in calculator output")
vllm = node.get("vllm", {})
print(vllm.get("adjusted_cpu_size", 0), vllm.get("replicas", 0))
' 2>/dev/null) || {
        log_error "Could not parse calculator output:"
        echo "$raw" | sed 's/^/    /' >&2
        return 1
    }
    CALC_CPU=$(echo "$pick" | awk '{print $1}')
    CALC_REPLICAS=$(echo "$pick" | awk '{print $2}')
    CALC_MEMORY="$llm_memory"

    if [[ -z "$CALC_CPU" || "$CALC_CPU" == "0" ]] \
       || [[ -z "$CALC_REPLICAS" || "$CALC_REPLICAS" == "0" ]]; then
        log_error "Calculator returned zero cpu/replicas — LLM won't fit alongside embed/rerank."
        log_error "  Raw output: $raw"
        return 1
    fi

    log_info "  Sized for deploy: cpu=${CALC_CPU}, memory=${CALC_MEMORY}, replicas=${CALC_REPLICAS}"
}

# Switch the served LLM to the given catalog name:
#   - if it is already the deployed LLM, use it as-is (no redeploy)
#   - otherwise undeploy the current LLM(s) (blocking on --wait so cores/balloons
#     release), size via calculate_replicas.py, then deploy the target
# Embed/rerank are left in place; their cpu feeds the calculator on deploy.
switch_model() {
    local model="$1"
    log_sep
    log_info "Switching served model → ${model}"

    if [[ $DRY_RUN -eq 1 ]]; then
        log_info "[DRY-RUN] Would undeploy any other LLM(s) and, if '${model}' is not already deployed, run calculate_replicas.py and deploy it."
        return 0
    fi

    export MM_CONFIG="$MODELS_YAML"

    # Detect current LLM(s) via category cross-reference so we don't touch
    # embed/rerank.
    local -a current_llms=()
    while IFS= read -r name; do
        [[ -n "$name" ]] && current_llms+=("$name")
    done < <(list_deployed_by_category "llm")

    # Is the target already the deployed LLM?
    local already_deployed=0 existing
    for existing in "${current_llms[@]}"; do
        [[ "$existing" == "$model" ]] && already_deployed=1
    done

    # Undeploy every current LLM that is NOT the target, blocking until gone so
    # its cores/balloons release before any (re)deploy.
    for existing in "${current_llms[@]}"; do
        [[ "$existing" == "$model" ]] && continue
        log_info "  Undeploying LLM: $existing"
        if ! "$MODEL_MANAGER" undeploy "$existing" --wait --wait-timeout "$MODEL_UNDEPLOY_WAIT"; then
            log_error "  Undeploy of '$existing' failed."
            return 1
        fi
    done

    if [[ $already_deployed -eq 1 ]]; then
        log_info "  '$model' is already deployed — using it as-is (no redeploy)."
    else
        # Detect embed / rerank cpu — inputs to the calculator.
        local embed_cpu=0 rerank_cpu=0 name c
        while IFS= read -r name; do
            [[ -z "$name" ]] && continue
            c=$(resolve_model_field "$name" "cpu" 2>/dev/null || echo 0)
            embed_cpu=$((embed_cpu + c))
        done < <(list_deployed_by_category "embed")
        while IFS= read -r name; do
            [[ -z "$name" ]] && continue
            c=$(resolve_model_field "$name" "cpu" 2>/dev/null || echo 0)
            rerank_cpu=$((rerank_cpu + c))
        done < <(list_deployed_by_category "rerank")

        # Probe topology and compute sizing for the incoming LLM.
        local nodes_json
        nodes_json=$(probe_topology_json) || return 1

        CALC_CPU="" CALC_MEMORY="" CALC_REPLICAS=""
        compute_sizing "$model" "$nodes_json" "$embed_cpu" "$rerank_cpu" || return 1

        log_info "  Deploying '${model}' (timeout ${MODEL_SWITCH_TIMEOUT}s)..."
        if ! "$MODEL_MANAGER" deploy "$model" \
                --cpu "$CALC_CPU" \
                --memory "$CALC_MEMORY" \
                --replicas "$CALC_REPLICAS" \
                --env "OMP_NUM_THREADS=${CALC_CPU}" \
                --wait --wait-timeout "$MODEL_SWITCH_TIMEOUT"; then
            log_error "  Deploy of '${model}' failed."
            return 1
        fi

        # model-manager --wait returns on first replica; block until all N Ready.
        wait_all_replicas_ready "$model" "$CALC_REPLICAS" || return 1
    fi

    # Point llm-svc (RAG's LLM step) at the served model so the gateway route
    # resolves, then fail fast if the RAG's view is still stale.
    sync_rag_llm_model "$model" || return 1
    validate_rag_llm_config "$model" || return 1

    log_ok "Model '${model}' is served and ready."
}

# ─────────────────────────────────────────────────────────────────────────────
# eRAG configuration helpers
# ─────────────────────────────────────────────────────────────────────────────
set_top_n() {
    log_info "Configuring reranker top_n=$1 …"
    if [[ $DRY_RUN -eq 1 ]]; then
        log_info "[DRY-RUN] Would run: bash ${SCRIPT_DIR}/prepare_change_reranker.sh $1"
        return
    fi
    bash "${SCRIPT_DIR}/prepare_change_reranker.sh" "$1"
}

set_k() {
    log_info "Configuring retriever k=$1 …"
    if [[ $DRY_RUN -eq 1 ]]; then
        log_info "[DRY-RUN] Would run: bash ${SCRIPT_DIR}/prepare_change_retriever.sh $1"
        return
    fi
    bash "${SCRIPT_DIR}/prepare_change_retriever.sh" "$1"
}

set_max_new_tokens() {
    log_info "Configuring LLM max_new_tokens=$1 …"
    if [[ $DRY_RUN -eq 1 ]]; then
        log_info "[DRY-RUN] Would run: bash ${SCRIPT_DIR}/prepare_change_max_tokens.sh $1"
        return
    fi
    bash "${SCRIPT_DIR}/prepare_change_max_tokens.sh" "$1"
}

# ─────────────────────────────────────────────────────────────────────────────
# Benchmark sweep
# ─────────────────────────────────────────────────────────────────────────────
run_sweep() {
    local model_name="$1"
    local hf_model="$2"
    local RESULTS_DIR="$3"

    log_sep
    mkdir -p "${RESULTS_DIR}"
    log_info "Model            : ${model_name}"
    log_info "Tokenizer (-m)   : ${hf_model}"
    log_info "Results directory: ${RESULTS_DIR}"

    local total_token_pairs=${#SWEEP_INPUT_TOKENS[@]}
    local total_runs=$(( ${#SWEEP_TOP_N[@]} * ${#SWEEP_K[@]} * total_token_pairs * ${#SWEEP_Q_FILES[@]} * ${#SWEEP_USERS[@]} ))
    log_info "Sweep dimensions:"
    log_info "  top_n values     : ${SWEEP_TOP_N[*]}"
    log_info "  k values         : ${SWEEP_K[*]}"
    log_info "  token pairs      : $(for i in "${!SWEEP_INPUT_TOKENS[@]}"; do printf "in%s/out%s " "${SWEEP_INPUT_TOKENS[$i]}" "${SWEEP_OUTPUT_TOKENS[$i]}"; done)"
    log_info "  question files   : ${SWEEP_Q_FILES[*]}"
    log_info "  user counts      : ${SWEEP_USERS[*]}"
    log_info "  duration per run : ${BENCHMARK_DURATION}"
    log_info "  total runs       : ${total_runs}"
    log_sep

    local current_run=0
    local prev_top_n="" prev_k="" prev_output_tokens=""

    for top_n in "${SWEEP_TOP_N[@]}"; do
      for k in "${SWEEP_K[@]}"; do

        # Only call the API when the value actually changes
        if [[ "$top_n" != "$prev_top_n" ]]; then
            set_top_n "$top_n"
            prev_top_n="$top_n"
        fi
        if [[ "$k" != "$prev_k" ]]; then
            set_k "$k"
            prev_k="$k"
        fi

        for tok_idx in "${!SWEEP_INPUT_TOKENS[@]}"; do
            local input_tokens="${SWEEP_INPUT_TOKENS[$tok_idx]}"
            local output_tokens="${SWEEP_OUTPUT_TOKENS[$tok_idx]}"

            if [[ "$output_tokens" != "$prev_output_tokens" ]]; then
                set_max_new_tokens "$output_tokens"
                prev_output_tokens="$output_tokens"
            fi

          for q_file in "${SWEEP_Q_FILES[@]}"; do
            for users in "${SWEEP_USERS[@]}"; do
                current_run=$((current_run + 1))

                local label="run${current_run}_users${users}_in${input_tokens}_out${output_tokens}_topn${top_n}_k${k}_${q_file}"
                local run_dir="${RESULTS_DIR}/${label}"
                mkdir -p "${run_dir}"

                log_sep
                log_info "Run ${current_run}/${total_runs}: ${label}"
                log_info "Results directory: ${run_dir}"

                # Regenerate tokens before every run – tokens expire after ~3 hours
                generate_tokens "$users"

                local benchmark_cmd=(
                    python3 "${SCRIPT_DIR}/benchmark.py"
                    -f "${SCRIPT_DIR}/${q_file}.csv"
                    -d "${BENCHMARK_DURATION}"
                    -c "${users}"
                    -b "${UAT_FILE}"
                    -m "${hf_model}"
                    -x "${input_tokens}"
                    -s "https://${ERAG_DOMAIN}/api/v1/chatqna"
                )

                log_info "Command: ${benchmark_cmd[*]}"

                if [[ $DRY_RUN -eq 1 ]]; then
                    log_info "[DRY-RUN] Skipping actual benchmark execution."
                    continue
                fi

                # Each benchmark.py execution runs in its own labelled subdirectory.
                # benchmark.py writes bench_<timestamp>_users-N_delay-Xs.csv into cwd.
                # Use || rc=$? to prevent set -e from killing the sweep on benchmark failure.
                local rc=0
                (
                    cd "${run_dir}" || exit 1
                    "${benchmark_cmd[@]}"
                ) || rc=$?

                if [[ $rc -ne 0 ]]; then
                    log_warn "Run ${current_run} exited with code ${rc}."
                    echo "${label}: FAILED (exit ${rc})" >> "${RESULTS_DIR}/sweep_summary.txt"
                else
                    log_ok "Run ${current_run} completed. Results in: ${run_dir}"
                    echo "${label}: OK" >> "${RESULTS_DIR}/sweep_summary.txt"
                fi

            done   # users
          done   # q_file
        done   # token pairs
      done   # k
    done   # top_n

    log_sep
    log_info "All ${total_runs} runs finished."

    if [[ $DRY_RUN -eq 0 ]]; then
        log_info "Sweep summary:"
        cat "${RESULTS_DIR}/sweep_summary.txt" 2>/dev/null || true

        # Aggregate parse.py output from all run dirs into one CSV
        if [[ -f "${SCRIPT_DIR}/parse.py" ]]; then
            log_info "Parsing all run results into ${RESULTS_DIR}/parsed_results.csv …"
            local first=1
            for run_dir_item in "${RESULTS_DIR}"/run*; do
                [[ -d "$run_dir_item" ]] || continue
                if [[ $first -eq 1 ]]; then
                    python3 "${SCRIPT_DIR}/parse.py" "${run_dir_item}" \
                        >> "${RESULTS_DIR}/parsed_results.csv" 2>/dev/null && first=0
                else
                    python3 "${SCRIPT_DIR}/parse.py" "${run_dir_item}" 2>/dev/null \
                        | tail -n +2 >> "${RESULTS_DIR}/parsed_results.csv"
                fi
            done
            if [[ -f "${RESULTS_DIR}/parsed_results.csv" ]]; then
                log_ok "Parsed results: ${RESULTS_DIR}/parsed_results.csv"
            else
                log_warn "No parsed results generated."
            fi
        fi
    fi
}

# ─────────────────────────────────────────────────────────────────────────────
# Entry point
# ─────────────────────────────────────────────────────────────────────────────
main() {
    echo -e "${BOLD}"
    echo "======================================================"
    echo " Chatbot Toolkit ChatQnA – Benchmark Sweep"
    echo " $(date)"
    if [[ $DRY_RUN -eq 1 ]]; then
        echo " *** DRY-RUN MODE – no commands will be executed ***"
    fi
    echo "======================================================"
    echo -e "${RESET}"

    check_prerequisites
    if [[ $SKIP_VECTORS -eq 1 ]]; then
        log_info "Skipping vector DB preparation (--skip-vectors)."
    else
        check_and_prepare_vectors
    fi

    # One timestamp for the whole multi-model run; each model gets its own
    # results_<ts>_<model>/ subdirectory.
    local run_ts
    run_ts="$(date +%Y%m%d_%H%M%S)"

    local model model_id results_dir
    for model in "${SWEEP_MODELS[@]}"; do
        model_id="$(resolve_model_id "$model")"
        results_dir="${RESULTS_ROOT:-${SCRIPT_DIR}/results_${run_ts}_${model}}"
        if [[ -n "${RESULTS_ROOT}" && ${#SWEEP_MODELS[@]} -gt 1 ]]; then
            # If the user pinned RESULTS_DIR explicitly, still separate per model
            # so results do not collide.
            results_dir="${RESULTS_ROOT}/${model}"
        fi

        switch_model "$model" || {
            log_error "Model switch to '${model}' failed. Aborting sweep."
            exit 1
        }

        run_sweep "$model" "$model_id" "$results_dir"
    done

    log_ok "All models finished."
}

main "$@"
