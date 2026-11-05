/*
* Copyright (C) 2024-2026 Intel Corporation
* SPDX-License-Identifier: Apache-2.0
 */

package v1alpha3

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	rt "k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// testKinds is the catalog the mock fingerprint serves for validation tests.
// The catalog-driven behaviour is proven by tests that mutate what the server
// serves (for example appending a brand-new kind, or serving only "llm"): a
// kind is accepted or rejected purely on what the fingerprint returns, not on
// any list compiled into the webhook.
var testKinds = []string{
	"llm",
	"retriever",
	"reranker",
	"query_rewrite",
	"prompt_template",
	"input_guard",
	"output_guard",
	"dataprep_guard",
}

// mockKindsServer starts a fingerprint stand-in that serves the given kinds at
// the kinds route (kindsPath, /v1/system_fingerprint/kinds) and counts how many
// times it is hit. It points the package catalog at a fresh instance so a fetch
// happens on the next check, and snapshots the URL, catalog, TTL, and bootstrap
// interval, restoring them on cleanup so tests do not leak state into one
// another. The hit count is atomic because it is written from the server
// goroutine and read from the test goroutine.
func mockKindsServer(t *testing.T, kinds []string) *atomic.Int32 {
	t.Helper()
	hits := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != kindsPath {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(kindsResponse{Kinds: kinds})
	}))
	t.Cleanup(srv.Close)

	prevURL, hadURL := os.LookupEnv(FingerprintServiceURLEnv)
	prevCatalog := kindCatalog
	prevTTL := kindsCatalogTTL
	prevBootstrap := kindsBootstrapRetry
	t.Cleanup(func() {
		if hadURL {
			os.Setenv(FingerprintServiceURLEnv, prevURL)
		} else {
			os.Unsetenv(FingerprintServiceURLEnv)
		}
		kindCatalog = prevCatalog
		kindsCatalogTTL = prevTTL
		kindsBootstrapRetry = prevBootstrap
	})

	os.Setenv(FingerprintServiceURLEnv, srv.URL)
	kindCatalog = newParamsKindCatalog()
	return hits
}

var (
	k8sClient client.Client
	testEnv   *envtest.Environment
	schem     *rt.Scheme
)

func TestMain(m *testing.M) {
	schem = rt.NewScheme()
	utilruntime.Must(AddToScheme(schem))

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,

		BinaryAssetsDirectory: filepath.Join("..", "..", "bin", "k8s",
			fmt.Sprintf("1.29.0-%s-%s", runtime.GOOS, runtime.GOARCH)),
	}

	cfg, err := testEnv.Start()
	if err != nil {
		panic(err)
	}
	defer func() {
		// Stop the test environment
		stopErr := testEnv.Stop()
		if stopErr != nil {
			panic(stopErr)
		}
	}()

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		panic(err)
	}

	code := m.Run()
	os.Exit(code)
}

