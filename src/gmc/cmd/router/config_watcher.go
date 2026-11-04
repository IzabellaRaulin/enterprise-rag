/*
* Copyright (C) 2024-2026 Intel Corporation
* SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	mcv1alpha3 "erag.intel.com/gmc/api/v1alpha3"
	"erag.intel.com/gmc/internal/natsauth"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/attribute"
	api "go.opentelemetry.io/otel/metric"
)

const (
	// configKVBucket is the JetStream key/value bucket the GMC controller
	// projects the fingerprint_config table into. The router only ever reads
	// from it.
	configKVBucket = "fingerprint"

	// configParamsKey is the step config entry that names the parameter group a
	// step should be injected with. When absent the step name (lowercased) is
	// used, so single-instance pipelines need no annotation.
	configParamsKey = "paramsKey"

	// fingerprintStepName marks the obsolete fingerprint step. Parameters now
	// come from the KV projection, so a step with this name is skipped rather
	// than called, keeping pipelines that still carry the node working.
	fingerprintStepName = "Fingerprint"

	// configReconnectBackoff is the pause between connection attempts while the
	// KV bucket is unreachable at startup or after a drop.
	configReconnectBackoff = time.Second

	// configHTTPTimeout bounds the legacy fallback request to the fingerprint
	// microservice.
	configHTTPTimeout = 5 * time.Second

	// legacyTTL bounds how long a legacy fallback result is reused from RAM
	// before the fingerprint microservice is queried again. It keeps a step
	// whose key is not yet in the KV projection from issuing an HTTP fetch on
	// every request while still letting a real KV update take over promptly.
	legacyTTL = 30 * time.Second

	// configPath is the fingerprint microservice endpoint that returns a single
	// parameter group for a (pipeline, tenant, params_key), used only when the KV
	// projection has no entry for a step. It is scope-aware, so the legacy path
	// resolves the same group the KV path would rather than the microservice's own
	// default pipeline's packed set.
	configPath = "/v1/system_fingerprint/config"

	// configValuesField is the field in the config response that carries the
	// group's wire-shaped fragment, the same payload the KV projection delivers.
	configValuesField = "values"

	// maxJWTClaimsSegmentBytes bounds the base64url claims segment the router
	// will decode from a request's Authorization header, so an oversized token
	// on the hot path cannot force large allocations. A real token's claims are
	// well under this.
	maxJWTClaimsSegmentBytes = 8192
)

// Source labels reported on the config.source metric.
const (
	sourceKV     = "kv"
	sourceLegacy = "legacy"
	sourceLKG    = "lkg"
)

// State labels reported on the config.nats.state metric. natsDisabled and
// natsFailed both leave the router on the legacy fallback, but only natsFailed
// is unexpected: NATS was configured yet the connection could not be
// established, so it is the state to alert on.
const (
	natsDisabled  = "disabled"
	natsConnected = "connected"
	natsFailed    = "failed"
)

// configScope identifies one cached parameter group. The tenant is the router's
// configured default (usually "_global") unless per-user resolution is enabled,
// in which case a request's own tenant, taken from its JWT, can select a
// user-specific group instead.
type configScope struct {
	tenant    string
	paramsKey string
}

// cachedConfig is one parameter group as held in RAM: the fragment as the KV
// projection delivered it, already in the wire shape downstream services read,
// plus the version and update time used for metrics. fromKV distinguishes a
// live value delivered by the KV watch from one cached after a legacy HTTP
// fetch, so the two are not reported under the same source.
type cachedConfig struct {
	params    map[string]interface{}
	version   int64
	updatedAt time.Time
	fromKV    bool
}

// configWatcher keeps an in-memory copy of the fingerprint parameter groups for
// this pipeline and injects the group a step asks for into that step's request.
// It is a read-only consumer of the KV bucket: a background watch keeps the
// cache fresh, and it never writes to the bucket. When the bucket is
// unreachable it falls back to a direct HTTP call to the fingerprint
// microservice and, failing that, to the last value it cached.
type configWatcher struct {
	pipeline       string
	tenant         string
	natsURL        string
	fingerprintURL string
	// perUserTenant enables resolving a request's tenant from its JWT "sub" so a
	// user can carry their own parameter group. When false the watcher behaves
	// exactly as before: every request resolves to the configured tenant.
	perUserTenant bool

	mu    sync.RWMutex
	cache map[configScope]cachedConfig
	// connected is true while the KV watch is established. It gates only the
	// KV-delivered fast path: a fromKV value is served as sourceKV only while
	// connected. Legacy TTL reuse and last-known-good are not gated by it, so a
	// recent legacy result is still reused and the last cached value is still
	// served when the watch is down.
	connected bool
	// kv is the bucket handle used for the resync reads triggered on reconnect.
	kv jetstream.KeyValue

	httpClient *http.Client
}

// newConfigWatcher builds the watcher from the environment. The pipeline is
// taken from PIPELINE_NAME, falling back to the graph's name and then to
// "default"; the tenant from TENANT_NAME (default "_global"). NATS_URL and
// FINGERPRINT_SERVICE_URL wire the KV source and the legacy fallback.
// FINGERPRINT_PER_USER_TENANT (default false) opts in to resolving a request's
// tenant from its JWT.
func newConfigWatcher(graph *mcv1alpha3.GMConnector) *configWatcher {
	pipeline := getEnvOrDefault("PIPELINE_NAME", "")
	if pipeline == "" && graph != nil {
		pipeline = graph.Name
	}
	if pipeline == "" {
		pipeline = "default"
	}

	return &configWatcher{
		pipeline:       pipeline,
		tenant:         getEnvOrDefault("TENANT_NAME", "_global"),
		natsURL:        getEnvOrDefault("NATS_URL", ""),
		fingerprintURL: getEnvOrDefault("FINGERPRINT_SERVICE_URL", "http://fingerprint-svc.fingerprint.svc:6012"),
		perUserTenant:  getEnvBool("FINGERPRINT_PER_USER_TENANT", false),
		cache:          make(map[configScope]cachedConfig),
		httpClient:     &http.Client{Timeout: configHTTPTimeout},
	}
}

// start connects to NATS and keeps the cache in sync in the background. When
// NATS_URL is empty the watcher does not connect and the router serves entirely
// from the HTTP fallback. When NATS_URL is set but NATS or the bucket cannot be
// reached, the watcher stays in HTTP fallback mode instead of failing the
// router and retries the connection with a fixed backoff until the context is
// cancelled.
func (w *configWatcher) start(ctx context.Context) {
	if w.natsURL == "" {
		log.Info("config watcher: NATS_URL not set, using fingerprint HTTP fallback",
			"pipeline", w.pipeline)
		w.recordNATSState(ctx, natsDisabled)
		return
	}
	go w.run(ctx)
}

// run drives the connect/resync/watch cycle. NATS handles reconnection
// transparently, so a lost connection is repaired underneath the running watch;
// a reconnect additionally triggers a full resync so revisions missed while
// disconnected are picked up.
func (w *configWatcher) run(ctx context.Context) {
	for ctx.Err() == nil {
		nc, kv := w.connect(ctx)
		if kv == nil {
			return
		}

		// Warm the cache from the current bucket contents before serving from
		// RAM, so requests during the startup window take the HTTP fallback
		// instead of seeing an empty cache.
		if err := w.resync(ctx, kv); err != nil {
			log.Error(err, "config watcher: initial resync failed", "pipeline", w.pipeline)
		}
		w.setConnected(kv, true)

		w.watch(ctx, kv)
		w.setConnected(nil, false)
		nc.Close()

		select {
		case <-ctx.Done():
			return
		default:
			// The watch ended without the context being cancelled, so the
			// connection or the watch was lost and the router is back on the
			// legacy fallback. Mark the failed state so the gauge does not stay
			// stuck at connected while degraded, then retry.
			w.recordNATSState(ctx, natsFailed)
			if !sleepCtx(ctx, configReconnectBackoff) {
				return
			}
		}
	}
}

// connect opens the NATS connection and the KV bucket, retrying with a backoff
// until it succeeds or the context is cancelled. The returned connection is
// owned by the caller, which closes it when the watch ends. Both values are nil
// when the context is cancelled or when the NATS auth options are configured
// inconsistently; in the latter case the router stays in HTTP fallback.
func (w *configWatcher) connect(ctx context.Context) (*nats.Conn, jetstream.KeyValue) {
	authOpts, err := natsauth.Options()
	if err != nil {
		// NATS was configured (NATS_URL set) but the auth material is missing or
		// inconsistent, so the router is stuck on the legacy fallback. Surface it
		// loudly and mark the failed state so alerting can catch a silent KV
		// outage rather than treating it like intentionally-disabled NATS.
		log.Error(err, "config watcher: invalid NATS auth options, staying in HTTP fallback", "url", w.natsURL)
		w.recordNATSState(ctx, natsFailed)
		return nil, nil
	}
	for ctx.Err() == nil {
		opts := append([]nats.Option{
			nats.MaxReconnects(-1),
			nats.ReconnectWait(configReconnectBackoff),
			nats.ReconnectHandler(func(_ *nats.Conn) {
				log.Info("config watcher: NATS reconnected, resyncing", "pipeline", w.pipeline)
				// Resync off the callback path: a full Keys()+Get() pass can be
				// slow and must not block NATS' reconnect handling.
				go w.resyncCurrent(ctx)
			}),
		}, authOpts...)
		nc, err := nats.Connect(w.natsURL, opts...)
		if err != nil {
			log.Error(err, "config watcher: NATS connect failed, will retry", "url", w.natsURL)
			w.recordNATSState(ctx, natsFailed)
			if !sleepCtx(ctx, configReconnectBackoff) {
				return nil, nil
			}
			continue
		}

		kv, err := w.openBucket(ctx, nc)
		if err != nil {
			log.Error(err, "config watcher: KV bucket unavailable, will retry", "bucket", configKVBucket)
			w.recordNATSState(ctx, natsFailed)
			nc.Close()
			if !sleepCtx(ctx, configReconnectBackoff) {
				return nil, nil
			}
			continue
		}
		log.Info("config watcher: connected", "bucket", configKVBucket, "pipeline", w.pipeline)
		w.recordNATSState(ctx, natsConnected)
		return nc, kv
	}
	return nil, nil
}

// openBucket returns a handle to the projection bucket. It is opened read-only:
// the router never creates or mutates the bucket, which the controller owns.
func (w *configWatcher) openBucket(ctx context.Context, nc *nats.Conn) (jetstream.KeyValue, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	kv, err := js.KeyValue(ctx, configKVBucket)
	if err != nil {
		return nil, fmt.Errorf("open KV bucket %q: %w", configKVBucket, err)
	}
	return kv, nil
}

// watch consumes updates for this pipeline's keys until the watch ends (context
// cancelled or connection lost). Each update replaces or removes the matching
// cache entry.
func (w *configWatcher) watch(ctx context.Context, kv jetstream.KeyValue) {
	watcher, err := kv.Watch(ctx, w.watchSubject())
	if err != nil {
		log.Error(err, "config watcher: failed to establish watch", "pipeline", w.pipeline)
		return
	}
	defer func() {
		if err := watcher.Stop(); err != nil {
			log.Error(err, "config watcher: error stopping watch")
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-watcher.Updates():
			if !ok {
				return
			}
			// A nil entry marks the end of the initial value set.
			if entry == nil {
				continue
			}
			switch entry.Operation() {
			case jetstream.KeyValueDelete, jetstream.KeyValuePurge:
				w.deleteKey(entry.Key())
			default:
				w.applyEntry(entry.Key(), entry.Value(), entry.Revision(), entry.Created())
			}
		}
	}
}

// resync reads every key for this pipeline and refreshes the cache. It runs on
// each (re)connection so revisions delivered while the watch was down are not
// missed.
func (w *configWatcher) resync(ctx context.Context, kv jetstream.KeyValue) error {
	keys, err := kv.Keys(ctx)
	if err != nil {
		if err == jetstream.ErrNoKeysFound {
			return nil
		}
		return fmt.Errorf("list keys: %w", err)
	}
	prefix := w.keyPrefix()
	for _, key := range keys {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		entry, err := kv.Get(ctx, key)
		if err != nil {
			log.Error(err, "config watcher: resync get failed", "key", key)
			continue
		}
		w.applyEntry(entry.Key(), entry.Value(), entry.Revision(), entry.Created())
	}
	return nil
}

// resyncCurrent runs a resync against the bucket handle in use, if any. It is
// called from the NATS reconnect callback, which has no handle of its own.
func (w *configWatcher) resyncCurrent(ctx context.Context) {
	w.mu.RLock()
	kv := w.kv
	w.mu.RUnlock()
	if kv == nil {
		return
	}
	if err := w.resync(ctx, kv); err != nil {
		log.Error(err, "config watcher: resync after reconnect failed", "pipeline", w.pipeline)
	}
}

// applyEntry parses a KV key and stores its value under the key's scope. The
// projected value is already in the wire shape downstream services read, so the
// fragment is cached and injected as delivered, apart from a numeric top-level
// "version" field that is reserved metadata and dropped; the router holds no
// knowledge of which groups are flat or nested. Keys for other pipelines and
// malformed values are ignored.
func (w *configWatcher) applyEntry(key string, value []byte, revision uint64, updatedAt time.Time) {
	tenant, paramsKey, ok := w.parseKey(key)
	if !ok {
		return
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(value, &raw); err != nil {
		log.Error(err, "config watcher: ignoring malformed KV value", "key", key)
		return
	}
	// A JSON null unmarshals to a nil map without error; treat it like any other
	// malformed value and skip it, so a stray null does not cache an empty
	// KV-sourced fragment that would suppress the legacy/last-known-good fallback.
	if raw == nil {
		log.Info("config watcher: ignoring null KV value", "key", key)
		return
	}

	version := versionFromValue(raw, revision)
	// A numeric top-level "version" is projection metadata for the version
	// metric, not a downstream parameter, so it is dropped before the fragment
	// is cached and injected. The same numeric check as versionFromValue is used
	// so what feeds the metric is exactly what is stripped, regardless of the
	// numeric type the value carries.
	if _, ok := numericVersion(raw["version"]); ok {
		delete(raw, "version")
	}

	entry := cachedConfig{
		params:    raw,
		version:   version,
		updatedAt: updatedAt,
		fromKV:    true,
	}
	w.mu.Lock()
	w.cache[configScope{tenant: tenant, paramsKey: paramsKey}] = entry
	w.mu.Unlock()
}

// deleteKey drops a scope from the cache when its KV key is removed.
func (w *configWatcher) deleteKey(key string) {
	tenant, paramsKey, ok := w.parseKey(key)
	if !ok {
		return
	}
	w.mu.Lock()
	delete(w.cache, configScope{tenant: tenant, paramsKey: paramsKey})
	w.mu.Unlock()
}

// parseKey splits a KV key of the form <pipeline>.<tenant>.<paramsKey> and keeps
// only well-formed keys for this router's pipeline, so entries for other
// pipelines or malformed keys never enter the cache. The key must have exactly
// three dot-separated segments (matching the controller's key format), and the
// paramsKey segment must be non-empty and free of whitespace and subject
// wildcards. The tenant segment must be the configured tenant unless per-user
// resolution is enabled, in which case any charset-safe tenant for this
// pipeline is kept so per-user groups populate the cache alongside the default.
// A malformed tenant segment is rejected: the router only ever resolves tenants
// that pass validTenant, so caching others would be unreachable and let stray
// bucket keys grow the cache.
func (w *configWatcher) parseKey(key string) (tenant, paramsKey string, ok bool) {
	parts := strings.Split(key, ".")
	if len(parts) != 3 || parts[0] != w.pipeline {
		return "", "", false
	}
	if w.perUserTenant {
		if !validTenant(parts[1]) {
			return "", "", false
		}
	} else if parts[1] != w.tenant {
		return "", "", false
	}
	if parts[2] == "" || strings.ContainsAny(parts[2], " \t\r\n*>") {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// watchSubject is the KV subject the watch subscribes to. It scopes to the
// configured tenant unless per-user resolution is enabled, in which case a
// two-token wildcard covers every tenant for this pipeline. Either way a
// single-token wildcard per segment matches exactly the
// <pipeline>.<tenant>.<paramsKey> shape; ">" could pull in keys with extra
// segments.
func (w *configWatcher) watchSubject() string {
	if w.perUserTenant {
		return w.pipeline + ".*.*"
	}
	return w.pipeline + "." + w.tenant + ".*"
}

// keyPrefix is the resync key prefix matching watchSubject: the pipeline alone
// when per-user resolution is enabled (every tenant is kept), or the
// pipeline.tenant pair otherwise.
func (w *configWatcher) keyPrefix() string {
	if w.perUserTenant {
		return w.pipeline + "."
	}
	return w.pipeline + "." + w.tenant + "."
}

// resolveTenant picks the tenant a request's parameters are looked up under. It
// returns the configured default unless per-user resolution is enabled and the
// request carries a Bearer JWT with a usable "sub" claim, in which case that
// sub is the tenant. Any missing, malformed or non-conforming sub falls back to
// the default so a request never yields an invalid KV key.
func (w *configWatcher) resolveTenant(headers http.Header) string {
	if !w.perUserTenant {
		return w.tenant
	}
	sub := subFromAuthHeader(headers.Get("Authorization"))
	if !validTenant(sub) {
		return w.tenant
	}
	return sub
}

// subFromAuthHeader extracts the "sub" claim from a Bearer JWT. The signature is
// not verified: the gateway does that upstream, and the router only reads the
// claim. It returns an empty string when the header is absent, does not use the
// Bearer scheme, or the token cannot be decoded.
func subFromAuthHeader(authHeader string) string {
	// The Bearer scheme name is case-insensitive (RFC 6750/7235); a header using
	// any other scheme carries no JWT to read.
	const bearerPrefix = "Bearer "
	if len(authHeader) < len(bearerPrefix) || !strings.EqualFold(authHeader[:len(bearerPrefix)], bearerPrefix) {
		return ""
	}
	token := strings.TrimSpace(authHeader[len(bearerPrefix):])
	if token == "" {
		return ""
	}
	// A JWT is a single token with no internal whitespace; anything with a space
	// (e.g. trailing data after the token) is not one, so reject it rather than
	// reading a claim from a malformed header.
	if strings.ContainsAny(token, " \t\r\n") {
		return ""
	}
	// A JWT is header.payload.signature; the claims live in the middle segment,
	// base64url-encoded without padding. The segments are sliced out by dot
	// index rather than split, so an untrusted header carrying many dots does
	// not allocate a slice entry per dot on the request hot path.
	firstDot := strings.IndexByte(token, '.')
	if firstDot < 0 {
		return ""
	}
	secondDot := strings.IndexByte(token[firstDot+1:], '.')
	if secondDot < 0 {
		return ""
	}
	secondDot += firstDot + 1
	// A well-formed JWT has exactly two dots; a third means it is not one.
	if strings.IndexByte(token[secondDot+1:], '.') >= 0 {
		return ""
	}
	// Every segment must be present: no empty header, claims or signature.
	if firstDot == 0 || secondDot == firstDot+1 || secondDot == len(token)-1 {
		return ""
	}
	claimsSegment := token[firstDot+1 : secondDot]
	// The header is untrusted input on the request hot path, so cap the claims
	// segment before decoding to bound the work an oversized token can cause. A
	// real token's claims are far smaller than this.
	if len(claimsSegment) > maxJWTClaimsSegmentBytes {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(claimsSegment)
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Sub
}

// validTenant reports whether a resolved tenant is safe to use in a KV key. It
// must be non-empty and limited to the characters the fingerprint API accepts
// so the router never emits a key the watch cannot match.
func validTenant(tenant string) bool {
	if tenant == "" {
		return false
	}
	for _, r := range tenant {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// getParamsForStep resolves the parameter group for a step under the given
// tenant. It prefers the RAM cache fed by the KV watch, falls back to a direct
// call to the fingerprint microservice on a miss, and finally to the last value
// it cached. It returns the values, the source that served them and the group
// version.
//
// When tenant is not the configured default it is a per-user tenant: a group is
// served for it only if the watch delivered one, otherwise the lookup falls
// through to the default tenant so a user without an override sees the default
// group. A per-user group is only ever populated by the watch, so when the
// watch is down a previously cached one is served as last-known-good rather
// than dropping the user to the default group. The default-tenant path below is
// unchanged.
func (w *configWatcher) getParamsForStep(ctx context.Context, step *mcv1alpha3.Step, tenant string) (map[string]interface{}, string, int64) {
	paramsKey := mcv1alpha3.ResolveParamsKey(*step)

	if tenant != w.tenant {
		userScope := configScope{tenant: tenant, paramsKey: paramsKey}
		w.mu.RLock()
		entry, found := w.cache[userScope]
		connected := w.connected
		w.mu.RUnlock()
		if found && entry.fromKV {
			if connected {
				return entry.params, sourceKV, entry.version
			}
			// The watch is down but this user's group was cached; keep the
			// override sticky through the outage instead of falling through to
			// the default tenant.
			return entry.params, sourceLKG, entry.version
		}
	}

	scope := configScope{tenant: w.tenant, paramsKey: paramsKey}

	w.mu.RLock()
	entry, found := w.cache[scope]
	connected := w.connected
	w.mu.RUnlock()

	// Serve from RAM only for a value the watch actually delivered; a value
	// merely cached from an earlier legacy fetch must not be reported as KV.
	if found && connected && entry.fromKV {
		return entry.params, sourceKV, entry.version
	}

	// Reuse a recent legacy result for a key the watch has not delivered yet
	// instead of re-fetching on every request. This is deliberately not gated on
	// connected: it applies only to non-fromKV entries, so it reuses a legacy
	// value whether the watch is up (key simply absent from KV) or down. A real
	// KV update replaces the entry with fromKV=true and is taken by the branch
	// above before this one, so it still supersedes the cached legacy value.
	if found && !entry.fromKV && time.Since(entry.updatedAt) < legacyTTL {
		return entry.params, sourceLegacy, entry.version
	}

	params, err := w.fetchLegacy(ctx, scope.paramsKey, scope.tenant)
	if err == nil {
		w.storeLegacy(scope, params)
		return params, sourceLegacy, 0
	}
	log.WithValues("paramsKey", scope.paramsKey).
		Error(err, "config watcher: legacy fingerprint fetch failed, using last-known-good")

	// Last-known-good: reuse whatever was cached before, even if the watch is
	// currently down or the entry is stale.
	if found {
		return entry.params, sourceLKG, entry.version
	}
	return map[string]interface{}{}, sourceLKG, 0
}

// storeLegacy caches a legacy fetch result under scope, but only if a KV value
// has not arrived for the scope in the meantime. The watch can write a fresh
// fromKV entry while the legacy fetch was in flight (the fetch holds no lock);
// overwriting it would drop the just-arrived KV value and mislabel the source,
// so a present fromKV entry is left untouched.
func (w *configWatcher) storeLegacy(scope configScope, params map[string]interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if entry, found := w.cache[scope]; found && entry.fromKV {
		return
	}
	w.cache[scope] = cachedConfig{params: params, updatedAt: time.Now()}
}

// injectParamsForStep merges the step's parameter group onto the top level of
// its request and records which source served the values. The tenant is
// resolved from the request headers, so a user with a per-user group receives
// it. The request is returned unchanged when the group is empty or is not a
// JSON object. The obsolete fingerprint step is skipped by the caller before
// this is reached.
func (w *configWatcher) injectParamsForStep(ctx context.Context, step *mcv1alpha3.Step, request []byte, headers http.Header) []byte {
	tenant := w.resolveTenant(headers)
	params, source, version := w.getParamsForStep(ctx, step, tenant)

	paramsKey := mcv1alpha3.ResolveParamsKey(*step)
	if configSourceCounter != nil {
		configSourceCounter.Add(ctx, 1, api.WithAttributes(
			attribute.String("source", source),
			attribute.String("pipeline", w.pipeline),
			attribute.String("params_key", paramsKey),
		))
	}
	if configVersionGauge != nil {
		configVersionGauge.Record(ctx, version, api.WithAttributes(
			attribute.String("pipeline", w.pipeline),
			attribute.String("params_key", paramsKey),
		))
	}

	if len(params) == 0 {
		return request
	}
	return injectTopLevel(request, params, callerParamsFromContext(ctx))
}

// fetchLegacy calls the fingerprint microservice for a single parameter group,
// resolved under this router's pipeline and the given tenant so the legacy path
// agrees with the KV path on scope. It is the degraded path used when the KV
// projection has no entry for a step. The response carries the group's fragment
// under "values" already in the wire shape downstream reads, so only that
// fragment is returned. A 404 means the key has no group for this scope — the
// same as an unmanaged step having no KV entry — so it yields an empty group
// rather than an error, injecting nothing.
func (w *configWatcher) fetchLegacy(ctx context.Context, paramsKey, tenant string) (map[string]interface{}, error) {
	base := strings.TrimRight(w.fingerprintURL, "/") + configPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("params_key", paramsKey)
	q.Set("pipeline", w.pipeline)
	q.Set("tenant", tenant)
	req.URL.RawQuery = q.Encode()

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Drain to EOF before closing so the connection can be reused: an early
		// return (a 404 once per TTL for an unmanaged step, or an error status)
		// otherwise leaves the body unread and forces a reconnect on the next
		// legacy lookup.
		_, _ = io.Copy(io.Discard, resp.Body)
		if cerr := resp.Body.Close(); cerr != nil {
			log.Error(cerr, "config watcher: error closing fingerprint response body")
		}
	}()
	if resp.StatusCode == http.StatusNotFound {
		return map[string]interface{}{}, nil
	}
	if !isSuccessFul(resp.StatusCode) {
		return nil, fmt.Errorf("fingerprint returned status %d", resp.StatusCode)
	}

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, err
	}
	values, ok := decoded[configValuesField].(map[string]interface{})
	if !ok {
		return map[string]interface{}{}, nil
	}
	return values, nil
}

// recordNATSState reports the KV watch state on the config.nats.state gauge,
// marking the active state 1 and the others 0 so a single series is set at a
// time. The pipeline and tenant are carried as attributes to match the other
// config metrics' labeling and to keep the series unambiguous if more than one
// watcher runs in-process. The failed state is the alertable one: NATS was
// configured but the connection could not be established or was lost, so the
// router is serving from the legacy fallback rather than the KV projection.
func (w *configWatcher) recordNATSState(ctx context.Context, active string) {
	if configNATSStateGauge == nil {
		return
	}
	for _, state := range []string{natsDisabled, natsConnected, natsFailed} {
		value := int64(0)
		if state == active {
			value = 1
		}
		configNATSStateGauge.Record(ctx, value, api.WithAttributes(
			attribute.String("state", state),
			attribute.String("pipeline", w.pipeline),
			attribute.String("tenant", w.tenant),
		))
	}
}

// setConnected updates the connection state and the bucket handle used for
// resyncs under the lock.
func (w *configWatcher) setConnected(kv jetstream.KeyValue, connected bool) {
	w.mu.Lock()
	w.kv = kv
	w.connected = connected
	w.mu.Unlock()
}

// versionFromValue returns the group version. The projected value does not
// carry one today, so the KV revision is used as a monotonic stand-in unless a
// numeric "version" field is present.
func versionFromValue(values map[string]interface{}, revision uint64) int64 {
	if n, ok := numericVersion(values["version"]); ok {
		return n
	}
	return int64(revision)
}

// numericVersion reads a "version" value as an integer. It handles float64 (the
// default json.Unmarshal number type) and json.Number, plus the int, int64 and
// uint64 a value assembled in Go rather than decoded could carry. A fractional
// value is truncated toward zero and a magnitude beyond the int64 range is
// clamped to its bound, so an out-of-range or non-integral version still yields
// a usable stand-in rather than a wrapped or negative one. It reports false for
// a missing or non-numeric value.
func numericVersion(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return floatToInt64(n), true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, true
		}
		if f, err := n.Float64(); err == nil {
			return floatToInt64(f), true
		}
	case int:
		return int64(n), true
	case int64:
		return n, true
	case uint64:
		if n > math.MaxInt64 {
			return math.MaxInt64, true
		}
		return int64(n), true
	}
	return 0, false
}

// floatToInt64 truncates f toward zero and clamps it to the int64 range, so a
// large or fractional version reported as a float does not wrap when cast.
func floatToInt64(f float64) int64 {
	t := math.Trunc(f)
	switch {
	case t >= math.MaxInt64:
		return math.MaxInt64
	case t <= math.MinInt64:
		return math.MinInt64
	default:
		return int64(t)
	}
}

// requestOverridableParams names the parameters a caller may set per request.
// The stored value wins for everything else, so a client cannot loosen a
// guardrail or a resource cap by putting it in the request body -- the guard
// services read their scanner config straight out of the request, so a blanket
// "request wins" would let any caller disable them. Keep this set minimal.
var requestOverridableParams = map[string]struct{}{
	// docsum exposes the chain type as a per-summary choice in its UI, so the
	// stored value is the default rather than an override.
	"summary_type": {},
	// Response framing only, for every pipeline; the stored value is the default
	// when a caller omits it.
	"stream": {},
}

// injectTopLevel overlays params onto the top level of a JSON object request.
// Overridable keys are matched against callerParams, not the request: upstream
// steps fill fields such as stream with their own defaults, so the step input
// cannot tell a caller's value from an upstream default.
func injectTopLevel(request []byte, params map[string]interface{}, callerParams map[string]interface{}) []byte {
	var data map[string]interface{}
	if err := json.Unmarshal(request, &data); err != nil {
		return request
	}
	for key, value := range params {
		if _, overridable := requestOverridableParams[key]; overridable {
			if sentValue, sent := callerParams[key]; sent {
				data[key] = sentValue
				continue
			}
		}
		data[key] = value
	}
	merged, err := json.Marshal(data)
	if err != nil {
		return request
	}
	return merged
}

// getEnvOrDefault returns the environment variable value for key, or def when
// it is unset or empty.
func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// getEnvBool reads a boolean environment variable, returning def when the value
// is unset or not parseable as a bool.
func getEnvBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// sleepCtx waits for d or until the context is cancelled. It reports false if
// the context was cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
