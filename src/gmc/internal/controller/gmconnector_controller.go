/*
* Copyright (C) 2024-2026 Intel Corporation
* SPDX-License-Identifier: Apache-2.0
 */

package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"

	mcv1alpha3 "erag.intel.com/gmc/api/v1alpha3"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// restartHashAnnotation carries a fingerprint of the ConfigMap and Service
// content a workload depends on. Its value only changes when that content
// changes, so the pod template (and thus the pods) stay stable across
// reconciles that make no real change.
const restartHashAnnotation = "gmc.erag.intel.com/config-hash"

const (
	Embedding                = "Embedding"
	Retriever                = "Retriever"
	PromptTemplate           = "PromptTemplate"
	Reranking                = "Reranking"
	Llm                      = "Llm"
	Router                   = "router"
	yaml_dir                 = "/tmp/microservices/yamls/"
	Service                  = "Service"
	Deployment               = "Deployment"
	StatefulSet              = "StatefulSet"
	ServiceAccount           = "ServiceAccount"
	dplymtSubfix             = "-deployment"
	DefaultRouterServiceName = "router-service"
	LLMGuardInput            = "LLMGuardInput"
	LLMGuardOutput           = "LLMGuardOutput"
	LanguageDetection        = "LanguageDetection"
	TextExtractor            = "TextExtractor"
	TextCompression          = "TextCompression"
	TextSplitter             = "TextSplitter"
	DocSum                   = "DocSum"
	QueryRewrite             = "QueryRewrite"
	monitoringCoreOSGroup    = "monitoring.coreos.com"
)

// sanitizeK8sName converts a string to a valid Kubernetes name by replacing
// dots with dashes and truncating to fit within Kubernetes limits.
// Kubernetes names must not contain dots, and labels are limited to 63 characters.
// We truncate to 52 chars to leave room for StatefulSet suffixes like "-0" and hashes like "-5f568f9f79".
func sanitizeK8sName(name string) string {
	sanitized := strings.ReplaceAll(name, ".", "-")
	// Truncate to 52 chars to leave room for pod suffixes (e.g., "-0" adds 2, hash adds up to 11)
	if len(sanitized) > 52 {
		sanitized = sanitized[:52]
		// Ensure we don't end with a dash
		sanitized = strings.TrimRight(sanitized, "-")
	}
	return sanitized
}

// truncateK8sName truncates a full resource name to fit Kubernetes limits.
// Used for StatefulSet names where the full name (prefix + node) must be truncated.
func truncateK8sName(name string) string {
	// Truncate to 52 chars to leave room for pod ordinal and controller hash
	if len(name) > 52 {
		name = name[:52]
		// Ensure we don't end with a dash
		name = strings.TrimRight(name, "-")
	}
	return name
}

var yamlDict = map[string]string{
	Embedding:         yaml_dir + "embedding-usvc.yaml",
	Retriever:         yaml_dir + "retriever-usvc.yaml",
	Reranking:         yaml_dir + "reranking-usvc.yaml",
	PromptTemplate:    yaml_dir + "prompt-template-usvc.yaml",
	Llm:               yaml_dir + "llm-usvc.yaml",
	Router:            yaml_dir + "gmc-router.yaml",
	LLMGuardInput:     yaml_dir + "in-guard-usvc.yaml",
	LLMGuardOutput:    yaml_dir + "out-guard-usvc.yaml",
	LanguageDetection: yaml_dir + "langdtct-usvc.yaml",
	TextExtractor:     yaml_dir + "text-extractor-usvc.yaml",
	TextCompression:   yaml_dir + "text-compression-usvc.yaml",
	TextSplitter:      yaml_dir + "text-splitter-usvc.yaml",
	DocSum:            yaml_dir + "docsum-usvc.yaml",
	QueryRewrite:      yaml_dir + "query-rewrite-usvc.yaml",
}

var (
	_log = ctrl.Log.WithName("GMC")

	GMCConfigMapName = func() string {
		gmcName := os.Getenv("GMC_CONFIGMAP_NAME")
		if gmcName == "" {
			gmcName = "gmc-config"
			_log.Info("GMC_CONFIGMAP_NAME environment variable is not set. Defaulting to " + gmcName)
		}
		return gmcName
	}()
)

// GMConnectorReconciler reconciles a GMConnector object
type GMConnectorReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Recorder surfaces reconcile problems as Events on the GMConnector: a step
	// whose manifest is missing, a monitor skipped for a missing CRD, and
	// ensure-keys exhaustion. Optional: a nil Recorder skips the event, so tests
	// that build a reconciler without one keep working.
	Recorder record.EventRecorder

	// ensureKeysFailures tracks the current ensure-keys failure spell per
	// pipeline, keyed by namespace/name. Reconciles are serial today, but the
	// map outlives a single pass and a mutex keeps it correct if
	// MaxConcurrentReconciles is ever raised.
	ensureKeysMu       sync.Mutex
	ensureKeysFailures map[string]*ensureKeysFailure
}

// errManifestNotFound is returned when a step's manifest cannot be resolved or
// read. The reconcile treats it as non-fatal: the step is skipped and an Event
// is emitted, so one missing manifest does not abort the whole pipeline.
var errManifestNotFound = errors.New("manifest not found")

// recordEvent emits a Kubernetes Event on the graph when a recorder is wired.
// The recorder is nil in some unit tests, so guard before using it.
func (r *GMConnectorReconciler) recordEvent(graph *mcv1alpha3.GMConnector, eventType, reason, message string) {
	if r.Recorder != nil {
		r.Recorder.Event(graph, eventType, reason, message)
	}
}

type RouterCfg struct {
	Namespace   string
	SvcName     string
	DplymntName string
	NoProxy     string
	HttpProxy   string
	HttpsProxy  string
	GRAPH_JSON  string
}

// documentSeparator matches a YAML document boundary: a "---" alone on its line,
// optionally followed by a trailing comment. Splitting on a bare "---" instead
// would cut a document wherever three hyphens appear inside a value, such as a
// serviceURL, leaving a fragment that no longer parses.
var documentSeparator = regexp.MustCompile(`(?m)^---[ \t]*(?:#.*)?$`)

// splitYAMLDocuments splits a multi-document manifest on its document
// boundaries.
func splitYAMLDocuments(manifest string) []string {
	return documentSeparator.Split(manifest, -1)
}

func lookupManifestDir(step string) string {
	value, exist := yamlDict[step]
	if exist {
		return value
	} else {
		return ""
	}
}

// sortedKeys returns the map's keys in a stable order. The rendered ENVs end up
// in the pod template, and Kubernetes hashes the template to derive
// pod-template-hash: iterating a map directly would reorder the ENVs on some
// reconciles and roll every pod of the pipeline for no reason at all.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func setEnvVars(containers []corev1.Container, envVars []corev1.EnvVar) []corev1.Container {
	for i := range containers {
		containers[i].Env = append(containers[i].Env, envVars...)
	}
	return containers
}

func (r *GMConnectorReconciler) getLLMModelNameFromVLLMConfigMap(ctx context.Context, graphNs string) string {
	for _, configMapName := range []string{"vllm-config"} {
		configMap := &corev1.ConfigMap{}
		err := r.Client.Get(ctx, client.ObjectKey{Namespace: graphNs, Name: configMapName}, configMap)
		if err != nil {
			if apierr.IsNotFound(err) {
				continue
			}
			_log.Error(err, "Failed to get ConfigMap", "namespace", graphNs, "name", configMapName)
			return ""
		}

		if modelName, exists := configMap.Data["LLM_VLLM_MODEL_NAME"]; exists {
			return modelName
		} else {
			_log.Error(err, "LLM_VLLM_MODEL_NAME not found in ConfigMap", "name", configMapName)
		}
	}

	_log.Info("No VLLM ConfigMap found")
	return ""
}

