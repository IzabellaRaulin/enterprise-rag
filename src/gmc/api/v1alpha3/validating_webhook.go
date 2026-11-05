/*
* Copyright (C) 2024-2026 Intel Corporation
* SPDX-License-Identifier: Apache-2.0
 */

package v1alpha3

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	// ConfigParamsKey is the internal-service config entry that names the
	// fingerprint parameter group injected into a step.
	ConfigParamsKey = "paramsKey"

	// ConfigParamsKind is the internal-service config entry that categorises
	// the parameter group so the fingerprint store can seed the right defaults.
	ConfigParamsKind = "paramsKind"
)

// paramsKeyPattern is the character set a resolved paramsKey may contain. The
// key becomes the final segment of the KV key the fingerprint bridge projects a
// row under, so it is held to the same segment charset the bridge accepts
// ('A'-'Z', 'a'-'z', '0'-'9', '-', '_', '/', '='). A dot, whitespace or a
// subject wildcard ('*', '>') would make the bridge drop the projection
// silently, so such a key is rejected at admission instead.
var paramsKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_/=-]+$`)

// serviceNamePattern is the charset a Service name may use. Names from a
// GMConnector become metadata.name on the Service the controller renders, and
// the API server holds a Service to a DNS-1035 label, which must start with a
// letter rather than a digit.
var serviceNamePattern = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

// maxDNSLabelLength is the Kubernetes limit for a name in a DNS label.
const maxDNSLabelLength = 63

// derivedNameSuffixLength reserves room for the longest suffix the controller
// appends to a service name it renders: "-deployment" for the workload, and
// "-pdb" on top of that for the router's PodDisruptionBudget.
const derivedNameSuffixLength = len(dplymtSuffix) + len("-pdb")

// dplymtSuffix mirrors the suffix the controller appends to a deployment name.
const dplymtSuffix = "-deployment"

// +kubebuilder:docs-gen:collapse=Go imports

var (
	// set up a logger for the webhooks.
	vlog = logf.Log.WithName("validating-webhook")

	// stepNames lists the step names a GMConnector may use. Every entry is a step
	// the controller can render for the active pipelines (chatqna, docsum,
	// translation); names for backends and modalities those pipelines do not use
	// were dropped so a broken graph is rejected at admission rather than failing
	// at request time.
	//
	// OvmsNer and VLLMQueryRewrite are admitted but have no manifest in
	// manifests_common and no yamlDict entry, so a graph using them reconciles
	// with the step skipped. They stay until those features are migrated or
	// removed.
	//
	// TODO: derive this list from the controller's step-name constants so the two
	// cannot drift. Deferred because api/v1alpha3 cannot import internal/controller
	// without an import cycle; a shared package is the follow-up.
	stepNames = []string{
		"Embedding",
		"Retriever",
		"Reranking",
		"PromptTemplate",
		"Llm",
		"Router",
		"LLMGuardInput",
		"LLMGuardOutput",
		"LanguageDetection",
		"TextExtractor",
		"TextCompression",
		"TextSplitter",
		"DocSum",
		"QueryRewrite",
		"OvmsNer",
		"VLLMQueryRewrite",
	}

	// validRouterTypes lists the routing modes a node may declare.
	validRouterTypes = []RouterType{Sequence, Ensemble, Switch}
)

const (
	// maxGraphNodes caps how many nodes a GMConnector graph may declare, so a
	// huge or malicious CR cannot make admission walk an unbounded graph.
	maxGraphNodes = 10

	// maxStepsPerNode caps how many steps a single node may declare, for the
	// same reason.
	maxStepsPerNode = 16

	// maxGraphFanout caps the concurrent steps one request may reach across the
	// whole graph. Nested Ensemble nodes multiply, so the node and step caps
	// above do not bound it.
	maxGraphFanout = 64
)

// SetupWebhookWithManager will setup the manager to manage the webhooks
func (r *GMConnector) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// +kubebuilder:webhook:verbs=create;update,path=/validate-gmc-erag-intel-com-v1alpha3-gmconnector,mutating=false,failurePolicy=fail,groups=gmc.erag.intel.com,resources=gmconnectors,versions=v1alpha3,name=vgmcconnector.gmc.erag.intel.com,sideEffects=None,admissionReviewVersions=v1

var _ webhook.Validator = &GMConnector{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *GMConnector) ValidateCreate() (admission.Warnings, error) {
	vlog.Info("validate create", "name", r.Name)

	return nil, r.validateGMConnector()
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *GMConnector) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	vlog.Info("validate update", "name", r.Name)

	return nil, r.validateGMConnector()
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *GMConnector) ValidateDelete() (admission.Warnings, error) {
	vlog.Info("validate delete", "name", r.Name)
	return nil, nil
}

/*
validate the name and the spec of the GMConnector.
*/
func (r *GMConnector) validateGMConnector() error {
	if err := r.checkfields(); err != nil {
		return apierrors.NewInvalid(
			schema.GroupKind{Group: GroupVersion.Group, Kind: "GMCConnector"},
			r.Name, err)
	}

	return nil

}

func (r *GMConnector) checkfields() field.ErrorList {
	// The field helpers from the kubernetes API machinery help us return nicely
	// structured validation errors.
	var allErrs field.ErrorList
	nodesPath := field.NewPath("spec").Child("nodes")

	// Reject an oversized graph before walking it, so a huge CR cannot make
	// admission iterate an unbounded number of nodes and steps.
	if errs := validateGraphSize(r.Spec.Nodes, nodesPath); len(errs) > 0 {
		return errs
	}

	allErrs = append(allErrs, validateRouterConfig(r.Spec.RouterConfig, field.NewPath("spec").Child("routerConfig"))...)

	if errs := validateNames(r.Spec.Nodes, nodesPath); len(errs) > 0 {
		allErrs = append(allErrs, errs...)
	}
	if err := validateRootExistance(r.Spec.Nodes, nodesPath); err != nil {
		allErrs = append(allErrs, err)
	}
	allErrs = append(allErrs, validateRouterTypes(r.Spec.Nodes, nodesPath)...)
	allErrs = append(allErrs, validateSwitchDefault(r.Spec.Nodes, nodesPath)...)
	if err := validateNoCycles(r.Spec.Nodes, nodesPath); err != nil {
		allErrs = append(allErrs, err)
	}
	allErrs = append(allErrs, validateReachability(r.Spec.Nodes, nodesPath)...)

	if len(allErrs) == 0 {
		return nil
	}
	return allErrs
}

// validateGraphSize rejects a graph that declares more than maxGraphNodes nodes
// or any node with more than maxStepsPerNode steps.
func validateGraphSize(nodes map[string]Router, fldPath *field.Path) field.ErrorList {
	var errs field.ErrorList
	if len(nodes) > maxGraphNodes {
		errs = append(errs, field.Invalid(fldPath, len(nodes),
			fmt.Sprintf("graph declares %d nodes, which exceeds the limit of %d", len(nodes), maxGraphNodes)))
	}
	for name, router := range nodes {
		if len(router.Steps) > maxStepsPerNode {
			errs = append(errs, field.Invalid(fldPath.Child(name).Child("steps"), len(router.Steps),
				fmt.Sprintf("node %v declares %d steps, which exceeds the limit of %d", name, len(router.Steps), maxStepsPerNode)))
		}
	}
	return append(errs, validateFanout(nodes, fldPath)...)
}

// validateFanout rejects a graph whose Ensemble nodes multiply into more
// concurrent work than the router can carry. Node and step counts bound the
// resource, not the tree: an Ensemble step routing into another Ensemble node
// starts that node's steps in parallel too, so the goroutines one request needs
// is the product of the fan-out along a path, not their sum.
func validateFanout(nodes map[string]Router, fldPath *field.Path) field.ErrorList {
	root, ok := nodes["root"]
	if !ok {
		// A graph with no root is rejected elsewhere; nothing to walk here.
		return nil
	}

	// visiting is the path being walked, so a cycle (reported separately) cannot
	// make this recurse forever.
	visiting := map[string]bool{}
	var walk func(name string, router Router) int
	walk = func(name string, router Router) int {
		if visiting[name] {
			return 1
		}
		visiting[name] = true
		defer delete(visiting, name)

		// A Sequence or Switch runs one step at a time, so only an Ensemble
		// multiplies. Take the widest sub-tree for the others.
		widest := 1
		total := 0
		for _, step := range router.Steps {
			cost := 1
			if sub, ok := nodes[step.NodeName]; step.NodeName != "" && ok {
				cost = walk(step.NodeName, sub)
			}
			total += cost
			if cost > widest {
				widest = cost
			}
			if total > maxGraphFanout {
				return total
			}
		}
		if router.RouterType == Ensemble {
			return total
		}
		return widest
	}

	if fanout := walk("root", root); fanout > maxGraphFanout {
		return field.ErrorList{field.Invalid(fldPath, fanout,
			fmt.Sprintf("graph fans out to %d concurrent steps, which exceeds the limit of %d; nest fewer Ensemble nodes or give them fewer steps", fanout, maxGraphFanout))}
	}
	return nil
}

// validateRouterConfig rejects router names the controller cannot safely render.
// Both names are interpolated into the router manifest as metadata.name, and the
// controller splits the rendered text on "---" before applying each document, so
// a value carrying a newline could introduce a document of its own and have the
// controller apply an arbitrary kind. Holding them to a DNS label keeps them
// valid Kubernetes names and leaves no character that could break out of the
// template.
func validateRouterConfig(cfg RouterConfig, fldPath *field.Path) field.ErrorList {
	if cfg.ServiceName == "" {
		// Empty is allowed: the controller falls back to its default name.
		return nil
	}
	return validateRenderedServiceName(cfg.ServiceName, fldPath.Child("serviceName"))
}

// validateRenderedServiceName rejects a service name the controller cannot
// safely render. The value is interpolated into a manifest as metadata.name and
// as an "app" label, and the controller splits the rendered text on "---" before
// applying each document, so a value carrying a newline could introduce a
// document of its own. Holding it to a Service name leaves no such character and
// keeps every name the controller derives from it valid.
func validateRenderedServiceName(name string, fldPath *field.Path) field.ErrorList {
	if !serviceNamePattern.MatchString(name) {
		return field.ErrorList{field.Invalid(fldPath, name,
			"must be a lowercase RFC 1035 label: a letter, then alphanumerics and '-', ending with an alphanumeric")}
	}
	if max := maxDNSLabelLength - derivedNameSuffixLength; len(name) > max {
		return field.ErrorList{field.Invalid(fldPath, name,
			fmt.Sprintf("must be at most %d characters, since the controller appends suffixes such as %q to it", max, dplymtSuffix))}
	}
	return nil
}

// validateRouterTypes rejects a node whose routerType is not one of the routing
// modes the router understands.
func validateRouterTypes(nodes map[string]Router, fldPath *field.Path) field.ErrorList {
	var errs field.ErrorList
	for name, router := range nodes {
		if !slices.Contains(validRouterTypes, router.RouterType) {
			errs = append(errs, field.Invalid(fldPath.Child(name).Child("routerType"), router.RouterType,
				fmt.Sprintf("invalid routerType %q for node %v; allowed: %v", router.RouterType, name, validRouterTypes)))
		}
	}
	return errs
}

// validateSwitchDefault rejects a Switch node whose every step carries a
// condition. Without a step that has no condition, a request matching none of
// the conditions is routed nowhere, so such a node is guaranteed to drop some
// input at request time.
func validateSwitchDefault(nodes map[string]Router, fldPath *field.Path) field.ErrorList {
	var errs field.ErrorList
	for name, router := range nodes {
		if router.RouterType != Switch || len(router.Steps) == 0 {
			continue
		}
		hasDefault := false
		for _, step := range router.Steps {
			if step.Condition == "" {
				hasDefault = true
				break
			}
		}
		if !hasDefault {
			errs = append(errs, field.Invalid(fldPath.Child(name).Child("steps"), router.Steps,
				fmt.Sprintf("Switch node %v has no default step (one with an empty condition); a request matching no condition would be routed nowhere", name)))
		}
	}
	return errs
}

// validateNoCycles rejects a graph whose node-to-node edges form a cycle, which
// would make the router recurse without end at request time. Edges follow each
// step's nodeName to the node it targets.
func validateNoCycles(nodes map[string]Router, fldPath *field.Path) *field.Error {
	const (
		unvisited = 0
		onStack   = 1
		done      = 2
	)
	state := make(map[string]int, len(nodes))

	var visit func(name string) bool
	visit = func(name string) bool {
		state[name] = onStack
		for _, step := range nodes[name].Steps {
			next := step.NodeName
			if next == "" {
				continue
			}
			if _, ok := nodes[next]; !ok {
				// A dangling nodeName is reported by validateNames.
				continue
			}
			switch state[next] {
			case onStack:
				return true
			case unvisited:
				if visit(next) {
					return true
				}
			}
		}
		state[name] = done
		return false
	}

	// Sort so the node reported on a cycle is stable between runs.
	names := getKeys(nodes)
	slices.Sort(names)
	for _, name := range names {
		if state[name] == unvisited && visit(name) {
			return field.Invalid(fldPath.Child(name), nodes[name].Steps,
				fmt.Sprintf("graph contains a cycle reachable from node %v; node references must form an acyclic graph", name))
		}
	}
	return nil
}

// validateReachability rejects a graph that declares a node no path from root
// reaches, since the router only ever walks nodes reachable from root, so an
// unreachable node is dead configuration that masks a wiring mistake.
func validateReachability(nodes map[string]Router, fldPath *field.Path) field.ErrorList {
	if _, ok := nodes["root"]; !ok {
		// A missing root is reported by validateRootExistance.
		return nil
	}
	reached := map[string]bool{}
	queue := []string{"root"}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if reached[name] {
			continue
		}
		reached[name] = true
		for _, step := range nodes[name].Steps {
			if next := step.NodeName; next != "" {
				if _, ok := nodes[next]; ok && !reached[next] {
					queue = append(queue, next)
				}
			}
		}
	}

	var errs field.ErrorList
	names := getKeys(nodes)
	slices.Sort(names)
	for _, name := range names {
		if !reached[name] {
			errs = append(errs, field.Invalid(fldPath.Child(name), name,
				fmt.Sprintf("node %v is not reachable from the root node", name)))
		}
	}
	return errs
}

func checkStepName(s Step, idx int, fldRoot *field.Path, nodeName string) *field.Error {
	if len(s.StepName) == 0 {
		return field.Invalid(fldRoot.Child(nodeName).Child(fmt.Sprintf("steps[%d]", idx)).Child("name"),
			s,
			fmt.Sprintf("the step name for node %v cannot be empty", nodeName))
	}
	if !slices.Contains(stepNames, s.StepName) {
		return field.Invalid(fldRoot.Child(nodeName).Child(fmt.Sprintf("steps[%d]", idx)).Child("name"),
			s,
			fmt.Sprintf("invalid step name: %s for node %v", s.StepName, nodeName))
	}
	return nil
}

// checkParamsKind validates a step's declared parameter-group category. A step
// opts into fingerprint parameter injection by setting the "paramsKind" config
// entry; when it does, the value must be one the fingerprint accepts, taken from
// the catalog it serves so a kind added there is valid here with no code change.
// The companion "paramsKey" entry is optional and defaults to the lowercased
// step name, so it needs no separate check here: it resolves to a non-empty
// value whenever the step name is non-empty, which checkStepName already
// enforces. Steps that declare no "paramsKind" entry are left untouched, so
// pipelines that predate this mechanism keep validating.
func checkParamsKind(s Step, idx int, fldRoot *field.Path, nodeName string) *field.Error {
	kind, ok := s.InternalService.Config[ConfigParamsKind]
	if !ok {
		return nil
	}
	// An empty kind names no group, is never in the catalog, and the controller
	// would skip it, so it is always a misconfiguration. Reject it up front,
	// before consulting the catalog, so the fail-open path (used when the
	// catalog cannot be fetched) cannot let it through.
	if strings.TrimSpace(kind) == "" {
		return field.Invalid(
			fldRoot.Child(nodeName).Child(fmt.Sprintf("steps[%d]", idx)).Child("internalService").Child("config").Child(ConfigParamsKind),
			kind,
			fmt.Sprintf("paramsKind for step '%s' must not be empty", s.StepName))
	}
	allowed, catalog := kindCatalog.allows(kind)
	if allowed {
		return nil
	}
	return field.Invalid(
		fldRoot.Child(nodeName).Child(fmt.Sprintf("steps[%d]", idx)).Child("internalService").Child("config").Child(ConfigParamsKind),
		kind,
		fmt.Sprintf("Invalid paramsKind '%s' for step '%s'; allowed: %v", kind, s.StepName, catalog))
}

// checkParamsKeyCharset rejects a step whose resolved paramsKey contains a
// character the fingerprint KV bridge cannot represent in a key segment. Such a
// key is admitted, written to Postgres and answered 200, but the bridge then
// skips it, so the config never reaches KV. Only steps that opt into parameter
// injection (they declare a paramsKind) are checked; others take no part in the
// projection. The empty-kind case is reported by checkParamsKind.
func checkParamsKeyCharset(s Step, idx int, fldRoot *field.Path, nodeName string) *field.Error {
	kind, ok := s.InternalService.Config[ConfigParamsKind]
	if !ok || strings.TrimSpace(kind) == "" {
		return nil
	}
	key := ResolveParamsKey(s)
	if key == "" || paramsKeyPattern.MatchString(key) {
		return nil
	}
	return field.Invalid(
		fldRoot.Child(nodeName).Child(fmt.Sprintf("steps[%d]", idx)).Child("internalService").Child("config").Child(ConfigParamsKey),
		key,
		fmt.Sprintf("paramsKey '%s' for step '%s' may only contain the characters [A-Za-z0-9_/=-]; other characters cannot be projected to the fingerprint KV store", key, s.StepName))
}

// checkParamsKeyConflict rejects a step whose resolved paramsKey is already
// bound to a different kind by an earlier step. It records the first kind seen
// for each key in seen, so the check spans every node of the pipeline. Steps
// that declare no paramsKind are ignored, since they take no part in parameter
// injection.
func checkParamsKeyConflict(s Step, idx int, fldRoot *field.Path, nodeName string, seen map[string]string) *field.Error {
	kind, ok := s.InternalService.Config[ConfigParamsKind]
	if !ok || strings.TrimSpace(kind) == "" {
		return nil
	}
	if allowed, _ := kindCatalog.allows(kind); !allowed {
		// An empty or unknown kind is already reported by checkParamsKind; do
		// not record it here so the step cannot raise a second, confusing
		// conflict error and an invalid kind never claims a key.
		return nil
	}
	key := ResolveParamsKey(s)
	if key == "" {
		return nil
	}
	if prev, exists := seen[key]; exists {
		if prev != kind {
			return field.Invalid(
				fldRoot.Child(nodeName).Child(fmt.Sprintf("steps[%d]", idx)).Child("internalService").Child("config").Child(ConfigParamsKey),
				key,
				fmt.Sprintf("paramsKey '%s' for step '%s' is already declared with kind '%s'; one key cannot bind two kinds", key, s.StepName, prev))
		}
		return nil
	}
	seen[key] = kind
	return nil
}

// ResolveParamsKey returns the parameter-group key a step addresses: the
// explicit "paramsKey" config entry, or the lowercased step name when that
// entry is unset. It is the shared rule the controller uses to register keys,
// so validation and registration agree on the resolved key.
func ResolveParamsKey(s Step) string {
	if key := s.InternalService.Config[ConfigParamsKey]; key != "" {
		return key
	}
	return strings.ToLower(s.StepName)
}

func nodeNameExists(name string, nodes []string) bool {
	// node name is not set, skip check
	if len(name) == 0 {
		return true
	}
	return slices.Contains(nodes, name)
}

func getKeys(m map[string]Router) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// validate step name and node name
func validateNames(nodes map[string]Router, fldPath *field.Path) field.ErrorList {
	nodeNames := getKeys(nodes)
	serviceNames := []string{}
	var errs field.ErrorList

	// Records the kind already claimed by each resolved paramsKey so a second
	// step cannot bind the same key to a different kind. The controller seeds
	// keys deduplicated by paramsKey, so a conflicting kind would otherwise be
	// dropped silently.
	kindByParamsKey := map[string]string{}

	// Iterate nodes in name order. Ranging a map directly is non-deterministic,
	// which would make the paramsKey-conflict error (which step is reported as
	// the duplicate) vary between runs.
	sortedNames := append([]string(nil), nodeNames...)
	slices.Sort(sortedNames)

	for _, name := range sortedNames {
		router := nodes[name]
		for idx, step := range router.Steps {
			// validate step name
			if err := checkStepName(step, idx, fldPath, name); err != nil {
				errs = append(errs, err)
			}

			// validate the declared parameter-group category
			if err := checkParamsKind(step, idx, fldPath, name); err != nil {
				errs = append(errs, err)
			}

			// reject a paramsKey the fingerprint KV bridge cannot represent
			if err := checkParamsKeyCharset(step, idx, fldPath, name); err != nil {
				errs = append(errs, err)
			}

			// reject two steps binding one paramsKey to different kinds
			if err := checkParamsKeyConflict(step, idx, fldPath, name, kindByParamsKey); err != nil {
				errs = append(errs, err)
			}

			// check node name has been defined in the spec
			if !nodeNameExists(step.NodeName, nodeNames) {
				errs = append(errs, field.Invalid(fldPath.Child(name).Child(fmt.Sprintf("steps[%d]", idx)).Child("nodeName"),
					step,
					fmt.Sprintf("node name: %v in step %v does not exist", step.NodeName, step.StepName)))
			}

			// The controller renders this value as a Service name, a Deployment
			// name and an "app" label, so hold it to the same rule as the router's
			// own service name.
			if len(step.InternalService.ServiceName) != 0 {
				errs = append(errs, validateRenderedServiceName(step.InternalService.ServiceName,
					fldPath.Child(name).Child(fmt.Sprintf("steps[%d]", idx)).Child("internalService").Child("serviceName"))...)
			}

			// check service name uniqueness
			if len(step.InternalService.ServiceName) != 0 && slices.Contains(serviceNames, step.InternalService.ServiceName) {
				errs = append(errs, field.Invalid(fldPath.Child(name).Child(fmt.Sprintf("steps[%d]", idx)).Child("internalService").Child("serviceName"),
					step,
					fmt.Sprintf("service name: %v in node %v already exists", step.InternalService.ServiceName, name)))
			} else {
				serviceNames = append(serviceNames, step.InternalService.ServiceName)
			}
		}
	}
	return errs
}

// check root node exists
func validateRootExistance(nodes map[string]Router, fldPath *field.Path) *field.Error {
	if _, ok := nodes["root"]; !ok {
		return field.Invalid(fldPath, nodes, "a root node is required")
	}
	return nil
}

// +kubebuilder:docs-gen:collapse=Existing Validation
