/*
* Copyright (C) 2024-2026 Intel Corporation
* SPDX-License-Identifier: Apache-2.0
 */

/* Modifications made to this file by [Intel] on [2024]
*  Portions of this file are derived from kserve: https://github.com/kserve/kserve
*  Copyright 2022 The KServe Author
 */

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"

	// "regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MrAlias/otlpr"
	"github.com/go-logr/logr"
	"github.com/tidwall/gjson"
	"golang.org/x/net/http/httpproxy"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	// "crypto/rand"
	// "math/big"

	mcv1alpha3 "erag.intel.com/gmc/api/v1alpha3"
	flag "github.com/spf13/pflag"

	// OpenTelemetry/Metrics: Prometheus and opentelemetry imports
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/metric"
	api "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.opentelemetry.io/otel/trace"

	// OpenTelemetry/Traces:

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	BufferSize    = 1024
	MaxGoroutines = 1024
	ServiceURL    = "serviceUrl"
	ServiceNode   = "node"
	Parameters    = "parameters"
	OtelVersion   = "v0.3.0"

	// EndpointKey names the step config field holding the path a step serves JSON
	// requests on; MultipartEndpointKey names the path it serves multipart uploads
	// on. A pipeline whose entry request may be a file upload (DocSum) sets the
	// latter so the router forwards the upload there instead of the JSON path.
	EndpointKey          = "endpoint"
	MultipartEndpointKey = "multipartEndpoint"
	// MultipartContentType is the media type of a file-upload entry request.
	MultipartContentType = "multipart/form-data"
	// JSONContentType is the media type every step-to-step call uses.
	JSONContentType = "application/json"

	// CallClientTimeoutSeconds bounds a single downstream call. It is long enough
	// for a slow model server to stream a full response but short enough that one
	// hung backend does not pin a connection indefinitely.
	CallClientTimeoutSeconds = 3600
	// GraphHandlerTimeoutSeconds is the overall budget for one request through the
	// whole graph, covering every step. It caps how long a request can occupy a
	// handler goroutine and a server slot.
	GraphHandlerTimeoutSeconds = 3600
	// ReadHeaderTimeoutSeconds bounds how long the server waits for a request's
	// headers, so a slow-loris client cannot hold a connection open cheaply.
	ReadHeaderTimeoutSeconds = 30

	// MaxRequestBodyBytes caps an inbound JSON request body so an oversized or
	// unbounded upload cannot exhaust memory.
	MaxRequestBodyBytes = 100 << 20 // 100 MiB

	// MaxConnsPerHost bounds total connections the shared transport opens to any
	// single downstream host, so a slow backend cannot drive the router to file
	// descriptor exhaustion.
	MaxConnsPerHost = 200
	// MaxInFlightRequests bounds concurrent graph handler goroutines; requests
	// over the limit are rejected with 503 rather than piling up unbounded.
	MaxInFlightRequests = 1024

	// MaxRecursionDepth caps how deep routeStep may recurse into sub-nodes. A
	// graph cycle would otherwise recurse until the stack overflows at request
	// time; exceeding the cap returns 500.
	MaxRecursionDepth = 32

	// MaxDownstreamResponseBytes caps how much of one downstream response the
	// router reads. Intermediate step bodies are read whole (io.ReadAll) before
	// being merged or forwarded, so without a cap a hostile or looping backend
	// could exhaust the router's memory with a single reply.
	MaxDownstreamResponseBytes = 64 << 20 // 64 MiB

	// MaxEnsembleConcurrency caps the steps one Ensemble node runs at a time.
	// The depth cap alone does not bound the work, because nested Ensemble nodes
	// multiply: a graph the webhook admits could otherwise start a goroutine per
	// step per level for a single request.
	MaxEnsembleConcurrency = 16

	// MaxMultipartFieldBytes bounds one non-file form field the router reads out of
	// a multipart entry request while looking for the parameters group. File parts
	// are never read: the body is forwarded to the entry step as it arrived.
	MaxMultipartFieldBytes = 1 << 20 // 1 MiB

	// MaxSSELineBytes bounds a single SSE line the streaming scanner will buffer,
	// so a backend that never emits a newline cannot force unbounded growth. A
	// non-streaming JSON answer arrives as one newline-free document and is read
	// through the same scanner, so this caps a whole response body too, and an
	// answer of this size peaks several times its own size in the handler
	// (scanner regrowth, the assembled string, the remap). Sized against the
	// router's own memory limit rather than the inbound body cap.
	MaxSSELineBytes = 8 << 20 // 8 MiB
)

var (
	OtelServiceName    = "router-service"    // will be overwriteen by OTEL_SERVICE_NAME
	OtelNamespace      = "unknown-namespace" // will be overwriteen by OTEL_NAMESPACE
	OtelExcludedUrls   = []string{}
	debugRequestLogs   = false
	debugRequestTraces = false
	log                logr.Logger

	jsonGraph       = flag.String("graph-json", "", "serialized json graph def")
	mcGraph         *mcv1alpha3.GMConnector
	configW         *configWatcher
	defaultNodeName = "root"
	semaphore       = make(chan struct{}, MaxGoroutines)
	transport       = &http.Transport{
		Proxy:                 proxyForRequest,
		MaxIdleConns:          1000,
		MaxIdleConnsPerHost:   100,
		MaxConnsPerHost:       MaxConnsPerHost,
		IdleConnTimeout:       2 * time.Minute,
		TLSHandshakeTimeout:   time.Minute,
		ExpectContinueTimeout: 30 * time.Second,
	}

	// inFlight counts graph handler goroutines currently running, so the handler
	// can shed load past MaxInFlightRequests instead of accepting work unbounded.
	inFlight atomic.Int64
)

// noProxyCtxKey carries a step's no_proxy list on the request context so the
// shared transport's proxy resolver can honor it per request.
type noProxyCtxKey struct{}

// depthCtxKey carries the current sub-node recursion depth on the request
// context so routeStep can reject a graph cycle before it overflows the stack.
type depthCtxKey struct{}

// routeDepth reads the current sub-node recursion depth from the context,
// defaulting to zero at the root node.
func routeDepth(ctx context.Context) int {
	if d, ok := ctx.Value(depthCtxKey{}).(int); ok {
		return d
	}
	return 0
}

// Carried on the context rather than re-parsed per step: later steps get JSON
// headers, so a multipart entry body cannot be read back from initInput there.
type entryRequestCtxKey struct{}

func withEntryRequest(ctx context.Context, body []byte, headers http.Header) context.Context {
	initReqData, err := entryRequestData(body, headers)
	if err != nil {
		return ctx
	}
	return context.WithValue(ctx, entryRequestCtxKey{}, initReqData)
}

func entryRequestFor(ctx context.Context, body []byte, headers http.Header) (map[string]interface{}, error) {
	if initReqData, ok := ctx.Value(entryRequestCtxKey{}).(map[string]interface{}); ok {
		return initReqData, nil
	}
	// Reached by direct routeStep callers, and by a body mcGraphHandler could not
	// parse, so the pipeline handler reports the parse error with its status code.
	return entryRequestData(body, headers)
}

func callerParamsFromContext(ctx context.Context) map[string]interface{} {
	initReqData, _ := ctx.Value(entryRequestCtxKey{}).(map[string]interface{})
	params, _ := initReqData[Parameters].(map[string]interface{})
	return params
}

// fanoutCtxKey carries one request's ensemble fan-out budget. The budget is
// shared by every node the request reaches, because a per-node limit does not
// bound the tree: nested Ensemble nodes each open their own group, so the
// concurrency still multiplies with depth.
type fanoutCtxKey struct{}

// withFanoutBudget attaches a fan-out budget to a request context, so the
// ensemble nodes it reaches acquire from one pool.
func withFanoutBudget(ctx context.Context) context.Context {
	if _, ok := ctx.Value(fanoutCtxKey{}).(chan struct{}); ok {
		return ctx
	}
	return context.WithValue(ctx, fanoutCtxKey{}, make(chan struct{}, MaxEnsembleConcurrency))
}

// fanoutBudget returns the request's fan-out budget, or nil when none was
// attached (a direct call in a test, for example).
func fanoutBudget(ctx context.Context) chan struct{} {
	budget, _ := ctx.Value(fanoutCtxKey{}).(chan struct{})
	return budget
}

// proxyForRequest resolves the proxy for a downstream request from the standard
// HTTP_PROXY/HTTPS_PROXY/NO_PROXY environment, extended with any per-step
// no_proxy carried on the request context. It replaces the previous
// os.Setenv("no_proxy") call, which mutated process-wide state and raced across
// concurrent requests.
func proxyForRequest(req *http.Request) (*url.URL, error) {
	cfg := httpproxy.FromEnvironment()
	if extra, ok := req.Context().Value(noProxyCtxKey{}).(string); ok && extra != "" {
		if cfg.NoProxy == "" {
			cfg.NoProxy = extra
		} else {
			cfg.NoProxy = cfg.NoProxy + "," + extra
		}
	}
	return cfg.ProxyFunc()(req.URL)
}

type GMCGraphRoutingError struct {
	ErrorMessage string `json:"error"`
	Cause        string `json:"cause"`
	UserMessage  string `json:"user_message"`
}

type ReadCloser struct {
	*bytes.Reader
}

type ErrorResponse struct {
	Detail string `json:"detail"`
}

type Detail struct {
	Type  string      `json:"type"`
	Loc   interface{} `json:"loc"`
	Msg   string      `json:"msg"`
	Input interface{} `json:"input"`
	Ctx   struct {
		Gt int `json:"gt"`
	} `json:"ctx"`
}

type PydanticErrorResponse struct {
	Detail []Detail    `json:"detail"`
	Body   interface{} `json:"body"` // Use interface{} to ignore the nested structure of Body
}

