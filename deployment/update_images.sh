#!/bin/bash
# Copyright (C) 2024-2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

REGISTRY_NAME=localhost:5000
REGISTRY_PATH=erag
TAG=latest
IMAGE_PREFIX="chatbot-toolkit-"
_max_parallel_jobs=4

components_to_build=()
failed_components=()
successful_components=()

repo_path=$(realpath "$(pwd)/../")
images_yaml="$repo_path/deployment/images.yaml"
logs_dir="$repo_path/deployment/logs"
mkdir -p $logs_dir

summary_log="$logs_dir/build_summary_$(date +%Y%m%d_%H%M%S).log"
touch "$summary_log"

# Clean up any leftover temporary files from previous runs
rm -f "${logs_dir}/failed_components.tmp" "${logs_dir}/successful_components.tmp"

log_info() {
    local message="[INFO] $(date '+%Y-%m-%d %H:%M:%S') - $1"
    echo "$message"
    echo "$message" >> "$summary_log"
}

log_error() {
    local message="[ERROR] $(date '+%Y-%m-%d %H:%M:%S') - $1"
    echo "$message" >&2
    echo "$message" >> "$summary_log"
}

log_success() {
    local message="[SUCCESS] $(date '+%Y-%m-%d %H:%M:%S') - $1"
    echo "$message"
    echo "$message" >> "$summary_log"
}

log_warning() {
    local message="[WARNING] $(date '+%Y-%m-%d %H:%M:%S') - $1"
    echo "$message"
    echo "$message" >> "$summary_log"
}

if ! command -v yq &> /dev/null; then
    log_error "'yq' is not installed."
    echo "Please install yq using one of the following commands:"
    echo -e "\tsudo apt install yq\t\t# Ubuntu"
    echo -e "\tpip install yq     \t\t# python version"
    exit 1
fi

if [ ! -f "$images_yaml" ]; then
    log_error "images.yaml not found at $images_yaml"
    echo "Please check out it from the repository."
    exit 1
fi

# Detect container runtime: docker -> nerdctl -> error
if command -v docker &> /dev/null; then
    CONTAINER_CLI="docker"
elif command -v nerdctl &> /dev/null; then
    CONTAINER_CLI="nerdctl"
else
    log_error "No container runtime found. Install docker or nerdctl (containerd)."
    echo "After kubespray localhost deployment, docker is not available."
    echo "Install nerdctl: https://github.com/containerd/nerdctl"
    exit 1
fi
log_info "Using container runtime: $CONTAINER_CLI"

default_components=($(yq -r '.images | keys | .[]' "$images_yaml"))

# only owner - read, write, and execute
chmod 700 $logs_dir

use_proxy=""
no_cache=""

[ -n "$https_proxy" ] && use_proxy+="--build-arg https_proxy=$https_proxy "
[ -n "$http_proxy" ] && use_proxy+="--build-arg http_proxy=$http_proxy "
[ -n "$no_proxy" ] && use_proxy+="--build-arg no_proxy=$no_proxy "

usage() {
    echo -e "Usage: $0 [OPTIONS] [COMPONENTS...]"
    echo -e "Options:"
    echo -e "\t--build: Build specified components."
    echo -e "\t-j|--jobs <N>: max number of parallel builds (default is $_max_parallel_jobs)."
    echo -e "\t--push: Push specified components to the registry."
    echo -e "\t--setup-registry: Set up a dev-only local registry at port 5000 (persists across restarts/reboots)."
    echo -e "\t--teardown-registry: Remove the local registry (developer-initiated shutdown; keeps image data)."
    echo -e "\t--purge-data: With --teardown-registry, also delete pushed image data in /var/lib/local-registry."
    echo -e "\t--registry: Specify the registry (default is $REGISTRY_NAME)."
    echo -e "\t--tag: Specify the tag version (default is latest)."
    echo -e "\t--use-alternate-tagging: Enable repo:component_tag tagging format instead of the default (repo/component:tag)."
    echo -e "\t\tCan be useful for using a single Docker repository to store multiple images."
    echo -e "\t\tExample: repo/erag:gmcrouter_1.2 instead of repo/erag/gmcrouter:1.2."
    echo -e "\t--no-cache: Build images without using docker cache."
    echo -e "\t--registry-path: Specify the registry path (default is $REGISTRY_PATH)."
    echo -e "Components available (default is all):"
    echo -e "\t ${default_components[*]}"
    echo -e "Example: $0 --build --push --registry my-registry embedding-usvc reranking-usvc"
}