func (r *GMConnectorReconciler) reconcileResource(ctx context.Context, graphNs string, stepCfg *mcv1alpha3.Step, nodeCfg *mcv1alpha3.Router, graph *mcv1alpha3.GMConnector) ([]*unstructured.Unstructured, error) {
	if stepCfg == nil || nodeCfg == nil {
		return nil, errors.New("invalid svc config")
	}

	_log.V(1).Info("processing step", "config", *stepCfg)

	var retObjs []*unstructured.Unstructured
	// Every object is applied with an owner reference to the GMConnector, and
	// Kubernetes forbids those across namespaces, so a step always lands in the
	// graph's own namespace.
	ns := graphNs
	svc := stepCfg.InternalService.ServiceName
	svcCfg := &stepCfg.InternalService.Config

	yamlFile, err := r.getTemplateBytes(ctx, stepCfg.StepName)
	if err != nil {
		// A missing manifest is handled non-fatally by the caller, so log it at
		// Info; reserve Error for genuine template read failures.
		if errors.Is(err, errManifestNotFound) {
			_log.Info("No manifest for step", "step", stepCfg.StepName, "reason", err.Error())
		} else {
			_log.Error(err, "Failed to get template bytes for", "step", stepCfg.StepName)
		}
		return nil, err
	}

	resources := splitYAMLDocuments(string(yamlFile))
	// Hash the ConfigMap and Service sections of this step's manifest so a
	// workload is restarted only when the config it depends on actually
	// changes, instead of on every reconcile.
	configHash := hashConfigResources(resources)
	for _, res := range resources {
		if res == "" || !strings.Contains(res, "kind:") {
			continue
		}

		decUnstructured := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
		obj := &unstructured.Unstructured{}
		_, _, err := decUnstructured.Decode([]byte(res), nil, obj)
		if err != nil {
			_log.Error(err, "Failed to decode YAML")
			return nil, err
		}

		// Set the namespace according to user defined value
		if ns != "" {
			obj.SetNamespace(ns)
		}

		// set the service name according to user defined value, and related selectors/labels
		if obj.GetKind() == Service && svc != "" {
			service_obj := &corev1.Service{}
			err = scheme.Scheme.Convert(obj, service_obj, nil)
			if err != nil {
				_log.Error(err, "Failed to convert unstructured to service", "name", svc)
				return nil, err
			}
			service_obj.SetName(svc)
			service_obj.Spec.Selector["app"] = svc
			err = scheme.Scheme.Convert(service_obj, obj, nil)
			if err != nil {
				_log.Error(err, "Failed to convert service to object", "name", svc)
				return nil, err
			}
		} else if obj.GetKind() == ServiceAccount {
			svc_account_obj := &corev1.ServiceAccount{}
			err = scheme.Scheme.Convert(obj, svc_account_obj, nil)
			if err != nil {
				_log.Error(err, "Failed to convert unstructured to service account", "name", svc)
				return nil, err
			}
			err = scheme.Scheme.Convert(svc_account_obj, obj, nil)
			if err != nil {
				_log.Error(err, "Failed to convert service account to object", "name", svc)
				return nil, err
			}
		} else if obj.GetKind() == Deployment {
			deploymentObj := &appsv1.Deployment{}
			err = scheme.Scheme.Convert(obj, deploymentObj, nil)
			if err != nil {
				_log.Error(err, "Failed to convert unstructured to deployment", "name", obj.GetName())
				return nil, err
			}
			if svc != "" {
				// Check for per-node deployment labels
				embeddingNode, hasEmbeddingNode := deploymentObj.Spec.Template.Labels["embedding-node"]
				rerankingNode, hasRerankingNode := deploymentObj.Spec.Template.Labels["reranking-node"]

				if hasEmbeddingNode && embeddingNode != "" {
					deploymentObj.SetName(truncateK8sName(svc + "-" + sanitizeK8sName(embeddingNode)))
				} else if hasRerankingNode && rerankingNode != "" {
					deploymentObj.SetName(truncateK8sName(svc + "-" + sanitizeK8sName(rerankingNode)))
				} else {
					deploymentObj.SetName(svc + dplymtSubfix)
				}
				// Set the labels if they're specified
				deploymentObj.Spec.Selector.MatchLabels["app"] = svc
				deploymentObj.Spec.Template.Labels["app"] = svc
			}

			deploymentObj.Spec.Strategy = appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
			}

			// append the user defined ENVs
			var newEnvVars []corev1.EnvVar
			if svcCfg != nil {
				for _, name := range sortedKeys(*svcCfg) {
					value := (*svcCfg)[name]
					if name == "endpoint" || name == "multipartEndpoint" || name == "nodes" || name == "LLM_VLLM_API_KEY" {
						continue
					}
					if name == "LLM_MODEL_SERVER_ENDPOINT" || name == "RERANKING_SERVICE_ENDPOINT" || name == "EMBEDDING_MODEL_SERVER_ENDPOINT" {
						_, err = url.ParseRequestURI(value)
						if err == nil {
							itemEnvVar := corev1.EnvVar{
								Name:  name,
								Value: value,
							}
							newEnvVars = append(newEnvVars, itemEnvVar)
							continue
						}
					}
					var endpoint, ns string
					var fetch bool

					parts := strings.SplitN(value, ":", 2)
					if len(parts) == 2 {
						if parts[0] == "fetch_from" {
							fetch = true
							value = parts[1]
						} else {
							return nil, fmt.Errorf("Invalid syntax: expected 'fetch_from:service/namespace', got '%s'", value)
						}
					}

					parts = strings.Split(value, "/")
					if len(parts) != 1 && len(parts) != 2 {
						return nil, fmt.Errorf("Invalid syntax: expected 'service/namespace', got '%s'", value)
					}

					if len(parts) == 2 {
						endpoint = parts[0]
						ns = parts[1]
					} else {
						endpoint = value
						ns = graphNs
					}

					var err error
					if fetch {
						value, err = r.fetchEnvVarFromService(ctx, ns, endpoint, name)
						if err != nil {
							return nil, fmt.Errorf("failed to fetch environment variable %s from service %s in namespace %s: %w", name, endpoint, ns, err)
						}
					} else if isDownStreamEndpointKey(name) {
						if ns == graphNs {
							ds := findDownStreamService(endpoint, stepCfg, nodeCfg)
							value, err = getDownstreamSvcEndpoint(ns, endpoint, ds)
						} else {
							value, err = r.getDownstreamSvcEndpointInNs(ctx, ns, endpoint)
						}

						if err != nil {
							return nil, fmt.Errorf("failed to find downstream service endpoint %s-%s: %w", name, endpoint, err)
						}
					}
					itemEnvVar := corev1.EnvVar{
						Name:  name,
						Value: value,
					}
					newEnvVars = append(newEnvVars, itemEnvVar)
				}
			}

			if stepCfg.StepName == Llm {
				llmModelName := r.getLLMModelNameFromVLLMConfigMap(ctx, graphNs)
				if llmModelName != "" {
					newEnvVars = append(newEnvVars, corev1.EnvVar{
						Name:  "LLM_MODEL_NAME",
						Value: llmModelName,
					})
					_log.Info("[DEBUG] LLM_MODEL_NAME set from graph configuration", "LLM_MODEL_NAME", llmModelName)
				}
			}

			if len(newEnvVars) > 0 {
				deploymentObj.Spec.Template.Spec.Containers = setEnvVars(deploymentObj.Spec.Template.Spec.Containers, newEnvVars)
				deploymentObj.Spec.Template.Spec.InitContainers = setEnvVars(deploymentObj.Spec.Template.Spec.InitContainers, newEnvVars)
			}

			// Stamp the config hash on the pod template so the workload rolls
			// only when the ConfigMap/Service content it depends on changed.
			if configHash != "" {
				if deploymentObj.Spec.Template.Annotations == nil {
					deploymentObj.Spec.Template.Annotations = make(map[string]string)
				}
				deploymentObj.Spec.Template.Annotations[restartHashAnnotation] = configHash
			}

			err = scheme.Scheme.Convert(deploymentObj, obj, nil)
			if err != nil {
				_log.Error(err, "Failed to convert deployment to obj", "name", deploymentObj.GetName())
				return nil, err
			}
		} else if obj.GetKind() == StatefulSet {
			deploymentObj := &appsv1.StatefulSet{}
			err = scheme.Scheme.Convert(obj, deploymentObj, nil)
			if err != nil {
				_log.Error(err, "Failed to convert unstructured to statefulset", "name", obj.GetName())
				return nil, err
			}
			if svc != "" {
				vllmNode, hasVllmNode := deploymentObj.Spec.Template.Labels["vllm-node"]
				if hasVllmNode && vllmNode != "" {
					deploymentObj.SetName(truncateK8sName(svc + "-" + sanitizeK8sName(vllmNode)))
				} else {
					deploymentObj.SetName(svc + dplymtSubfix)
				}
				// Set the labels if they're specified
				deploymentObj.Spec.Selector.MatchLabels["app"] = svc
				deploymentObj.Spec.Template.Labels["app"] = svc
			}

			deploymentObj.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
			}

			// append the user defined ENVs
			var newEnvVars []corev1.EnvVar
			if svcCfg != nil {
				for _, name := range sortedKeys(*svcCfg) {
					value := (*svcCfg)[name]
					if name == "endpoint" || name == "multipartEndpoint" || name == "nodes" {
						continue
					}
					if name == "LLM_MODEL_SERVER_ENDPOINT" || name == "RERANKING_SERVICE_ENDPOINT" || name == "EMBEDDING_MODEL_SERVER_ENDPOINT" {
						_, err = url.ParseRequestURI(value)
						if err == nil {
							itemEnvVar := corev1.EnvVar{
								Name:  name,
								Value: value,
							}
							newEnvVars = append(newEnvVars, itemEnvVar)
							continue
						}
					}

					var endpoint, ns string
					var fetch bool

					parts := strings.SplitN(value, ":", 2)
					if len(parts) == 2 {
						if parts[0] == "fetch_from" {
							fetch = true
							value = parts[1]
						} else {
							return nil, fmt.Errorf("Invalid syntax: expected 'fetch_from:service/namespace', got '%s'", value)
						}
					}

					parts = strings.Split(value, "/")
					if len(parts) != 1 && len(parts) != 2 {
						return nil, fmt.Errorf("Invalid syntax: expected 'service/namespace', got '%s'", value)
					}

					if len(parts) == 2 {
						endpoint = parts[0]
						ns = parts[1]
					} else {
						endpoint = value
						ns = graphNs
					}

					var err error
					if fetch {
						value, err = r.fetchEnvVarFromServiceStatefulSet(ctx, ns, endpoint, name)
						if err != nil {
							return nil, fmt.Errorf("failed to fetch environment variable %s from service %s in namespace %s: %w", name, endpoint, ns, err)
						}
					} else if isDownStreamEndpointKey(name) {
						if ns == graphNs {
							ds := findDownStreamService(endpoint, stepCfg, nodeCfg)
							value, err = getDownstreamSvcEndpoint(ns, endpoint, ds)
						} else {
							value, err = r.getDownstreamSvcEndpointInNs(ctx, ns, endpoint)
						}

						if err != nil {
							return nil, fmt.Errorf("failed to find downstream service endpoint %s-%s: %w", name, endpoint, err)
						}
					}
					itemEnvVar := corev1.EnvVar{
						Name:  name,
						Value: value,
					}
					newEnvVars = append(newEnvVars, itemEnvVar)
				}
			}

			if len(newEnvVars) > 0 {
				deploymentObj.Spec.Template.Spec.Containers = setEnvVars(deploymentObj.Spec.Template.Spec.Containers, newEnvVars)
				deploymentObj.Spec.Template.Spec.InitContainers = setEnvVars(deploymentObj.Spec.Template.Spec.InitContainers, newEnvVars)
			}

			// Stamp the config hash on the pod template so the workload rolls
			// only when the ConfigMap/Service content it depends on changed.
			if configHash != "" {
				if deploymentObj.Spec.Template.Annotations == nil {
					deploymentObj.Spec.Template.Annotations = make(map[string]string)
				}
				deploymentObj.Spec.Template.Annotations[restartHashAnnotation] = configHash
			}

			err = scheme.Scheme.Convert(deploymentObj, obj, nil)
			if err != nil {
				_log.Error(err, "Failed to convert statefulset to obj", "name", deploymentObj.GetName())
				return nil, err
			}
		}

		objectChanged, err := r.applyResourceToK8s(graph, ctx, obj)
		if err != nil {
			_log.Error(err, "Failed to reconcile resource", "name", obj.GetName())
			return nil, err
		} else {
			_log.Info("Success to reconcile resource", "kind", obj.GetKind(), "name", obj.GetName(), "changed", objectChanged)
			retObjs = append(retObjs, obj)
		}
	}
	return retObjs, nil
}