var (
	llmFirstTokenLatencyMeasure metric.Float64Histogram
	llmNextTokenLatencyMeasure  metric.Float64Histogram
	llmAllTokensLatencyMeasure  metric.Float64Histogram

	// pipeline preceding LLM (pre-LLM)
	pipelineLatencyMeasure metric.Float64Histogram
	stepLatencyMeasure     metric.Float64Histogram

	// e2e (pipeline + llm)
	e2eLatencyMeasure                 metric.Float64Histogram
	e2eTimeToFirstTokenLatencyMeasure metric.Float64Histogram

	// fingerprint parameter injection
	configSourceCounter  metric.Int64Counter
	configVersionGauge   metric.Int64Gauge
	configNATSStateGauge metric.Int64Gauge

	// mixed-pipeline fan-out/decision observability
	ensembleFanoutMeasure metric.Int64Histogram
	switchDecisionMeasure metric.Int64Counter

	// RED metrics: request counter by status
	requestCounter metric.Int64Counter
)

func initMeter() {
	// The exporter embeds a default OpenTelemetry Reader and
	// implements prometheus.Collector, allowing it to be used as
	// both a Reader and Collector.
	exporter, err := prometheus.New()
	if err != nil {
		log.Error(err, "metrics: cannot init prometheus collector")
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	otel.SetMeterProvider(provider)

	// ppalucki: Own metrics definition below
	const meterName = "entrag-telemetry"
	meter := provider.Meter(meterName)

	llmFirstTokenLatencyMeasure, err = meter.Float64Histogram(
		"router.llm.first.token.latency",
		metric.WithUnit("ms"),
		metric.WithDescription("Measures the duration of first token generation from the LLM server after."),
		api.WithExplicitBucketBoundaries(1, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16364),
	)
	if err != nil {
		log.Error(err, "metrics: cannot register LLM first token histogram measure")
	}
	llmNextTokenLatencyMeasure, err = meter.Float64Histogram(
		"router.llm.next.token.latency",
		metric.WithUnit("ms"),
		metric.WithDescription("Measures the average latency of generating each token from the LLM server after the first token."),
		api.WithExplicitBucketBoundaries(1, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16364),
	)
	if err != nil {
		log.Error(err, "metrics: cannot register LLM next token histogram measure")
	}

	llmAllTokensLatencyMeasure, err = meter.Float64Histogram(
		"router.llm.all.tokens.latency",
		metric.WithUnit("ms"),
		metric.WithDescription("Measures the duration to generate response from the LLM model server with all tokens."),
		api.WithExplicitBucketBoundaries(1, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16364),
	)
	if err != nil {
		log.Error(err, "metrics: cannot register LLM all token histogram measure")
	}

	pipelineLatencyMeasure, err = meter.Float64Histogram(
		"router.pipeline.latency",
		metric.WithUnit("ms"),
		metric.WithDescription("Measures the duration to going through pipeline steps until first token is being generated (including read data time from client)."),
		api.WithExplicitBucketBoundaries(1, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16364),
	)
	if err != nil {
		log.Error(err, "metrics: cannot register pipeline histogram measure")
	}

	stepLatencyMeasure, err = meter.Float64Histogram(
		"router.pipeline.step",
		metric.WithUnit("ms"),
		metric.WithDescription("Measures the duration to going through step."),
		api.WithExplicitBucketBoundaries(1, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16364),
	)
	if err != nil {
		log.Error(err, "metrics: cannot register step histogram measure")
	}

	e2eLatencyMeasure, err = meter.Float64Histogram(
		"router.e2e.latency",
		metric.WithUnit("ms"),
		metric.WithDescription("Measures the duration to going through all steps end-to-end."),
		api.WithExplicitBucketBoundaries(1, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16364),
	)
	if err != nil {
		log.Error(err, "metrics: cannot register e2e latency histogram measure")
	}

	e2eTimeToFirstTokenLatencyMeasure, err = meter.Float64Histogram(
		"router.e2e.ttft.latency",
		metric.WithUnit("ms"),
		metric.WithDescription("Measures the time from request start to the generation of the first token in the end-to-end."),
		api.WithExplicitBucketBoundaries(1, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16364),
	)
	if err != nil {
		log.Error(err, "metrics: cannot register e2e TTFT latency histogram measure")
	}

	configSourceCounter, err = meter.Int64Counter(
		"router.config.source",
		metric.WithDescription("Counts parameter fetches by the source that served them (kv, legacy, lkg)."),
	)
	if err != nil {
		log.Error(err, "metrics: cannot register config source counter")
	}

	configVersionGauge, err = meter.Int64Gauge(
		"router.config.version",
		metric.WithDescription("Reports the version of the parameter group injected per pipeline and params_key."),
	)
	if err != nil {
		log.Error(err, "metrics: cannot register config version gauge")
	}

	configNATSStateGauge, err = meter.Int64Gauge(
		"router.config.nats.state",
		metric.WithDescription("Reports the KV watch state (1) by state label (disabled, connected, failed); failed means NATS was configured but the connection could not be established, so the router is stuck on the legacy fallback."),
	)
	if err != nil {
		log.Error(err, "metrics: cannot register config NATS state gauge")
	}

	ensembleFanoutMeasure, err = meter.Int64Histogram(
		"router.pipeline.ensemble.fanout",
		metric.WithDescription("Records an Ensemble node's fan-out width, labelled by node name, failure count and resulting status."),
		api.WithExplicitBucketBoundaries(1, 2, 4, 8, 16, 32),
	)
	if err != nil {
		log.Error(err, "metrics: cannot register ensemble fan-out histogram measure")
	}

	switchDecisionMeasure, err = meter.Int64Counter(
		"router.pipeline.switch.decision",
		metric.WithDescription("Counts Switch node routing decisions by chosen branch (or no-match), labelled by node name."),
	)
	if err != nil {
		log.Error(err, "metrics: cannot register switch decision counter")
	}

	requestCounter, err = meter.Int64Counter(
		"router.requests",
		metric.WithDescription("Counts requests by status code class, enabling RED metrics."),
	)
	if err != nil {
		log.Error(err, "metrics: cannot register request counter")
	}

	println("otel/metrics: configured")
}

func initLogs() {
	// if OTEL_LOGS_GRPC_ENDPOINT is set to grpc otlp endpoint like this OTEL_LOGS_GRPC_ENDPOINT=127.0.0.1:4317
	// then global variable log (logr.Logger) will be replaced with logr with sink that sends data to otlp endpoint https://github.com/MrAlias/otlpr
	// otherwise log uses zap from controller-runtime logf.WithName...
	otlpTarget, configured := os.LookupEnv("OTEL_LOGS_GRPC_ENDPOINT")
	if configured {
		conn, err := grpc.NewClient(otlpTarget, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			fmt.Println("error", err)
			//log.Error(err, "failed to configure logger grpc connection")
			os.Exit(1)
		}
		res := resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String(OtelServiceName),
		)
		log = otlpr.NewWithOptions(conn, otlpr.Options{
			LogCaller:     otlpr.All,
			LogCallerFunc: true,
			Batcher:       otlpr.Batcher{Messages: 1, Timeout: 5 * time.Second},
		})
		log = otlpr.WithResource(log, res)

		println("otel/logs: enabled - otlpr logger configured with:", otlpTarget)
		log.Info("OTEL OTLPR sink configured")
	} else {
		log = logf.Log.WithName("GMCGraphRouter")
		logf.SetLogger(zap.New())
		println("otel/logs: disabled - otlrp not configured (OTEL_LOGS_GRPC_ENDPOINT empty)")
	}

}

func initTraces() {
	// BY DEFAULT DO NOT INSTALL TRACES if URLS is NOT GIVEN
	otlpEndpoint, endpointFound := os.LookupEnv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if !endpointFound {
		println("otel/traces: disabled - OTEL_EXPORTER_OTLP_ENDPOINT not set")
		return
	}
	if otlpEndpoint == "" {
		println("otel/traces: disabled - OTEL_EXPORTER_OTLP_ENDPOINT is empty ")
		return
	}

	if os.Getenv("OTEL_TRACES_DISABLED") == "true" {
		println("otel/traces: disabled - because of OTEL_TRACES_DISABLED=true")
		return
	}

	println("otel/traces: enabled OTEL_EXPORTER_OTLP_ENDPOINT (or default localhost will be used):", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))

	excludedUrlsStr, urlsFound := os.LookupEnv("OTEL_GO_EXCLUDED_URLS")
	if urlsFound {
		OtelExcludedUrls = strings.Split(excludedUrlsStr, ",")
	}
	fmt.Println("otel/traces: OTEL_GO_EXCLUDED_URLS =", OtelExcludedUrls)

	ctx := context.Background()
	exporterOtlp, err := otlptracehttp.New(ctx)
	if err != nil {
		log.Error(err, "failed to init trace exporters")
		os.Exit(1)
	}

	samplerRatio := 1.0
	ratioStr, ratioFound := os.LookupEnv("OTEL_TRACES_SAMPLER_FRACTION")
	if ratioFound {
		if samplerRatio, err = strconv.ParseFloat(ratioStr, 64); err == nil {
			if err != nil {
				log.Error(err, "failed to conver sampler ratio to float64")
				os.Exit(1)
			}
		}

	}
	fmt.Println("otel/traces: OTEL_TRACES_SAMPLER_FRACTION =", samplerRatio)

	// Use sdktrace.AlwaysSample sampler to sample all traces.
	// In a production application, use sdktrace.ProbabilitySampler with a desired probability.
	var tp trace.TracerProvider
	if os.Getenv("OTEL_TRACES_CONSOLE_EXPORTER") == "true" {
		println("otel/traces: console exporter enabled (OTEL_TRACES_CONSOLE_EXPORTER=true)")
		exporterStdout, err := stdouttrace.New(
			stdouttrace.WithPrettyPrint(),
			//stdouttrace.WithWriter(os.Stderr),
		)
		if err != nil {
			log.Error(err, "failed to init trace console exporter")
			os.Exit(1)
		}
		tp = sdktrace.NewTracerProvider(
			sdktrace.WithSampler(sdktrace.TraceIDRatioBased(samplerRatio)),
			sdktrace.WithBatcher(exporterOtlp),
			sdktrace.WithSyncer(exporterStdout),
			sdktrace.WithResource(resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(OtelServiceName))),
		)
	} else {
		println("otel/traces: console exporter disabled (missing OTEL_TRACES_CONSOLE_EXPORTER=true)")
		tp = sdktrace.NewTracerProvider(
			sdktrace.WithSampler(sdktrace.TraceIDRatioBased(samplerRatio)),
			sdktrace.WithBatcher(exporterOtlp),
			sdktrace.WithResource(resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(OtelServiceName))),
		)
	}

	// Later us this like this: mainTracer := otel.GetTracerProvider().Tracer("graphtracer")
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
}

