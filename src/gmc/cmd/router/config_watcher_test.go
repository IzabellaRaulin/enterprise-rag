/*
* Copyright (C) 2024-2026 Intel Corporation
* SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcv1alpha3 "erag.intel.com/gmc/api/v1alpha3"
	"github.com/stretchr/testify/assert"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"knative.dev/pkg/apis"
)

// natsStateReader installs a manual-reader-backed config.nats.state gauge and
// returns a reader for the value recorded under a given state label, plus a
// restore function. It lets a test observe recordNATSState without a Prometheus
// scrape.
func natsStateReader(t *testing.T) (read func(state string) int64, dp func(state string) metricdata.DataPoint[int64], restore func()) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	gauge, err := provider.Meter("test").Int64Gauge("router.config.nats.state")
	if err != nil {
		t.Fatalf("failed to build gauge: %v", err)
	}
	prev := configNATSStateGauge
	configNATSStateGauge = gauge

	dp = func(state string) metricdata.DataPoint[int64] {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("collect failed: %v", err)
		}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				g, ok := m.Data.(metricdata.Gauge[int64])
				if !ok {
					continue
				}
				for _, point := range g.DataPoints {
					if v, present := point.Attributes.Value("state"); present && v.AsString() == state {
						return point
					}
				}
			}
		}
		return metricdata.DataPoint[int64]{Value: -1}
	}
	read = func(state string) int64 { return dp(state).Value }
	return read, dp, func() { configNATSStateGauge = prev }
}

// TestStartRecordsDisabledWhenNATSURLEmpty verifies that a watcher with no
// NATS_URL reports the disabled state, distinguishing intentionally-off NATS
// from a configured-but-failed connection.
func TestStartRecordsDisabledWhenNATSURLEmpty(t *testing.T) {
	read, dp, restore := natsStateReader(t)
	defer restore()

	w := &configWatcher{pipeline: "chatqa", tenant: "_global", cache: make(map[configScope]cachedConfig)}
	w.start(context.Background())

	assert.Equal(t, int64(1), read(natsDisabled))
	assert.Equal(t, int64(0), read(natsConnected))
	assert.Equal(t, int64(0), read(natsFailed))

	// The pipeline and tenant travel as attributes, matching the other config
	// metrics, so the series stays unambiguous with more than one watcher.
	attrs := dp(natsDisabled).Attributes
	pipeline, ok := attrs.Value("pipeline")
	assert.True(t, ok)
	assert.Equal(t, "chatqa", pipeline.AsString())
	tenant, ok := attrs.Value("tenant")
	assert.True(t, ok)
	assert.Equal(t, "_global", tenant.AsString())
}

// TestConnectRecordsFailedOnInvalidAuth verifies that a configured NATS URL
// whose auth material is inconsistent records the failed state rather than the
// disabled one, so a silent KV outage is alertable.
func TestConnectRecordsFailedOnInvalidAuth(t *testing.T) {
	read, _, restore := natsStateReader(t)
	defer restore()

	// Only the client cert is set (not the key), so natsauth.Options rejects it
	// and connect returns without connecting.
	t.Setenv("NATS_TLS_CERT_FILE", "/tmp/does-not-matter.crt")

	w := &configWatcher{pipeline: "chatqa", tenant: "_global", natsURL: "nats://127.0.0.1:4222", cache: make(map[configScope]cachedConfig)}
	nc, kv := w.connect(context.Background())
	assert.Nil(t, nc)
	assert.Nil(t, kv)

	assert.Equal(t, int64(1), read(natsFailed))
	assert.Equal(t, int64(0), read(natsDisabled))
}

// TestRecordNATSStateFailedClearsConnected verifies that recording the failed
// state after a lost watch clears the connected series, so the gauge does not
// stay stuck at connected while the router is back on the legacy fallback.
func TestRecordNATSStateFailedClearsConnected(t *testing.T) {
	read, _, restore := natsStateReader(t)
	defer restore()

	w := &configWatcher{pipeline: "chatqa", tenant: "_global", cache: make(map[configScope]cachedConfig)}

	w.recordNATSState(context.Background(), natsConnected)
	assert.Equal(t, int64(1), read(natsConnected))

	// A subsequent lost watch is reported failed; connected must drop to 0.
	w.recordNATSState(context.Background(), natsFailed)
	assert.Equal(t, int64(1), read(natsFailed))
	assert.Equal(t, int64(0), read(natsConnected))
}

// newTestWatcher builds a watcher wired for the given pipeline with an empty
// cache and marked connected, so lookups are served from RAM.
func newTestWatcher(pipeline string) *configWatcher {
	w := &configWatcher{
		pipeline:   pipeline,
		tenant:     "_global",
		cache:      make(map[configScope]cachedConfig),
		httpClient: &http.Client{Timeout: configHTTPTimeout},
		connected:  true,
	}
	return w
}

func llmStep(name, paramsKey string) *mcv1alpha3.Step {
	cfg := map[string]string{}
	if paramsKey != "" {
		cfg[configParamsKey] = paramsKey
	}
	return &mcv1alpha3.Step{
		StepName: name,
		Executor: mcv1alpha3.Executor{
			InternalService: mcv1alpha3.GMCTarget{
				ServiceName: "llm-svc",
				Config:      cfg,
			},
		},
	}
}

func TestApplyEntryPopulatesCacheFlat(t *testing.T) {
	w := newTestWatcher("chatqa")
	value := []byte(`{"max_new_tokens": 512, "temperature": 0.7}`)
	w.applyEntry("chatqa._global.llm", value, 3, time.Now())

	params, source, version := w.getParamsForStep(context.Background(), llmStep("Llm", "llm"), "_global")
	assert.Equal(t, sourceKV, source)
	assert.Equal(t, int64(3), version)
	assert.EqualValues(t, 512, params["max_new_tokens"])
	assert.InDelta(t, 0.7, params["temperature"], 1e-9)
}

func TestApplyEntryIgnoresOtherPipelines(t *testing.T) {
	w := newTestWatcher("chatqa")
	w.applyEntry("docsum._global.llm", []byte(`{"max_new_tokens": 99}`), 1, time.Now())

	w.mu.RLock()
	defer w.mu.RUnlock()
	assert.Empty(t, w.cache)
}

func TestApplyEntryRejectsMalformedKeys(t *testing.T) {
	w := newTestWatcher("chatqa")
	// Extra segment, wildcard and empty paramsKey must all be ignored.
	w.applyEntry("chatqa._global.llm.extra", []byte(`{"max_new_tokens": 1}`), 1, time.Now())
	w.applyEntry("chatqa._global.*", []byte(`{"max_new_tokens": 2}`), 1, time.Now())
	w.applyEntry("chatqa._global.", []byte(`{"max_new_tokens": 3}`), 1, time.Now())

	w.mu.RLock()
	defer w.mu.RUnlock()
	assert.Empty(t, w.cache)
}

// TestApplyEntryIgnoresNullValue verifies that a JSON null value, which
// unmarshals to a nil map without error, is skipped rather than cached as an
// empty KV fragment that would suppress the fallback chain.
func TestApplyEntryIgnoresNullValue(t *testing.T) {
	w := newTestWatcher("chatqa")
	w.applyEntry("chatqa._global.llm", []byte(`null`), 1, time.Now())

	w.mu.RLock()
	defer w.mu.RUnlock()
	assert.Empty(t, w.cache)
}

// TestNestedFragmentInjectedVerbatim verifies that a fragment the KV projection
// already delivered in nested wire shape is cached and injected unchanged: the
// router does no reshaping and keeps the top-level wrapper key downstream reads.
func TestNestedFragmentInjectedVerbatim(t *testing.T) {
	w := newTestWatcher("chatqa")
	value := []byte(`{"input_guardrail_params": {"toxicity": {"enabled": true}}}`)
	w.applyEntry("chatqa._global.input_guard", value, 1, time.Now())

	step := &mcv1alpha3.Step{
		StepName: "LLMGuardInput",
		Executor: mcv1alpha3.Executor{
			InternalService: mcv1alpha3.GMCTarget{
				Config: map[string]string{configParamsKey: "input_guard"},
			},
		},
	}
	params, _, _ := w.getParamsForStep(context.Background(), step, "_global")
	nested, ok := params["input_guardrail_params"].(map[string]interface{})
	assert.True(t, ok, "nested fragment must be injected under input_guardrail_params verbatim")
	assert.Contains(t, nested, "toxicity")
}

// TestNewNestedKindNeedsNoRouterChange verifies that a nested kind the router
// has never heard of injects correctly with no code change: whatever wrapper
// key the fingerprint chose travels in the fragment and is merged as-is.
func TestNewNestedKindNeedsNoRouterChange(t *testing.T) {
	w := newTestWatcher("chatqa")
	value := []byte(`{"future_guardrail_params": {"policy": "strict"}}`)
	w.applyEntry("chatqa._global.future_guard", value, 1, time.Now())

	step := &mcv1alpha3.Step{
		StepName: "FutureGuard",
		Executor: mcv1alpha3.Executor{
			InternalService: mcv1alpha3.GMCTarget{
				Config: map[string]string{configParamsKey: "future_guard"},
			},
		},
	}
	out := w.injectParamsForStep(context.Background(), step, []byte(`{"query": "hi"}`), http.Header{})
	var got map[string]interface{}
	assert.NoError(t, json.Unmarshal(out, &got))
	nested, ok := got["future_guardrail_params"].(map[string]interface{})
	assert.True(t, ok, "an unknown nested kind must inject under its own wrapper key")
	assert.Equal(t, "strict", nested["policy"])
	assert.Equal(t, "hi", got["query"])
}

// TestSelectiveInjectionTwoLLMs verifies that two steps of the same kind with
// different paramsKey receive distinct parameter groups.
func TestSelectiveInjectionTwoLLMs(t *testing.T) {
	w := newTestWatcher("chatqa")
	w.applyEntry("chatqa._global.llm_primary", []byte(`{"max_new_tokens": 1024}`), 1, time.Now())
	w.applyEntry("chatqa._global.llm_secondary", []byte(`{"max_new_tokens": 128}`), 1, time.Now())

	ctx := context.Background()
	req := []byte(`{"query": "hi"}`)

	primary := w.injectParamsForStep(ctx, llmStep("Llm", "llm_primary"), req, http.Header{})
	secondary := w.injectParamsForStep(ctx, llmStep("Llm", "llm_secondary"), req, http.Header{})

	var p, s map[string]interface{}
	assert.NoError(t, json.Unmarshal(primary, &p))
	assert.NoError(t, json.Unmarshal(secondary, &s))
	assert.EqualValues(t, 1024, p["max_new_tokens"])
	assert.EqualValues(t, 128, s["max_new_tokens"])
	// The untouched request field is preserved on both.
	assert.Equal(t, "hi", p["query"])
	assert.Equal(t, "hi", s["query"])
}

// TestVersionMetadataNotInjected verifies that a numeric top-level "version"
// carried by a fragment is used for the version metric but stripped before the
// fragment is injected, so projection metadata never reaches downstream.
func TestVersionMetadataNotInjected(t *testing.T) {
	w := newTestWatcher("chatqa")
	w.applyEntry("chatqa._global.llm", []byte(`{"version": 7, "max_new_tokens": 256}`), 3, time.Now())

	params, source, version := w.getParamsForStep(context.Background(), llmStep("Llm", "llm"), "_global")
	assert.Equal(t, sourceKV, source)
	assert.Equal(t, int64(7), version, "numeric version is reported for the metric")
	assert.NotContains(t, params, "version", "version metadata must not be injected downstream")
	assert.EqualValues(t, 256, params["max_new_tokens"])
}

// TestNumericVersionAcrossTypes verifies that a "version" value is recognised
// as metadata regardless of the numeric type it decoded into, so the strip and
// the version metric stay in step even if the decode strategy changes.
func TestNumericVersionAcrossTypes(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   interface{}
		want int64
		ok   bool
	}{
		{"float64", float64(9), 9, true},
		{"json.Number", json.Number("11"), 11, true},
		{"int", int(4), 4, true},
		{"int64", int64(5), 5, true},
		{"uint64", uint64(6), 6, true},
		{"fractional float truncates toward zero", float64(7.9), 7, true},
		{"float above int64 clamps", float64(1e19), math.MaxInt64, true},
		{"uint64 above int64 clamps", uint64(math.MaxUint64), math.MaxInt64, true},
		{"string", "not-a-number", 0, false},
		{"missing", nil, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := numericVersion(tc.in)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestWatchUpdateReplacesCache verifies that a later revision for a key
// overwrites the earlier cached value, as the watch callback does.
func TestWatchUpdateReplacesCache(t *testing.T) {
	w := newTestWatcher("chatqa")
	w.applyEntry("chatqa._global.llm", []byte(`{"max_new_tokens": 256}`), 1, time.Now())
	w.applyEntry("chatqa._global.llm", []byte(`{"max_new_tokens": 512}`), 2, time.Now())

	params, source, version := w.getParamsForStep(context.Background(), llmStep("Llm", "llm"), "_global")
	assert.Equal(t, sourceKV, source)
	assert.Equal(t, int64(2), version)
	assert.EqualValues(t, 512, params["max_new_tokens"])
}

func TestDeleteKeyRemovesScope(t *testing.T) {
	w := newTestWatcher("chatqa")
	w.applyEntry("chatqa._global.llm", []byte(`{"max_new_tokens": 256}`), 1, time.Now())
	w.deleteKey("chatqa._global.llm")

	w.mu.RLock()
	_, ok := w.cache[configScope{tenant: "_global", paramsKey: "llm"}]
	w.mu.RUnlock()
	assert.False(t, ok)
}

// TestFallbackKVMissCallsLegacy verifies that a lookup with no cached entry
// falls back to the fingerprint HTTP endpoint and caches the result.
func TestFallbackKVMissCallsLegacy(t *testing.T) {
	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		called.Add(1)
		resp := map[string]interface{}{
			"params_key": "llm",
			"values":     map[string]interface{}{"max_new_tokens": 777},
		}
		bytes, _ := json.Marshal(resp)
		_, _ = rw.Write(bytes)
	}))
	defer srv.Close()

	w := newTestWatcher("chatqa")
	w.fingerprintURL = srv.URL

	params, source, _ := w.getParamsForStep(context.Background(), llmStep("Llm", "llm"), "_global")
	assert.Equal(t, sourceLegacy, source)
	assert.EqualValues(t, 777, params["max_new_tokens"])
	assert.Equal(t, int32(1), called.Load())

	// The legacy result is cached, so the scope is now present.
	w.mu.RLock()
	_, ok := w.cache[configScope{tenant: "_global", paramsKey: "llm"}]
	w.mu.RUnlock()
	assert.True(t, ok)
}

// TestFallbackLegacyResolvesUnderScope verifies the legacy fallback queries the
// scope-aware per-key config route under the router's own (pipeline, tenant),
// so a managed step falling to legacy is injected with only that key's group
// rather than another pipeline's packed parameter set.
func TestFallbackLegacyResolvesUnderScope(t *testing.T) {
	var gotMethod, gotPath, gotParamsKey, gotPipeline, gotTenant string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		gotMethod = req.Method
		gotPath = req.URL.Path
		gotParamsKey = req.URL.Query().Get("params_key")
		gotPipeline = req.URL.Query().Get("pipeline")
		gotTenant = req.URL.Query().Get("tenant")
		// The route returns a single group's wire-shaped values, never the
		// packed cross-pipeline set. Guardrail fields belonging to another
		// pipeline are deliberately absent from a chatqa llm group.
		resp := map[string]interface{}{
			"params_key":  "llm",
			"params_kind": "llm",
			"version":     7,
			"values":      map[string]interface{}{"max_new_tokens": 128, "temperature": 0.5},
		}
		bytes, _ := json.Marshal(resp)
		_, _ = rw.Write(bytes)
	}))
	defer srv.Close()

	w := newTestWatcher("chatqa")
	w.fingerprintURL = srv.URL

	params, source, _ := w.getParamsForStep(context.Background(), llmStep("Llm", "llm"), "_global")
	assert.Equal(t, sourceLegacy, source)

	// The config route is a GET; a regression to the old POST would return 405
	// from the real service, so pin the method here.
	assert.Equal(t, http.MethodGet, gotMethod)
	assert.Equal(t, configPath, gotPath)
	assert.Equal(t, "llm", gotParamsKey)
	assert.Equal(t, "chatqa", gotPipeline, "legacy path must use the router's pipeline, not the fingerprint's default")
	assert.Equal(t, "_global", gotTenant)

	// Only the group's own fields are injected; no cross-pipeline packed fields.
	assert.EqualValues(t, 128, params["max_new_tokens"])
	assert.InDelta(t, 0.5, params["temperature"], 1e-9)
	assert.NotContains(t, params, "output_guardrail_params")
	assert.NotContains(t, params, "query_rewrite_params")
	assert.NotContains(t, params, "dataprep_guardrail_params")
}

// TestFallbackLegacyResolvesUnderDefaultTenantForPerUserRequest verifies that a
// per-user request that has no watch-delivered group falls through to the
// default tenant on the legacy path too: a per-user group is only ever
// populated by the watch, so the legacy fetch stays scoped to the configured
// tenant rather than querying a per-user scope the endpoint would never manage.
func TestFallbackLegacyResolvesUnderDefaultTenantForPerUserRequest(t *testing.T) {
	var gotTenant string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		gotTenant = req.URL.Query().Get("tenant")
		resp := map[string]interface{}{
			"params_key": "llm",
			"values":     map[string]interface{}{"max_new_tokens": 64},
		}
		bytes, _ := json.Marshal(resp)
		_, _ = rw.Write(bytes)
	}))
	defer srv.Close()

	w := newTestWatcher("chatqa")
	w.fingerprintURL = srv.URL

	params, source, _ := w.getParamsForStep(context.Background(), llmStep("Llm", "llm"), "user123")
	assert.Equal(t, sourceLegacy, source)
	assert.Equal(t, "_global", gotTenant, "legacy fetch stays on the default tenant; per-user groups come only from the watch")
	assert.EqualValues(t, 64, params["max_new_tokens"])
}

// TestFallbackLegacyNotFoundInjectsNothing verifies that a 404 from the config
// route — an unmanaged step with no group for this scope, e.g. embedding — is
// treated as empty so nothing is injected, matching KV behaviour where an
// unmanaged step simply has no group.
func TestFallbackLegacyNotFoundInjectsNothing(t *testing.T) {
	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		called.Add(1)
		rw.WriteHeader(http.StatusNotFound)
		_, _ = rw.Write([]byte(`{"detail": "No config for params_key 'embedding'."}`))
	}))
	defer srv.Close()

	w := newTestWatcher("chatqa")
	w.fingerprintURL = srv.URL

	params, source, version := w.getParamsForStep(context.Background(), llmStep("Embedding", "embedding"), "_global")
	assert.Equal(t, sourceLegacy, source)
	assert.Empty(t, params, "a 404 must inject no fields")
	assert.Equal(t, int64(0), version)
	assert.Equal(t, int32(1), called.Load())

	// The empty group is cached like any other legacy result, so a request does
	// not hit the endpoint again within the TTL.
	req := []byte(`{"input": "hi"}`)
	out := w.injectParamsForStep(context.Background(), llmStep("Embedding", "embedding"), req, http.Header{})
	assert.JSONEq(t, string(req), string(out), "the request must be unchanged when the group is empty")
	assert.Equal(t, int32(1), called.Load(), "an empty 404 result must be cached, not re-fetched every request")
}

// TestLegacyMissReusesCacheWithinTTL verifies that repeated lookups for a key
// absent from KV reuse the cached legacy result within the TTL window instead
// of issuing an HTTP fetch every time.
func TestLegacyMissReusesCacheWithinTTL(t *testing.T) {
	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		called.Add(1)
		resp := map[string]interface{}{
			"params_key": "llm",
			"values":     map[string]interface{}{"max_new_tokens": 55},
		}
		bytes, _ := json.Marshal(resp)
		_, _ = rw.Write(bytes)
	}))
	defer srv.Close()

	w := newTestWatcher("chatqa")
	w.fingerprintURL = srv.URL
	ctx := context.Background()
	step := llmStep("Llm", "llm")

	// First lookup fetches; subsequent lookups within the TTL reuse the cache.
	for i := 0; i < 4; i++ {
		params, source, _ := w.getParamsForStep(ctx, step, "_global")
		assert.Equal(t, sourceLegacy, source)
		assert.EqualValues(t, 55, params["max_new_tokens"])
	}
	assert.Equal(t, int32(1), called.Load(), "legacy fetch should happen at most once per TTL window")
}

// TestLegacyMissRefetchesAfterTTL verifies that once the cached legacy result
// ages past the TTL, the next lookup fetches from the fingerprint service again.
func TestLegacyMissRefetchesAfterTTL(t *testing.T) {
	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		called.Add(1)
		resp := map[string]interface{}{
			"params_key": "llm",
			"values":     map[string]interface{}{"max_new_tokens": 55},
		}
		bytes, _ := json.Marshal(resp)
		_, _ = rw.Write(bytes)
	}))
	defer srv.Close()

	w := newTestWatcher("chatqa")
	w.fingerprintURL = srv.URL
	ctx := context.Background()
	step := llmStep("Llm", "llm")

	_, source, _ := w.getParamsForStep(ctx, step, "_global")
	assert.Equal(t, sourceLegacy, source)

	// Age the cached entry past the TTL so the next lookup re-fetches.
	scope := configScope{tenant: "_global", paramsKey: "llm"}
	w.mu.Lock()
	entry := w.cache[scope]
	entry.updatedAt = time.Now().Add(-2 * legacyTTL)
	w.cache[scope] = entry
	w.mu.Unlock()

	_, source, _ = w.getParamsForStep(ctx, step, "_global")
	assert.Equal(t, sourceLegacy, source)
	assert.Equal(t, int32(2), called.Load(), "an expired legacy entry should trigger a re-fetch")
}

// TestKVUpdateSupersedesCachedLegacy verifies that a KV value delivered after a
// legacy result was cached is served as sourceKV and takes over from the legacy
// value even inside the legacy TTL window.
func TestKVUpdateSupersedesCachedLegacy(t *testing.T) {
	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		called.Add(1)
		resp := map[string]interface{}{
			"params_key": "llm",
			"values":     map[string]interface{}{"max_new_tokens": 10},
		}
		bytes, _ := json.Marshal(resp)
		_, _ = rw.Write(bytes)
	}))
	defer srv.Close()

	w := newTestWatcher("chatqa")
	w.fingerprintURL = srv.URL
	ctx := context.Background()
	step := llmStep("Llm", "llm")

	// KV miss caches a legacy result.
	_, source, _ := w.getParamsForStep(ctx, step, "_global")
	assert.Equal(t, sourceLegacy, source)

	// The watch then delivers the key; the next lookup must serve it as KV.
	w.applyEntry("chatqa._global.llm", []byte(`{"max_new_tokens": 20}`), 9, time.Now())
	params, source, version := w.getParamsForStep(ctx, step, "_global")
	assert.Equal(t, sourceKV, source)
	assert.Equal(t, int64(9), version)
	assert.EqualValues(t, 20, params["max_new_tokens"])
	assert.Equal(t, int32(1), called.Load(), "no further legacy fetch once KV supersedes")
}

// TestStoreLegacyDoesNotClobberKV verifies the compare-and-set on the legacy
// write: a KV value that arrived during an in-flight legacy fetch is not
// overwritten by the later legacy store.
func TestStoreLegacyDoesNotClobberKV(t *testing.T) {
	w := newTestWatcher("chatqa")
	scope := configScope{tenant: "_global", paramsKey: "llm"}

	// Simulate the watch writing a fresh KV value while a legacy fetch was in
	// flight, then the legacy write landing afterwards.
	w.applyEntry("chatqa._global.llm", []byte(`{"max_new_tokens": 20}`), 9, time.Now())
	w.storeLegacy(scope, map[string]interface{}{"max_new_tokens": 10})

	params, source, version := w.getParamsForStep(context.Background(), llmStep("Llm", "llm"), "_global")
	assert.Equal(t, sourceKV, source, "the KV value must not be clobbered by the legacy write")
	assert.Equal(t, int64(9), version)
	assert.EqualValues(t, 20, params["max_new_tokens"])
}

// TestConcurrentLegacyFetchAndKVUpdate exercises the read-then-write race under
// the -race detector: a legacy-fetching lookup runs concurrently with the watch
// applying a KV value for the same scope. The KV value must win and never be
// overwritten by the legacy store.
func TestConcurrentLegacyFetchAndKVUpdate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		resp := map[string]interface{}{
			"params_key": "llm",
			"values":     map[string]interface{}{"max_new_tokens": 10},
		}
		bytes, _ := json.Marshal(resp)
		_, _ = rw.Write(bytes)
	}))
	defer srv.Close()

	scope := configScope{tenant: "_global", paramsKey: "llm"}
	step := llmStep("Llm", "llm")

	for i := 0; i < 100; i++ {
		w := newTestWatcher("chatqa")
		w.fingerprintURL = srv.URL

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			w.getParamsForStep(context.Background(), step, "_global")
		}()
		go func() {
			defer wg.Done()
			w.applyEntry("chatqa._global.llm", []byte(`{"max_new_tokens": 20}`), 9, time.Now())
		}()
		wg.Wait()

		// applyEntry always runs, so a KV entry must be present regardless of the
		// interleaving; the legacy store must never clobber it, so it stays
		// flagged fromKV with the KV value.
		w.mu.RLock()
		entry, found := w.cache[scope]
		w.mu.RUnlock()
		assert.True(t, found, "the KV entry must be present after applyEntry ran")
		assert.True(t, entry.fromKV, "the KV entry must not be overwritten by the legacy store")
		assert.EqualValues(t, 20, entry.params["max_new_tokens"])
	}
}

// TestLegacyCachedEntryNotReportedAsKV verifies that a value cached from a
// legacy HTTP fetch is not later served as if it came from the KV watch.
func TestLegacyCachedEntryNotReportedAsKV(t *testing.T) {
	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		called.Add(1)
		resp := map[string]interface{}{
			"params_key": "llm",
			"values":     map[string]interface{}{"max_new_tokens": 5},
		}
		bytes, _ := json.Marshal(resp)
		_, _ = rw.Write(bytes)
	}))
	defer srv.Close()

	w := newTestWatcher("chatqa")
	w.fingerprintURL = srv.URL
	ctx := context.Background()
	step := llmStep("Llm", "llm")

	// First lookup: KV miss -> legacy fetch, cached.
	_, source, _ := w.getParamsForStep(ctx, step, "_global")
	assert.Equal(t, sourceLegacy, source)

	// Second lookup: the entry is cached but did not come from KV, so it is
	// still reported as legacy rather than a KV hit. Within the TTL it is served
	// from the cache without a second HTTP fetch.
	_, source, _ = w.getParamsForStep(ctx, step, "_global")
	assert.Equal(t, sourceLegacy, source)
	assert.Equal(t, int32(1), called.Load())
}

// TestFallbackLegacyFailsUsesLastKnownGood verifies that when the KV watch is
// down and the HTTP fallback fails, the last cached value is returned.
func TestFallbackLegacyFailsUsesLastKnownGood(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	w := newTestWatcher("chatqa")
	w.fingerprintURL = srv.URL
	// Seed a value, then mark the watch as disconnected so the lookup skips the
	// fresh-KV path and treats the seeded value as last-known-good.
	w.applyEntry("chatqa._global.llm", []byte(`{"max_new_tokens": 42}`), 5, time.Now())
	w.mu.Lock()
	w.connected = false
	w.mu.Unlock()

	params, source, version := w.getParamsForStep(context.Background(), llmStep("Llm", "llm"), "_global")
	assert.Equal(t, sourceLKG, source)
	assert.Equal(t, int64(5), version)
	assert.EqualValues(t, 42, params["max_new_tokens"])
}

// TestFallbackEmptyWhenNothingCached verifies that with no cache and a failing
// HTTP fallback the lookup yields empty params rather than an error.
func TestFallbackEmptyWhenNothingCached(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	w := newTestWatcher("chatqa")
	w.connected = false
	w.fingerprintURL = srv.URL

	params, source, _ := w.getParamsForStep(context.Background(), llmStep("Llm", "llm"), "_global")
	assert.Equal(t, sourceLKG, source)
	assert.Empty(t, params)
}

func TestInjectTopLevelOverwrites(t *testing.T) {
	out := injectTopLevel([]byte(`{"a": 1, "b": 2}`), map[string]interface{}{"b": 9, "c": 3}, nil)
	var got map[string]interface{}
	assert.NoError(t, json.Unmarshal(out, &got))
	assert.EqualValues(t, 1, got["a"])
	assert.EqualValues(t, 9, got["b"])
	assert.EqualValues(t, 3, got["c"])
}

func TestInjectTopLevelNonObjectUnchanged(t *testing.T) {
	req := []byte(`["not", "an", "object"]`)
	out := injectTopLevel(req, map[string]interface{}{"a": 1}, nil)
	assert.Equal(t, req, out)
}

func TestInjectTopLevelKeepsRequestSummaryType(t *testing.T) {
	// The docsum UI sends summary_type per summary. Overwriting it with the
	// stored value made the dropdown a no-op: the request said "stuff" and the
	// pipeline ran whatever the store held.
	out := injectTopLevel(
		[]byte(`{"summary_type": "stuff", "texts": ["x"]}`),
		map[string]interface{}{"summary_type": "refine", "max_new_tokens": 512},
		map[string]interface{}{"summary_type": "stuff"})

	var got map[string]interface{}
	assert.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "stuff", got["summary_type"], "the caller's summary_type must survive injection")
	// Everything the caller did not send still comes from the store.
	assert.EqualValues(t, 512, got["max_new_tokens"])
}

func TestInjectTopLevelSuppliesSummaryTypeWhenAbsent(t *testing.T) {
	// With no summary_type in the request the stored value is the default.
	out := injectTopLevel(
		[]byte(`{"texts": ["x"]}`),
		map[string]interface{}{"summary_type": "refine"}, nil)

	var got map[string]interface{}
	assert.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "refine", got["summary_type"])
}

func TestInjectTopLevelKeepsRequestStream(t *testing.T) {
	out := injectTopLevel(
		[]byte(`{"stream": "this_should_be_a_boolean"}`),
		map[string]interface{}{"stream": true},
		map[string]interface{}{"stream": "this_should_be_a_boolean"})

	var got map[string]interface{}
	assert.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "this_should_be_a_boolean", got["stream"])
}

func TestInjectTopLevelStoreWinsOverUpstreamStreamDefault(t *testing.T) {
	out := injectTopLevel(
		[]byte(`{"docs": [], "stream": true}`),
		map[string]interface{}{"stream": false}, nil)

	var got map[string]interface{}
	assert.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, false, got["stream"])
}

func TestWithEntryRequestReadsEntryParameters(t *testing.T) {
	ctx := withEntryRequest(context.Background(),
		[]byte(`{"texts": ["x"], "parameters": {"stream": false}}`),
		http.Header{"Content-Type": []string{JSONContentType}})
	assert.Equal(t, map[string]interface{}{"stream": false}, callerParamsFromContext(ctx))

	ctx = withEntryRequest(context.Background(), []byte(`not json`), http.Header{})
	assert.Nil(t, callerParamsFromContext(ctx))
}

func TestInjectTopLevelStoreStillWinsForGuardConfig(t *testing.T) {
	// The guard services read their scanner config out of the request body, so a
	// caller must not be able to relax one by sending it. Only the parameters in
	// requestOverridableParams are the caller's to set.
	out := injectTopLevel(
		[]byte(`{"input_guardrail_params": {"ban_substrings": {"enabled": false}}, "text": "x"}`),
		map[string]interface{}{
			"input_guardrail_params": map[string]interface{}{
				"ban_substrings": map[string]interface{}{"enabled": true},
			},
		},
		map[string]interface{}{
			"input_guardrail_params": map[string]interface{}{
				"ban_substrings": map[string]interface{}{"enabled": false},
			},
		})

	var got map[string]interface{}
	assert.NoError(t, json.Unmarshal(out, &got))
	guard := got["input_guardrail_params"].(map[string]interface{})
	ban := guard["ban_substrings"].(map[string]interface{})
	assert.Equal(t, true, ban["enabled"], "a request must not be able to disable a guard")
}

func TestInjectTopLevelStoreStillWinsForCaps(t *testing.T) {
	// max_new_tokens is a resource cap an operator sets, not a caller's choice.
	out := injectTopLevel(
		[]byte(`{"max_new_tokens": 99999}`),
		map[string]interface{}{"max_new_tokens": 1024},
		map[string]interface{}{"max_new_tokens": 99999})

	var got map[string]interface{}
	assert.NoError(t, json.Unmarshal(out, &got))
	assert.EqualValues(t, 1024, got["max_new_tokens"])
}

func TestNewConfigWatcherPipelineFromGraph(t *testing.T) {
	t.Setenv("PIPELINE_NAME", "")
	t.Setenv("NATS_URL", "")
	graph := &mcv1alpha3.GMConnector{}
	graph.Name = "chatqa"
	w := newConfigWatcher(graph)
	assert.Equal(t, "chatqa", w.pipeline)
	assert.Equal(t, "_global", w.tenant)
}

func TestNewConfigWatcherPipelineFromEnv(t *testing.T) {
	t.Setenv("PIPELINE_NAME", "translation")
	w := newConfigWatcher(&mcv1alpha3.GMConnector{})
	assert.Equal(t, "translation", w.pipeline)
}

// TestNewConfigWatcherPerUserTenantDefaultsOff verifies per-user resolution is
// opt-in: absent the env var the watcher keeps every request on the default
// tenant, so an existing deployment sees no behaviour change.
func TestNewConfigWatcherPerUserTenantDefaultsOff(t *testing.T) {
	t.Setenv("NATS_URL", "")
	w := newConfigWatcher(&mcv1alpha3.GMConnector{})
	assert.False(t, w.perUserTenant)

	t.Setenv("FINGERPRINT_PER_USER_TENANT", "true")
	w = newConfigWatcher(&mcv1alpha3.GMConnector{})
	assert.True(t, w.perUserTenant)
}

// bearerFor builds a Bearer Authorization header carrying an unsigned JWT whose
// claims are the given JSON, so tenant resolution can be exercised without a
// signing key.
func bearerFor(claimsJSON string) http.Header {
	segment := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	token := segment(`{"alg":"none"}`) + "." + segment(claimsJSON) + ".sig"
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	return h
}

// TestResolveTenantDisabledAlwaysDefault verifies that with per-user resolution
// off, a request carrying a valid sub still resolves to the default tenant.
func TestResolveTenantDisabledAlwaysDefault(t *testing.T) {
	w := newTestWatcher("chatqa")
	w.perUserTenant = false
	headers := bearerFor(`{"sub":"11111111-2222-3333-4444-555555555555"}`)
	assert.Equal(t, "_global", w.resolveTenant(headers))
}

// TestResolveTenantEnabledUsesSub verifies that with per-user resolution on, a
// valid Bearer JWT resolves to its sub, and that the scheme name is matched
// case-insensitively.
func TestResolveTenantEnabledUsesSub(t *testing.T) {
	w := newTestWatcher("chatqa")
	w.perUserTenant = true
	sub := "11111111-2222-3333-4444-555555555555"
	headers := bearerFor(`{"sub":"` + sub + `"}`)
	assert.Equal(t, sub, w.resolveTenant(headers))

	// The Bearer scheme name is case-insensitive.
	lower := http.Header{}
	lower.Set("Authorization", strings.Replace(headers.Get("Authorization"), "Bearer ", "bearer ", 1))
	assert.Equal(t, sub, w.resolveTenant(lower))
}

// TestResolveTenantEnabledFallsBackToDefault verifies that with per-user
// resolution on, a request with no token, a malformed token, or a
// non-conforming sub falls back to the default tenant rather than emitting an
// invalid KV key.
func TestResolveTenantEnabledFallsBackToDefault(t *testing.T) {
	w := newTestWatcher("chatqa")
	w.perUserTenant = true

	for _, tc := range []struct {
		name    string
		headers http.Header
	}{
		{"no authorization header", http.Header{}},
		{"not a bearer token", func() http.Header { h := http.Header{}; h.Set("Authorization", "Basic abc"); return h }()},
		{"non-bearer scheme carrying a jwt shape", func() http.Header {
			seg := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
			h := http.Header{}
			h.Set("Authorization", "Basic "+seg(`{}`)+"."+seg(`{"sub":"11111111-2222-3333-4444-555555555555"}`)+".sig")
			return h
		}()},
		{"malformed jwt", func() http.Header { h := http.Header{}; h.Set("Authorization", "Bearer not.a.jwt"); return h }()},
		{"too few segments", func() http.Header { h := http.Header{}; h.Set("Authorization", "Bearer a.b"); return h }()},
		{"too many segments", func() http.Header {
			h := http.Header{}
			h.Set("Authorization", "Bearer "+strings.Repeat("a.", 5000)+"a")
			return h
		}()},
		{"empty segment", func() http.Header { h := http.Header{}; h.Set("Authorization", "Bearer a..c"); return h }()},
		{"trailing data after the token", func() http.Header {
			seg := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
			h := http.Header{}
			h.Set("Authorization", "Bearer "+seg(`{}`)+"."+seg(`{"sub":"11111111-2222-3333-4444-555555555555"}`)+".sig trailing")
			return h
		}()},
		{"missing sub claim", bearerFor(`{"aud":"account"}`)},
		{"empty sub claim", bearerFor(`{"sub":""}`)},
		{"sub with invalid characters", bearerFor(`{"sub":"user:1234"}`)},
		{"sub with a dot", bearerFor(`{"sub":"a.b"}`)},
		{"sub with whitespace", bearerFor(`{"sub":"a b"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, "_global", w.resolveTenant(tc.headers))
		})
	}
}

// TestInjectParamsForStepPerUserScope verifies the end-to-end opt-in path: with
// per-user resolution on and a per-user KV group present, the step receives the
// user's parameters instead of the default group.
func TestInjectParamsForStepPerUserScope(t *testing.T) {
	w := newTestWatcher("chatqa")
	w.perUserTenant = true
	sub := "11111111-2222-3333-4444-555555555555"
	w.applyEntry("chatqa._global.llm", []byte(`{"max_new_tokens": 256}`), 1, time.Now())
	w.applyEntry("chatqa."+sub+".llm", []byte(`{"max_new_tokens": 999}`), 1, time.Now())

	out := w.injectParamsForStep(context.Background(), llmStep("Llm", "llm"), []byte(`{"query": "hi"}`), bearerFor(`{"sub":"`+sub+`"}`))
	var got map[string]interface{}
	assert.NoError(t, json.Unmarshal(out, &got))
	assert.EqualValues(t, 999, got["max_new_tokens"])
}

// TestGetParamsForStepPerUserFallsThroughToDefault verifies that a per-user
// tenant with no override for a key falls through to the default tenant's group
// rather than missing the cache.
func TestGetParamsForStepPerUserFallsThroughToDefault(t *testing.T) {
	w := newTestWatcher("chatqa")
	w.perUserTenant = true
	sub := "11111111-2222-3333-4444-555555555555"
	// Only the default tenant has a group for this key.
	w.applyEntry("chatqa._global.llm", []byte(`{"max_new_tokens": 256}`), 4, time.Now())

	params, source, version := w.getParamsForStep(context.Background(), llmStep("Llm", "llm"), sub)
	assert.Equal(t, sourceKV, source)
	assert.Equal(t, int64(4), version)
	assert.EqualValues(t, 256, params["max_new_tokens"])
}

// TestApplyEntryPerUserRejectsMalformedTenant verifies that with per-user
// resolution on, a key whose tenant segment is not charset-safe is not cached:
// the router only resolves tenants that pass validTenant, so such keys would be
// unreachable and let stray bucket keys grow the cache.
func TestApplyEntryPerUserRejectsMalformedTenant(t *testing.T) {
	w := newTestWatcher("chatqa")
	w.perUserTenant = true
	// Empty tenant, and tenants carrying characters the resolver never emits.
	w.applyEntry("chatqa..llm", []byte(`{"max_new_tokens": 1}`), 1, time.Now())
	w.applyEntry("chatqa.user:1.llm", []byte(`{"max_new_tokens": 2}`), 1, time.Now())
	w.applyEntry("chatqa.a b.llm", []byte(`{"max_new_tokens": 3}`), 1, time.Now())

	w.mu.RLock()
	defer w.mu.RUnlock()
	assert.Empty(t, w.cache)
}

// TestApplyEntryPerUserAcceptsValidTenant verifies that with per-user
// resolution on, a charset-safe per-user tenant key is cached under its scope.
func TestApplyEntryPerUserAcceptsValidTenant(t *testing.T) {
	w := newTestWatcher("chatqa")
	w.perUserTenant = true
	sub := "11111111-2222-3333-4444-555555555555"
	w.applyEntry("chatqa."+sub+".llm", []byte(`{"max_new_tokens": 7}`), 1, time.Now())

	w.mu.RLock()
	_, ok := w.cache[configScope{tenant: sub, paramsKey: "llm"}]
	w.mu.RUnlock()
	assert.True(t, ok)
}

// TestResolveTenantRejectsOversizedToken verifies that a token whose claims
// segment exceeds the size cap is not decoded and falls back to the default,
// bounding the work an untrusted header can cause on the hot path.
func TestResolveTenantRejectsOversizedToken(t *testing.T) {
	w := newTestWatcher("chatqa")
	w.perUserTenant = true

	seg := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	sub := "11111111-2222-3333-4444-555555555555"
	// A valid sub padded with a large ignored claim so the encoded segment
	// exceeds the cap.
	claims := `{"sub":"` + sub + `","pad":"` + strings.Repeat("x", maxJWTClaimsSegmentBytes) + `"}`
	h := http.Header{}
	h.Set("Authorization", "Bearer "+seg(`{}`)+"."+seg(claims)+".sig")

	assert.Equal(t, "_global", w.resolveTenant(h))
}

// TestGetParamsForStepPerUserLastKnownGoodDuringOutage verifies that a per-user
// group cached by the watch stays sticky when the watch goes down, rather than
// silently dropping the user to the default tenant's group.
func TestGetParamsForStepPerUserLastKnownGoodDuringOutage(t *testing.T) {
	w := newTestWatcher("chatqa")
	w.perUserTenant = true
	sub := "11111111-2222-3333-4444-555555555555"
	w.applyEntry("chatqa._global.llm", []byte(`{"max_new_tokens": 256}`), 1, time.Now())
	w.applyEntry("chatqa."+sub+".llm", []byte(`{"max_new_tokens": 999}`), 2, time.Now())

	// The watch drops.
	w.mu.Lock()
	w.connected = false
	w.mu.Unlock()

	params, source, version := w.getParamsForStep(context.Background(), llmStep("Llm", "llm"), sub)
	assert.Equal(t, sourceLKG, source)
	assert.Equal(t, int64(2), version)
	assert.EqualValues(t, 999, params["max_new_tokens"], "the user's cached override must survive the outage")
}

func TestValidTenant(t *testing.T) {
	assert.True(t, validTenant("_global"))
	assert.True(t, validTenant("11111111-2222-3333-4444-555555555555"))
	assert.True(t, validTenant("User_123-abc"))
	assert.False(t, validTenant(""))
	assert.False(t, validTenant("user:123"))
	assert.False(t, validTenant("a.b"))
	assert.False(t, validTenant("a b"))
	assert.False(t, validTenant("a*b"))
}

// TestMcGraphHandlerAllStepsSkipped verifies that a graph whose only step is
// the obsolete Fingerprint step returns an empty success instead of
// dereferencing a nil response body.
func TestMcGraphHandlerAllStepsSkipped(t *testing.T) {
	prevGraph := mcGraph
	prevW := configW
	defer func() { mcGraph = prevGraph; configW = prevW }()

	configW = newTestWatcher("chatqa")
	mcGraph = &mcv1alpha3.GMConnector{
		Spec: mcv1alpha3.GMConnectorSpec{
			Nodes: map[string]mcv1alpha3.Router{
				"root": {
					RouterType: mcv1alpha3.Sequence,
					Steps: []mcv1alpha3.Step{
						{
							StepName: "Fingerprint",
							Executor: mcv1alpha3.Executor{
								InternalService: mcv1alpha3.GMCTarget{ServiceName: "fgp-svc"},
							},
						},
					},
				},
			},
		},
	}

	req, err := http.NewRequest("POST", "/", bytes.NewBufferString(`{"query": "hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	mcGraphHandler(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Empty(t, rr.Body.String())
}

// TestSequencePipelineSkipsFingerprintAndInjects drives handleSequencePipeline
// through a graph that still carries a Fingerprint step and asserts the step is
// skipped (no HTTP call) while the following step receives its injected group.
func TestSequencePipelineSkipsFingerprintAndInjects(t *testing.T) {
	var fingerprintCalled atomic.Int32
	fingerprint := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		fingerprintCalled.Add(1)
		_, _ = rw.Write([]byte(`{}`))
	}))
	defer fingerprint.Close()
	fingerprintURL, err := apis.ParseURL(fingerprint.URL)
	if err != nil {
		t.Fatalf("failed to parse fingerprint url")
	}

	// The LLM step echoes the request body it received, so the test can assert
	// the injected parameter reached it.
	var llmBody atomic.Value
	llm := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		llmBody.Store(body)
		_, _ = rw.Write([]byte(`{"text": "ok"}`))
	}))
	defer llm.Close()
	llmURL, err := apis.ParseURL(llm.URL)
	if err != nil {
		t.Fatalf("failed to parse llm url")
	}

	w := newTestWatcher("chatqa")
	w.applyEntry("chatqa._global.llm", []byte(`{"max_new_tokens": 321}`), 1, time.Now())
	prev := configW
	configW = w
	defer func() { configW = prev }()

	graph := mcv1alpha3.GMConnector{
		Spec: mcv1alpha3.GMConnectorSpec{
			Nodes: map[string]mcv1alpha3.Router{
				"root": {
					RouterType: mcv1alpha3.Sequence,
					Steps: []mcv1alpha3.Step{
						{
							StepName:   "Fingerprint",
							ServiceURL: fingerprintURL.String(),
							Executor: mcv1alpha3.Executor{
								InternalService: mcv1alpha3.GMCTarget{ServiceName: "fgp-svc"},
							},
						},
						{
							StepName:   "Llm",
							ServiceURL: llmURL.String(),
							Data:       "$response",
							Executor: mcv1alpha3.Executor{
								InternalService: mcv1alpha3.GMCTarget{ServiceName: "llm-svc"},
							},
						},
					},
				},
			},
		},
	}

	input := []byte(`{"query": "hi"}`)
	res, _, err := routeStep(context.Background(), "root", graph, input, input, http.Header{})
	if err != nil {
		t.Fatalf("routeStep returned error: %v", err)
	}
	if res != nil {
		_, _ = io.ReadAll(res)
		_ = res.Close()
	}

	assert.Equal(t, int32(0), fingerprintCalled.Load(), "fingerprint step must not be called")

	raw, ok := llmBody.Load().([]byte)
	assert.True(t, ok, "llm step should have received a request")
	var llmReq map[string]interface{}
	assert.NoError(t, json.Unmarshal(raw, &llmReq))
	assert.EqualValues(t, 321, llmReq["max_new_tokens"], "llm step should receive its injected params")
}