// hashConfigResources returns a hex SHA-256 over the ConfigMap and Service
// sections of a rendered manifest. The workload's pod template carries this
// value as an annotation so it rolls only when the config it depends on
// changes. It returns an empty string when the manifest has no such sections.
func hashConfigResources(resources []string) string {
	h := sha256.New()
	found := false
	for _, res := range resources {
		if res == "" || !strings.Contains(res, "kind:") {
			continue
		}
		// Decode each document and match on the exact kind so "Service" does
		// not also catch "ServiceAccount".
		obj := &unstructured.Unstructured{}
		decoder := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
		if _, _, err := decoder.Decode([]byte(res), nil, obj); err != nil {
			continue
		}
		if obj.GetKind() == "ConfigMap" || obj.GetKind() == Service {
			h.Write([]byte(res))
			found = true
		}
	}
	if !found {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func isDownStreamEndpointKey(keyname string) bool {
	return keyname == "VLLM_EMBEDDING_ENDPOINT" ||
		keyname == "RERANKING_SERVICE_ENDPOINT" ||
		keyname == "VLLM_ENDPOINT" ||
		keyname == "LLM_MODEL_SERVER_ENDPOINT" ||
		keyname == "EMBEDDING_MODEL_SERVER_ENDPOINT" ||
		keyname == "DOCSUM_LLM_USVC_ENDPOINT" ||
		keyname == "ASR_ENDPOINT" ||
		keyname == "TTS_ENDPOINT" ||
		keyname == "NER_ENDPOINT" ||
		keyname == "QUERY_REWRITE_LLM_ENDPOINT"
}

func findDownStreamService(dsName string, stepCfg *mcv1alpha3.Step, nodeCfg *mcv1alpha3.Router) *mcv1alpha3.Step {
	if stepCfg == nil || nodeCfg == nil {
		return nil
	}
	_log.Info("Find downstream service for step", "name", stepCfg.StepName, "downstream", dsName)

	for _, otherStep := range nodeCfg.Steps {
		if otherStep.InternalService.ServiceName == dsName && otherStep.InternalService.IsDownstreamService {
			return &otherStep
		}
	}
	return nil
}

func (r *GMConnectorReconciler) getDownstreamSvcEndpointInNs(ctx context.Context, namespace string, dsName string) (string, error) {
	_log.Info("Find downstream service in namespace", "namespace", namespace, "downstream", dsName)

	svc := &corev1.Service{}
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: dsName}, svc)
	if err != nil {
		return "", fmt.Errorf("failed to get service %s in namespace %s: %w", dsName, namespace, err)
	}

	_log.Info("Found downstream service in provided namespace", "name", dsName, "namespace", namespace)

	port := svc.Spec.Ports[0].Port
	return fmt.Sprintf("http://%s.%s.svc:%d", svc.Name, svc.Namespace, port), nil
}

func (r *GMConnectorReconciler) fetchEnvVarFromService(ctx context.Context, namespace string, serviceName string, envVarName string) (string, error) {
	_log.Info("Fetch environment variable from service", "namespace", namespace, "service", serviceName, "envVar", envVarName)

	// Query the Kubernetes API for the specific deployment in the specified namespace
	deployment := &appsv1.Deployment{}
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: serviceName}, deployment)
	if err != nil {
		return "", fmt.Errorf("failed to get deployment %s in namespace %s: %w", serviceName, namespace, err)
	}

	_log.Info("Found deployment in provided namespace", "name", serviceName, "namespace", namespace)

	// Iterate through the containers in the deployment to find the environment variable
	for _, container := range deployment.Spec.Template.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == envVarName {
				return env.Value, nil
			}
		}
	}

	return "", fmt.Errorf("environment variable %s not found in deployment %s in namespace %s", envVarName, serviceName, namespace)
}

