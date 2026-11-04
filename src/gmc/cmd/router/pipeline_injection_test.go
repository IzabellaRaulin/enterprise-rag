/*
* Copyright (C) 2024-2026 Intel Corporation
* SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcv1alpha3 "erag.intel.com/gmc/api/v1alpha3"
	"github.com/stretchr/testify/assert"
	"knative.dev/pkg/apis"
)

// echoService starts a server that captures the request body it receives and
// replies with a fixed JSON object. The captured body is delivered on the
// returned channel so a test can assert what reached the service after
// injection. It is used to check that a leaf step is sent its parameter group.
func echoService(t *testing.T) (url string, received chan []byte, closeFn func()) {
	t.Helper()
	received = make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("echoService failed to read body: %v", err)
			return
		}
		received <- body
		if _, err := rw.Write([]byte(`{"predictions": "ok"}`)); err != nil {
			t.Errorf("echoService failed to write response: %v", err)
		}
	}))
	parsed, err := apis.ParseURL(srv.URL)
	if err != nil {
		t.Fatalf("failed to parse echo service url: %v", err)
	}
	return parsed.String(), received, srv.Close
}

// useTestWatcher installs a watcher as the package-level configW for the
// duration of a test and restores the previous value afterwards, so the
// pipeline handlers pick up the seeded parameter groups.
func useTestWatcher(t *testing.T, w *configWatcher) {
	t.Helper()
	prev := configW
	configW = w
	t.Cleanup(func() { configW = prev })
}

// recvWithTimeout reads the body an echoService captured, failing the test
// rather than blocking forever if the service was never called (an early error
// path in the router would otherwise hang the suite).
func recvWithTimeout(t *testing.T, received chan []byte) []byte {
	t.Helper()
	select {
	case body := <-received:
		return body
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the service to be called")
		return nil
	}
}

// closeResponse drains and closes a response body returned by routeStep so the
// test does not leak the connection across the suite.
func closeResponse(t *testing.T, body io.ReadCloser) {
	t.Helper()
	if body == nil {
		return
	}
	if err := body.Close(); err != nil {
		t.Errorf("failed to close response body: %v", err)
	}
}

// leafStep builds a leaf step (no NodeName) that targets a real service URL and
// carries a paramsKey, mirroring how a service step is described in a graph.
func leafStep(name, url, paramsKey string) mcv1alpha3.Step {
	cfg := map[string]string{}
	if paramsKey != "" {
		cfg[configParamsKey] = paramsKey
	}
	return mcv1alpha3.Step{
		StepName:   name,
		ServiceURL: url,
		Executor: mcv1alpha3.Executor{
			InternalService: mcv1alpha3.GMCTarget{
				ServiceName: "llm-svc",
				Config:      cfg,
			},
		},
	}
}

// TestSwitchPipelineInjectsParams verifies that a leaf step reached through a
// Switch node receives its parameter group merged onto the request.
func TestSwitchPipelineInjectsParams(t *testing.T) {
	url, received, closeFn := echoService(t)
	defer closeFn()

	w := newTestWatcher("chatqa")
	w.applyEntry("chatqa._global.llm", []byte(`{"max_new_tokens": 512}`), 1, time.Now())
	useTestWatcher(t, w)

	graph := mcv1alpha3.GMConnector{
		Spec: mcv1alpha3.GMConnectorSpec{
			Nodes: map[string]mcv1alpha3.Router{
				"root": {
					RouterType: mcv1alpha3.Switch,
					Steps:      []mcv1alpha3.Step{leafStep("Llm", url, "llm")},
				},
			},
		},
	}

	input := []byte(`{"query": "hi"}`)
	body, _, err := routeStep(context.Background(), "root", graph, input, input, http.Header{})
	assert.NoError(t, err)
	closeResponse(t, body)

	var got map[string]interface{}
	assert.NoError(t, json.Unmarshal(recvWithTimeout(t, received), &got))
	assert.EqualValues(t, 512, got["max_new_tokens"], "Switch leaf step must receive its parameter group")
	assert.Equal(t, "hi", got["query"], "the original request field must be preserved")
}

// TestEnsemblePipelineInjectsParams verifies that two same-kind leaf steps
// running in parallel under an Ensemble node each receive their own parameter
// group, so two of a kind stay independently addressable (mirrors
// TestSelectiveInjectionTwoLLMs on the Ensemble path).
func TestEnsemblePipelineInjectsParams(t *testing.T) {
	url1, received1, close1 := echoService(t)
	defer close1()
	url2, received2, close2 := echoService(t)
	defer close2()

	w := newTestWatcher("chatqa")
	w.applyEntry("chatqa._global.llm_primary", []byte(`{"max_new_tokens": 1024}`), 1, time.Now())
	w.applyEntry("chatqa._global.llm_secondary", []byte(`{"max_new_tokens": 128}`), 1, time.Now())
	useTestWatcher(t, w)

	graph := mcv1alpha3.GMConnector{
		Spec: mcv1alpha3.GMConnectorSpec{
			Nodes: map[string]mcv1alpha3.Router{
				"root": {
					RouterType: mcv1alpha3.Ensemble,
					Steps: []mcv1alpha3.Step{
						leafStep("Primary", url1, "llm_primary"),
						leafStep("Secondary", url2, "llm_secondary"),
					},
				},
			},
		},
	}

	input := []byte(`{"query": "hi"}`)
	body, _, err := routeStep(context.Background(), "root", graph, input, input, http.Header{})
	assert.NoError(t, err)
	closeResponse(t, body)

	var primary, secondary map[string]interface{}
	assert.NoError(t, json.Unmarshal(recvWithTimeout(t, received1), &primary))
	assert.NoError(t, json.Unmarshal(recvWithTimeout(t, received2), &secondary))
	assert.EqualValues(t, 1024, primary["max_new_tokens"])
	assert.EqualValues(t, 128, secondary["max_new_tokens"])
	assert.Equal(t, "hi", primary["query"])
	assert.Equal(t, "hi", secondary["query"])
}

// TestSubNodeStepNotInjected verifies that a step routing to a sub-node is not
// injected: its parameter group must not bleed into the nested leaf steps,
// which receive only their own group when the recursion reaches them.
func TestSubNodeStepNotInjected(t *testing.T) {
	url, received, closeFn := echoService(t)
	defer closeFn()

	w := newTestWatcher("chatqa")
	// The routing step's group carries a marker that must never reach a leaf.
	w.applyEntry("chatqa._global.routing_group", []byte(`{"bleed_marker": true}`), 1, time.Now())
	w.applyEntry("chatqa._global.leaf_llm", []byte(`{"max_new_tokens": 42}`), 1, time.Now())
	useTestWatcher(t, w)

	routingStep := mcv1alpha3.Step{
		StepName: "Router",
		Executor: mcv1alpha3.Executor{
			NodeName: "leaf",
			InternalService: mcv1alpha3.GMCTarget{
				Config: map[string]string{configParamsKey: "routing_group"},
			},
		},
	}
	graph := mcv1alpha3.GMConnector{
		Spec: mcv1alpha3.GMConnectorSpec{
			Nodes: map[string]mcv1alpha3.Router{
				"root": {
					RouterType: mcv1alpha3.Switch,
					Steps:      []mcv1alpha3.Step{routingStep},
				},
				"leaf": {
					RouterType: mcv1alpha3.Sequence,
					Steps:      []mcv1alpha3.Step{leafStep("Llm", url, "leaf_llm")},
				},
			},
		},
	}

	input := []byte(`{"query": "hi"}`)
	body, _, err := routeStep(context.Background(), "root", graph, input, input, http.Header{})
	assert.NoError(t, err)
	closeResponse(t, body)

	var got map[string]interface{}
	assert.NoError(t, json.Unmarshal(recvWithTimeout(t, received), &got))
	assert.EqualValues(t, 42, got["max_new_tokens"], "the leaf step must receive its own group")
	assert.NotContains(t, got, "bleed_marker", "the routing step's group must not bleed into the leaf")
	assert.Equal(t, "hi", got["query"])
}

// TestLeafStepInjectedOncePerPipeline verifies that the same leaf step
// definition reached through each pipeline type receives its group merged
// exactly once, so switching the surrounding node type does not double-inject
// or skip injection.
func TestLeafStepInjectedOncePerPipeline(t *testing.T) {
	for _, routerType := range []mcv1alpha3.RouterType{
		mcv1alpha3.Sequence,
		mcv1alpha3.Switch,
		mcv1alpha3.Ensemble,
	} {
		t.Run(string(routerType), func(t *testing.T) {
			url, received, closeFn := echoService(t)
			defer closeFn()

			w := newTestWatcher("chatqa")
			w.applyEntry("chatqa._global.llm", []byte(`{"max_new_tokens": 256}`), 1, time.Now())
			useTestWatcher(t, w)

			graph := mcv1alpha3.GMConnector{
				Spec: mcv1alpha3.GMConnectorSpec{
					Nodes: map[string]mcv1alpha3.Router{
						"root": {
							RouterType: routerType,
							Steps:      []mcv1alpha3.Step{leafStep("Llm", url, "llm")},
						},
					},
				},
			}

			input := []byte(`{"query": "hi"}`)
			body, _, err := routeStep(context.Background(), "root", graph, input, input, http.Header{})
			assert.NoError(t, err)
			closeResponse(t, body)

			var got map[string]interface{}
			assert.NoError(t, json.Unmarshal(recvWithTimeout(t, received), &got))
			assert.EqualValues(t, 256, got["max_new_tokens"])
			assert.Equal(t, "hi", got["query"])
		})
	}
}

func multipartUpload(t *testing.T, parameters string) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("files", "doc.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte("text"))
	if err := mw.WriteField(Parameters, parameters); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String(), mw.FormDataContentType()
}

func TestMcGraphHandlerCallerParamsReachLeaf(t *testing.T) {
	uploadBody, uploadType := multipartUpload(t, `{"summary_type": "stuff"}`)
	cases := []struct {
		name        string
		body        string
		contentType string
		key         string
		want        interface{}
	}{
		{"caller stream wins", `{"texts": ["x"], "parameters": {"stream": "this_should_be_a_boolean"}}`, JSONContentType, "stream", "this_should_be_a_boolean"},
		{"stored stream beats upstream default", `{"texts": ["x"]}`, JSONContentType, "stream", false},
		{"multipart caller summary_type wins", uploadBody, uploadType, "summary_type", "stuff"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(rw, `{"docs": [], "stream": true}`)
			}))
			defer upstream.Close()
			upstreamURL, err := apis.ParseURL(upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			leafURL, received, closeFn := echoService(t)
			defer closeFn()

			w := newTestWatcher("docsum")
			w.applyEntry("docsum._global.docsum", []byte(`{"stream": false, "summary_type": "refine"}`), 1, time.Now())
			useTestWatcher(t, w)

			docsum := leafStep("DocSum", leafURL, "docsum")
			docsum.Data = "$response"
			prevGraph := mcGraph
			t.Cleanup(func() { mcGraph = prevGraph })
			mcGraph = &mcv1alpha3.GMConnector{
				Spec: mcv1alpha3.GMConnectorSpec{
					Nodes: map[string]mcv1alpha3.Router{
						"root": {
							RouterType: mcv1alpha3.Sequence,
							Steps:      []mcv1alpha3.Step{leafStep("TextExtractor", upstreamURL.String(), ""), docsum},
						},
					},
				},
			}

			req := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			mcGraphHandler(httptest.NewRecorder(), req)

			var got map[string]interface{}
			assert.NoError(t, json.Unmarshal(recvWithTimeout(t, received), &got))
			assert.Equal(t, tc.want, got[tc.key])
		})
	}
}

func TestEnsembleRejectsMultipartEntry(t *testing.T) {
	url, received, closeFn := echoService(t)
	defer closeFn()

	graph := mcv1alpha3.GMConnector{
		Spec: mcv1alpha3.GMConnectorSpec{
			Nodes: map[string]mcv1alpha3.Router{
				"root": {
					RouterType: mcv1alpha3.Ensemble,
					Steps:      []mcv1alpha3.Step{leafStep("Llm", url, "llm")},
				},
			},
		},
	}

	body, contentType := multipartUpload(t, `{"summary_type": "stuff"}`)
	headers := http.Header{"Content-Type": []string{contentType}}
	ctx := withEntryRequest(context.Background(), []byte(body), headers)
	respBody, statusCode, err := routeStep(ctx, "root", graph, []byte(body), []byte(body), headers)
	assert.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, statusCode)
	closeResponse(t, respBody)
	assert.Empty(t, received)
}