func init() {
	println("otel: version:", OtelVersion)
	serviceNameFromEnv, found := os.LookupEnv("OTEL_SERVICE_NAME")
	if found {
		OtelServiceName = serviceNameFromEnv
	}
	println("otel: servicename:", OtelServiceName)
	namespaceFromEnv, found := os.LookupEnv("OTEL_NAMESPACE")
	if found {
		OtelNamespace = namespaceFromEnv
	}
	println("otel: namespace:", OtelNamespace)
	initMeter()
	initLogs()
	initTraces()

	// ENABLE_DEBUG_REQUEST_LOGS will enable debug logs (if "true")
	debugEnvStr, debugEnvFound := os.LookupEnv("ENABLE_DEBUG_REQUEST_LOGS")
	if debugEnvFound && debugEnvStr == "true" {
		debugRequestLogs = true
	}
	fmt.Println("debugRequestLogs:", debugRequestLogs)

	// ENABLE_DEBUG_REQUEST_TRACES will enable debug traces (if "true")
	debugTracesEnvStr, debugTracesEnvFound := os.LookupEnv("ENABLE_DEBUG_REQUEST_TRACES")
	if debugTracesEnvFound && debugTracesEnvStr == "true" {
		debugRequestTraces = true
	}
	fmt.Println("debugRequestTraces:", debugRequestTraces)
}

func (ReadCloser) Close() error {
	// Typically, you would release resources here, but for bytes.Reader, there's nothing to do.
	return nil
}

func NewReadCloser(b []byte) io.ReadCloser {
	return ReadCloser{bytes.NewReader(b)}
}

// limitedReadCloser bounds how much of a downstream response is read while
// keeping the underlying body closable, so the connection is still released.
type limitedReadCloser struct {
	io.Reader
	closer io.Closer
}

func (l limitedReadCloser) Close() error { return l.closer.Close() }

// newLimitedReadCloser caps a response body at n bytes. A backend that streams
// without end, or answers with far more than a pipeline step should, is then
// truncated rather than read into memory whole by the callers downstream.
func newLimitedReadCloser(body io.ReadCloser, n int64) io.ReadCloser {
	return limitedReadCloser{Reader: io.LimitReader(body, n), closer: body}
}

// scanSSELines is a bufio.SplitFunc that returns one line per token including
// its trailing newline, so a "data:"/"json:" prefix is always seen on a whole
// line. The final line without a trailing newline is returned at EOF. Unlike
// bufio.ScanLines it does not strip the newline, since the router forwards each
// SSE frame to the client verbatim.
func scanSSELines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i+1], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	// Request more data.
	return 0, nil, nil
}

func (e *GMCGraphRoutingError) Error() string {
	return fmt.Sprintf("%s. %s", e.ErrorMessage, e.Cause)
}

func timeTrack(ctx context.Context, start time.Time, nodeOrStep string, name string) {
	elapsed := time.Since(start)
	otlpr.WithContext(log, ctx).Info("elapsed time", nodeOrStep, name, "time", elapsed)
}

func isSuccessFul(statusCode int) bool {
	if statusCode >= 200 && statusCode <= 299 {
		return true
	}
	return false
}

func pickupRouteByCondition(initInput []byte, condition string) bool {
	//sample config supported by gjson
	//"instances" : [
	//	{"model_id", "1"},
	//  ]
	// sample condition support by gjson query: "instances.#(modelId==\"1\")""
	if !gjson.ValidBytes(initInput) {
		fmt.Println("the initInput json format is invalid")
		return false
	}

	if gjson.GetBytes(initInput, condition).Exists() {
		return true
	}
	// ' and # will define a gjson query
	if strings.ContainsAny(condition, ".") || strings.ContainsAny(condition, "#") {
		return false
	}
	// key == value without nested json
	// sample config support by direct query {"model_id", "1"}
	// smaple condition support by json query: "modelId==\"1\""
	index := strings.Index(condition, "==")
	if index == -1 {
		fmt.Println("No '==' found in the route with condition [", condition, "]")
		return false
	} else {
		key := strings.TrimSpace(condition[:index])
		value := strings.TrimSpace(condition[index+2:])
		v := gjson.GetBytes(initInput, key).String()
		if v == value {
			return true
		}
	}
	return false
}

// extractUserMessage attempts to extract a user-friendly error message from the raw error cause string.
// It searches for a 'message' field in common error formats returned by LLM/embedding backends
//
// Extraction priority:
//  1. 'message' field value from Python dict or JSON formats, e.g.:
//     - Input:  `{'message': "Token limit exceeded", 'code': 400}` -> Output: `Token limit exceeded`
//     - Input:  `{"message":"Token limit exceeded","code":400}` -> Output: `Token limit exceeded`
//  2. Text after "Error details: " prefix, e.g.:
//     - Input:  `Error occured for step... Error details: Internal server error` -> Output: `Internal server error`
//  3. Full cause string as-is (fallback)
func extractUserMessage(cause string) string {
	// Try to find 'message' field in various quote/spacing formats used by Python dicts and JSON.
	// The last character of each prefix serves as the closing delimiter for the value.
	for _, prefix := range []string{`'message': "`, `'message': '`, `"message":"`, `"message": "`} {
		idx := strings.Index(cause, prefix)
		if idx == -1 {
			continue
		}
		start := idx + len(prefix)
		delimiter := prefix[len(prefix)-1]
		end := strings.Index(cause[start:], string(delimiter))
		if end == -1 {
			continue
		}
		return cause[start : start+end]
	}

	// Fallback: extract everything after "Error details: " prefix
	const errorDetailsPrefix = "Error details: "
	if idx := strings.Index(cause, errorDetailsPrefix); idx != -1 {
		return cause[idx+len(errorDetailsPrefix):]
	}

	return cause
}

func prepareErrorResponse(err error, errorMessage string) []byte {
	cause := fmt.Sprintf("%v", err)
	igRoutingErr := &GMCGraphRoutingError{
		errorMessage,
		cause,
		extractUserMessage(cause),
	}
	errorResponseBytes, err := json.Marshal(igRoutingErr)
	if err != nil {
		log.Error(err, "marshalling error")
	}
	return errorResponseBytes
}

func isMultipartBody(headers http.Header) bool {
	mediaType, _, err := mime.ParseMediaType(headers.Get("Content-Type"))
	return err == nil && mediaType == MultipartContentType
}

func entryRequestData(body []byte, headers http.Header) (map[string]interface{}, error) {
	if isMultipartBody(headers) {
		return multipartInitParams(body, headers.Get("Content-Type"))
	}
	initReqData := make(map[string]interface{})
	if err := json.Unmarshal(body, &initReqData); err != nil {
		return nil, err
	}
	return initReqData, nil
}

func multipartInitParams(body []byte, contentType string) (map[string]interface{}, error) {
	_, mediaParams, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, err
	}
	boundary := mediaParams["boundary"]
	if boundary == "" {
		return nil, errors.New("multipart body carries no boundary")
	}

	initReqData := make(map[string]interface{})
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return initReqData, nil
		}
		if err != nil {
			return nil, err
		}
		if part.FileName() != "" || part.FormName() != Parameters {
			_ = part.Close()
			continue
		}
		value, err := io.ReadAll(io.LimitReader(part, MaxMultipartFieldBytes))
		_ = part.Close()
		if err != nil {
			return nil, err
		}
		var parameters map[string]interface{}
		if err := json.Unmarshal(value, &parameters); err != nil {
			return nil, fmt.Errorf("multipart %q field is not a JSON object: %w", Parameters, err)
		}
		initReqData[Parameters] = parameters
	}
}

// multipartServiceURL swaps a step's JSON endpoint for the multipart endpoint it
// declares, leaving the URL untouched when the step declares none.
func multipartServiceURL(step *mcv1alpha3.Step, serviceURL string) string {
	multipartEndpoint := step.InternalService.Config[MultipartEndpointKey]
	if multipartEndpoint == "" {
		return serviceURL
	}
	return strings.TrimSuffix(serviceURL, step.InternalService.Config[EndpointKey]) + multipartEndpoint
}