func (r *GMConnectorReconciler) fetchEnvVarFromServiceStatefulSet(ctx context.Context, namespace string, serviceName string, envVarName string) (string, error) {
	_log.Info("Fetch environment variable from service", "namespace", namespace, "service", serviceName, "envVar", envVarName)

	// Query the Kubernetes API for the specific deployment in the specified namespace
	deployment := &appsv1.StatefulSet{}
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: serviceName}, deployment)
	if err != nil {
		return "", fmt.Errorf("failed to get deployment %s in namespace %s: %w", serviceName, namespace, err)
	}

	_log.Info("Found deployment in provided namespace", "name", serviceName, "namespace", namespace)

	// Iterate through the containers in the deployment to find the environment variable
	for _, container := range deployment.Spec.Template.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == envVarName {
				return env.Value, nil
			}
		}
	}

	return "", fmt.Errorf("environment variable %s not found in deployment %s in namespace %s", envVarName, serviceName, namespace)
}

func getDownstreamSvcEndpoint(graphNs string, dsName string, stepCfg *mcv1alpha3.Step) (string, error) {
	if stepCfg == nil {
		return "", errors.New(fmt.Sprintf("empty stepCfg for %s", dsName))
	}
	tmplt := lookupManifestDir(stepCfg.StepName)
	if tmplt == "" {
		return "", errors.New(fmt.Sprintf("failed to find yaml file for %s", dsName))
	}

	svcName, port, err := getServiceDetailsFromManifests(tmplt)
	if err == nil {
		// A step may rename its service; it is always in the graph's namespace.
		if altSvcName := getSvcNameFromStep(stepCfg); altSvcName != "" {
			svcName = altSvcName
		}

		return fmt.Sprintf("http://%s.%s.svc:%d", svcName, graphNs, port), nil
	} else {
		return "", errors.New(fmt.Sprintf("failed to get service details for %s: %v\n", dsName, err))
	}
}

func getServiceURL(service *corev1.Service) string {
	switch service.Spec.Type {
	case corev1.ServiceTypeClusterIP:
		// For ClusterIP, return the cluster IP and port
		if len(service.Spec.Ports) > 0 {
			return fmt.Sprintf("http://%s.%s.svc:%d", service.Name, service.Namespace, service.Spec.Ports[0].Port)
		}
	case corev1.ServiceTypeNodePort:
		// For NodePort, return the node IP and node port. You need to replace <node-ip> with the actual node IP.
		if len(service.Spec.Ports) > 0 {
			return fmt.Sprintf("<node-ip>:%d", service.Spec.Ports[0].NodePort)
		}
	case corev1.ServiceTypeLoadBalancer:
		// For LoadBalancer, return the load balancer IP and port
		if len(service.Spec.Ports) > 0 && len(service.Status.LoadBalancer.Ingress) > 0 {
			return fmt.Sprintf("%s:%d", service.Status.LoadBalancer.Ingress[0].IP, service.Spec.Ports[0].Port)
		}
	case corev1.ServiceTypeExternalName:
		// For ExternalName, return the external name
		return service.Spec.ExternalName
	}
	return ""
}

// +kubebuilder:rbac:groups=gmc.erag.intel.com,resources=gmconnectors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gmc.erag.intel.com,resources=gmconnectors/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gmc.erag.intel.com,resources=gmconnectors/finalizers,verbs=update
// +kubebuilder:rbac:groups=gmc.erag.intel.com,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gmc.erag.intel.com,resources=deployments/status,verbs=get
// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// the GMConnector object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.3/pkg/reconcile
func (r *GMConnectorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// _ = log.FromContext(ctx)
	_log.Info("----RECONCILE REQUEST----", "req", req)

	// Initialize graphList to an empty list
	graphList := []*mcv1alpha3.GMConnector{}
	var configMap *corev1.ConfigMap

	if strings.HasPrefix(req.Name, GMCConfigMapName) {
		_log.Info("ConfigMap change detected", "name", req.Name)
		// Handle the ConfigMap change
		var err error
		graphList, configMap, err = r.handleConfigMapChange(ctx, req)
		if err != nil {
			return ctrl.Result{}, err
		}
		if len(graphList) == 0 {
			return ctrl.Result{}, nil
		}
	} else {
		graph := &mcv1alpha3.GMConnector{}
		if err := r.Get(ctx, req.NamespacedName, graph); err != nil {
			if apierr.IsNotFound(err) {
				// The pipeline is gone; drop any ensure-keys failure recorded
				// for it so the map does not keep the entry alive.
				r.forgetEnsureKeysFailures(req.Namespace, req.Name)
				// Object not found, could be deployments
				deployment := &appsv1.Deployment{}
				err := r.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, deployment)
				if err != nil {
					_log.Info("Resource not found or deleted, ignore", "name", req.Name, "err", err)
					return ctrl.Result{}, nil
				}
				return r.handleStatusUpdate(ctx, deployment)
			} else {
				return reconcile.Result{}, errors.Wrapf(err, "Failed to get GMConnector %s", req.Name)
			}
		}

		graphList = append(graphList, graph)
	}

	// print if configMap is empty
	if configMap == nil {
		_log.Info("ConfigMap is empty")
	} else {
		_log.Info("ConfigMap is not empty")
	}

	// Track whether any graph's parameter groups could not be seeded so the
	// reconcile can requeue itself and retry once the fingerprint service is
	// ready, rather than leaving the keys unregistered until the next spec
	// change (which may never come).
	ensureKeysPending := false

	for _, graph := range graphList {
		// in case the type meta is not set, in ut
		// check if typemeta is empty
		if reflect.DeepEqual(graph.TypeMeta, metav1.TypeMeta{}) {
			graph.TypeMeta = metav1.TypeMeta{
				APIVersion: "gmc.erag.intel.com/v1alpha3",
				Kind:       "GMConnector",
			}
		}

		// Register the parameter groups this pipeline declares with the
		// fingerprint service before reconciling the resources, so the router
		// finds their default rows already seeded instead of reading empty
		// keys. A failure here (typically the service not yet ready at startup)
		// must not block the rest of the reconcile, but it does schedule a
		// requeue below so seeding is retried until it succeeds.
		if err := r.ensureParamKeys(ctx, graph); err != nil {
			_log.Error(err, "ensure-keys failed; will retry after backoff",
				"pipeline", graph.Name, "namespace", graph.Namespace)
			ensureKeysPending = true
			r.noteEnsureKeysFailure(graph, err)
		} else {
			r.clearEnsureKeysFailures(graph)
		}

		var totalService uint
		var externalService uint
		var updateExistGraph bool = false
		var oldAnnotations map[string]string

		if len(graph.Status.Annotations) == 0 {
			graph.Status.Annotations = make(map[string]string)
		} else {
			updateExistGraph = true
			//save the old annotations
			oldAnnotations = graph.Status.Annotations
			graph.Status.Annotations = make(map[string]string)
		}

		// Collect per-step errors instead of returning on the first one, so a
		// single failing step does not leave the rest of the pipeline
		// unreconciled. A missing manifest is skipped (with an Event) rather
		// than treated as an error at all.
		var stepErrs []error
		for nodeName, node := range graph.Spec.Nodes {
			for i, step := range node.Steps {
				if step.NodeName != "" {
					_log.Info("This is a nested step", "step", step.StepName)
					continue
				}
				_log.Info("Reconcile step", "graph", graph.Name, "name", step.StepName)
				totalService += 1
				if step.Executor.ExternalService == "" {
					_log.Info("Trying to reconcile internal service", "service", step.Executor.InternalService.ServiceName)

					objs, err := r.reconcileResource(ctx, graph.Namespace, &step, &node, graph)
					if err != nil {
						if errors.Is(err, errManifestNotFound) {
							_log.Info("Skipping step with no manifest", "step", step.StepName, "reason", err.Error())
							r.recordEvent(graph, corev1.EventTypeWarning, "ManifestMissing",
								fmt.Sprintf("Skipped step %s: %v", step.StepName, err))
							continue
						}
						stepErrs = append(stepErrs, errors.Wrapf(err, "Failed to reconcile service for %s", step.StepName))
						continue
					}
					for _, obj := range objs {
						if err := recordResource(graph, nodeName, i, obj); err != nil {
							stepErrs = append(stepErrs, errors.Wrapf(err, "Resource created with failure %s", step.StepName))
						}
					}
				} else {
					_log.Info("External service is found", "name", step.ExternalService)
					graph.Spec.Nodes[nodeName].Steps[i].ServiceURL = step.ExternalService
					externalService += 1
				}
			}
		}
		if len(stepErrs) > 0 {
			return reconcile.Result{Requeue: true}, utilerrors.NewAggregate(stepErrs)
		}

		//to start a router service
		//in case the graph changes, we need to apply the changes to router service
		//so we need to apply the router config every time
		err := r.reconcileRouterService(ctx, graph)
		if err != nil {
			// A missing router manifest cannot be fixed by retrying: the ConfigMap
			// the chart renders has no gmc-router.yaml key. Report it once and
			// stop, rather than requeueing forever.
			if errors.Is(err, errManifestNotFound) {
				msg := fmt.Sprintf("Router not deployed: %v", err)
				_log.Info(msg)
				r.recordEvent(graph, corev1.EventTypeWarning, "RouterManifestMissing", msg)
				return reconcile.Result{}, nil
			}
			return reconcile.Result{Requeue: true}, errors.Wrapf(err, "Failed to reconcile router service")
		}

		if updateExistGraph {
			//check if the old annotations are still in the new graph
			for k := range oldAnnotations {
				if _, ok := graph.Status.Annotations[k]; !ok {
					//if not, remove the resource from k8s
					r.deleteRecordedResource(k, ctx)
				}
			}
		}

		graph.Status.Status = fmt.Sprintf("%d/%d/%d", 0, externalService, 0)
		err = r.collectResourceStatus(graph, ctx)
		if err != nil {
			return reconcile.Result{Requeue: true}, errors.Wrapf(err, "Failed to collect service status")
		}
	}

	// The resources are reconciled; if seeding the fingerprint keys did not
	// succeed, requeue after a backoff so it is retried once the fingerprint
	// service is ready.
	if ensureKeysPending {
		return ctrl.Result{RequeueAfter: ensureKeysRequeueAfter}, nil
	}

	return ctrl.Result{}, nil
}