# Dev-only local registry. Not part of a default deployment: it only exists
# when a developer opts in with --setup-registry, and once set up it must
# survive containerd restarts, kubelet garbage collection, cluster teardowns,
# and reboots until the developer removes it with --teardown-registry.
LOCAL_REGISTRY_NAME=local-registry
REGISTRY_PORT=5000
REGISTRY_IMAGE=registry:2
# Dedicated containerd namespace keeps the registry out of the kubelet-managed
# 'k8s.io' pool so image GC can never evict it (nerdctl runtime only).
REGISTRY_NAMESPACE=registry
# Host path for pushed images, so data persists across container recreation.
REGISTRY_DATA_DIR=/var/lib/local-registry
SYSTEMD_UNIT=local-registry.service
SYSTEMD_UNIT_PATH="/etc/systemd/system/${SYSTEMD_UNIT}"

# Populated by resolve_registry_endpoint from REGISTRY_NAME (default or --registry):
#   REGISTRY_HOST   host part (e.g. localhost or a node IP/hostname)
#   REGISTRY_PORT   port part (default 5000)
#   REGISTRY_LISTEN_ADDR  address registry:2 binds. Loopback for localhost so the
#                   registry is NOT exposed on the network; 0.0.0.0 for a routable
#                   host so other cluster nodes can reach it (multi-node).
#   CERTS_D_DIR     containerd certs.d dir, keyed on the actual host:port.
REGISTRY_HOST=localhost
REGISTRY_LISTEN_ADDR="127.0.0.1:${REGISTRY_PORT}"
CERTS_D_DIR="/etc/containerd/certs.d/localhost:${REGISTRY_PORT}"

# sudo wrapper: no-op when already root, otherwise prefix sudo.
_sudo() {
    if [ "$(id -u)" -eq 0 ]; then
        "$@"
    else
        sudo "$@"
    fi
}

# Parse REGISTRY_NAME (host[:port]) into host/port/listen-addr/certs.d. A bare
# 'localhost' binds loopback only (single-node, not network-exposed); any other
# host is treated as routable and binds all interfaces so worker nodes can pull
# (multi-node). Skips remote registries (aws/custom) - those are not local.
resolve_registry_endpoint() {
    REGISTRY_HOST="${REGISTRY_NAME%%:*}"
    if [ "$REGISTRY_NAME" != "$REGISTRY_HOST" ]; then
        REGISTRY_PORT="${REGISTRY_NAME##*:}"
    fi
    if [ "$REGISTRY_HOST" = "localhost" ] || [ "$REGISTRY_HOST" = "127.0.0.1" ]; then
        REGISTRY_LISTEN_ADDR="127.0.0.1:${REGISTRY_PORT}"
    else
        # Routable endpoint: bind all interfaces so other nodes can reach it.
        REGISTRY_LISTEN_ADDR="0.0.0.0:${REGISTRY_PORT}"
        log_warning "Registry ${REGISTRY_HOST}:${REGISTRY_PORT} binds 0.0.0.0 and is UNAUTHENTICATED - only use on a trusted network."
    fi
    CERTS_D_DIR="/etc/containerd/certs.d/${REGISTRY_HOST}:${REGISTRY_PORT}"
}

wait_for_registry() {
    if ! command -v curl &> /dev/null; then
        log_warning "curl not found - skipping readiness check; assuming registry is up."
        return 0
    fi
    local probe_host="$REGISTRY_HOST"
    [ "$REGISTRY_LISTEN_ADDR" = "127.0.0.1:${REGISTRY_PORT}" ] && probe_host=127.0.0.1
    local i
    for i in $(seq 1 15); do
        if curl -sf --noproxy '*' "http://${probe_host}:${REGISTRY_PORT}/v2/" >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    return 1
}