func callService(
	ctx context.Context,
	step *mcv1alpha3.Step,
	serviceUrl string,
	input []byte,
	headers http.Header,
) (io.ReadCloser, int, error) {
	semaphore <- struct{}{}
	defer func() { <-semaphore }()

	defer timeTrack(ctx, time.Now(), "step", serviceUrl)
	otlpr.WithContext(log, ctx).Info("Entering callService", "url", serviceUrl)

	// log the http header from the original request
	if debugRequestLogs {
		otlpr.WithContext(log, ctx).Info("Print the http request headers", "HTTP_Header", headers)
	}
	// Carry any step-specific no_proxy on the context so the shared transport's
	// proxy resolver honors it for this request only, rather than mutating
	// process-wide environment (which raced across concurrent requests).
	if step.InternalService.Config != nil {
		if noProxy := step.InternalService.Config["no_proxy"]; noProxy != "" {
			ctx = context.WithValue(ctx, noProxyCtxKey{}, noProxy)
		}
	}

	// Determine timeout and max retries based on namespace and step name
	timeout := CallClientTimeoutSeconds * time.Second
	maxRetries := 0

	// Special handling for docsum namespace TextExtractor and TextSplitter steps
	// FIXME: Workaroud for 2nd request after deployment freezing on services with ProcessPoolExecutor
	if OtelNamespace == "docsum" && (step.StepName == "TextExtractor" || step.StepName == "TextSplitter") {
		if step.StepName == "TextExtractor" {
			timeout = 120 * time.Second
		} else if step.StepName == "TextSplitter" {
			timeout = 60 * time.Second
		}
		maxRetries = 2
		otlpr.WithContext(log, ctx).Info("Using custom timeout and retries for step", "stepName", step.StepName, "timeout", timeout, "maxRetries", maxRetries)
	}

	var lastErr error
	var resp *http.Response

	// Retry loop
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			otlpr.WithContext(log, ctx).Info("Retrying request", "attempt", attempt, "maxRetries", maxRetries, "stepName", step.StepName)
		}

		//req, err := http.NewRequest("POST", serviceUrl, bytes.NewBuffer(input))
		req, err := http.NewRequestWithContext(ctx, "POST", serviceUrl, bytes.NewBuffer(input))
		if err != nil {
			otlpr.WithContext(log, ctx).Error(err, "An error occurred while preparing request object with serviceUrl.", "serviceUrl", serviceUrl)
			return nil, 500, err
		}

		contentType := JSONContentType
		if isMultipartBody(headers) {
			contentType = headers.Get("Content-Type")
		}
		req.Header.Set("Content-Type", contentType)
		if val := headers.Get("Authorization"); val != "" {
			req.Header.Add("Authorization", val)
		}
		// normal client
		// callClient := http.Client{
		// 	Transport: transport,
		// 	Timeout:   600 * time.Second,
		// }

		// otel client
		// we want to use existing tracer instad creating a new one, but how !!!
		callClient := http.Client{
			Transport: otelhttp.NewTransport(
				transport,
				otelhttp.WithServerName(serviceUrl),
				otelhttp.WithSpanNameFormatter(
					func(operation string, r *http.Request) string {
						return "HTTP " + r.Method + " " + r.URL.String()
					}),
				otelhttp.WithFilter(func(r *http.Request) bool {
					for _, excludedUrl := range OtelExcludedUrls {
						if r.RequestURI == excludedUrl {
							return false
						}
					}
					return true
				}),
				otelhttp.WithMessageEvents(otelhttp.ReadEvents, otelhttp.WriteEvents),
				// ////  GEnerate EXTRA spans for dns/sent/reciver
				// otelhttp.WithClientTrace(
				// 	func(ctx context.Context) *httptrace.ClientTrace {
				// 		return otelhttptrace.NewClientTrace(ctx)
				// 	},
				// ),
			),
			Timeout: timeout,
		}
		resp, err = callClient.Do(req)
		if err != nil {
			lastErr = err
			otlpr.WithContext(log, ctx).Error(err, "An error has occurred while calling service", "service", serviceUrl, "attempt", attempt)

			// If we have retries left, continue to next attempt
			if attempt < maxRetries {
				// Add a small backoff delay before retry
				time.Sleep(time.Duration(attempt+1) * time.Second)
				continue
			}
			// No more retries, return the error
			return nil, 500, err
		}

		// Success - return the response, bounded so a backend that streams
		// without end cannot exhaust memory in the callers that read it whole.
		return newLimitedReadCloser(resp.Body, MaxDownstreamResponseBytes), resp.StatusCode, nil
	}

	// If we get here, all retries failed
	otlpr.WithContext(log, ctx).Error(lastErr, "All retry attempts failed", "service", serviceUrl, "maxRetries", maxRetries)
	return nil, 500, lastErr
}

// Use step service name to create a K8s service if serviceURL is empty
// TODO: add more features here, such as K8s service selector, labels, etc.
func getServiceURLByStepTarget(step *mcv1alpha3.Step, svcNameSpace string) string {
	if step.ServiceURL == "" {
		serviceURL := fmt.Sprintf("http://%s.%s.svc", step.StepName, svcNameSpace)
		return serviceURL
	}
	return step.ServiceURL
}

// executeStep runs one step and is the shared funnel for every pipeline type,
// so per-step instrumentation lives here rather than in a single handler: every
// step, whether reached through Sequence, Switch or Ensemble, emits a span and a
// router.pipeline.step latency sample carrying the router type, node name and
// recursion depth. A step naming a sub-node recurses through routeStep; a leaf
// step has its parameter group injected and is sent to its service.
func executeStep(
	ctx context.Context,
	step *mcv1alpha3.Step,
	nodeName string,
	graph mcv1alpha3.GMConnector,
	initInput []byte,
	input []byte,
	headers http.Header,
) (io.ReadCloser, int, error) {
	stepStartTime := time.Now()
	stepType := ServiceURL
	if step.NodeName != "" {
		stepType = ServiceNode
	}
	routerType := string(graph.Spec.Nodes[nodeName].RouterType)

	stepTracer := otel.GetTracerProvider().Tracer(OtelNamespace + "/steptracer")
	ctx, stepSpan := stepTracer.Start(ctx, "step "+step.StepName)
	stepSpan.SetAttributes(
		attribute.String("stepType", stepType),
		attribute.String("stepName", step.StepName),
		attribute.String("routerType", routerType),
		attribute.String("nodeName", nodeName),
		attribute.Int("depth", routeDepth(ctx)),
	)
	defer stepSpan.End()

	var body io.ReadCloser
	var statusCode int
	var err error
	if step.NodeName != "" {
		// when nodeName is specified make a recursive call for routing to next step
		body, statusCode, err = routeStep(ctx, step.NodeName, graph, initInput, input, headers)
	} else {
		// Inject this leaf step's parameter group from the config projection,
		// selecting only the group named by the step's paramsKey so that two
		// steps of the same kind can receive different parameters. Every pipeline
		// type (Sequence, Switch, Ensemble) reaches a real service through this
		// path, so injection happens here once per leaf step. A step routing to a
		// sub-node is not injected: its leaf steps are injected when the recursion
		// reaches them, so injecting a routing step would bleed one paramsKey
		// across the nested steps.
		if configW != nil {
			input = configW.injectParamsForStep(ctx, step, input, headers)
			if debugRequestLogs {
				otlpr.WithContext(log, ctx).Info("Print Request Bytes after parameter injection", "Request Bytes", string(input[:]))
			}
		}
		serviceURL := getServiceURLByStepTarget(step, graph.Namespace)

		if isMultipartBody(headers) {
			serviceURL = multipartServiceURL(step, serviceURL)
		}
		body, statusCode, err = callService(ctx, step, serviceURL, input, headers)
	}

	stepLatencyMilliseconds := float64(time.Since(stepStartTime)) / float64(time.Millisecond)
	// initMeter logs and continues if an instrument fails to register, so guard
	// the record like the other measures rather than risk a nil dereference on
	// every step when metrics init failed.
	if stepLatencyMeasure != nil {
		stepLatencyMeasure.Record(ctx, stepLatencyMilliseconds, api.WithAttributes(
			attribute.Int("statusCode", statusCode),
			attribute.String("stepName", step.StepName),
			attribute.String("routerType", routerType),
			attribute.String("nodeName", nodeName),
		))
	}
	stepSpan.SetAttributes(
		attribute.Int("statusCode", statusCode),
		attribute.Float64("llm.step.latency.ms", stepLatencyMilliseconds),
	)
	if err != nil {
		stepSpan.RecordError(err)
		stepSpan.SetStatus(codes.Error, err.Error())
	}
	return body, statusCode, err
}

func mergeRequests(ctx context.Context, respReq []byte, initReqData map[string]interface{}) []byte {
	var respReqData map[string]interface{}

	if _, exists := initReqData[Parameters]; exists {
		params, ok := initReqData[Parameters].(map[string]interface{})
		if !ok {
			otlpr.WithContext(log, ctx).Error(nil, "Parameters field is not a valid JSON object, skipping merge")
			return respReq
		}
		if err := json.Unmarshal(respReq, &respReqData); err != nil {
			otlpr.WithContext(log, ctx).Error(err, "Error unmarshaling respReqData:")
			return nil
		}
		// Merge init request into respReq
		for key, value := range params {
			// overwrite the respReq by initial request
			respReqData[key] = value
		}
		mergedBytes, err := json.Marshal(respReqData)
		if err != nil {
			otlpr.WithContext(log, ctx).Error(err, "Error marshaling merged data:")
			return nil
		}
		return mergedBytes
	}
	return respReq
}

// switchDecisionMeasure records which branch a Switch node selected, or that no
// branch matched, so a routing decision is observable.
func recordSwitchDecision(ctx context.Context, nodeName, stepName string, matched bool) {
	if switchDecisionMeasure == nil {
		return
	}
	branch := stepName
	if !matched {
		branch = "no-match"
	}
	switchDecisionMeasure.Add(ctx, 1, api.WithAttributes(
		attribute.String("nodeName", nodeName),
		attribute.String("branch", branch),
		attribute.Bool("matched", matched),
	))
}