func (r *GMConnectorReconciler) handleStatusUpdate(ctx context.Context, deployment *appsv1.Deployment) (ctrl.Result, error) {
	for _, owner := range deployment.OwnerReferences {
		if owner.Kind == "GMConnector" {
			// Get the GMConnector object
			graph := &mcv1alpha3.GMConnector{}
			err := r.Get(ctx, types.NamespacedName{Namespace: deployment.Namespace, Name: owner.Name}, graph)
			if err == nil {
				ue := r.collectResourceStatus(graph, ctx)
				if ue != nil {
					_log.Error(ue, "Failed to update graph status", "name", graph.Name)
					return reconcile.Result{}, ue
				}
			}
		}
	}
	return ctrl.Result{}, nil
}

func (r *GMConnectorReconciler) handleConfigMapChange(ctx context.Context, req ctrl.Request) ([]*mcv1alpha3.GMConnector, *corev1.ConfigMap, error) {
	configMap := &corev1.ConfigMap{}
	if err := r.Get(ctx, req.NamespacedName, configMap); err != nil {
		if apierr.IsNotFound(err) {
			_log.Info("ConfigMap not found", "name", req.Name)
			return nil, nil, nil
		}
		return nil, nil, errors.Wrapf(err, "Failed to get ConfigMap %s", req.Name)
	}

	// Print the configmap name and namespace
	_log.Info("ConfigMap change detected", "name", configMap.Name, "namespace", configMap.Namespace)

	// List all GMConnector objects across all namespaces
	gmConnectorList := &mcv1alpha3.GMConnectorList{}
	if err := r.List(ctx, gmConnectorList); err != nil {
		_log.Error(err, "Failed to list GMConnector objects")
		return nil, nil, err
	}

	// Filter GMConnector objects by the namespace of the ConfigMap
	graphList := []*mcv1alpha3.GMConnector{}
	for _, gmConnector := range gmConnectorList.Items {
		// Print every step name for each GMConnector object found
		steps := make([]string, 0)
		for _, node := range gmConnector.Spec.Nodes {
			for _, step := range node.Steps {
				steps = append(steps, step.StepName)
				yamlPath := lookupManifestDir(step.StepName)
				yamlName := strings.TrimPrefix(yamlPath, yaml_dir)
				steps = append(steps, yamlName)

				_, exists := configMap.Data[yamlName]
				if !exists {
					steps = append(steps, "❌")
				} else {
					steps = append(steps, "✅")
				}
			}
		}
		_log.Info(fmt.Sprintf("Namespace: %s, Graph: %s, Steps: %s", gmConnector.Namespace, gmConnector.Name, steps))
		graphList = append(graphList, &gmConnector)
	}

	return graphList, configMap, nil
}

func (r *GMConnectorReconciler) deleteRecordedResource(key string, ctx context.Context) {
	kind := strings.Split(key, ":")[0]
	apiVersion := strings.Split(key, ":")[1]
	name := strings.Split(key, ":")[2]
	ns := strings.Split(key, ":")[3]
	obj := &unstructured.Unstructured{}
	obj.SetKind(kind)
	obj.SetName(name)
	obj.SetNamespace(ns)
	obj.SetAPIVersion(apiVersion)
	err := r.Delete(ctx, obj)
	// the resource may have been deleted by other means, i.e. user manually delete or delete namespace
	// ignore the error if delete failed i.e resource not found
	// since I don't want to block the process for not clearing the finalizer
	if err != nil {
		_log.Info("Failed to delete resource", "namespace", ns, "kind", kind, "name", name, "error", err)
	} else {
		_log.Info("Success to delete resource", "namespace", ns, "kind", kind, "name", name)
	}
}

func (r *GMConnectorReconciler) collectResourceStatus(graph *mcv1alpha3.GMConnector, ctx context.Context) error {
	if graph == nil || len(graph.Status.Annotations) == 0 {
		return errors.New("graph is empty or no annotations")
	}
	var totalCnt uint = 0
	var readyCnt uint = 0
	for resName := range graph.Status.Annotations {
		kind := strings.Split(resName, ":")[0]
		name := strings.Split(resName, ":")[2]
		ns := strings.Split(resName, ":")[3]

		if kind == Deployment || kind == StatefulSet {
			totalCnt += 1

			var deployment client.Object
			if kind == Deployment {
				deployment = &appsv1.Deployment{}
			} else {
				deployment = &appsv1.StatefulSet{}
			}

			err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, deployment)
			if err != nil {
				_log.Info("Collecting status: failed to get object", "name", name, "kind", kind, "error", err)
				continue
			}

			var deploymentStatus strings.Builder
			statusVerbose := "Not ready"

			if kind == Deployment {
				d := deployment.(*appsv1.Deployment)
				if d.Status.AvailableReplicas == *d.Spec.Replicas {
					readyCnt += 1
					statusVerbose = "Ready"
				}
				deploymentStatus.WriteString(fmt.Sprintf("%s; Replicas: %d desired | %d updated | %d total | %d available | %d unavailable\n",
					statusVerbose,
					*d.Spec.Replicas,
					d.Status.UpdatedReplicas,
					d.Status.Replicas,
					d.Status.AvailableReplicas,
					d.Status.UnavailableReplicas))
				deploymentStatus.WriteString("Conditions:\n")
				for _, condition := range d.Status.Conditions {
					deploymentStatus.WriteString(fmt.Sprintf("  Type: %s\n", condition.Type))
					deploymentStatus.WriteString(fmt.Sprintf("  Status: %s\n", condition.Status))
					deploymentStatus.WriteString(fmt.Sprintf("  Reason: %s\n", condition.Reason))
					deploymentStatus.WriteString(fmt.Sprintf("  Message: %s\n", condition.Message))
				}
			} else { // StatefulSet
				s := deployment.(*appsv1.StatefulSet)
				if s.Status.ReadyReplicas == *s.Spec.Replicas {
					readyCnt += 1
					statusVerbose = "Ready"
				}
				deploymentStatus.WriteString(fmt.Sprintf("%s; Replicas: %d desired | %d ready | %d current | %d updated\n",
					statusVerbose,
					*s.Spec.Replicas,
					s.Status.ReadyReplicas,
					s.Status.CurrentReplicas,
					s.Status.UpdatedReplicas))
			}
			graph.Status.Annotations[resName] = deploymentStatus.String()
		}
	}
	externalResourceCntStr := strings.Split(graph.Status.Status, "/")[1]
	externalResourceCnt, err := strconv.Atoi(externalResourceCntStr)
	if err != nil {
		return errors.Wrapf(err, "Error converting externalResourceCnt to int")
	}
	graph.Status.Status = fmt.Sprintf("%d/%d/%d", readyCnt, externalResourceCnt, totalCnt)

	//update the revision in case it has changed
	var latestGraph mcv1alpha3.GMConnector
	err = r.Client.Get(ctx, types.NamespacedName{Namespace: graph.Namespace, Name: graph.Name}, &latestGraph)
	if err != nil && apierr.IsNotFound(err) {
		_log.Info("Failed to get graph before update status", "name", graph.Name, "error", err)
	} else {
		graph.SetResourceVersion(latestGraph.GetResourceVersion())
	}

	if err = r.Status().Update(ctx, graph); err != nil {
		return errors.Wrapf(err, "Failed to Update CR status to %s", graph.Status.Status)
	}

	return nil
}