# Tell containerd to pull from the local registry over plain HTTP. containerd
# reads certs.d dynamically per pull, so this needs no containerd restart and
# does not disturb a running cluster. Reproduces the config on a fresh machine.
configure_insecure_registry() {
    if [ ! -d /etc/containerd ]; then
        log_warning "/etc/containerd not found - skipping insecure-registry config (is containerd installed?)."
        return 0
    fi
    log_info "Configuring containerd to trust ${REGISTRY_HOST}:${REGISTRY_PORT} (plain HTTP)"
    _sudo mkdir -p "$CERTS_D_DIR" || return 1
    _sudo tee "${CERTS_D_DIR}/hosts.toml" >/dev/null <<EOF || return 1
server = "http://${REGISTRY_HOST}:${REGISTRY_PORT}"

[host."http://${REGISTRY_HOST}:${REGISTRY_PORT}"]
  capabilities = ["pull", "resolve"]
  skip_verify = true
EOF
}

# nerdctl path: containerd has no daemon to restart containers, so a systemd
# unit owns the registry lifecycle (auto-start on boot and after containerd
# restarts). Falls back to a plain restart-always container when sudo/systemd
# is unavailable (still persistent, just not boot-managed).
# Pre-pull registry:2 into the target containerd namespace using the current
# shell's proxy. The systemd unit runs with an empty environment (no proxy), so
# it cannot pull from docker.io itself on proxy-bound hosts; and containerd
# namespaces are isolated, so an image in k8s.io is not visible in 'registry'.
# Pulling here means the unit starts from the local image store, offline.
ensure_registry_image() {
    # Query with the same privilege as the pull; the containerd socket needs root.
    if _sudo nerdctl --namespace "$REGISTRY_NAMESPACE" images -q "$REGISTRY_IMAGE" 2>/dev/null | grep -q .; then
        log_info "$REGISTRY_IMAGE already present in namespace '${REGISTRY_NAMESPACE}'"
        return 0
    fi
    log_info "Pulling $REGISTRY_IMAGE into namespace '${REGISTRY_NAMESPACE}'"
    if _sudo env \
        ${http_proxy:+http_proxy="$http_proxy"} \
        ${https_proxy:+https_proxy="$https_proxy"} \
        ${no_proxy:+no_proxy="$no_proxy"} \
        nerdctl --namespace "$REGISTRY_NAMESPACE" pull "$REGISTRY_IMAGE"; then
        log_success "Pulled $REGISTRY_IMAGE"
    else
        log_error "Failed to pull $REGISTRY_IMAGE. Check network/proxy (http_proxy=${http_proxy:-unset})."
        return 1
    fi
}

_can_sudo_noninteractive() {
    [ "$(id -u)" -eq 0 ] && return 0
    command -v sudo &> /dev/null && sudo -n true &> /dev/null
}