func handleSwitchPipeline(
	ctx context.Context,
	nodeName string,
	graph mcv1alpha3.GMConnector,
	initInput []byte,
	input []byte,
	headers http.Header,
) (io.ReadCloser, int, error) {
	currentNode := graph.Spec.Nodes[nodeName]

	for index, route := range currentNode.Steps {
		if route.InternalService.IsDownstreamService {
			otlpr.WithContext(log, ctx).Info("InternalService DownstreamService is true, skip the execution of step", "type", currentNode.RouterType, "stepName", route.StepName)
			continue
		}

		// make sure that the process goes to the correct step
		if route.Condition != "" {
			if !pickupRouteByCondition(initInput, route.Condition) {
				continue
			}
		}
		// A Switch selects exactly one branch: the first whose condition matches.
		// Execute it and return its result rather than continuing the loop, which
		// would run every matching branch and return only the last.
		recordSwitchDecision(ctx, nodeName, route.StepName, true)
		otlpr.WithContext(log, ctx).Info("Current Step Information", "Node Name", nodeName, "Step Index", index)

		if debugRequestLogs {
			otlpr.WithContext(log, ctx).Info("Print Original Request Bytes", "Request Bytes", string(input[:]))
		}
		responseBody, statusCode, err := executeStep(ctx, &currentNode.Steps[index], nodeName, graph, initInput, input, headers)
		if err != nil {
			return nil, statusCode, err
		}
		// A hard-dependency branch that answered unsuccessfully stops the switch
		// with that status rather than being silently ignored, so a required step
		// failing is not reported as success.
		if route.Dependency == mcv1alpha3.Hard && !isSuccessFul(statusCode) {
			otlpr.WithContext(log, ctx).Info("This step is a hard dependency and it is unsuccessful", "stepName", route.StepName, "statusCode", statusCode)
			return responseBody, statusCode, fmt.Errorf("hard dependency step (stepName=%s) in switch node %s returned statusCode=%d", route.StepName, nodeName, statusCode)
		}
		return responseBody, statusCode, nil
	}
	// No branch matched: report the decision and return a defined error rather
	// than a nil body with a zero status, which downstream code cannot act on.
	recordSwitchDecision(ctx, nodeName, "", false)
	otlpr.WithContext(log, ctx).Info("No switch branch matched", "Node Name", nodeName)
	return nil, http.StatusNotFound, fmt.Errorf("no matching route in switch node %s", nodeName)
}

// ensembleResult holds one parallel step's outcome collected by
// handleEnsemblePipeline. response and rawResponse are set only when the step
// answered; err carries a transport failure. decodeErr is kept apart from err
// because a body that is not a JSON object is still a successful answer: it is
// merged as raw text rather than failing the request.
type ensembleResult struct {
	response    map[string]interface{}
	rawResponse []byte
	statusCode  int
	err         error
	decodeErr   error
}

func handleEnsemblePipeline(
	ctx context.Context,
	nodeName string,
	graph mcv1alpha3.GMConnector,
	initInput []byte,
	input []byte,
	headers http.Header,
) (io.ReadCloser, int, error) {
	currentNode := graph.Spec.Nodes[nodeName]

	if isMultipartBody(headers) {
		err := errors.New("an Ensemble node cannot take a multipart entry request")
		otlpr.WithContext(log, ctx).Error(err, "Rejecting multipart request")
		return nil, http.StatusBadRequest, err
	}
	initReqData, err := entryRequestFor(ctx, initInput, headers)
	if err != nil {
		otlpr.WithContext(log, ctx).Error(err, "Error unmarshaling initReqData:")
		return nil, 500, err
	}

	// Cancel siblings as soon as a hard-dependency step fails, and wait for every
	// goroutine to finish before returning so none is left blocked on a send or a
	// half-read body. Results are written to per-index slots, so no channel is
	// needed and each goroutine touches only its own slot.
	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	g, gctx := errgroup.WithContext(gctx)
	// Every node the request reaches acquires from one budget carried on the
	// context. A per-group limit would not bound the tree: each nested Ensemble
	// opens its own group, so the concurrency would still multiply with depth.
	budget := fanoutBudget(gctx)

	results := make([]ensembleResult, len(currentNode.Steps))
	for i := range currentNode.Steps {
		i := i
		step := &currentNode.Steps[i]
		stepType := ServiceURL
		if step.NodeName != "" {
			stepType = ServiceNode
		}
		otlpr.WithContext(log, gctx).Info("Starting execution of step", "type", stepType, "stepName", step.StepName)
		g.Go(func() (gerr error) {
			// A malformed backend response must not take the whole router down with
			// an unrecovered panic in this goroutine; convert it to an error.
			defer func() {
				if r := recover(); r != nil {
					gerr = fmt.Errorf("panic in ensemble step %s: %v", step.StepName, r)
					results[i].err = gerr
				}
			}()

			// Hold a slot from the request's fan-out budget while this step calls a
			// service, so the whole graph is bounded rather than each node
			// separately. A step that routes to a sub-node takes no slot: it only
			// waits for the steps below it, which take their own, and holding one
			// here would let a parent block on children that can never start.
			if budget != nil && step.NodeName == "" {
				select {
				case budget <- struct{}{}:
					defer func() { <-budget }()
				case <-gctx.Done():
					results[i].err = gctx.Err()
					return nil
				}
			}

			// Propagate the initial request's parameters into each parallel step so
			// fingerprint params reach every branch, not only sequential ones.
			request := mergeRequests(gctx, input, initReqData)
			responseBody, statusCode, err := executeStep(gctx, step, nodeName, graph, initInput, request, headers)
			results[i].statusCode = statusCode
			if err != nil {
				results[i].err = err
				if step.Dependency == mcv1alpha3.Hard {
					return err
				}
				return nil
			}
			defer func() {
				if cerr := responseBody.Close(); cerr != nil {
					otlpr.WithContext(log, gctx).Error(cerr, "Error while trying to close the responseBody in handleEnsemblePipeline")
				}
			}()

			output, rerr := io.ReadAll(responseBody)
			if rerr != nil {
				otlpr.WithContext(log, gctx).Error(rerr, "Error while reading the response body")
				results[i].err = rerr
				if step.Dependency == mcv1alpha3.Hard {
					return rerr
				}
				return nil
			}
			results[i].rawResponse = output
			if err := json.Unmarshal(output, &results[i].response); err != nil {
				// Not a JSON object: keep the raw body and note it, so the merge
				// below can carry it as text instead of failing the request.
				results[i].decodeErr = err
			}
			// A hard-dependency step that answered unsuccessfully stops the whole
			// ensemble with that step's status, cancelling the siblings.
			if !isSuccessFul(statusCode) && step.Dependency == mcv1alpha3.Hard {
				otlpr.WithContext(log, gctx).Info("This step is a hard dependency and it is unsuccessful", "stepName", step.StepName, "statusCode", statusCode)
				return fmt.Errorf("hard dependency step (stepName=%s) in ensemble node %s returned statusCode=%d", step.StepName, nodeName, statusCode)
			}
			return nil
		})
	}
	waitErr := g.Wait()

	// A hard-dependency failure short-circuits: return that step's own response
	// and status so the failure is not merged away behind a 200.
	for i := range currentNode.Steps {
		if currentNode.Steps[i].Dependency != mcv1alpha3.Hard {
			continue
		}
		r := results[i]
		if r.rawResponse != nil && !isSuccessFul(r.statusCode) {
			recordEnsembleFanout(ctx, nodeName, len(currentNode.Steps), 1, r.statusCode)
			return NewReadCloser(r.rawResponse), r.statusCode, nil
		}
		if r.err != nil && !errors.Is(r.err, context.Canceled) {
			recordEnsembleFanout(ctx, nodeName, len(currentNode.Steps), 1, 500)
			return nil, 500, r.err
		}
	}
	if waitErr != nil && !errors.Is(waitErr, context.Canceled) {
		recordEnsembleFanout(ctx, nodeName, len(currentNode.Steps), 1, 500)
		return nil, 500, waitErr
	}

	// Merge the responses that came back, keyed by step name. A step that failed
	// softly is counted as a partial failure but does not fail the request.
	response := map[string]interface{}{}
	failures := 0
	for i := range currentNode.Steps {
		key := currentNode.Steps[i].StepName
		if key == "" {
			key = strconv.Itoa(i) // Use index if no step name
		}
		r := results[i]
		if r.err != nil || !isSuccessFul(r.statusCode) {
			failures++
		}
		switch {
		case r.response != nil:
			response[key] = r.response
		case r.decodeErr != nil && r.rawResponse != nil:
			// The step answered with something other than a JSON object (SSE or
			// plain text); carry it as a string so the body is not dropped.
			response[key] = string(r.rawResponse)
		}
	}

	statusCode := http.StatusOK
	if failures > 0 {
		// Some soft steps did not succeed; report a multi-status so the partial
		// failure is visible rather than hidden behind 200.
		statusCode = http.StatusMultiStatus
	}
	recordEnsembleFanout(ctx, nodeName, len(currentNode.Steps), failures, statusCode)

	combinedResponse, _ := json.Marshal(response) // TODO check if you need err handling for Marshalling
	combinedIOReader := NewReadCloser(combinedResponse)
	return combinedIOReader, statusCode, nil
}

// recordEnsembleFanout reports an ensemble node's fan-out width, how many steps
// did not succeed and the resulting status, so parallel execution and partial
// failures are observable.
func recordEnsembleFanout(ctx context.Context, nodeName string, width, failures, statusCode int) {
	if ensembleFanoutMeasure == nil {
		return
	}
	ensembleFanoutMeasure.Record(ctx, int64(width), api.WithAttributes(
		attribute.String("nodeName", nodeName),
		attribute.Int("failures", failures),
		attribute.Int("statusCode", statusCode),
	))
}

// recordRequest increments the request counter by status code class (1xx, 2xx,
// 3xx, 4xx, 5xx), enabling RED metrics (Rate, Errors, Duration). Errors are 4xx
// or 5xx status codes.
func recordRequest(ctx context.Context, statusCode int) {
	if requestCounter == nil {
		return
	}
	statusClass := "unknown"
	if statusCode >= 500 {
		statusClass = "5xx"
	} else if statusCode >= 400 {
		statusClass = "4xx"
	} else if statusCode >= 300 {
		statusClass = "3xx"
	} else if statusCode >= 200 {
		statusClass = "2xx"
	} else if statusCode >= 100 {
		statusClass = "1xx"
	}
	requestCounter.Add(ctx, 1, api.WithAttributes(
		attribute.String("statusClass", statusClass),
		attribute.Int("statusCode", statusCode),
	))
}