func recordResource(graph *mcv1alpha3.GMConnector, nodeName string, stepIdx int, obj *unstructured.Unstructured) error {
	// save the resource name into annotation for status update and resource management
	graph.Status.Annotations[fmt.Sprintf("%s:%s:%s:%s", obj.GetKind(), obj.GetAPIVersion(), obj.GetName(), obj.GetNamespace())] = "provisioned"

	if obj.GetKind() == Service {
		service := &corev1.Service{}
		err := scheme.Scheme.Convert(obj, service, nil)
		if err != nil {
			return errors.Wrapf(err, "Failed to convert service %s", obj.GetName())
		}

		if len(graph.Spec.Nodes) != 0 && len(graph.Spec.Nodes[nodeName].Steps) != 0 {
			url := getServiceURL(service) + graph.Spec.Nodes[nodeName].Steps[stepIdx].InternalService.Config["endpoint"]
			//set this for router
			graph.Spec.Nodes[nodeName].Steps[stepIdx].ServiceURL = url
			graph.Status.Annotations[fmt.Sprintf("%s:%s:%s:%s", obj.GetKind(), obj.GetAPIVersion(), obj.GetName(), obj.GetNamespace())] = url
			_log.Info("Service URL is: ", "URL", url)
		} else {
			url := getServiceURL(service)
			graph.Status.Annotations[fmt.Sprintf("%s:%s:%s:%s", obj.GetKind(), obj.GetAPIVersion(), obj.GetName(), obj.GetNamespace())] = url
			graph.Status.AccessURL = url
			_log.Info("Router URL is: ", "URL", url)
		}
	}
	return nil
}

func (r *GMConnectorReconciler) getTemplateBytes(ctx context.Context, resourceType string) ([]byte, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	namespace := gmcNs
	var err error

	if namespace == "" {
		_log.Info("Warning: NAMESPACE environment variable is not set, trying to use kubeconfig namespace for the service")

		namespace, _, err = kubeConfig.Namespace()
		if err != nil {
			_log.Error(err, "Failed to get namespace from kubeconfig")
			return nil, err
		}
	}

	configMap := &corev1.ConfigMap{}
	err = r.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: GMCConfigMapName}, configMap)

	if err != nil {
		_log.Error(err, "Failed to get ConfigMap", "namespace", namespace, "name", GMCConfigMapName)
		return nil, err
	}

	tmpltFile := lookupManifestDir(resourceType)
	if tmpltFile == "" {
		return nil, errors.Wrapf(errManifestNotFound, "no manifest mapped for step %q", resourceType)
	}

	if configMap != nil {
		_log.Info("[DEBUG] Configmap found", "configMapName", GMCConfigMapName, "namespace", namespace)
		yamlName := strings.TrimPrefix(tmpltFile, yaml_dir)
		if yamlContent, exists := configMap.Data[yamlName]; exists {
			_log.Info("[DEBUG] Using yaml from configmap", "configMapName", GMCConfigMapName, "namespace", namespace, "yaml", yamlContent)
			return []byte(yamlContent), nil
		}
	}

	yamlBytes, err := os.ReadFile(tmpltFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.Wrapf(errManifestNotFound, "manifest %q for step %q", tmpltFile, resourceType)
		}
		return nil, err
	}
	_log.Info("[DEBUG] Using yaml from file", "file", tmpltFile)
	return yamlBytes, nil
}

func (r *GMConnectorReconciler) reconcileRouterService(ctx context.Context, graph *mcv1alpha3.GMConnector) error {
	configForRouter := make(map[string]string)

	var routerNs string
	var routerServiceName string
	var routerDeploymentName string
	var graphJson mcv1alpha3.GMConnector
	graphJson.Spec = *graph.Spec.DeepCopy()

	jsonBytes, err := json.Marshal(graphJson)
	if err != nil {
		// handle error
		return errors.Wrapf(err, "Failed to Marshal routes for %s", graph.Spec.RouterConfig.Name)
	}
	jsonString := string(jsonBytes)
	// The value is emitted into a single-quoted YAML scalar, where a quote is
	// escaped by doubling it. A backslash is not an escape there, so "\\'" would
	// end the scalar and let graph text break out into document structure.
	escapedString := strings.ReplaceAll(jsonString, "'", "''")
	configForRouter["nodes"] = "'" + escapedString + "'"

	// The router carries an owner reference to the GMConnector like every other
	// applied object, so it lives in the graph's namespace.
	routerNs = graph.Namespace
	configForRouter["namespace"] = routerNs

	if graph.Spec.RouterConfig.ServiceName != "" {
		routerServiceName = graph.Spec.RouterConfig.ServiceName
		routerDeploymentName = graph.Spec.RouterConfig.ServiceName + dplymtSubfix
	} else {
		routerServiceName = DefaultRouterServiceName
		routerDeploymentName = DefaultRouterServiceName + dplymtSubfix
	}
	configForRouter["svcName"] = routerServiceName
	configForRouter["dplymntName"] = routerDeploymentName

	templateBytes, err := r.getTemplateBytes(ctx, Router)
	if err != nil {
		return errors.Wrapf(err, "Failed to get template bytes for %s", Router)
	}
	var resources []string
	appliedCfg, err := applyRouterConfigToTemplates(Router, &configForRouter, templateBytes)
	if err != nil {
		_log.Error(err, "Failed to apply user config")
		return err
	}

	resources = splitYAMLDocuments(appliedCfg)
	for _, res := range resources {
		if res == "" || !strings.Contains(res, "kind:") {
			continue
		}
		decUnstructured := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
		obj := &unstructured.Unstructured{}
		_, _, err := decUnstructured.Decode([]byte(res), nil, obj)
		if err != nil {
			_log.Error(err, "Failed to decode YAML")
			return err
		}

		// Place every router document in the router namespace, the same way the
		// step path does for a step manifest. Most documents in the template
		// already set it, but a document that omits it (or names another
		// namespace) would otherwise be rejected by SetControllerReference and
		// requeue the whole pipeline.
		obj.SetNamespace(routerNs)

		objectChanged, err := r.applyResourceToK8s(graph, ctx, obj)
		if err != nil {
			_log.Error(err, "Failed to reconcile resource", "name", obj.GetName())
			return err
		} else {
			_log.Info("Success to reconcile resource", "kind", obj.GetKind(), "name", obj.GetName(), "changed", objectChanged)
		}
		// save the resource name into annotation for status update and resource management
		err = recordResource(graph, "", 0, obj)
		if err != nil {
			_log.Error(err, "Resource created with failure", "name", obj.GetName())
			return err
		}
	}

	return nil
}