setup_local_registry_nerdctl() {
    ensure_registry_image || return 1

    local use_systemd=false
    if command -v systemctl &> /dev/null; then
        if _can_sudo_noninteractive; then
            use_systemd=true
        elif command -v sudo &> /dev/null && [ "$(id -u)" -ne 0 ]; then
            # Prompt visibly, once, rather than hanging on a hidden prompt.
            log_info "Root privileges are required to install the systemd-managed registry."
            sudo -v && use_systemd=true
        fi
    fi

    if [ "$use_systemd" = true ]; then
        if _sudo systemctl is-active --quiet "$SYSTEMD_UNIT"; then
            log_warning "$SYSTEMD_UNIT already active. Existing registry will be used."
        else
            log_info "Installing systemd-managed local registry (namespace=${REGISTRY_NAMESPACE}, listen=${REGISTRY_LISTEN_ADDR})"
            local unit_src="${repo_path}/deployment/scripts/${SYSTEMD_UNIT}"
            if [ ! -f "$unit_src" ]; then
                log_error "Unit template not found: $unit_src"
                return 1
            fi
            _sudo mkdir -p "$REGISTRY_DATA_DIR" || return 1
            local nerdctl_bin; nerdctl_bin=$(command -v nerdctl)
            # All substitution values are script-controlled; guard the binary
            # path against the sed delimiter just in case.
            case "$nerdctl_bin" in
                /*) : ;;
                *)  log_error "nerdctl path is not absolute: '$nerdctl_bin'"; return 1 ;;
            esac
            case "$nerdctl_bin$REGISTRY_DATA_DIR" in
                *'|'*) log_error "Registry paths must not contain '|'"; return 1 ;;
            esac
            sed -e "s|@NERDCTL@|${nerdctl_bin}|g" \
                -e "s|@NAMESPACE@|${REGISTRY_NAMESPACE}|g" \
                -e "s|@DATA_DIR@|${REGISTRY_DATA_DIR}|g" \
                -e "s|@LISTEN_ADDR@|${REGISTRY_LISTEN_ADDR}|g" \
                "$unit_src" | _sudo tee "$SYSTEMD_UNIT_PATH" >/dev/null || return 1
            _sudo systemctl daemon-reload || return 1
            _sudo systemctl enable --now "$SYSTEMD_UNIT" || { log_error "Failed to start ${SYSTEMD_UNIT}."; return 1; }
        fi
    else
        log_warning "systemd/sudo unavailable - starting a restart-always container instead (not boot-managed)."
        if [ "$(_sudo nerdctl --namespace "$REGISTRY_NAMESPACE" ps -q -f name="$LOCAL_REGISTRY_NAME")" ]; then
            log_warning "$LOCAL_REGISTRY_NAME already running. Existing registry will be used."
        else
            _sudo nerdctl --namespace "$REGISTRY_NAMESPACE" rm -f "$LOCAL_REGISTRY_NAME" &> /dev/null || true
            _sudo mkdir -p "$REGISTRY_DATA_DIR" || return 1
            _sudo nerdctl --namespace "$REGISTRY_NAMESPACE" run -d --restart=always --pull=never --net=host \
                --name "$LOCAL_REGISTRY_NAME" -e "REGISTRY_HTTP_ADDR=${REGISTRY_LISTEN_ADDR}" \
                -v "${REGISTRY_DATA_DIR}:/var/lib/registry" "$REGISTRY_IMAGE" || return 1
        fi
    fi
    configure_insecure_registry || return 1
}

# docker path: the docker daemon already persists containers across restarts
# and reboots, so --restart=always plus a host volume is sufficient. The port
# publish is scoped to the resolved listen address (loopback for localhost, all
# interfaces for a routable host) so the registry is not silently network-exposed.
setup_local_registry_docker() {
    if [ "$(docker ps -q -f name="$LOCAL_REGISTRY_NAME")" ]; then
        log_warning "$LOCAL_REGISTRY_NAME already running. Existing registry will be used."
    else
        log_info "Starting $LOCAL_REGISTRY_NAME (docker, restart=always, listen=${REGISTRY_LISTEN_ADDR})"
        docker rm -f "$LOCAL_REGISTRY_NAME" &> /dev/null || true
        mkdir -p "$REGISTRY_DATA_DIR" || return 1
        local publish="${REGISTRY_LISTEN_ADDR%:*}:${REGISTRY_PORT}:${REGISTRY_PORT}"
        docker run -d --restart=always \
            --name "$LOCAL_REGISTRY_NAME" -p "$publish" \
            -v "${REGISTRY_DATA_DIR}:/var/lib/registry" "$REGISTRY_IMAGE" || return 1
    fi
    # A containerd-based cluster coexisting with a docker daemon still needs the
    # certs.d trust entry to pull from this registry.
    configure_insecure_registry || return 1
}

effective_runtime() {
    if [ "$CONTAINER_CLI" = "nerdctl" ]; then
        echo "nerdctl"; return
    fi
    if "$CONTAINER_CLI" --version 2>/dev/null | grep -qi nerdctl; then
        echo "nerdctl"; return
    fi
    echo "docker"
}

setup_local_registry() {
    # Honor --registry (default localhost:5000). localhost binds loopback only;
    # a routable host:port binds all interfaces for multi-node access.
    resolve_registry_endpoint
    local runtime; runtime=$(effective_runtime)
    log_info "Setting up local registry ${REGISTRY_HOST}:${REGISTRY_PORT} (cli=${CONTAINER_CLI}, effective runtime=${runtime})..."

    if [ "$runtime" = "docker" ]; then
        setup_local_registry_docker || return 1
    else
        setup_local_registry_nerdctl || return 1
    fi

    if wait_for_registry; then
        log_success "Local registry is up at ${REGISTRY_NAME}"
    else
        log_error "Local registry did not become ready on ${REGISTRY_NAME}. Check 'systemctl status ${SYSTEMD_UNIT}' or the container logs."
        return 1
    fi
}

# Explicit, developer-initiated removal - the only path that shuts the registry
# down. Leaves pushed image data in REGISTRY_DATA_DIR unless --purge-data given.
teardown_local_registry() {
    # Resolve so CERTS_D_DIR matches the endpoint that was set up (--registry).
    resolve_registry_endpoint
    local runtime; runtime=$(effective_runtime)
    log_info "Tearing down local registry ${REGISTRY_HOST}:${REGISTRY_PORT} (cli=${CONTAINER_CLI}, effective runtime=${runtime})..."

    if command -v systemctl &> /dev/null && _sudo test -f "$SYSTEMD_UNIT_PATH"; then
        _sudo systemctl disable --now "$SYSTEMD_UNIT" &> /dev/null || true
        _sudo rm -f "$SYSTEMD_UNIT_PATH"
        _sudo systemctl daemon-reload
        log_success "Removed systemd unit ${SYSTEMD_UNIT}"
    fi

    # Remove any standalone container in the runtime/namespace it would live in.
    if [ "$runtime" = "docker" ]; then
        docker rm -f "$LOCAL_REGISTRY_NAME" &> /dev/null || true
    else
        _sudo nerdctl --namespace "$REGISTRY_NAMESPACE" rm -f "$LOCAL_REGISTRY_NAME" &> /dev/null || true
    fi

    _sudo rm -rf "$CERTS_D_DIR"

    if [ "$purge_data_flag" = true ]; then
        _sudo rm -rf "$REGISTRY_DATA_DIR"
        log_success "Removed registry data at ${REGISTRY_DATA_DIR}"
    else
        log_info "Registry image data left at ${REGISTRY_DATA_DIR} (use --purge-data to delete)."
    fi
    log_success "Local registry torn down"
}

tag_and_push() {
    if [[ "$do_push_flag" == false ]]; then
        log_info "Skipping push for $3 (push flag disabled)"
        return 0
    fi

    local registry_url=$1
    local repo_name=$2
    local image=$3
    local log_file=$4

    local full_image_name="${repo_name}/${image}:${TAG}"

    log_info "Starting push for $full_image_name"

    if [[ "$registry_url" == *"aws"* ]]; then
        log_info "Checking if repository ${repo_name}/${image} exists in ${registry_url}"
        aws ecr describe-repositories --repository-names "${repo_name}/${image}" > /dev/null 2>&1
        if [ $? -eq 0 ]; then
            log_info "Repository ${repo_name}/${image} exists in ${registry_url}"
        else
            log_info "Repository ${repo_name}/${image} does not exist in ${registry_url}. Creating it..."
            aws ecr create-repository --repository-name "${repo_name}/${image}" > /dev/null 2>&1
        fi
    fi

    log_info "Tagging image: $CONTAINER_CLI tag ${full_image_name} ${registry_url}/${full_image_name}"
    $CONTAINER_CLI tag "${full_image_name}" "${registry_url}/${full_image_name}"

    log_info "Pushing image: ${registry_url}/${full_image_name}"
    $CONTAINER_CLI push "${registry_url}/${full_image_name}" &> "${log_file}"

    if [ $? -eq 0 ]; then
        log_success "$full_image_name pushed successfully"
        return 0
    else
        log_error "Push failed for $full_image_name. Check logs at ${log_file}"
        return 1
    fi
}

docker_login_aws() {
    local region=""
    local aws_account_id=""

    log_info "Attempting AWS ECR login..."

    region=$(aws configure get region)
    aws_account_id=$(aws sts get-caller-identity --query "Account" --output text)

    if [ -z "$region" ] || [ -z "$aws_account_id" ]; then
        log_error "AWS region or account ID could not be determined."
        echo "Please login to aws to be able to pull or push images"
        exit 1
    fi

    local ecr_registry_url="${aws_account_id}.dkr.ecr.${region}.amazonaws.com"
    local ecr_password=""
    ecr_password=$(aws ecr get-login-password --region "$region")

    echo "${ecr_password}" | \
    $CONTAINER_CLI login --username AWS --password-stdin "${ecr_registry_url}" > /dev/null 2>&1

    if [ $? -eq 0 ]; then
        log_success "AWS ECR login successful"
    else
        log_error "AWS ECR login failed"
    fi

    echo "${ecr_registry_url}"
}

build_component() {
    if [[ "$do_build_flag" == false ]]; then
        log_info "Skipping build for $4 (build flag disabled)"
        return 0
    fi

    local component_path=$1
    local dockerfile_path=${2#$component_path/}
    local repo_name=$3
    local image=$4
    local log_file=$5
    local build_args=${6:-""}

    local full_image_name="${repo_name}/${image}:${TAG}"

    log_info "Starting build for $full_image_name"
    log_info "Build context: ${repo_path}/${component_path}"
    log_info "Dockerfile: ${dockerfile_path}"

    cd "${repo_path}/${component_path}"
    $CONTAINER_CLI build -t ${full_image_name} ${use_proxy} -f ${dockerfile_path} . ${build_args} ${no_cache} --progress=plain &> "${log_file}"

    if [ $? -eq 0 ]; then
        log_success "$full_image_name built successfully"
        return 0
    else
        log_error "Build failed for $full_image_name. Check logs at ${log_file}"
        return 1
    fi
}

get_content() {
    local component="$1"
    local field="$2"
    local value

    value=$(yq -r '.images["'"$component"'"].'"$field" "$images_yaml" 2>/dev/null)
    [[ "$value" == "null" ]] && value=""

    echo "$value"
}

do_build_flag=false
do_push_flag=false
setup_registry_flag=false
teardown_registry_flag=false
purge_data_flag=false

while [ $# -gt 0 ]; do
    case "$1" in
        --build)
            do_build_flag=true
            ;;
        --push)
            do_push_flag=true
            ;;
        --setup-registry)
            setup_registry_flag=true
            ;;
        --teardown-registry)
            teardown_registry_flag=true
            ;;
        --purge-data)
            purge_data_flag=true
            ;;
        --registry)
            shift
            REGISTRY_NAME=${1}
            ;;
        --tag)
            shift
            TAG=${1}
            ;;
        -j|--jobs)
            shift
            if { [ -n "$1" ] && [ "$1" -eq "$1" ] ; } &> /dev/null; then
                _max_parallel_jobs=${1}
            else
                log_warning "The input '${1}' is not a valid number. Setting number of max parallel jobs to the default value of ${_max_parallel_jobs}."
            fi
            ;;
        --no-cache)
            no_cache="--no-cache"
            ;;
        --registry-path)
            shift
            REGISTRY_PATH=${1}
            ;;
        --help)
            usage
            exit 0
            ;;
        *)
            # Check if $1 is in default_components
            if [[ " ${default_components[*]} " == *" $1 "* ]]; then
                components_to_build+=("$1")
            else
                log_warning "'$1' is not a valid component and it will be ignored. Run '$0 --help' to see available components."
            fi
            ;;
    esac
    shift
done

if [ ${#components_to_build[@]} -eq 0 ]; then
    log_info "No specific components provided, using all default components"
    components_to_build=("${default_components[@]}")
    log_info "Default components to build: ${components_to_build[*]}"
fi

log_info "=== BUILD CONFIGURATION ==="
log_info "Registry: $REGISTRY_NAME"
log_info "Build enabled: $do_build_flag"
log_info "Push enabled: $do_push_flag"
log_info "Max parallel jobs: $_max_parallel_jobs"
log_info "Tag version: $TAG"
log_info "Registry path: $REGISTRY_PATH"
log_info "Components to build: ${components_to_build[*]}"
log_info "Summary log: $summary_log"

if $teardown_registry_flag; then
    teardown_local_registry
    exit $?
fi

if $setup_registry_flag; then
    setup_local_registry || exit 1
fi

if [ ${#components_to_build[@]} -eq 0 ]; then
    log_info "No specific components provided, using all default components"
    components_to_build=("${default_components[@]}")
fi

log_info "=== STARTING BUILD PROCESS ==="
log_info "Processing ${#components_to_build[@]} components"

count_current_jobs=0

for component in "${components_to_build[@]}"; do

    log_info "Processing component: $component"

    (
        component_success=true
        failed_log=""

        docker_context=$(get_content "$component" "docker_context")
        dockerfile_path=$(get_content "$component" "dockerfile_path")
        image_name=$(get_content "$component" "image_name")

        if [[ -z "$docker_context" || -z "$dockerfile_path" || -z "$image_name" ]]; then
            log_error "Required field is missing for component '$component'. Values:"
            log_error "  docker_context: $docker_context"
            log_error "  dockerfile_path: $dockerfile_path"
            log_error "  image_name: $image_name"
            echo "$component" >> "${logs_dir}/failed_components.tmp"
            exit 1
        fi

        image_name="${IMAGE_PREFIX}${image_name}"
        build_log="${logs_dir}/build_${image_name}:${TAG}.log"
        push_log="${logs_dir}/push_${image_name}:${TAG}.log"

        if $do_build_flag; then
            if ! build_component "${docker_context}" "$dockerfile_path" "$REGISTRY_PATH" "$image_name" "$build_log"; then
                component_success=false
                failed_log=$build_log
            fi
        fi

        if $do_push_flag && $component_success; then
            if ! tag_and_push "$REGISTRY_NAME" "$REGISTRY_PATH" "$image_name" "$push_log"; then
                component_success=false
                failed_log=$push_log
            fi
        fi

        if $component_success; then
            echo "$component" >> "${logs_dir}/successful_components.tmp"
            log_success "Component '$component' completed successfully"
        else
            echo "$component $failed_log" >> "${logs_dir}/failed_components.tmp"
            log_error "Component '$component' failed"
        fi
    ) &

    count_current_jobs=$((count_current_jobs + 1))
    if [ "$count_current_jobs" -ge "$_max_parallel_jobs" ]; then
        wait -n
        count_current_jobs=$((count_current_jobs - 1))
    fi

done

wait

log_info "=== BUILD SUMMARY ==="

if [ -f "${logs_dir}/successful_components.tmp" ]; then
    successful_components=($(cat "${logs_dir}/successful_components.tmp" | sort | uniq))
    log_success "Successfully processed ${#successful_components[@]} components:"
    for comp in "${successful_components[@]}"; do
        log_success "$comp"
    done
    rm -f "${logs_dir}/successful_components.tmp"
else
    log_info "No components were successfully processed"
fi

if [ -f "${logs_dir}/failed_components.tmp" ]; then
    mapfile -t failed_components < <(sort -u "${logs_dir}/failed_components.tmp")
    log_error "Failed to process ${#failed_components[@]} components:"
    for line in "${failed_components[@]}"; do
        read -r comp comp_log <<< "$line"
        log_error "$comp"
        if [ -n "$comp_log" ] && [ -f "$comp_log" ]; then
            log_error "    Last error: $(tail -n 1 "$comp_log")"
            log_error "    Log: $comp_log"
        fi
    done
    rm -f "${logs_dir}/failed_components.tmp"

    log_error "Build completed with errors. Check individual log files in $logs_dir"
    exit 1
else
    log_success "All components processed successfully!"
fi

log_info "=== DETAILED LOGS ==="
log_info "Summary log: $summary_log"
log_info "Individual logs: $logs_dir/build_*.log"
log_info "Push logs: $logs_dir/push_*.log"