func handleSequencePipeline(
	ctx context.Context,
	nodeName string,
	graph mcv1alpha3.GMConnector,
	initInput []byte,
	input []byte,
	headers http.Header,
) (io.ReadCloser, int, error) {
	currentNode := graph.Spec.Nodes[nodeName]
	var statusCode int
	var responseBody io.ReadCloser
	var responseBytes []byte
	var err error

	multipartEntry := isMultipartBody(headers)
	initReqData, err := entryRequestFor(ctx, initInput, headers)
	if err != nil {
		otlpr.WithContext(log, ctx).Error(err, "Error reading entry request:")
		if multipartEntry {
			return nil, http.StatusBadRequest, err
		}
		return nil, 500, err
	}
	// Steps after the entry step are called with the previous step's JSON response,
	// so the entry request's multipart content type must not travel with them.
	jsonHeaders := headers
	if multipartEntry {
		jsonHeaders = headers.Clone()
		jsonHeaders.Set("Content-Type", JSONContentType)
	}
	// Tracks whether an earlier step in the sequence produced a response, so a
	// `$response` step merges against it only when one exists. This is decoupled
	// from the loop index because skipped steps (downstream services, the
	// obsolete fingerprint step) leave no response for the next step to read.
	priorStepExecuted := false
	for i := range currentNode.Steps {
		step := &currentNode.Steps[i]
		stepType := ServiceURL
		if step.NodeName != "" {
			stepType = ServiceNode
		}
		if step.InternalService.IsDownstreamService {
			otlpr.WithContext(log, ctx).Info("InternalService DownstreamService is true, skip the execution of step", "type", stepType, "stepName", step.StepName)
			continue
		}
		// Parameters now come from the config projection, injected per step, so
		// the standalone fingerprint step is no longer called. Skipping it keeps
		// pipelines that still carry the node working.
		if step.StepName == fingerprintStepName {
			otlpr.WithContext(log, ctx).Info("Skipping fingerprint step; parameters are injected from the config projection", "stepName", step.StepName)
			continue
		}
		otlpr.WithContext(log, ctx).Info("Starting execution of step", "type", stepType, "stepName", step.StepName)
		request := input
		if debugRequestLogs {
			otlpr.WithContext(log, ctx).Info("Print Original Request Bytes", "Request Bytes", string(request[:]))
		}

		if responseBody != nil {
			responseBytes, err = io.ReadAll(responseBody)
			if err != nil {
				otlpr.WithContext(log, ctx).Error(err, "Error while reading the response body")
				return nil, 500, err
			}
			if debugRequestLogs {
				otlpr.WithContext(log, ctx).Info("Print Previous Response Bytes", "Previous Response Bytes", string(responseBytes[:]), "Previous Status Code", statusCode)
			}
			err := responseBody.Close()
			if err != nil {
				otlpr.WithContext(log, ctx).Error(err, "Error while trying to close the responseBody in handleSequencePipeline")
			}
		}

		if step.Data == "$response" && priorStepExecuted {
			request = mergeRequests(ctx, responseBytes, initReqData)
		}
		if debugRequestLogs {
			otlpr.WithContext(log, ctx).Info("Print New Request Bytes", "Request Bytes", string(request[:]))
		}
		if step.Condition != "" {
			if !gjson.ValidBytes(responseBytes) {
				return nil, 500, fmt.Errorf("invalid response")
			}
			// if the condition does not match for the step in the sequence we stop
			// and return the response the earlier step produced as a success rather
			// than a 500, since no error occurred.
			if !gjson.GetBytes(responseBytes, step.Condition).Exists() {
				return responseBody, statusCode, nil
			}
		}
		// Parameter injection and per-step instrumentation happen in executeStep,
		// the common leaf send for every pipeline type, so a step routing to a
		// sub-node is not injected here.
		stepHeaders := headers
		if priorStepExecuted {
			stepHeaders = jsonHeaders
		}
		if responseBody, statusCode, err = executeStep(ctx, step, nodeName, graph, initInput, request, stepHeaders); err != nil {
			return nil, statusCode, err
		}
		priorStepExecuted = true

		/*
		   Only if a step is a hard dependency, we will check for its success.
		*/
		if step.Dependency == mcv1alpha3.Hard {
			if !isSuccessFul(statusCode) {
				if statusCode == 466 {
					// Guardrails scanners error will be parsed later
					return responseBody, statusCode, err
				}
				if responseBody != nil {
					responseBytes, err = io.ReadAll(responseBody)
					if err != nil {
						otlpr.WithContext(log, ctx).Error(err, "Error while reading the error message")
						return nil, 500, err
					}
					err = responseBody.Close()
					if err != nil {
						otlpr.WithContext(log, ctx).Error(err, "Error while trying to close the error message in handleSequencePipeline")
						return nil, 500, err
					}
				}

				var errorDetail string
				if statusCode == 422 {
					var errorResponse PydanticErrorResponse
					err := json.Unmarshal([]byte(responseBytes), &errorResponse)
					if err != nil {
						otlpr.WithContext(log, ctx).Error(err, "Error while unmarshalling the error message")
						return nil, 500, err
					}
					// A 422 with an empty or absent detail list must not be indexed
					// blindly, which would panic; fall back to the raw body instead.
					if len(errorResponse.Detail) > 0 {
						detailJSON, err := json.Marshal(errorResponse.Detail[0])
						if err != nil {
							otlpr.WithContext(log, ctx).Error(err, "Error while marshalling the error message")
							return nil, 500, err
						}
						errorDetail = "Pydantic error: " + string(detailJSON)
					} else {
						errorDetail = "Pydantic error: " + string(responseBytes)
					}
				} else {
					var errorResponse ErrorResponse
					err = json.Unmarshal(responseBytes, &errorResponse)
					if err != nil {
						otlpr.WithContext(log, ctx).Error(err, "Error while unmarshalling the error message")
						return nil, 500, err
					}
					errorDetail = errorResponse.Detail
				}

				// Stop the execution of sequence right away if step is a hard dependency and is unsuccessful
				otlpr.WithContext(log, ctx).Info("This step is a hard dependency and it is unsuccessful. Stop pipeline execution.", "stepName", step.StepName, "statusCode", statusCode, "responseBytes", errorDetail)
				err := fmt.Errorf("Error occured for step (stepName=%s) with statusCode=%d. Stopping pipeline execution. Error details: %s", step.StepName, statusCode, errorDetail)
				return responseBody, statusCode, err
			}
		}
	}
	return responseBody, statusCode, nil
}

func routeStep(
	ctx context.Context,
	nodeName string,
	graph mcv1alpha3.GMConnector,
	initInput, input []byte,
	headers http.Header,
) (io.ReadCloser, int, error) {
	defer timeTrack(ctx, time.Now(), "node", nodeName)

	// Guard against a graph cycle: each hop into a sub-node increments the depth
	// carried on the context, and exceeding the cap returns an error rather than
	// recursing until the stack overflows at request time.
	depth := routeDepth(ctx) + 1
	if depth > MaxRecursionDepth {
		otlpr.WithContext(log, ctx).Error(nil, "max routing depth exceeded, possible graph cycle", "Node Name", nodeName, "depth", depth)
		return nil, http.StatusInternalServerError, fmt.Errorf("max routing depth %d exceeded at node %s, possible graph cycle", MaxRecursionDepth, nodeName)
	}
	ctx = context.WithValue(ctx, depthCtxKey{}, depth)
	// Attach the fan-out budget on the way into the graph. It is attached once
	// per request, so every node below shares one pool.
	ctx = withFanoutBudget(ctx)

	currentNode := graph.Spec.Nodes[nodeName]
	otlpr.WithContext(log, ctx).Info("Current Node", "Node Name", nodeName)

	if currentNode.RouterType == mcv1alpha3.Switch {
		return handleSwitchPipeline(ctx, nodeName, graph, initInput, input, headers)
	}

	if currentNode.RouterType == mcv1alpha3.Ensemble {
		return handleEnsemblePipeline(ctx, nodeName, graph, initInput, input, headers)
	}

	if currentNode.RouterType == mcv1alpha3.Sequence {
		return handleSequencePipeline(ctx, nodeName, graph, initInput, input, headers)
	}
	otlpr.WithContext(log, ctx).Error(nil, "invalid route type", "type", currentNode.RouterType)
	return nil, 500, fmt.Errorf("invalid route type: %v", currentNode.RouterType)
}