func applyRouterConfigToTemplates(step string, svcCfg *map[string]string, yamlFile []byte) (string, error) {
	var userDefinedCfg RouterCfg
	if step == "router" {
		userDefinedCfg = RouterCfg{
			Namespace:   (*svcCfg)["namespace"],
			SvcName:     (*svcCfg)["svcName"],
			DplymntName: (*svcCfg)["dplymntName"],
			NoProxy:     (*svcCfg)["no_proxy"],
			HttpProxy:   (*svcCfg)["http_proxy"],
			HttpsProxy:  (*svcCfg)["https_proxy"],
			GRAPH_JSON:  (*svcCfg)["nodes"]}
		_log.V(1).Info("Apply the config to router", "content", userDefinedCfg)

		tmpl, err := template.New("yamlTemplate").Parse(string(yamlFile))
		if err != nil {
			return string(yamlFile), fmt.Errorf("error parsing template: %v", err)
		}

		var appliedCfg bytes.Buffer
		err = tmpl.Execute(&appliedCfg, userDefinedCfg)
		if err != nil {
			return string(yamlFile), fmt.Errorf("error executing template: %v", err)
		} else {
			_log.V(1).Info("applied config", "content", appliedCfg.String())
			return appliedCfg.String(), nil
		}
	} else {
		return string(yamlFile), nil
	}

}

// skipIfCRDMissing reports whether obj is a prometheus-operator resource
// (monitoring.coreos.com, e.g. ServiceMonitor/PodMonitor) whose CRD is not
// installed. When the RESTMapper cannot resolve the GVK it emits a Warning
// Event, logs, and returns skip=true so the caller drops the object without
// failing the reconcile. Other resolution errors are returned to the caller.
func (r *GMConnectorReconciler) skipIfCRDMissing(graph *mcv1alpha3.GMConnector, obj *unstructured.Unstructured) (bool, error) {
	gvk := obj.GroupVersionKind()
	if gvk.Group != monitoringCoreOSGroup {
		return false, nil
	}
	_, err := r.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	if err == nil {
		return false, nil
	}
	if meta.IsNoMatchError(err) {
		msg := fmt.Sprintf("Skipping %s %q: CRD %s not installed", gvk.Kind, obj.GetName(), gvk.GroupKind())
		_log.Info(msg)
		r.recordEvent(graph, corev1.EventTypeWarning, "MonitoringCRDMissing", msg)
		return true, nil
	}
	return false, fmt.Errorf("failed to resolve %s mapping: %w", gvk.Kind, err)
}

func (r *GMConnectorReconciler) applyResourceToK8s(graph *mcv1alpha3.GMConnector, ctx context.Context, obj *unstructured.Unstructured) (bool, error) {
	// ServiceMonitors and PodMonitors depend on the prometheus-operator CRDs,
	// which this repo does not install (external Prometheus is assumed). Skip
	// applying one when its CRD is absent instead of failing the reconcile, so a
	// missing operator does not hot-requeue the whole pipeline.
	if skip, err := r.skipIfCRDMissing(graph, obj); err != nil {
		return false, err
	} else if skip {
		return false, nil
	}

	// Create the object if it is absent, otherwise update it in place. Updates
	// are retried only on version conflicts via RetryOnConflict, which refetches
	// the latest revision between attempts, so there is no busy-wait poll and no
	// blind re-apply on every reconcile.
	if err := controllerutil.SetControllerReference(graph, obj, r.Scheme); err != nil {
		return false, fmt.Errorf("failed to set controller reference: %w", err)
	}

	latest := &unstructured.Unstructured{}
	latest.SetGroupVersionKind(obj.GroupVersionKind())
	err := r.Client.Get(ctx, client.ObjectKeyFromObject(obj), latest)
	if err != nil {
		if apierr.IsNotFound(err) {
			if err := r.Client.Create(ctx, obj, &client.CreateOptions{}); err != nil {
				return false, fmt.Errorf("failed to create resource: %w", err)
			}
			return true, nil
		}
		return false, fmt.Errorf("failed to get resource: %w", err)
	}

	// PVCs are immutable after creation, so leave an existing one untouched.
	if obj.GetKind() == "PersistentVolumeClaim" {
		_log.Info("Skipping update for PersistentVolumeClaim", "name", obj.GetName())
		return false, nil
	}

	var objectChanged bool
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &unstructured.Unstructured{}
		current.SetGroupVersionKind(obj.GroupVersionKind())
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(obj), current); err != nil {
			return err
		}
		obj.SetResourceVersion(current.GetResourceVersion())
		// Carry over any server-managed labels/annotations the live object grew
		// (e.g. deployment.kubernetes.io/revision) that the rendered manifest
		// does not set, so the update neither strips them nor reports a spurious
		// diff for keys the controller does not own.
		mergeServerManagedMetadata(obj, current)
		// Keep the live replica count when the manifest omits it, so an update
		// does not reset a workload an autoscaler owns back to the API server
		// default of one.
		mergeAutoscaledReplicas(obj, current)
		// Skip the write when the rendered fields already match the live object,
		// so an unchanged step no longer triggers an update on every reconcile.
		if desiredMatchesLive(obj, current) {
			objectChanged = false
			return nil
		}
		if err := r.Client.Update(ctx, obj, &client.UpdateOptions{}); err != nil {
			return err
		}
		objectChanged = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("failed to update resource: %w", err)
	}
	return objectChanged, nil
}

// mergeServerManagedMetadata copies labels and annotations present on the live
// object but absent from the desired one into the desired object, so an
// in-place update keeps server-managed keys (e.g.
// deployment.kubernetes.io/revision) that the rendered manifest never sets.
// Keys the controller does render take precedence.
func mergeServerManagedMetadata(desired, live *unstructured.Unstructured) {
	// Merge into a fresh map rather than the one the live object returned, so the
	// live object keeps reporting its own values.
	merge := func(liveEntries, desiredEntries map[string]string) map[string]string {
		merged := make(map[string]string, len(liveEntries)+len(desiredEntries))
		for k, v := range liveEntries {
			merged[k] = v
		}
		for k, v := range desiredEntries {
			merged[k] = v
		}
		return merged
	}
	if liveLabels := live.GetLabels(); len(liveLabels) > 0 {
		desired.SetLabels(merge(liveLabels, desired.GetLabels()))
	}
	if liveAnnotations := live.GetAnnotations(); len(liveAnnotations) > 0 {
		desired.SetAnnotations(merge(liveAnnotations, desired.GetAnnotations()))
	}
}

// mergeAutoscaledReplicas copies spec.replicas from the live object when the
// desired one does not set it. The router manifest omits replicas while its HPA
// is enabled so the autoscaler owns the count, but an Update that leaves the
// field unset makes the API server apply its default of one, which would undo
// every scaling decision on the next reconcile.
func mergeAutoscaledReplicas(desired, live *unstructured.Unstructured) {
	// Only workloads carry spec.replicas; the function runs on every applied
	// object, so do not invent the field on a kind that has no such concept.
	switch desired.GetKind() {
	case Deployment, StatefulSet:
	default:
		return
	}
	if _, found, err := unstructured.NestedFieldNoCopy(desired.Object, "spec", "replicas"); found || err != nil {
		return
	}
	replicas, found, err := unstructured.NestedFieldNoCopy(live.Object, "spec", "replicas")
	if err != nil || !found {
		return
	}
	if err := unstructured.SetNestedField(desired.Object, replicas, "spec", "replicas"); err != nil {
		_log.Error(err, "Failed to carry over live replica count", "name", desired.GetName())
	}
}