func TestGMConnector_SetupWebhookWithManager(t *testing.T) {
	type args struct {
		mgr ctrl.Manager
	}
	port := 9440
	host := "localhost"

	m, err := ctrl.NewManager(testEnv.Config,
		ctrl.Options{
			Scheme:        schem,
			WebhookServer: webhook.NewServer(webhook.Options{Port: port, Host: host}),
		})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "success",
			args: args{
				mgr: m,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := (&GMConnector{}).SetupWebhookWithManager(tt.args.mgr); (err != nil) != tt.wantErr {
				t.Errorf("GMConnector.SetupWebhookWithManager() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_checkStepName(t *testing.T) {
	testNode := "test-node"
	type args struct {
		s        Step
		fldRoot  *field.Path
		nodeName string
	}
	tests := []struct {
		name string
		args args
		want *field.Error
	}{
		{
			name: "empty step name",
			args: args{
				fldRoot:  field.NewPath("spec").Child("nodes"),
				s:        Step{},
				nodeName: testNode,
			},
			want: field.Invalid(
				field.NewPath("spec").Child("nodes").Child(testNode).Child("steps[0]").Child("name"),
				Step{},
				fmt.Sprintf("the step name for node %v cannot be empty", testNode),
			),
		},
		{
			name: "invalid step name",
			args: args{
				fldRoot: field.NewPath("spec").Child("nodes"),
				s: Step{
					StepName: "invalid",
					Executor: Executor{},
				},
				nodeName: testNode,
			},
			want: field.Invalid(
				field.NewPath("spec").Child("nodes").Child(testNode).Child("steps[0]").Child("name"),
				Step{
					StepName: "invalid",
					Executor: Executor{},
				},
				fmt.Sprintf("invalid step name: %s for node %v", "invalid", testNode),
			),
		},
		{
			name: "success",
			args: args{
				fldRoot: field.NewPath("spec").Child("nodes"),
				s: Step{
					StepName: "Embedding",
					Executor: Executor{},
				},
				nodeName: testNode,
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkStepName(tt.args.s, 0, tt.args.fldRoot, tt.args.nodeName); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("checkStepName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_checkParamsKind(t *testing.T) {
	mockKindsServer(t, testKinds)
	testNode := "test-node"
	fldRoot := field.NewPath("spec").Child("nodes")
	type args struct {
		s        Step
		fldRoot  *field.Path
		nodeName string
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "no paramsKind declared",
			args: args{
				fldRoot:  fldRoot,
				s:        stepWithConfig("Llm", map[string]string{"endpoint": "/v1/chat"}),
				nodeName: testNode,
			},
			wantErr: false,
		},
		{
			name: "valid paramsKind with key",
			args: args{
				fldRoot:  fldRoot,
				s:        stepWithConfig("Llm", map[string]string{"paramsKind": "llm", "paramsKey": "llm"}),
				nodeName: testNode,
			},
			wantErr: false,
		},
		{
			name: "invalid paramsKind",
			args: args{
				fldRoot:  fldRoot,
				s:        stepWithConfig("Llm", map[string]string{"paramsKind": "bogus", "paramsKey": "llm"}),
				nodeName: testNode,
			},
			wantErr: true,
		},
		{
			name: "valid paramsKind, key defaults from step name",
			args: args{
				fldRoot:  fldRoot,
				s:        stepWithConfig("Llm", map[string]string{"paramsKind": "llm"}),
				nodeName: testNode,
			},
			wantErr: false,
		},
		{
			name: "invalid paramsKind, key still defaults from step name",
			args: args{
				fldRoot:  fldRoot,
				s:        stepWithConfig("Llm", map[string]string{"paramsKind": "bogus"}),
				nodeName: testNode,
			},
			wantErr: true,
		},
		{
			name: "empty paramsKind is rejected",
			args: args{
				fldRoot:  fldRoot,
				s:        stepWithConfig("Llm", map[string]string{"paramsKind": ""}),
				nodeName: testNode,
			},
			wantErr: true,
		},
		{
			name: "whitespace paramsKind is rejected",
			args: args{
				fldRoot:  fldRoot,
				s:        stepWithConfig("Llm", map[string]string{"paramsKind": "  "}),
				nodeName: testNode,
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkParamsKind(tt.args.s, 0, tt.args.fldRoot, tt.args.nodeName); (got != nil) != tt.wantErr {
				t.Errorf("checkParamsKind() = %v, wantErr %v", got, tt.wantErr)
			}
		})
	}

	// The invalid case must point at the config.paramsKind field, carry the
	// offending kind as the invalid value, and use the documented message.
	t.Run("invalid error shape", func(t *testing.T) {
		step := stepWithConfig("Llm", map[string]string{"paramsKind": "bogus", "paramsKey": "llm"})
		want := field.Invalid(
			fldRoot.Child(testNode).Child("steps[0]").Child("internalService").Child("config").Child(ConfigParamsKind),
			"bogus",
			fmt.Sprintf("Invalid paramsKind 'bogus' for step 'Llm'; allowed: %v", testKinds))
		if got := checkParamsKind(step, 0, fldRoot, testNode); !reflect.DeepEqual(got, want) {
			t.Errorf("checkParamsKind() = %v, want %v", got, want)
		}
	})
}

func Test_checkParamsKeyConflict(t *testing.T) {
	mockKindsServer(t, testKinds)
	testNode := "test-node"
	fldRoot := field.NewPath("spec").Child("nodes")

	t.Run("same key different kind conflicts", func(t *testing.T) {
		seen := map[string]string{}
		first := stepWithConfig("Llm", map[string]string{"paramsKind": "llm", "paramsKey": "shared"})
		second := stepWithConfig("Retriever", map[string]string{"paramsKind": "retriever", "paramsKey": "shared"})
		if err := checkParamsKeyConflict(first, 0, fldRoot, testNode, seen); err != nil {
			t.Fatalf("first step must not conflict, got %v", err)
		}
		if err := checkParamsKeyConflict(second, 1, fldRoot, testNode, seen); err == nil {
			t.Error("second step reusing the key with a different kind should conflict")
		}
	})

	t.Run("same key same kind is allowed", func(t *testing.T) {
		seen := map[string]string{}
		first := stepWithConfig("Llm", map[string]string{"paramsKind": "llm", "paramsKey": "llm_primary"})
		second := stepWithConfig("VLLM", map[string]string{"paramsKind": "llm", "paramsKey": "llm_primary"})
		_ = checkParamsKeyConflict(first, 0, fldRoot, testNode, seen)
		if err := checkParamsKeyConflict(second, 1, fldRoot, testNode, seen); err != nil {
			t.Errorf("same key with same kind should be allowed, got %v", err)
		}
	})

	t.Run("step without kind is ignored", func(t *testing.T) {
		seen := map[string]string{}
		step := stepWithConfig("Retriever", map[string]string{"endpoint": "/retrieve"})
		if err := checkParamsKeyConflict(step, 0, fldRoot, testNode, seen); err != nil {
			t.Errorf("step without paramsKind should be ignored, got %v", err)
		}
		if len(seen) != 0 {
			t.Errorf("step without paramsKind should not record a key, seen=%v", seen)
		}
	})
}

func Test_checkParamsKeyCharset(t *testing.T) {
	testNode := "test-node"
	fldRoot := field.NewPath("spec").Child("nodes")

	t.Run("dotted key is rejected", func(t *testing.T) {
		step := stepWithConfig("Llm", map[string]string{"paramsKind": "llm", "paramsKey": "llm.primary"})
		if err := checkParamsKeyCharset(step, 0, fldRoot, testNode); err == nil {
			t.Error("a paramsKey containing a dot must be rejected")
		}
	})

	t.Run("whitespace and wildcard keys are rejected", func(t *testing.T) {
		for _, key := range []string{"llm primary", "llm*", "llm>"} {
			step := stepWithConfig("Llm", map[string]string{"paramsKind": "llm", "paramsKey": key})
			if err := checkParamsKeyCharset(step, 0, fldRoot, testNode); err == nil {
				t.Errorf("paramsKey %q must be rejected", key)
			}
		}
	})

	t.Run("alnum underscore key is allowed", func(t *testing.T) {
		step := stepWithConfig("Llm", map[string]string{"paramsKind": "llm", "paramsKey": "llm_primary"})
		if err := checkParamsKeyCharset(step, 0, fldRoot, testNode); err != nil {
			t.Errorf("a valid paramsKey must be allowed, got %v", err)
		}
	})

	t.Run("step without kind is ignored", func(t *testing.T) {
		step := stepWithConfig("Llm", map[string]string{"paramsKey": "llm.primary"})
		if err := checkParamsKeyCharset(step, 0, fldRoot, testNode); err != nil {
			t.Errorf("a step that declares no paramsKind must be ignored, got %v", err)
		}
	})
}

func Test_ResolveParamsKey(t *testing.T) {
	tests := []struct {
		name string
		step Step
		want string
	}{
		{
			name: "explicit paramsKey",
			step: stepWithConfig("Llm", map[string]string{"paramsKey": "llm_primary"}),
			want: "llm_primary",
		},
		{
			name: "defaults to lowercased step name",
			step: stepWithConfig("Retriever", map[string]string{"paramsKind": "retriever"}),
			want: "retriever",
		},
		{
			name: "empty step name resolves empty",
			step: stepWithConfig("", nil),
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveParamsKey(tt.step); got != tt.want {
				t.Errorf("ResolveParamsKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

// stepWithConfig builds a step whose internal-service config carries the given
// entries.
func stepWithConfig(stepName string, config map[string]string) Step {
	return Step{
		StepName: stepName,
		Executor: Executor{
			InternalService: GMCTarget{Config: config},
		},
	}
}

func Test_nodeNameExists(t *testing.T) {
	type args struct {
		name  string
		nodes []string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "nodeName is unset",
			args: args{
				name:  "",
				nodes: []string{"root", "test-node"},
			},
			want: true,
		},
		{
			name: "unknown nodeName",
			args: args{
				name:  "unknown",
				nodes: []string{"root", "test-node"},
			},
			want: false,
		},
		{
			name: "existing nodeName",
			args: args{
				name:  "root",
				nodes: []string{"root", "test-node"},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeNameExists(tt.args.name, tt.args.nodes); got != tt.want {
				t.Errorf("nodeNameExists() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_validateRootExistance(t *testing.T) {
	type args struct {
		nodes   map[string]Router
		fldPath *field.Path
	}
	tests := []struct {
		name string
		args args
		want *field.Error
	}{
		{
			name: "root node exists",
			args: args{
				nodes: map[string]Router{
					"root": {},
				},
				fldPath: field.NewPath("spec").Child("nodes"),
			},
			want: nil,
		},
		{
			name: "root node does not exist",
			args: args{
				nodes:   map[string]Router{},
				fldPath: field.NewPath("spec").Child("nodes"),
			},
			want: field.Invalid(field.NewPath("spec").Child("nodes"),
				map[string]Router{},
				"a root node is required"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validateRootExistance(tt.args.nodes, tt.args.fldPath); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("validateRootExistance() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getKeys(t *testing.T) {
	type args struct {
		m map[string]Router
	}
	tests := []struct {
		name string
		args args
		want []string
	}{
		{
			name: "succes",
			args: args{
				m: map[string]Router{
					"root": {},
				},
			},
			want: []string{"root"},
		},
		{
			name: "empty map",
			args: args{
				m: map[string]Router{},
			},
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getKeys(tt.args.m); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getKeys() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_validateNames(t *testing.T) {
	var errs field.ErrorList
	type args struct {
		nodes   map[string]Router
		fldPath *field.Path
	}
	tests := []struct {
		name string
		args args
		want field.ErrorList
	}{
		{
			name: "duplicate service name",
			args: args{
				nodes: map[string]Router{
					"root": {
						Steps: []Step{
							{
								StepName: "Embedding",
								Executor: Executor{
									InternalService: GMCTarget{
										ServiceName: "embedding_svc",
									},
								},
							},
							{
								StepName: "Embedding",
								Executor: Executor{
									InternalService: GMCTarget{
										ServiceName: "embedding_svc",
									},
								},
							},
						},
					},
				},
				fldPath: field.NewPath("spec").Child("nodes"),
			},
			want: append(errs, field.Invalid(
				field.NewPath("spec").Child("nodes").Child("root").Child("steps[1]").Child("internalService").Child("serviceName"),
				Step{
					StepName: "Embedding",
					Executor: Executor{
						InternalService: GMCTarget{
							ServiceName: "embedding_svc",
						},
					},
				},
				"service name: embedding_svc in node root already exists")),
		},
		{
			name: "no error",
			args: args{
				nodes: map[string]Router{
					"root": {
						Steps: []Step{
							{
								StepName: "Embedding",
								Executor: Executor{
									InternalService: GMCTarget{
										ServiceName: "embedding_svc1",
									},
								},
							},
							{
								StepName: "Embedding",
								Executor: Executor{
									InternalService: GMCTarget{
										ServiceName: "embedding_svc2",
									},
								},
							},
						},
					},
				},
				fldPath: field.NewPath("spec").Child("nodes"),
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validateNames(tt.args.nodes, tt.args.fldPath); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("validateNames() = %v, want %v", got, tt.want)
			}
		})
	}
}

// routerNode builds a node of the given RouterType with the given steps.
func routerNode(rt RouterType, steps ...Step) Router {
	return Router{RouterType: rt, Steps: steps}
}

// stepTo builds a step with a name that targets nextNode.
func stepTo(stepName, nextNode string) Step {
	return Step{StepName: stepName, Executor: Executor{NodeName: nextNode}}
}

func Test_checkStepName_allowlist(t *testing.T) {
	testNode := "test-node"
	fldRoot := field.NewPath("spec").Child("nodes")

	// OvmsNer and VLLMQueryRewrite are retained ERAG steps and must be accepted.
	for _, retained := range []string{"OvmsNer", "VLLMQueryRewrite"} {
		if err := checkStepName(Step{StepName: retained}, 0, fldRoot, testNode); err != nil {
			t.Errorf("retained step name %q should be allowed, got %v", retained, err)
		}
	}

	// Ghost names dropped from the allowlist must now be rejected.
	for _, ghost := range []string{"TeiEmbedding", "TeiReranking", "WebRetriever", "Asr", "Whisper", "VLLM", "LateChunking"} {
		if err := checkStepName(Step{StepName: ghost}, 0, fldRoot, testNode); err == nil {
			t.Errorf("dropped ghost step name %q should be rejected", ghost)
		}
	}
}

func Test_validateGraphSize(t *testing.T) {
	fldPath := field.NewPath("spec").Child("nodes")

	t.Run("too many nodes", func(t *testing.T) {
		nodes := map[string]Router{}
		for i := 0; i <= maxGraphNodes; i++ {
			nodes[fmt.Sprintf("n%d", i)] = Router{RouterType: Sequence}
		}
		if errs := validateGraphSize(nodes, fldPath); len(errs) == 0 {
			t.Error("a graph over the node cap should be rejected")
		}
	})

	t.Run("too many steps in a node", func(t *testing.T) {
		steps := make([]Step, maxStepsPerNode+1)
		nodes := map[string]Router{"root": {RouterType: Sequence, Steps: steps}}
		if errs := validateGraphSize(nodes, fldPath); len(errs) == 0 {
			t.Error("a node over the step cap should be rejected")
		}
	})

	t.Run("within limits", func(t *testing.T) {
		nodes := map[string]Router{"root": {RouterType: Sequence, Steps: []Step{stepTo("Llm", "")}}}
		if errs := validateGraphSize(nodes, fldPath); len(errs) != 0 {
			t.Errorf("a small graph should pass, got %v", errs)
		}
	})
}

func Test_validateRouterTypes(t *testing.T) {
	fldPath := field.NewPath("spec").Child("nodes")

	t.Run("unknown router type", func(t *testing.T) {
		nodes := map[string]Router{"root": {RouterType: RouterType("Bogus")}}
		if errs := validateRouterTypes(nodes, fldPath); len(errs) == 0 {
			t.Error("an unknown routerType should be rejected")
		}
	})

	t.Run("known router types", func(t *testing.T) {
		nodes := map[string]Router{
			"root": {RouterType: Sequence},
			"a":    {RouterType: Ensemble},
			"b":    {RouterType: Switch},
		}
		if errs := validateRouterTypes(nodes, fldPath); len(errs) != 0 {
			t.Errorf("known routerTypes should pass, got %v", errs)
		}
	})
}

func Test_validateSwitchDefault(t *testing.T) {
	fldPath := field.NewPath("spec").Child("nodes")

	t.Run("switch with no default is rejected", func(t *testing.T) {
		nodes := map[string]Router{
			"root": routerNode(Switch,
				Step{StepName: "Llm", Condition: "instances.#(modelId==\"1\")"},
				Step{StepName: "Llm", Condition: "instances.#(modelId==\"2\")"},
			),
		}
		if errs := validateSwitchDefault(nodes, fldPath); len(errs) == 0 {
			t.Error("a Switch node where every step has a condition should be rejected")
		}
	})

	t.Run("switch with a default is allowed", func(t *testing.T) {
		nodes := map[string]Router{
			"root": routerNode(Switch,
				Step{StepName: "Llm", Condition: "instances.#(modelId==\"1\")"},
				Step{StepName: "Llm"},
			),
		}
		if errs := validateSwitchDefault(nodes, fldPath); len(errs) != 0 {
			t.Errorf("a Switch node with a default step should pass, got %v", errs)
		}
	})

	t.Run("non-switch node is ignored", func(t *testing.T) {
		nodes := map[string]Router{
			"root": routerNode(Sequence, Step{StepName: "Llm", Condition: "x"}),
		}
		if errs := validateSwitchDefault(nodes, fldPath); len(errs) != 0 {
			t.Errorf("a Sequence node should not be checked for a default step, got %v", errs)
		}
	})
}

func Test_validateNoCycles(t *testing.T) {
	fldPath := field.NewPath("spec").Child("nodes")

	t.Run("cyclic graph is rejected", func(t *testing.T) {
		nodes := map[string]Router{
			"root": routerNode(Sequence, stepTo("Llm", "a")),
			"a":    routerNode(Sequence, stepTo("Llm", "root")),
		}
		if err := validateNoCycles(nodes, fldPath); err == nil {
			t.Error("a graph with a cycle should be rejected")
		}
	})

	t.Run("self loop is rejected", func(t *testing.T) {
		nodes := map[string]Router{
			"root": routerNode(Sequence, stepTo("Llm", "root")),
		}
		if err := validateNoCycles(nodes, fldPath); err == nil {
			t.Error("a self-referencing node should be rejected")
		}
	})

	t.Run("acyclic graph is allowed", func(t *testing.T) {
		nodes := map[string]Router{
			"root": routerNode(Sequence, stepTo("Llm", "a")),
			"a":    routerNode(Ensemble, stepTo("Llm", "")),
		}
		if err := validateNoCycles(nodes, fldPath); err != nil {
			t.Errorf("an acyclic graph should pass, got %v", err)
		}
	})
}

func Test_validateReachability(t *testing.T) {
	fldPath := field.NewPath("spec").Child("nodes")

	t.Run("unreachable node is rejected", func(t *testing.T) {
		nodes := map[string]Router{
			"root":   routerNode(Sequence, stepTo("Llm", "")),
			"orphan": routerNode(Sequence, stepTo("Llm", "")),
		}
		if errs := validateReachability(nodes, fldPath); len(errs) == 0 {
			t.Error("a node not reachable from root should be rejected")
		}
	})

	t.Run("all nodes reachable is allowed", func(t *testing.T) {
		nodes := map[string]Router{
			"root": routerNode(Sequence, stepTo("Llm", "a")),
			"a":    routerNode(Sequence, stepTo("Llm", "b")),
			"b":    routerNode(Ensemble, stepTo("Llm", "")),
		}
		if errs := validateReachability(nodes, fldPath); len(errs) != 0 {
			t.Errorf("a fully reachable graph should pass, got %v", errs)
		}
	})
}

// A valid Sequence -> Ensemble -> Sequence -> Switch graph must pass every
// structural check.
func Test_checkfields_validGraph(t *testing.T) {
	mockKindsServer(t, testKinds)
	c := &GMConnector{
		Spec: GMConnectorSpec{
			Nodes: map[string]Router{
				"root": routerNode(Sequence, stepTo("Embedding", "ensemble")),
				"ensemble": routerNode(Ensemble,
					stepTo("Retriever", "seq"),
					stepTo("TextSplitter", ""),
				),
				"seq": routerNode(Sequence, stepTo("Reranking", "switch")),
				"switch": routerNode(Switch,
					Step{StepName: "Llm", Condition: "instances.#(modelId==\"1\")", Executor: Executor{NodeName: ""}},
					Step{StepName: "Llm"},
				),
			},
		},
	}
	if errs := c.checkfields(); errs != nil {
		t.Errorf("a valid Sequence->Ensemble->Sequence->Switch graph should pass, got %v", errs)
	}
}

// A graph with a dangling nodeName is rejected by the name check.
func Test_checkfields_danglingNodeName(t *testing.T) {
	mockKindsServer(t, testKinds)
	c := &GMConnector{
		Spec: GMConnectorSpec{
			Nodes: map[string]Router{
				"root": routerNode(Sequence, stepTo("Llm", "missing")),
			},
		},
	}
	if errs := c.checkfields(); errs == nil {
		t.Error("a graph referencing a missing node should be rejected")
	}
}

// A kind is accepted purely because the fingerprint serves it, even one absent
// from the list the webhook once compiled in. This is the point of sourcing the
// catalog: adding a kind in the fingerprint needs no webhook code change.
func Test_checkParamsKind_acceptsServedKind(t *testing.T) {
	mockKindsServer(t, append(append([]string(nil), testKinds...), "brand_new_kind"))
	fldRoot := field.NewPath("spec").Child("nodes")

	step := stepWithConfig("Llm", map[string]string{"paramsKind": "brand_new_kind"})
	if err := checkParamsKind(step, 0, fldRoot, "test-node"); err != nil {
		t.Errorf("a kind present in the served catalog should be accepted, got %v", err)
	}
}

// A kind the fingerprint does not serve is rejected even though it was once a
// valid built-in value, proving the catalog, not a compiled-in list, decides.
func Test_checkParamsKind_rejectsUnservedKind(t *testing.T) {
	mockKindsServer(t, []string{"llm"})
	fldRoot := field.NewPath("spec").Child("nodes")

	step := stepWithConfig("Reranking", map[string]string{"paramsKind": "reranker"})
	if err := checkParamsKind(step, 0, fldRoot, "test-node"); err == nil {
		t.Error("a kind absent from the served catalog should be rejected")
	}
}

// The catalog is fetched once and reused within the TTL, so a burst of
// admissions does not fetch per check.
func Test_paramsKindCatalog_caches(t *testing.T) {
	hits := mockKindsServer(t, testKinds)

	for i := 0; i < 5; i++ {
		if allowed, _ := kindCatalog.allows("llm"); !allowed {
			t.Fatalf("llm should be allowed on check %d", i)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("catalog should be fetched once within the TTL, got %d fetches", got)
	}
}

// Once the TTL elapses the next check refreshes the catalog, so a kind added in
// the fingerprint becomes valid without a restart.
func Test_paramsKindCatalog_refreshesAfterTTL(t *testing.T) {
	hits := mockKindsServer(t, testKinds)

	if allowed, _ := kindCatalog.allows("llm"); !allowed {
		t.Fatal("llm should be allowed on the first check")
	}
	kindsCatalogTTL = 0

	if allowed, _ := kindCatalog.allows("llm"); !allowed {
		t.Fatal("llm should still be allowed after the TTL elapses")
	}
	if got := hits.Load(); got < 2 {
		t.Errorf("catalog should refresh after the TTL, got %d fetches", got)
	}
}

// When the fingerprint cannot be reached and no catalog has ever been fetched,
// admission fails open: any kind is accepted so a fingerprint outage does not
// block every GMConnector write under the Fail failurePolicy.
func Test_paramsKindCatalog_failsOpenWhenUnreachable(t *testing.T) {
	prevURL, hadURL := os.LookupEnv(FingerprintServiceURLEnv)
	prevCatalog := kindCatalog
	t.Cleanup(func() {
		if hadURL {
			os.Setenv(FingerprintServiceURLEnv, prevURL)
		} else {
			os.Unsetenv(FingerprintServiceURLEnv)
		}
		kindCatalog = prevCatalog
	})
	// A port nothing listens on, so the fetch fails fast.
	os.Setenv(FingerprintServiceURLEnv, "http://127.0.0.1:1")
	kindCatalog = newParamsKindCatalog()

	if allowed, catalog := kindCatalog.allows("anything"); !allowed || catalog != nil {
		t.Errorf("an unreachable fingerprint should fail open with no catalog, got allowed=%v catalog=%v", allowed, catalog)
	}

	// Fail-open must not admit an empty kind: that is always a misconfiguration
	// and is rejected before the catalog is consulted.
	fldRoot := field.NewPath("spec").Child("nodes")
	step := stepWithConfig("Llm", map[string]string{"paramsKind": ""})
	if err := checkParamsKind(step, 0, fldRoot, "test-node"); err == nil {
		t.Error("an empty paramsKind should be rejected even while failing open")
	}
}

// While no catalog has ever been fetched, a failed fetch is retried on the
// shorter bootstrap interval rather than waiting the full TTL, so the fail-open
// window closes soon after the fingerprint becomes reachable. Once a catalog is
// in hand the longer TTL governs, so a good catalog is not refetched every call.
func Test_paramsKindCatalog_bootstrapRetriesBeforeTTL(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != kindsPath {
			http.NotFound(w, r)
			return
		}
		// Fail the first attempt so the catalog stays unfetched, then succeed.
		if attempts.Add(1) == 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(kindsResponse{Kinds: testKinds})
	}))
	prevURL, hadURL := os.LookupEnv(FingerprintServiceURLEnv)
	prevCatalog := kindCatalog
	prevTTL := kindsCatalogTTL
	prevBootstrap := kindsBootstrapRetry
	t.Cleanup(func() {
		if hadURL {
			os.Setenv(FingerprintServiceURLEnv, prevURL)
		} else {
			os.Unsetenv(FingerprintServiceURLEnv)
		}
		kindCatalog = prevCatalog
		kindsCatalogTTL = prevTTL
		kindsBootstrapRetry = prevBootstrap
		srv.Close()
	})

	os.Setenv(FingerprintServiceURLEnv, srv.URL)
	kindCatalog = newParamsKindCatalog()
	// A long TTL would gate the retry for minutes; the bootstrap interval must
	// govern instead while no catalog has been fetched yet.
	kindsCatalogTTL = time.Hour
	kindsBootstrapRetry = 0

	// First check: fetch fails, so the catalog stays unfetched and fails open.
	if allowed, catalog := kindCatalog.allows("llm"); !allowed || catalog != nil {
		t.Fatal("first check should fail open while the catalog is unfetched")
	}
	// Second check: because no catalog is in hand, the bootstrap interval (not
	// the hour-long TTL) governs, so it retries and now succeeds.
	if allowed, _ := kindCatalog.allows("llm"); !allowed {
		t.Fatal("llm should be allowed after the bootstrap retry succeeds")
	}
	if allowed, _ := kindCatalog.allows("bogus"); allowed {
		t.Error("an unknown kind should be rejected once the catalog is fetched")
	}
	// Third check: a catalog is now in hand, so the TTL governs and it is not
	// refetched despite the zero bootstrap interval.
	before := attempts.Load()
	_, _ = kindCatalog.allows("llm")
	if got := attempts.Load(); got != before {
		t.Errorf("a fetched catalog should be governed by the TTL, not refetched; got %d attempts", got)
	}
}

// A refresh must not hold a lock across the network fetch: while one caller is
// blocked in a slow fetch, concurrent admissions must keep answering promptly
// from the last known-good catalog rather than serializing behind the fetch.
func Test_paramsKindCatalog_refreshDoesNotSerialize(t *testing.T) {
	var slow atomic.Bool
	fetchStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFetch := func() { releaseOnce.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != kindsPath {
			http.NotFound(w, r)
			return
		}
		if slow.Load() {
			select {
			case fetchStarted <- struct{}{}:
			default:
			}
			<-release
		}
		_ = json.NewEncoder(w).Encode(kindsResponse{Kinds: []string{"llm"}})
	}))
	prevURL, hadURL := os.LookupEnv(FingerprintServiceURLEnv)
	prevCatalog := kindCatalog
	prevTTL := kindsCatalogTTL
	t.Cleanup(func() {
		if hadURL {
			os.Setenv(FingerprintServiceURLEnv, prevURL)
		} else {
			os.Unsetenv(FingerprintServiceURLEnv)
		}
		kindCatalog = prevCatalog
		kindsCatalogTTL = prevTTL
		releaseFetch()
		srv.Close()
	})

	os.Setenv(FingerprintServiceURLEnv, srv.URL)
	kindCatalog = newParamsKindCatalog()

	// Prime the last known-good catalog from the fast server.
	if allowed, _ := kindCatalog.allows("llm"); !allowed {
		t.Fatal("llm should be allowed once the catalog is primed")
	}

	// Make the next check due while a generous TTL keeps late arrivals from each
	// firing their own fetch once the first caller has claimed the refresh.
	kindsCatalogTTL = time.Minute
	kindCatalog.mu.Lock()
	kindCatalog.lastAttempt = time.Now().Add(-time.Hour)
	kindCatalog.mu.Unlock()
	slow.Store(true)

	// One caller triggers the refresh and blocks in the slow fetch, holding no
	// lock while it waits.
	refreshDone := make(chan struct{})
	go func() {
		kindCatalog.allows("llm")
		close(refreshDone)
	}()
	select {
	case <-fetchStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh never reached the fetch; the refresh path was not taken")
	}

	// A concurrent admission must answer from the last known-good catalog without
	// waiting on the in-flight fetch; if allows() held a lock across the fetch it
	// would hang here until the fetch is released.
	answered := make(chan bool, 1)
	go func() {
		allowed, _ := kindCatalog.allows("llm")
		answered <- allowed
	}()
	select {
	case allowed := <-answered:
		if !allowed {
			t.Error("a known kind should stay allowed on the last known-good catalog during a refresh")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("allows() blocked on the in-flight fetch; a lock is held across the network call")
	}
	// The last known-good catalog still decides while the refresh is in flight,
	// so an unknown kind stays rejected.
	if allowed, _ := kindCatalog.allows("bogus"); allowed {
		t.Error("an unknown kind should stay rejected on the last known-good catalog during a refresh")
	}

	// Let the fetch finish so the triggering caller returns.
	releaseFetch()
	<-refreshDone
}

// A blip after a successful fetch keeps the last known-good catalog rather than
// falling back to accept-all, so validation stays strict across a transient
// outage.
func Test_paramsKindCatalog_keepsLastKnownGood(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != kindsPath {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(kindsResponse{Kinds: []string{"llm"}})
	}))
	prevURL, hadURL := os.LookupEnv(FingerprintServiceURLEnv)
	prevCatalog := kindCatalog
	prevTTL := kindsCatalogTTL
	t.Cleanup(func() {
		if hadURL {
			os.Setenv(FingerprintServiceURLEnv, prevURL)
		} else {
			os.Unsetenv(FingerprintServiceURLEnv)
		}
		kindCatalog = prevCatalog
		kindsCatalogTTL = prevTTL
		srv.Close()
	})

	os.Setenv(FingerprintServiceURLEnv, srv.URL)
	kindCatalog = newParamsKindCatalog()

	// Prime the cache from the live server.
	if allowed, _ := kindCatalog.allows("llm"); !allowed {
		t.Fatal("llm should be allowed while the fingerprint is up")
	}

	// Take the fingerprint down and force a refresh; the primed catalog must
	// survive so a known kind stays valid and an unknown one stays rejected.
	srv.Close()
	kindsCatalogTTL = 0
	if allowed, _ := kindCatalog.allows("llm"); !allowed {
		t.Error("a known kind should stay valid on the last known-good catalog after a blip")
	}
	if allowed, _ := kindCatalog.allows("bogus"); allowed {
		t.Error("an unknown kind should stay rejected on the last known-good catalog after a blip")
	}
}

func Test_validateRouterConfig(t *testing.T) {
	fldPath := field.NewPath("spec").Child("routerConfig")

	t.Run("a plain name is accepted", func(t *testing.T) {
		cfg := RouterConfig{Name: "router", ServiceName: "router-service"}
		if errs := validateRouterConfig(cfg, fldPath); len(errs) != 0 {
			t.Errorf("a valid routerConfig should be accepted, got %v", errs)
		}
	})

	t.Run("empty falls back to the controller default", func(t *testing.T) {
		if errs := validateRouterConfig(RouterConfig{}, fldPath); len(errs) != 0 {
			t.Errorf("an empty routerConfig should be accepted, got %v", errs)
		}
	})

	t.Run("a newline cannot introduce another document", func(t *testing.T) {
		// The controller interpolates serviceName into the router manifest and
		// splits the result on "---", so a newline here would let the value
		// declare a document of its own.
		cfg := RouterConfig{ServiceName: "router-service\n---\nkind: RoleBinding\njunk: #"}
		if errs := validateRouterConfig(cfg, fldPath); len(errs) == 0 {
			t.Error("a serviceName carrying a newline must be rejected")
		}
	})

	t.Run("names that are not DNS labels are rejected", func(t *testing.T) {
		for _, bad := range []string{"Router-Service", "router_service", "-router", "router-", "router service", "router/../etc"} {
			if errs := validateRouterConfig(RouterConfig{ServiceName: bad}, fldPath); len(errs) == 0 {
				t.Errorf("serviceName %q must be rejected", bad)
			}
		}
	})

	t.Run("serviceName leaves room for the deployment suffix", func(t *testing.T) {
		// The controller appends "-deployment", so the name must stay short
		// enough for the result to remain a valid DNS label.
		cfg := RouterConfig{ServiceName: strings.Repeat("a", maxDNSLabelLength-len("-deployment")+1)}
		if errs := validateRouterConfig(cfg, fldPath); len(errs) == 0 {
			t.Error("a serviceName leaving no room for the -deployment suffix must be rejected")
		}
	})
}

func Test_validateFanout(t *testing.T) {
	fldPath := field.NewPath("spec").Child("nodes")

	t.Run("a flat sequence is accepted", func(t *testing.T) {
		steps := make([]Step, maxStepsPerNode)
		nodes := map[string]Router{"root": {RouterType: Sequence, Steps: steps}}
		if errs := validateFanout(nodes, fldPath); len(errs) != 0 {
			t.Errorf("a single Sequence node should be accepted, got %v", errs)
		}
	})

	t.Run("one ensemble at the step cap is accepted", func(t *testing.T) {
		steps := make([]Step, maxStepsPerNode)
		nodes := map[string]Router{"root": {RouterType: Ensemble, Steps: steps}}
		if errs := validateFanout(nodes, fldPath); len(errs) != 0 {
			t.Errorf("one Ensemble node within the step cap should be accepted, got %v", errs)
		}
	})

	t.Run("nested ensembles that multiply past the cap are rejected", func(t *testing.T) {
		// A chain of Ensemble nodes each routing into the next multiplies its
		// fan-out, which the node and step caps do not bound.
		nodes := map[string]Router{}
		for i := 0; i < maxGraphNodes-1; i++ {
			name := "root"
			if i > 0 {
				name = fmt.Sprintf("n%d", i)
			}
			next := fmt.Sprintf("n%d", i+1)
			steps := make([]Step, maxStepsPerNode)
			for s := range steps {
				steps[s] = Step{StepName: "Llm", Executor: Executor{NodeName: next}}
			}
			nodes[name] = Router{RouterType: Ensemble, Steps: steps}
		}
		nodes[fmt.Sprintf("n%d", maxGraphNodes-1)] = Router{RouterType: Sequence, Steps: []Step{{StepName: "Llm"}}}

		errs := validateFanout(nodes, fldPath)
		if len(errs) == 0 {
			t.Fatal("a graph of nested Ensemble nodes must be rejected")
		}
		t.Logf("rejected: %v", errs[0].Detail)
	})

	t.Run("a cycle does not make the walk recurse forever", func(t *testing.T) {
		// Cycles are reported by validateNoCycles; this must simply terminate.
		nodes := map[string]Router{
			"root": {RouterType: Ensemble, Steps: []Step{{StepName: "Llm", Executor: Executor{NodeName: "b"}}}},
			"b":    {RouterType: Ensemble, Steps: []Step{{StepName: "Llm", Executor: Executor{NodeName: "root"}}}},
		}
		done := make(chan struct{})
		go func() { validateFanout(nodes, fldPath); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("validateFanout did not terminate on a cyclic graph")
		}
	})
}