func mcGraphHandler(w http.ResponseWriter, req *http.Request) {
	// Shed load past the in-flight cap so a burst cannot pile up unbounded handler
	// goroutines; the downstream semaphore only gates outbound calls, not handlers.
	if n := inFlight.Add(1); n > MaxInFlightRequests {
		inFlight.Add(-1)
		recordRequest(req.Context(), http.StatusServiceUnavailable)
		http.Error(w, "too many concurrent requests", http.StatusServiceUnavailable)
		return
	}
	defer inFlight.Add(-1)

	// Bound the inbound body so an oversized or unbounded upload cannot exhaust
	// memory in the io.ReadAll below.
	req.Body = http.MaxBytesReader(w, req.Body, MaxRequestBodyBytes)

	ctx, cancel := context.WithTimeout(req.Context(), GraphHandlerTimeoutSeconds*time.Second)
	defer cancel()

	done := make(chan struct{})
	// finalStatus holds the response status code for RED metric recording. It is
	// written by the worker goroutine and the timeout path (after the worker has
	// finished); correctness relies on the happens-before edge from receiving on
	// done before reading finalStatus.
	var finalStatus int
	go func() {
		defer close(done)

		mainTracer := otel.GetTracerProvider().Tracer(OtelNamespace + "graphtracer")
		_, spanReadInitialRequest := mainTracer.Start(ctx, "read initial request")

		// Return x-trace-id to the user, for debbugging purposes
		w.Header().Set("x-trace-id", spanReadInitialRequest.SpanContext().TraceID().String())

		// ### Example event
		// uk := attribute.Key("foo")
		// bag := baggage.FromContext(ctx)
		// spanReadInitialRequest.AddEvent("handling this...", trace.WithAttributes(uk.String(bag.Member("bar").Value())))

		// ---------------------- ReadRequestBody
		routerRequestStartTime := time.Now()
		inputBytes, err := io.ReadAll(req.Body)
		if err != nil {
			otlpr.WithContext(log, ctx).Error(err, "failed to read request body")
			spanReadInitialRequest.RecordError(err)
			spanReadInitialRequest.SetStatus(codes.Error, err.Error())
			spanReadInitialRequest.End()
			finalStatus = http.StatusBadRequest
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		if debugRequestLogs {
			otlpr.WithContext(log, ctx).Info("Data from input request", "inputBytes", string(inputBytes[:]))
		}
		if debugRequestTraces {
			spanReadInitialRequest.SetAttributes(attribute.String("initial request", string(inputBytes[:])))
		}
		spanReadInitialRequest.SetAttributes(attribute.Int("initial request body size", len(inputBytes)))
		spanReadInitialRequest.End()

		// ---------------------- RouterAllSteps
		allStepsCtx, spanRouterAllSteps := mainTracer.Start(ctx, "router all steps") // this context will be used for callClient instrumenation (POSTs)
		allStepsCtx = withEntryRequest(allStepsCtx, inputBytes, req.Header)
		responseBody, statusCode, err := routeStep(allStepsCtx, defaultNodeName, *mcGraph, inputBytes, inputBytes, req.Header)

		pipeLatencyMilliseconds := float64(time.Since(routerRequestStartTime)) / float64(time.Millisecond)
		pipelineLatencyMeasure.Record(ctx, pipeLatencyMilliseconds)

		spanRouterAllSteps.SetAttributes(attribute.Int("last_step.statusCode", statusCode))
		spanRouterAllSteps.SetAttributes(attribute.Float64("router.pipeline.latency.ms", pipeLatencyMilliseconds))

		if statusCode == 466 { // Guardrails code!
			// Info: statusCode != 200 is unrealted to err being nil or not and for Guardrails err is nil
			otlpr.WithContext(log, ctx).Info("Guardrails activated!")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusCode)
			respBytes, err := io.ReadAll(responseBody)
			if debugRequestLogs {
				otlpr.WithContext(log, ctx).Info("Print the http response body", "body", string(respBytes[:]))
			}
			if debugRequestTraces {
				spanRouterAllSteps.SetAttributes(attribute.String("response body", string(respBytes[:])))
			}
			if err != nil {
				otlpr.WithContext(log, ctx).Error(err, "failed to read all request body from guardrails")
				spanRouterAllSteps.RecordError(err)
				spanRouterAllSteps.SetStatus(codes.Error, err.Error())
				spanRouterAllSteps.End()
				finalStatus = http.StatusBadRequest
				http.Error(w, "failed to read request body", http.StatusBadRequest)
				return
			}

			finalStatus = statusCode
			w.Write(prepareErrorResponse(err, string(respBytes)))
			spanRouterAllSteps.End()
			return
		}

		if err != nil {
			otlpr.WithContext(log, ctx).Error(err, "failed to process request")
			spanRouterAllSteps.RecordError(err)
			spanRouterAllSteps.SetStatus(codes.Error, err.Error())
			spanRouterAllSteps.End()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusCode)
			finalStatus = statusCode
			if _, err := w.Write(prepareErrorResponse(err, "Failed to process request")); err != nil {
				otlpr.WithContext(log, ctx).Error(err, "failed to write mcGraphHandler response")
			}
			return
		}

		// Close span if there was not err and not guardarils were activated
		spanRouterAllSteps.End() // end "router all steps" span

		// A pipeline can finish with no response body when every step was
		// skipped (for example a node holding only the obsolete fingerprint
		// step). Return an empty success rather than dereferencing a nil body.
		if responseBody == nil {
			otlpr.WithContext(log, ctx).Info("No step produced a response; returning empty body")
			if statusCode == 0 {
				statusCode = http.StatusOK
			}
			finalStatus = statusCode
			w.WriteHeader(statusCode)
			return
		}

		defer func() {
			err := responseBody.Close()
			if err != nil {
				otlpr.WithContext(log, ctx).Error(err, "Error while trying to close the responseBody in mcGraphHandler")
			}
		}()

		w.Header().Set("Content-Type", "text/event-stream")
		firstTokenCollected := false
		llmStartTime := time.Now()
		llmFirstTokenLatencyMilliseconds := 0.0
		timeToFirstTokenLatencyMilliseconds := 0.0
		llmNextTokenLatencyTotal := 0.0
		llmNextTokenLatencyCount := 0.0
		var lastChunk string
		var collectedParts []string
		// streaming becomes true on the first "data:" line, after which every line
		// (data frames and the blank lines that separate SSE events alike) is
		// forwarded verbatim; the non-streaming JSON remap below runs only when no
		// "data:" line was ever seen.
		streaming := false
		// Scan the upstream response one SSE line at a time (each token keeps its
		// trailing newline) so a "data:"/"json:" prefix is always classified on a
		// whole line and cannot be misread when it straddles a fixed read boundary.
		scanner := bufio.NewScanner(responseBody)
		scanner.Buffer(make([]byte, 0, BufferSize), MaxSSELineBytes)
		scanner.Split(scanSSELines)
		// ---------------------- Tokens
		ctx, spanTokens := mainTracer.Start(ctx, "tokens")
		// tokenStartTime marks the moment we began waiting for the next SSE line.
		// It is set BEFORE scanner.Scan() (which blocks until the upstream sends
		// the token) so elapsedTimeMilisecond captures the wait for the token, not
		// just the in-memory line handling. Seeded with llmStartTime so the first
		// token's latency is measured from when the LLM request went out.
		tokenStartTime := llmStartTime
		for scanner.Scan() {
			// time spent waiting for this SSE line (token) to arrive upstream,
			// measured from when we started waiting for it (previous iteration end
			// or, for the first token, llmStartTime). Reset immediately so the next
			// iteration measures the gap to the following token — independent of any
			// continue/break below.
			elapsedTimeMilisecond := float64(time.Since(tokenStartTime)) / float64(time.Millisecond)
			tokenStartTime = time.Now()
			currentChunk := scanner.Text()
			lastChunk = currentChunk

			// Check if this is a json: response or already identified as one
			isJSONStart := strings.HasPrefix(currentChunk, "json:")
			isDataDoneWithJSON := strings.HasPrefix(currentChunk, "data:") &&
				strings.Contains(currentChunk, "[DONE]") &&
				strings.Contains(currentChunk, "json:")

			if isJSONStart || isDataDoneWithJSON {
				// Keep reading through the scanner rather than the body: the
				// scanner has already buffered ahead, so the body is drained and
				// reading it directly would drop the rest of the document.
				var remaining strings.Builder
				for scanner.Scan() {
					remaining.WriteString(scanner.Text())
				}
				if readErr := scanner.Err(); readErr != nil {
					otlpr.WithContext(log, ctx).Error(readErr, "failed to read remaining response body after json: prefix")
					spanTokens.RecordError(readErr)
					spanTokens.SetStatus(codes.Error, readErr.Error())
					spanTokens.End()
					finalStatus = http.StatusBadGateway
					http.Error(w, "failed to read upstream response", http.StatusBadGateway)
					return
				}

				// Combine first chunk with remaining data
				fullResponse := currentChunk + remaining.String()

				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write([]byte(fullResponse)); err != nil {
					otlpr.WithContext(log, ctx).Error(err, "failed to write json response")
					spanTokens.RecordError(err)
					spanTokens.SetStatus(codes.Error, err.Error())
					spanTokens.End()
					finalStatus = http.StatusInternalServerError
					return
				}
				// Flush the data to the client immediately
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				} else {
					err := errors.New("unable to flush data")
					otlpr.WithContext(log, ctx).Error(err, "ResponseWriter does not support flushing")
					spanTokens.RecordError(err)
					spanTokens.SetStatus(codes.Error, err.Error())
					spanTokens.End()
					finalStatus = http.StatusInternalServerError
					return
				}
				// The response is complete and already written. Return rather than
				// break: breaking would fall through to the non-streaming remap
				// below, which appends a second JSON document whenever an earlier
				// line was collected.
				finalStatus = http.StatusOK
				spanTokens.End()
				return
			}

			if !firstTokenCollected {
				firstTokenCollected = true
				llmFirstTokenLatencyMeasure.Record(ctx, elapsedTimeMilisecond)
				llmFirstTokenLatencyMilliseconds = elapsedTimeMilisecond

				timeToFirstTokenLatencyMilliseconds = pipeLatencyMilliseconds + llmFirstTokenLatencyMilliseconds
				e2eTimeToFirstTokenLatencyMeasure.Record(ctx, timeToFirstTokenLatencyMilliseconds)
			} else {
				llmNextTokenLatencyMeasure.Record(ctx, elapsedTimeMilisecond)
				llmNextTokenLatencyTotal += elapsedTimeMilisecond
				llmNextTokenLatencyCount += 1.0
			}

			// Before any "data:" line the response might still be a single
			// non-streaming JSON document from the LLM or Output Guardrails; collect
			// such lines and remap them once the stream ends. Once a "data:" line has
			// been seen the response is an SSE stream, so every subsequent line
			// (including the blank lines that separate events) is forwarded verbatim.
			if strings.HasPrefix(currentChunk, "data:") {
				streaming = true
			} else if !streaming {
				collectedParts = append(collectedParts, currentChunk)
				continue
			}

			// Write the chunk to the ResponseWriter
			if _, err := w.Write(scanner.Bytes()); err != nil {
				otlpr.WithContext(log, ctx).Error(err, "failed to write to ResponseWriter")
				spanTokens.RecordError(err)
				spanTokens.SetStatus(codes.Error, err.Error())
				spanTokens.End()
				finalStatus = http.StatusInternalServerError
				return
			}

			// Flush the data to the client immediately
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			} else {
				err := errors.New("unable to flush data")
				otlpr.WithContext(log, ctx).Error(err, "ResponseWriter does not support flushing")
				spanTokens.RecordError(err)
				spanTokens.SetStatus(codes.Error, err.Error())
				spanTokens.End()
				finalStatus = http.StatusInternalServerError
				return
			}
		}
		if err := scanner.Err(); err != nil {
			otlpr.WithContext(log, ctx).Error(err, "failed to read from response body")
			spanTokens.RecordError(err)
			spanTokens.SetStatus(codes.Error, err.Error())
			spanTokens.End()
			finalStatus = http.StatusInternalServerError
			http.Error(w, "failed to read from response body", http.StatusInternalServerError)
			return
		}

		// A non-streaming response was collected as a whole JSON document; remap the
		// GeneratedDoc shape and write it once the stream has ended. This runs only
		// when no "data:" line was ever seen, so it never appends JSON after data
		// has already been streamed.
		if !streaming && len(collectedParts) > 0 {
			fullJSON := strings.Join(collectedParts, "")

			var jsonResponse map[string]interface{}
			if err := json.Unmarshal([]byte(fullJSON), &jsonResponse); err != nil {
				otlpr.WithContext(log, ctx).Error(err, "failed to unmarshal JSON response")
				spanTokens.RecordError(err)
				spanTokens.SetStatus(codes.Error, err.Error())
				spanTokens.End()
				finalStatus = http.StatusInternalServerError
				return
			}
			// response is expected to be a GeneratedDoc class, need to clean it up
			// remap data attribute to json from the GeneratedDoc class
			cleanedResponse := map[string]interface{}{
				"id":   jsonResponse["id"],
				"text": jsonResponse["text"],
				"json": jsonResponse["data"],
			}

			cleanedResponseBytes, err := json.Marshal(cleanedResponse)
			if err != nil {
				otlpr.WithContext(log, ctx).Error(err, "failed to marshal cleaned JSON response")
				spanTokens.RecordError(err)
				spanTokens.SetStatus(codes.Error, err.Error())
				spanTokens.End()
				finalStatus = http.StatusInternalServerError
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if _, err := w.Write(cleanedResponseBytes); err != nil {
				otlpr.WithContext(log, ctx).Error(err, "failed to write cleaned JSON response")
				spanTokens.RecordError(err)
				spanTokens.SetStatus(codes.Error, err.Error())
				spanTokens.End()
				finalStatus = http.StatusInternalServerError
				return
			}
		}

		// Statistics for metrics and traces attributes
		llmAllTokensElapsedTimeMilisecond := float64(time.Since(llmStartTime)) / float64(time.Millisecond)
		e2eElapsedTimeMilisecond := float64(time.Since(routerRequestStartTime)) / float64(time.Millisecond)

		llmAllTokensLatencyMeasure.Record(ctx, llmAllTokensElapsedTimeMilisecond)
		e2eLatencyMeasure.Record(ctx, e2eElapsedTimeMilisecond)

		spanTokens.SetAttributes(attribute.Float64("router.llm.first.token.latency.ms", llmFirstTokenLatencyMilliseconds))
		spanTokens.SetAttributes(attribute.Float64("router.llm.next.token.latency.total.ms", llmNextTokenLatencyTotal))
		spanTokens.SetAttributes(attribute.Float64("router.llm.next.token.latency.count", llmNextTokenLatencyCount))
		spanTokens.SetAttributes(attribute.Float64("router.llm.next.token.latency.avg.ms", llmNextTokenLatencyTotal/llmNextTokenLatencyCount))
		spanTokens.SetAttributes(attribute.Float64("router.llm.all.tokens.latency.ms", llmAllTokensElapsedTimeMilisecond))

		spanTokens.SetAttributes(attribute.Float64("router.e2e.ttft.latency.ms", timeToFirstTokenLatencyMilliseconds))
		spanTokens.SetAttributes(attribute.Float64("router.e2e.latency.ms", e2eElapsedTimeMilisecond))

		if debugRequestTraces {
			spanTokens.SetAttributes(attribute.String("response buffer", lastChunk))
		}
		spanTokens.SetStatus(codes.Ok, "response send")
		spanTokens.End()

		// The stream completed successfully; record 200 for RED metrics.
		finalStatus = http.StatusOK

	}()

	select {
	case <-ctx.Done():
		otlpr.WithContext(log, ctx).Error(ctx.Err(), "context is in done state")
		// Whether the deadline was exceeded or the client cancelled, wait for the
		// worker goroutine to finish before touching w: it may still be writing to
		// the same ResponseWriter, and a concurrent write crashes the router with
		// 'fatal error: concurrent map writes'.
		deadlineExceeded := errors.Is(ctx.Err(), context.DeadlineExceeded)
		otlpr.WithContext(log, ctx).Info("waiting for subroutine due to context error")
		<-done
		otlpr.WithContext(log, ctx).Info("mcGraphHandler is done after previous context error")
		if deadlineExceeded {
			// The worker has stopped, so writing the timeout status is now safe. It
			// only takes effect if the worker had not already written a header.
			otlpr.WithContext(log, ctx).Error(errors.New("request timed out"), "failed to process request")
			http.Error(w, "request timed out", http.StatusGatewayTimeout)
			finalStatus = http.StatusGatewayTimeout
		}
	case <-done:
		otlpr.WithContext(log, ctx).Info("mcGraphHandler is done")
	}
	// Record the request counter after the worker completes and finalStatus is set.
	if finalStatus != 0 {
		recordRequest(ctx, finalStatus)
	}
}

