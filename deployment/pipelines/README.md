<!-- Copyright (C) 2026 Intel Corporation -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Modular pipelines - reference guide

This directory holds the **modular pipeline definitions** for Intel® Chatbot
Toolkit. A pipeline describes a GenAI Microservices Connector (GMC) service graph as a set
of small, composable files. At install time the composer
(`../scripts/compose_pipeline.py`) assembles those files into a single `GMConnector`
custom resource plus a matching Helm resources overlay, and the `app_pipeline` role
applies both to the cluster.

This replaces the older model of one static `reference.yaml` GMConnector plus a
monolithic `resources-reference.yaml` per pipeline. Steps and their recommended
resources are now co-located and composed on demand, so variant selection changes both
the graph and the deployed resources in one pass.

## Table of contents

- [Available pipelines and variants](#available-pipelines-and-variants)
- [Two kinds of pipeline folder](#two-kinds-of-pipeline-folder)
- [How the files map to `init` and flavours](#how-the-files-map-to-init-and-flavours)
- [`pipeline.yaml` - pipeline metadata](#pipelineyaml---pipeline-metadata)
- [Unified step file format](#unified-step-file-format)
  - [`serviceName` vs resources key](#servicename-vs-resources-key)
  - [File uploads (`multipartEndpoint`)](#file-uploads-multipartendpoint)
  - [Fingerprint parameters (`paramsKind` / `paramsKey`)](#fingerprint-parameters-paramskind--paramskey)
- [Shared steps (`_shared/steps/`)](#shared-steps-_sharedsteps)
- [Variants (`variants/<name>/`)](#variants-variantsname)
  - [Variant-aware resource aggregation](#variant-aware-resource-aggregation)
- [How the composer and `app_pipeline` consume these files](#how-the-composer-and-app_pipeline-consume-these-files)
- [GMConnector labels](#gmconnector-labels)
- [Adding a new modular pipeline](#adding-a-new-modular-pipeline)
- [Adding a variant to a pipeline](#adding-a-variant-to-a-pipeline)

## Available pipelines and variants

| Flavour | Kind | `pipeline_type` | Variants | Notes |
|---------|------|-----------------|----------|-------|
| `chatqna` | modular | `chatqna` | `base`, `query-rewrite`, `output_guard`, `hybrid-retrieval`, `retrieve-rerank`, `upload` | hybrid-retrieval is not yet deployable; `upload` is ingestion-only (no chat) |
| `docsum` | modular | `docsum` | `base` | document summarization |
| `translation` | modular | `translation` | `base` | language detection + translation |
| `audioqna` | thin flavour | `chatqna` | `base` | ChatQnA + `audio_enabled: true` |
| `pl_chatqna` | thin flavour | `chatqna` | `base` | ChatQnA + Polish language |
| `chatqna_intel_gpu` | thin flavour | `chatqna` | `base` | ChatQnA + `kubernetes_accelerator: intel-gpu` (LLM on Intel GPU, embed/rerank on CPU) — **experimental** |

Base steps live in `_shared/steps/`; a pipeline may still add a local `steps/<id>.yaml.j2`
to override a shared step of the same name (see [Shared steps](#shared-steps-_sharedsteps)).

## Two kinds of pipeline folder

### Modular pipelines (`chatqna`, `docsum`, `translation`)

A modular pipeline owns a `pipeline.yaml` (and optionally `variants/`). Its base steps
are resolved from `_shared/steps/`; the composer builds its GMConnector from these files.
A pipeline may add a local `steps/` directory only to override a shared step.

### Thin flavours (`audioqna`, `pl_chatqna`)

A thin flavour is a `config.yaml`-only folder. It does not define its own graph; it
reuses a modular pipeline's graph through `pipeline_type` and expresses its difference
as flat runtime configuration:

- `audioqna` = ChatQnA graph (`pipeline_type: chatqna`, `pipeline_variant: base`) plus
  `audio_enabled: true`. Audio is a runtime add-on, not a graph
  change, so it is a flat toggle rather than a composer variant.
- `pl_chatqna` = ChatQnA graph plus `solution_language: "pl"` and a Polish LLM.
  Language and model choice are runtime configuration, not a graph change.
- `chatqna_intel_gpu` (**experimental**) = ChatQnA graph plus `kubernetes_accelerator: "intel-gpu"`.
  The accelerator is where models are served, not a graph change: the LLM runs on
  the vLLM-XPU runtime (Intel GPU) while embedding/reranking are pinned back to CPU
  via a per-entry `accelerator: cpu` on their `inference_models` entries. Requires
  the core cluster to be installed with `kubernetes_accelerator=intel-gpu`.

Each thin flavour keeps only its deltas versus ChatQnA plus the path block that a config
surface needs. They remain their own folders (not `chatqna/variants/` subdirectories)
because a flavour is a whole config surface, while a variant is one graph edit selected
through `pipeline_variant`.

## How the files map to `init` and flavours

The core installer's `init` step (`es_auto_installer.sh init erag --flavour <type>`)
copies `pipelines/<flavour>/config.yaml` verbatim into `env/<name>/config.erag.yaml`.
That file is a **flat** override surface loaded via `-e @file`, which shallow-merges -
so every key in `config.yaml` must be a flat scalar. Do not introduce nested structures
such as `pipeline: {type, variant}`; use `pipeline_type` and `pipeline_variant` as
top-level keys.

Whichever route is taken, `config.erag.yaml` is a **flat** override surface loaded via
`-e @file`, which shallow-merges - so every key must be a flat scalar. Do not introduce
nested structures such as `pipeline: {type, variant}`; use `pipeline_type` and
`pipeline_variant` as top-level keys.

Consequently:

- Every flavour folder keeps a `config.yaml`, so it stays copyable as a whole.
- Folders beginning with `_` (for example `_shared`) hold shared building blocks, not
  flavours.
- `variants/` subdirectories are not flavours; they are selected via
  `pipeline_variant`, which is the only opt-in mechanism for a graph change.

`config.yaml` and `pipeline.yaml` are kept separate on purpose. `config.yaml` is the flat
override surface; `pipeline.yaml` is nested metadata loaded via `include_vars`. Merging
them would put nested keys on the flat surface.

## `pipeline.yaml` - pipeline metadata

Nested metadata for a modular pipeline, loaded by `app_pipeline` via `include_vars`.
Top-level keys:

- `name` - GMConnector `metadata.name`.
- `namespace` - GMConnector `metadata.namespace` (also the deploy namespace).
- `description` - free text.
- `base_flow` - ordered list of step ids; each `<id>` resolves to `steps/<id>.yaml.j2`,
  searched first in the pipeline folder and then in `_shared/steps/`, so a pipeline-local
  file overrides a shared one of the same name.
- `endpoints` - the only variable set passed into step templates (the composer injects
  it as `endpoints`). Keep it minimal: model-server wiring
  (`EMBEDDING_MODEL_SERVER_ENDPOINT`, `RERANKING_SERVICE_ENDPOINT`,
  `LLM_MODEL_SERVER_ENDPOINT`, `LLM_MODEL_NAME`, ...) is **not** declared here - it is
  set once in `app_pipeline/templates/values.yaml.j2`, which renders it into each usvc's
  ConfigMap, consumed via `envFrom`. Duplicating it in a step's `config:` adds a second
  source of truth for the same Ansible facts and, for an empty endpoint, makes GMC read
  `""` as a downstream service name and fail reconcile. Declare a key here only when a
  step template actually reads it - currently `endpoints.llm.url` (the query-rewrite
  variant's `QUERY_REWRITE_LLM_ENDPOINT`) plus the shared llm step's `resources`,
  `disable_streaming`, and `downstream` (see
  [Shared steps](#shared-steps-_sharedsteps)).
- `router` - the root node's `name`, `service_name`, and GMC `type` (for example
  `Sequence`).

See [`chatqna/pipeline.yaml`](chatqna/pipeline.yaml) for a complete, working example.

## Unified step file format

Each step file (`_shared/steps/<id>.yaml.j2`, a pipeline-local override, or a variant's
`steps.yaml.j2`) is a Jinja2 template that renders **one YAML document** holding both the
GMC step definition and its recommended chart resources. The composer parses both
sections.

A base step file holds a `step:` mapping (the GMC step definition, with `name`,
`internalService.serviceName`, and its `config`) plus a `resources:` block keyed by the
`<name>-usvc` chart key. See [`_shared/steps/retriever.yaml.j2`](_shared/steps/retriever.yaml.j2)
for a base step, and [`_shared/steps/llm.yaml.j2`](_shared/steps/llm.yaml.j2) for a
`pipeline.yaml`-parameterized one. The recognized keys are:

- `step:` - a single GMC step mapping, appended to the root node's `steps` list in
  `base_flow` order.
- `steps:` - a list of GMC step mappings (used by variant files that emit more than one
  step). Use `step:` OR `steps:`, not both.
- `__nodes__:` (optional) - extra GMC nodes (nested routers such as an `Ensemble` or a
  nested `Sequence`). Merged into `spec.nodes`. Used by variants that build parallel
  branches. See the hybrid-retrieval variant.
- `resources:` (optional) - a map keyed by the microservice chart key (`<name>-usvc`).
  The composer aggregates these across all active steps into a single `services:`
  overlay.

An HPA block may be added under a resources entry (see
`_shared/steps/llm-guard-input.yaml.j2`, which sizes `in-guard-usvc` with an `hpa:`
block). Nested HPA dicts are carried through unchanged.

### `serviceName` vs resources key

The step's `internalService.serviceName` is the GMC-deployed Service name (for example
`llm-svc`, `input-scan-svc`, `prompt-template-svc`), while the `resources` key is the
**chart microservice key** (`<name>-usvc`, for example `llm-usvc`, `in-guard-usvc`,
`prompt-template-usvc`). The two are not mechanically derivable from each other, so the
resources key is declared explicitly. It must match a key under the GMC chart's
`services.<usvc>` consumption (the chart's `getReplicas` helper and per-manifest
resources read `.Values.services.<usvc>`). A wrong key silently drops the sizing and the
pod falls back to chart defaults, so verify new keys against
`components/gmc/values.yaml` and the `manifests_common/<name>-usvc.yaml.tpl` templates.

The composer raises an error if two active step files declare the same resources key,
to catch typos and unintended overrides.

### File uploads (`multipartEndpoint`)

A step whose service can be handed raw files declares a second path in its `config`:

```yaml
config:
  endpoint: /v1/text_extractor                 # JSON requests
  multipartEndpoint: /v1/text_extractor/upload # multipart/form-data requests
```

When a client posts `multipart/form-data` to the router (DocSum file summarization), the
router forwards the body and its content type to the entry step's `multipartEndpoint`
untouched, and calls every later step with JSON as usual. A multipart request carries no
JSON body, so its pipeline parameters travel in a `parameters` form field holding the
same group a JSON request sends under `parameters`. Only the entry step of a `Sequence`
can receive an upload; without `multipartEndpoint` the step keeps being called on
`endpoint`.

### Fingerprint parameters (`paramsKind` / `paramsKey`)

Runtime parameters (LLM `max_new_tokens`, retriever `k`, guard scanners, ...) are not
fetched by a pipeline step. They are stored in the fingerprint Postgres, projected into a
NATS KV bucket by the GMC controller, and overlaid onto the request by the **router**, per
step. There is therefore no `fingerprint` step in any `base_flow`; a step opts in through
two entries in its `internalService.config`:

- `paramsKind` - the parameter group's category, which tells the fingerprint store which
  defaults to seed. It must be one of the kinds the fingerprint microservice serves from
  its catalog: `llm`, `retriever`, `reranker`, `query_rewrite`, `docsum`,
  `prompt_template`, `input_guard`, `output_guard`, `dataprep_guard`. The GMC admission
  webhook fetches that catalog and **rejects** any other value, so a typo fails at
  `kubectl apply` rather than silently. A step that omits `paramsKind` takes no part in
  injection.
- `paramsKey` - the group's row name, so two steps of the same kind (two `Llm` steps, say)
  can be addressed independently. Optional: it defaults to the **lowercased step name**.

The default is a trap for steps whose group name differs from their step name, so set
`paramsKey` explicitly. Current mapping:

| step `name` | `paramsKey` | `paramsKind` |
|---|---|---|
| `Retriever` | `retriever` | `retriever` |
| `Reranking` | `reranker` | `reranker` |
| `PromptTemplate` | `prompt_template` | `prompt_template` |
| `LLMGuardInput` | `input_guard` | `input_guard` |
| `LLMGuardOutput` | `output_guard` | `output_guard` |
| `Llm` | `llm` | `llm` |
| `DocSum` | `docsum` | `docsum` |
| `QueryRewrite` | `query_rewrite` | `query_rewrite` |

Steps with no tunable parameters (`embedding`, `text-extractor`, `text-compression`,
`text-splitter`, `language-detection`) declare neither key.

Injection happens in the router's leaf-step send path, so a step that routes to a
sub-node (`nodeName`) is never injected itself - its leaf steps are, when the recursion
reaches them. The overlay is applied at the **top level** of the request and overwrites
existing fields, so two leaf steps that share one `paramsKey` receive identical values;
branches that must differ need distinct `paramsKey`s of the same `paramsKind`.

## Shared steps (`_shared/steps/`)

All base steps live in `pipelines/_shared/steps/`, so pipelines do not redefine common
steps. The composer builds its Jinja loader with the search path `[pipeline_dir,
_shared]`, so a pipeline may add a local `steps/<id>.yaml.j2` that **overrides** the
shared file of the same name. The shared directory is derived inside the composer
(`pipeline_dir.parent / '_shared'`); there is no CLI argument for it.

Current shared steps: `embedding`, `llm`, `llm-guard-input`,
`prompt-template`, `reranking`, `retriever` (ChatQnA); `docsum`, `text-compression`,
`text-extractor`, `text-splitter` (DocSum); `language-detection` (translation). Because
step ids are unique across pipelines, they coexist in one folder. A `base_flow` selects
only the steps a given pipeline uses.

Two shared steps are parameterized from `pipeline.yaml` rather than hardcoded:

- `llm.yaml.j2` - answer/summary/translation generation. Per-pipeline deltas come from
  `endpoints.llm` in `pipeline.yaml`:
  - `disable_streaming` - emit `LLM_DISABLE_STREAMING` when defined (translation).
  - `downstream` - mark the step `isDownstreamService: true` and drop its `data:`
    (default `false`; docsum sets `true`). The router then skips executing the step, and
    GMC resolves `llm-svc` by name for whichever step drives it - docsum's
    `DOCSUM_LLM_USVC_ENDPOINT: llm-svc`. Without it that lookup finds no matching step
    and reconcile fails.
  - `resources` - **required**. Unlike other steps whose resources are hardcoded in the
    step file, the shared llm step is sized per pipeline, so every `pipeline.yaml` that
    uses it must define `endpoints.llm.resources` with `replicas`, `requests`, and
    `limits`. If the block is missing, the composer fails closed with an actionable
    message (via the `required` Jinja filter) rather than a cryptic attribute error.
- `prompt-template.yaml.j2` - format context and query for the LLM. Per-pipeline deltas
  come from `endpoints.prompt_template` in `pipeline.yaml`:
  - `inject_params` - emit `paramsKey`/`paramsKind` so the router injects the stored
    `prompt_template` parameters (default `true`). Translation sets `false`, so the
    router keeps the template that language detection sends instead of overwriting it
    with the stored ChatQnA one.

## Variants (`variants/<name>/`)

A variant modifies the base flow. It has two files:

- `meta.yaml` - one composition directive plus metadata:
  - `insert_after: <step-id>` - insert the variant's step(s) immediately after an
    existing step (for example output_guard inserts after `llm`, because it scans the
    answer that step generated).
  - `insert_before: <step-id>` - insert the variant's step(s) immediately before an
    existing step (for example query-rewrite inserts before `embedding`, because it
    rewrites the query that then gets embedded).
  - `replaces: [<step-id>, ...]` - replace a **contiguous** span of base-flow steps with
    the variant's step(s). The composer validates that the listed steps are adjacent.
    A variant may list a step it re-emits itself (for example upload replaces `embedding`
    and renders its own Embedding step, because it re-sizes `embedding-usvc` and no two
    active steps may claim the same resources key).
  - `description` is informational. The services a variant introduces are declared by
    its `steps.yaml.j2` (`internalService.serviceName`) and its `resources:` keys; those
    are the authoritative, composer-consumed source.
  Exactly one of `insert_after` / `insert_before` / `replaces` is required.
- `steps.yaml.j2` - the variant's step content, in the same unified format as base
  steps. Variants typically use the `steps:` list form and may add `__nodes__:` for
  nested routers, and they carry their own `resources:`. A variant may also render
  `steps: []` and no `resources:` block, which makes `replaces:` a pure removal - see
  `retrieve-rerank` variant.

Selection: set `pipeline_variant: <name>` in `config.yaml` (or rely on the role default
`base`). The `app_pipeline` role validates the value against the discovered
`variants/` directories plus `base`, converts it to the composer's CSV argument, and the
composer inserts or replaces accordingly. `base` means no variant.

### Variant-aware resource aggregation

Resources are aggregated **only for steps in the active flow** (base plus the selected
variant). Services for unselected variants are omitted entirely - there is no
`replicas: 0` placeholder. Selecting `query-rewrite` automatically includes
`query-rewrite-usvc`; a variant that replaces steps carries the replaced services'
resources in its own file (for example hybrid-retrieval re-declares `retriever-usvc` and
`reranking-usvc` alongside `rrf-usvc`, since it replaces the base retriever and reranking
steps).

## How the composer and `app_pipeline` consume these files

The composer CLI is:

```
compose_pipeline.py <pipeline_dir> <enabled_variants_csv> <template_vars_json>
```

- `<pipeline_dir>` - for example `pipelines/chatqna`.
- `<enabled_variants_csv>` - comma-separated variant names, empty for `base`. Duplicates
  are collapsed; more than one distinct variant is rejected (see the note above).
- `<template_vars_json>` - JSON carrying `endpoints`, plus the control keys
  `pipeline_type`, `pipeline_variant`, and `resources_output` (the composer pops these;
  they do not reach the templates).

It writes the aggregated resources overlay (a `services:` map) to the path in
`resources_output`, and prints the composed GMConnector YAML to stdout.

The `app_pipeline` role (`../roles/app_pipeline/tasks/install.yaml`) drives it:

1. Discovers pipeline types by listing `pipelines/*/`; validates `pipeline_type` against
   them and requires a `pipeline.yaml`.
2. Discovers variants under `<type>/variants/`; validates `pipeline_variant` against them
   plus `base`.
3. Loads `pipeline.yaml` as `pipeline_meta`; takes the deploy namespace from
   `pipeline_meta.namespace`.
4. Builds `composer_template_vars` (`endpoints`, `pipeline_type`, `pipeline_variant`,
   `resources_output`) and converts `pipeline_variant` to the CSV argument.
5. Runs the composer; writes the GMConnector to `rag_tmp_dir/gmconnector-<type>.yaml`
   and, when `ANSIBLE_LOG_PATH` is set, an additional timestamped audit copy.
6. Installs the GMC operator Helm chart with a `values_files` list that includes the
   composed resources overlay (`helm_resources_path`), so the aggregated `services:`
   sizing is applied to the chart.
7. Applies the composed GMConnector, waits for `router-service`, applies the APISIX
   routes, and waits for the GMConnector status.

## GMConnector labels

The composer sets deterministic trace labels on the generated GMConnector's
`metadata.labels`, in addition to the standard chart labels:

- `gmc/pipeline-type: <pipeline_type>` (for example `chatqna`)
- `gmc/variant: <pipeline_variant>` (for example `base`, `query-rewrite`)

These reflect the Ansible-selected type and variant (`base` is always emitted for
uniform querying) and are stable across re-runs, so they do not trigger spurious
controller re-reconciles. The GMConnector CRD `metadata` is a permissive
`type: object`, so no schema change is required for these labels.

## Adding a new modular pipeline

1. Create `pipelines/<type>/`.
2. Add `pipeline.yaml` with `name`, `namespace`, `base_flow`, `endpoints`, and `router`.
   If the pipeline uses the shared llm step, include `endpoints.llm.resources`.
3. Provide a step file for each `base_flow` entry. Reuse a `_shared/steps/<id>.yaml.j2`
   where one exists; add new shared steps there, or a pipeline-local `steps/<id>.yaml.j2`
   only to override a shared step. Each step file carries its `step:`/`steps:` definition
   and its `resources:`.
4. Add `config.yaml` (flat) with at least `pipeline_type: "<type>"`,
   `pipeline_variant: "base"`, the container `registry`/`tag`, `inference_models`, and
   the `deployment_dir`/`components_dir`/`pipelines_dir` path block, so the file works as
   a drop-in `config.erag.yaml`.
5. Make sure every step `name:` is a registered GMC `StepNameType`, and every
   `<usvc>` resources key matches a service in `components/gmc/values.yaml` and has a
   `manifests_common/<name>-usvc.yaml.tpl`.

## Adding a variant to a pipeline

1. Create `pipelines/<type>/variants/<name>/`.
2. Add `meta.yaml` with exactly one of `insert_after` or `replaces` (contiguous span),
   plus `description`.
3. Add `steps.yaml.j2` in the unified format (`step:` or `steps:`, optional
   `__nodes__:`, and `resources:` for the services the variant introduces or replaces).
4. Select it with `pipeline_variant: "<name>"` in a flavour's `config.yaml`.
5. Confirm every step name is registered in the GMC operator and every `<usvc>` has a
   chart image and manifest, or the operator will reject the composed CR (as with
   hybrid-retrieval today).