// desiredMatchesLive reports whether the fields the controller renders already
// match the live object. It compares only the parts the reconcile owns —
// labels, annotations, and the content fields (spec, data, stringData, rules,
// subsets, webhooks) — so server-managed metadata (uid, managedFields,
// resourceVersion, creationTimestamp) and status do not force a needless write.
func desiredMatchesLive(desired, live *unstructured.Unstructured) bool {
	if !reflect.DeepEqual(desired.GetLabels(), live.GetLabels()) {
		return false
	}
	if !reflect.DeepEqual(desired.GetAnnotations(), live.GetAnnotations()) {
		return false
	}
	for _, field := range []string{"spec", "data", "stringData", "rules", "subsets", "webhooks"} {
		d, dOK := desired.Object[field]
		l, lOK := live.Object[field]
		if dOK != lOK || !reflect.DeepEqual(d, l) {
			return false
		}
	}
	return true
}

// getSvcNameFromStep returns the service name a step overrides its manifest's
// with, or "" when it does not. An internal step is always reached in the
// graph's own namespace, so only the name is configurable.
func getSvcNameFromStep(step *mcv1alpha3.Step) string {
	if step.Executor.ExternalService != "" {
		return ""
	}
	return step.Executor.InternalService.ServiceName
}

func getServiceDetailsFromManifests(filePath string) (string, int, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", 0, err
	}
	resources := splitYAMLDocuments(string(data))

	for _, res := range resources {
		if res == "" || !strings.Contains(res, "kind: Service") {
			continue
		}
		svc := &corev1.Service{}
		decoder := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
		_, _, err = decoder.Decode([]byte(res), nil, svc)
		if err != nil {
			return "", 0, err
		}
		if svc.Kind == "Service" {
			if len(svc.Spec.Ports) > 0 {
				return svc.Name, int(svc.Spec.Ports[0].Port), nil
			}
		}

	}

	return "", 0, fmt.Errorf("service name or port not found")
}

func isMetadataChanged(oldObject, newObject *metav1.ObjectMeta) bool {
	if oldObject == nil || newObject == nil {
		_log.Info("Metadata changes detected, old/new object is nil")
		return oldObject != newObject
	}
	// only care limited changes
	if oldObject.Name != newObject.Name {
		_log.Info("Metadata.Name changes detected", "old", oldObject.Name, "new", newObject.Name)
		return true
	}
	if oldObject.Namespace != newObject.Namespace {
		_log.Info("Metadata.Namespace changes detected", "old", oldObject.Namespace, "new", newObject.Namespace)
		return true
	}
	if !reflect.DeepEqual(oldObject.Labels, newObject.Labels) {
		_log.Info("Metadata.Labels changes detected", "old", oldObject.Labels, "new", newObject.Labels)
		return true
	}
	if !reflect.DeepEqual(oldObject.DeletionTimestamp, newObject.DeletionTimestamp) {
		_log.Info("Metadata.DeletionTimestamp changes detected", "old", oldObject.DeletionTimestamp, "new", newObject.DeletionTimestamp)
		return true
	}
	// Add more fields as needed
	return false
}

func isGMCSpecOrMetadataChanged(e event.UpdateEvent) bool {
	oldObject, ok1 := e.ObjectOld.(*mcv1alpha3.GMConnector)
	newObject, ok2 := e.ObjectNew.(*mcv1alpha3.GMConnector)
	if !ok1 || !ok2 {
		// Not the correct type, allow the event through
		return true
	}

	specChanged := !reflect.DeepEqual(oldObject.Spec, newObject.Spec)
	metadataChanged := isMetadataChanged(&(oldObject.ObjectMeta), &(newObject.ObjectMeta))

	_log.V(1).Info("Check trigger condition?", "spec changed", specChanged, "meta changed", metadataChanged)
	// Compare the old and new spec, ignore metadata, status changes
	// metadata change: name, namespace, such change should create a new GMC
	// status change: deployment status
	return specChanged || metadataChanged
}

func isDeploymentStatusChanged(e event.UpdateEvent) bool {
	oldDeployment, ok1 := e.ObjectOld.(*appsv1.Deployment)
	newDeployment, ok2 := e.ObjectNew.(*appsv1.Deployment)
	if !ok1 || !ok2 {
		// Not the correct type, allow the event through
		return true
	}

	ownedByGMC := false
	for _, owner := range newDeployment.OwnerReferences {
		if owner.Kind == "GMConnector" {
			_log.V(1).Info("Owner is GMConnector", "ns", newDeployment.Namespace, "name", newDeployment.Name, "owner", owner.Name)
			ownedByGMC = true
			break
		}
	}
	if !ownedByGMC {
		_log.V(1).Info("No GMConnector owner reference", "ns", newDeployment.Namespace, "name", newDeployment.Name)
		return false
	}

	if reflect.DeepEqual(oldDeployment.Status, newDeployment.Status) &&
		reflect.DeepEqual(oldDeployment.Spec.Replicas, newDeployment.Spec.Replicas) {
		return false
	}

	if desiredReplicas(oldDeployment) != desiredReplicas(newDeployment) ||
		oldDeployment.Status.AvailableReplicas != newDeployment.Status.AvailableReplicas ||
		oldDeployment.Status.Replicas != newDeployment.Status.Replicas ||
		oldDeployment.Status.UpdatedReplicas != newDeployment.Status.UpdatedReplicas ||
		oldDeployment.Status.UnavailableReplicas != newDeployment.Status.UnavailableReplicas {
		_log.Info("replica counts changed", "ns",
			newDeployment.Namespace, "name", newDeployment.Name,
			"desired", desiredReplicas(newDeployment),
			"available", newDeployment.Status.AvailableReplicas)
		return true
	}

	oldStatus := availableCondition(oldDeployment)
	newStatus := availableCondition(newDeployment)
	if oldStatus != newStatus {
		_log.Info("status changed", "ns",
			newDeployment.Namespace, "name", newDeployment.Name,
			"from", oldStatus, "to", newStatus)
		return true
	}
	return false
}

// desiredReplicas resolves Spec.Replicas, which Kubernetes defaults to 1 when unset.
func desiredReplicas(d *appsv1.Deployment) int32 {
	if d.Spec.Replicas == nil {
		return 1
	}
	return *d.Spec.Replicas
}

func availableCondition(d *appsv1.Deployment) corev1.ConditionStatus {
	for _, condition := range d.Status.Conditions {
		if condition.Type == appsv1.DeploymentAvailable {
			return condition.Status
		}
	}
	return corev1.ConditionUnknown
}

func isInWorkingNamespace(namespace string) bool {
	return namespace == workingNs || namespace == gmcNs
}

// SetupWithManager sets up the controller with the Manager.
func (r *GMConnectorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// logging the working namespace
	_log.Info("Setting up controller with manager ", "namespace", workingNs)

	// Predicate to ignore updates to status subresource
	gmcfilter := predicate.Funcs{
		UpdateFunc: isGMCSpecOrMetadataChanged,
		// Other funcs like CreateFunc, DeleteFunc, GenericFunc can be left as default
		// if you only want to customize the UpdateFunc behavior.
	}

	nsFilter := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return isInWorkingNamespace(e.Object.GetNamespace())
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return isInWorkingNamespace(e.ObjectNew.GetNamespace())
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return isInWorkingNamespace(e.Object.GetNamespace())
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return isInWorkingNamespace(e.Object.GetNamespace())
		},
	}

	// Predicate to only trigger on status changes for Deployment
	deploymentFilter := predicate.Funcs{
		UpdateFunc: isDeploymentStatusChanged,
		//ignore create and delete events, otherwise it will trigger the nested reconcile which is meaningless
		CreateFunc: func(e event.CreateEvent) bool {
			return false
		}, DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
	}

	// Predicate to trigger on changes to ConfigMap
	configMapFilter := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			return true
		},
		CreateFunc: func(e event.CreateEvent) bool {
			return true
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return true
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&mcv1alpha3.GMConnector{}, builder.WithPredicates(predicate.And(gmcfilter, nsFilter))).
		Watches(
			&appsv1.Deployment{},
			&handler.EnqueueRequestForObject{},
			builder.WithPredicates(predicate.And(deploymentFilter, nsFilter)),
		).
		Watches(
			&corev1.ConfigMap{},
			&handler.EnqueueRequestForObject{},
			builder.WithPredicates(predicate.And(configMapFilter, nsFilter)),
		).
		Complete(r)
}