// healthHandler answers liveness and readiness probes for the router process
// with a 200 and a short body. The router is ready once its routes are served;
// the KV watch degrades to the HTTP fallback rather than failing readiness, so
// it does not gate this endpoint.
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("ok")); err != nil {
		log.Error(err, "failed to write health response")
	}
}

func initializeRoutes() *http.ServeMux {
	mux := http.NewServeMux()

	// Wrap connector handlers with otelhttp wrappers
	// "http.server.request.size" -  Int64Counter -  "Measures the size of HTTP request messages" (Incoming request bytes total)
	// "http.server.response.size" - Int64Counter  - "Measures the size of HTTP response messages" (Incoming response bytes total)
	// "http.server.duration" - Float64histogram "Measures the duration of inbound HTTP requests." (Incoming end to end duration, milliseconds)
	handleFunc := func(pattern string, handlerFunc func(http.ResponseWriter, *http.Request), operation string) {
		// Wrap with otelhttp handler.
		handler := otelhttp.NewHandler(
			otelhttp.WithRouteTag(pattern, http.HandlerFunc(handlerFunc)),
			operation,
			otelhttp.WithFilter(func(r *http.Request) bool {
				for _, excludedUrl := range OtelExcludedUrls {
					if r.RequestURI == excludedUrl {
						return false
					}
				}
				return true
			}),
		)
		mux.Handle(pattern, handler)

		// Original code with wrapping with OTLP.
		// mux.Handle(pattern, http.HandlerFunc(handlerFunc))
	}

	handleFunc("/", mcGraphHandler, OtelNamespace+"/mcGraphHandler")

	// Router liveness/readiness. These are the router process's own endpoints on
	// :8080, distinct from the controller's /readyz-bridge on its own :8080; the
	// two run in separate processes. Registered without the otel wrapper so probe
	// traffic does not generate spans.
	mux.HandleFunc("/healthz", healthHandler)
	mux.HandleFunc("/readyz", healthHandler)

	promHandler := promhttp.Handler()
	handleFunc("/metrics", promHandler.ServeHTTP, OtelNamespace+"metrics")
	log.Info("Metrics exposed on /metrics.", "version", OtelVersion)

	return mux
}

func main() {
	flag.Parse()

	mcGraph = &mcv1alpha3.GMConnector{}
	err := json.Unmarshal([]byte(*jsonGraph), mcGraph)
	if err != nil {
		log.Error(err, "failed to unmarshall gmc graph json")
		os.Exit(1)
	}

	// Start watching the fingerprint parameter projection so steps can be
	// injected from RAM. The watch runs for the lifetime of the process.
	configW = newConfigWatcher(mcGraph)
	configW.start(context.Background())

	log.Info("Listen on :8080", "GraphTimeout(s):", CallClientTimeoutSeconds, "CallClientTimeout(s):", GraphHandlerTimeoutSeconds)
	mcRouter := initializeRoutes()

	server := &http.Server{
		// specify the address and port
		Addr: ":8080",
		// specify the HTTP routers
		Handler: mcRouter,
		// bound how long the server waits for a request's headers so a slow-loris
		// client cannot hold a connection open cheaply
		ReadHeaderTimeout: ReadHeaderTimeoutSeconds * time.Second,
		// set the maximum duration for reading the entire request, including the body
		ReadTimeout: GraphHandlerTimeoutSeconds * time.Second,
		// set the maximum duration before timing out writes of the response
		WriteTimeout: GraphHandlerTimeoutSeconds * time.Second,
		// set the maximum amount of time to wait for the next request when keep-alive are enabled
		IdleTimeout: 3 * time.Minute,
	}
	err = server.ListenAndServe()

	if err != nil {
		log.Error(err, "failed to listen on 8080")
		os.Exit(1)
	}
}
