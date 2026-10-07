package main

// viya_validation.go
//
// Post-installation SAS Viya validation checks for the SAS Viya Health Checker.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/term"
)

// ---------------------------------------------------------------------------
// Tunables
// ---------------------------------------------------------------------------

const (
	kubectlTimeout    = 90 * time.Second
	viyaHTTPTimeout   = 60 * time.Second
	computeMaxRetries = 5

	// Pagination fallback used by paginateCollectionCount when an endpoint's
	// response has no "count" field.
	countPageSize = 1000
	maxCountPages = 50

	// countSource values, recorded on ViyaAPICheck so a JSON consumer can
	// tell an authoritative count from a paginated approximation.
	countSourceField     = "count_field"
	countSourcePaginated = "paginated"
	countSourceUnknown   = "unknown"
)

// ---------------------------------------------------------------------------
// Result models
// ---------------------------------------------------------------------------

// ResourceFailure describes a single resource that failed validation.
type ResourceFailure struct {
	Name    string `json:"name"`
	Status  string `json:"status,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Details string `json:"details,omitempty"`
}

// ResourceCheck is the result of validating one Kubernetes resource kind.
type ResourceCheck struct {
	Kind     string            `json:"kind"`
	Status   string            `json:"status"`
	Message  string            `json:"message"`
	Total    int               `json:"total"`
	Ready    int               `json:"ready"`
	NotReady int               `json:"not_ready"`
	Excluded int               `json:"excluded"`
	Failures []ResourceFailure `json:"failures,omitempty"`
}

// WorkloadHealth aggregates all namespace-scoped Kubernetes validations.
type WorkloadHealth struct {
	Status       string        `json:"status"`
	Message      string        `json:"message"`
	Namespace    string        `json:"namespace"`
	Exclusions   []string      `json:"exclusions,omitempty"`
	Nodes        ResourceCheck `json:"nodes"`
	Pods         ResourceCheck `json:"pods"`
	Deployments  ResourceCheck `json:"deployments"`
	StatefulSets ResourceCheck `json:"stateful_sets"`
	DaemonSets   ResourceCheck `json:"daemon_sets"`
	ReplicaSets  ResourceCheck `json:"replica_sets"`
	Services     ResourceCheck `json:"services_and_endpoints"`
	PVCs         ResourceCheck `json:"persistent_volume_claims"`
}

// DeploymentMetadataHealth validates deployment identity artifacts.
type DeploymentMetadataHealth struct {
	Status             string `json:"status"`
	Message            string `json:"message"`
	CadenceName        string `json:"cadence_name,omitempty"`
	CadenceVersion     string `json:"cadence_version,omitempty"`
	CadenceRelease     string `json:"cadence_release,omitempty"`
	ConsulTokenPresent bool   `json:"consul_token_present"`
	ConfigMapsFound    int    `json:"configmaps_found"`
	SecretsFound       int    `json:"secrets_found"`
}

// ComputeContextRef identifies the compute context used by a session check,
// so the JSON report records which context was exercised.
type ComputeContextRef struct {
	Name string `json:"name,omitempty"`
	ID   string `json:"id,omitempty"`
}

// RoutingSummary breaks down the routing mechanisms (Ingress and/or Contour
// HTTPProxy) and HTTPProxy resource health backing the "HTTP Routing
// Endpoints" check.
type RoutingSummary struct {
	RoutingType       string `json:"routing_type"`
	IngressPaths      int    `json:"ingress_paths"`
	HTTPProxyRoutes   int    `json:"httpproxy_routes"`
	HTTPProxyValid    int    `json:"httpproxy_valid"`
	HTTPProxyInvalid  int    `json:"httpproxy_invalid"`
	HTTPProxyOrphaned int    `json:"httpproxy_orphaned"`
	TargetsProbed     int    `json:"targets_probed"`
	AuthIssues        int    `json:"auth_issues"`
	Failures          int    `json:"failures"`
}

// ViyaAPICheck is a single application-level API validation.
type ViyaAPICheck struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
	// Count has no omitempty: a genuine zero is the signal that detects a
	// lossy restore, and must not disappear from the JSON report.
	Count int `json:"count"`
	// CountSource records how Count was derived (count_field, paginated or
	// unknown) so a consumer can tell an authoritative count from an
	// approximation.
	CountSource string `json:"count_source,omitempty"`
	// ErrorDetail carries a sanitized, truncated diagnostic (e.g. a parsed
	// SAS error body) for checks that failed with a non-obvious cause.
	ErrorDetail    string             `json:"error_detail,omitempty"`
	ComputeContext *ComputeContextRef `json:"compute_context,omitempty"`
	Routing        *RoutingSummary    `json:"routing_summary,omitempty"`
}

// ContentInventory records object counts, useful as a DR / restore baseline.
type ContentInventory struct {
	ComputeContexts int `json:"compute_contexts"`
	CASServers      int `json:"cas_servers"`
	CASLibs         int `json:"caslibs"`
	QKBs            int `json:"qkbs"`
	Reports         int `json:"reports"`
	Folders         int `json:"folders"`
	Users           int `json:"users"`
	Groups          int `json:"groups"`
}

// ViyaAPIHealth aggregates SAS Viya application validation.
type ViyaAPIHealth struct {
	Status    string           `json:"status"`
	Message   string           `json:"message"`
	Executed  bool             `json:"executed"`
	BaseURL   string           `json:"base_url,omitempty"`
	Checks    []ViyaAPICheck   `json:"checks,omitempty"`
	Inventory ContentInventory `json:"content_inventory"`
}

// ViyaAPIConfig holds the inputs required for application-level checks.
type ViyaAPIConfig struct {
	BaseURL  string
	Username string
	Password string
	Enabled  bool

	// InsecureSkipTLSVerify and CACertPath control the HTTPS client's TLS
	// verification; both are set from CLI flags in main.go.
	InsecureSkipTLSVerify bool
	CACertPath            string
	// APITimeout bounds every individual SAS Viya API call (--viya-api-timeout).
	APITimeout time.Duration
}

// ---------------------------------------------------------------------------
// kubectl helpers (JSON based - no column parsing)
// ---------------------------------------------------------------------------

func kubectlJSON(kubeconfig string, target interface{}, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), kubectlTimeout)
	defer cancel()

	full := append([]string{}, args...)
	full = append(full, "-o", "json")

	cmd := exec.CommandContext(ctx, "kubectl", full...)
	if kubeconfig != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	}

	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return fmt.Errorf("kubectl %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return fmt.Errorf("kubectl %s failed: %w", strings.Join(args, " "), err)
	}
	if err := json.Unmarshal(out, target); err != nil {
		return fmt.Errorf("unable to parse kubectl output: %w", err)
	}
	return nil
}

func isExcluded(name string, exclusions []string) bool {
	for _, e := range exclusions {
		if e == "" {
			continue
		}
		if strings.Contains(name, e) {
			return true
		}
	}
	return false
}

// loadExclusions reads VIYA_EXCLUSION_LIST (semicolon separated).
func loadExclusions() []string {
	exclusions := []string{"prepull-ds"}
	raw := strings.TrimSpace(os.Getenv("VIYA_EXCLUSION_LIST"))
	if raw == "" {
		return exclusions
	}
	for _, item := range strings.Split(raw, ";") {
		item = strings.TrimSpace(item)
		if item != "" {
			exclusions = append(exclusions, item)
		}
	}
	return exclusions
}

// ---------------------------------------------------------------------------
// Kubernetes API object shapes (only fields we need)
// ---------------------------------------------------------------------------

type k8sCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// podOwnerRef captures just enough of metadata.ownerReferences to detect
// whether a pod belongs to a Job (whose completed retries can leave failed
// sibling pods behind without that being a real failure).
type podOwnerRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type podList struct {
	Items []struct {
		Metadata struct {
			Name            string            `json:"name"`
			Labels          map[string]string `json:"labels"`
			OwnerReferences []podOwnerRef     `json:"ownerReferences"`
		} `json:"metadata"`
		Status struct {
			Phase             string         `json:"phase"`
			Reason            string         `json:"reason"`
			Conditions        []k8sCondition `json:"conditions"`
			ContainerStatuses []struct {
				Ready        bool `json:"ready"`
				RestartCount int  `json:"restartCount"`
			} `json:"containerStatuses"`
		} `json:"status"`
	} `json:"items"`
}

type nodeList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			Conditions []k8sCondition `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

type deploymentList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Replicas int `json:"replicas"`
		} `json:"spec"`
		Status struct {
			Replicas            int `json:"replicas"`
			ReadyReplicas       int `json:"readyReplicas"`
			AvailableReplicas   int `json:"availableReplicas"`
			UnavailableReplicas int `json:"unavailableReplicas"`
		} `json:"status"`
	} `json:"items"`
}

type statefulSetList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Replicas int `json:"replicas"`
		} `json:"spec"`
		Status struct {
			Replicas      int `json:"replicas"`
			ReadyReplicas int `json:"readyReplicas"`
		} `json:"status"`
	} `json:"items"`
}

type daemonSetList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			DesiredNumberScheduled int `json:"desiredNumberScheduled"`
			NumberReady            int `json:"numberReady"`
			NumberUnavailable      int `json:"numberUnavailable"`
			NumberMisscheduled     int `json:"numberMisscheduled"`
		} `json:"status"`
	} `json:"items"`
}

type replicaSetList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			Replicas          int `json:"replicas"`
			ReadyReplicas     int `json:"readyReplicas"`
			AvailableReplicas int `json:"availableReplicas"`
		} `json:"status"`
	} `json:"items"`
}

type serviceList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Type      string `json:"type"`
			ClusterIP string `json:"clusterIP"`
		} `json:"spec"`
	} `json:"items"`
}

type endpointsList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Subsets []struct {
			Addresses []struct {
				IP string `json:"ip"`
			} `json:"addresses"`
			NotReadyAddresses []struct {
				IP string `json:"ip"`
			} `json:"notReadyAddresses"`
		} `json:"subsets"`
	} `json:"items"`
}

// endpointSliceList mirrors `kubectl get endpointslices -o json`
// (discovery.k8s.io/v1). Preferred over the deprecated Endpoints API.
type endpointSliceList struct {
	Items []struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Endpoints []struct {
			Addresses  []string `json:"addresses"`
			Conditions struct {
				Ready *bool `json:"ready"`
			} `json:"conditions"`
		} `json:"endpoints"`
	} `json:"items"`
}

type configMapList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	} `json:"items"`
}

type secretList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	} `json:"items"`
}

type ingressList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Rules []struct {
				Host string `json:"host"`
				HTTP struct {
					Paths []struct {
						Path string `json:"path"`
					} `json:"paths"`
				} `json:"http"`
			} `json:"rules"`
		} `json:"spec"`
	} `json:"items"`
}

// ---------------------------------------------------------------------------
// Status helpers
// ---------------------------------------------------------------------------

// statusRule maps (ready, notReady, total) counts to a status string. It is a
// package-level var, rather than inlined logic, so the DEGRADED/UNHEALTHY
// boundary can be tuned or overridden (e.g. in tests) without touching every
// call site.
var statusRule = func(ready, notReady, total int) string {
	switch {
	case notReady == 0:
		return "HEALTHY"
	case ready == 0 && total > 0:
		return "UNHEALTHY"
	default:
		// Some but not all instances are ready - degraded, not a full outage.
		return "DEGRADED"
	}
}

func statusFromCounts(ready, notReady, total int) string {
	return statusRule(ready, notReady, total)
}

func worstStatus(statuses ...string) string {
	result := "HEALTHY"
	for _, s := range statuses {
		switch s {
		case "UNHEALTHY":
			return "UNHEALTHY"
		case "DEGRADED":
			result = "DEGRADED"
		}
	}
	return result
}

func errorCheck(kind, message string) ResourceCheck {
	return ResourceCheck{
		Kind:    kind,
		Status:  "UNHEALTHY",
		Message: message,
	}
}

func finalizeCheck(c ResourceCheck) ResourceCheck {
	c.Status = statusFromCounts(c.Ready, c.NotReady, c.Total)
	if c.Message == "" {
		c.Message = fmt.Sprintf("%d/%d ready (%d excluded)", c.Ready, c.Total, c.Excluded)
	}
	return c
}

// ---------------------------------------------------------------------------
// Workload validation
// ---------------------------------------------------------------------------

func checkViyaWorkloadHealth(namespace, kubeconfig string, pvcUsage PVCUsageHealth) WorkloadHealth {
	exclusions := loadExclusions()

	wh := WorkloadHealth{
		Namespace:  namespace,
		Exclusions: exclusions,
	}

	wh.Nodes = checkNodeReadiness(kubeconfig, exclusions)
	wh.Pods = checkNamespacePods(namespace, kubeconfig, exclusions)
	wh.Deployments = checkDeployments(namespace, kubeconfig, exclusions)
	wh.StatefulSets = checkStatefulSets(namespace, kubeconfig, exclusions)
	wh.DaemonSets = checkDaemonSets(namespace, kubeconfig, exclusions)
	wh.ReplicaSets = checkReplicaSets(namespace, kubeconfig, exclusions)
	wh.Services = checkServicesAndEndpoints(namespace, kubeconfig, exclusions)
	// PVCs are validated once in pvc_usage.go (capacity + usage superset) and
	// adapted here so this report has a single, non-conflicting PVC section.
	wh.PVCs = pvcUsageToResourceCheck(pvcUsage)

	wh.Status = worstStatus(
		wh.Nodes.Status,
		wh.Pods.Status,
		wh.Deployments.Status,
		wh.StatefulSets.Status,
		wh.DaemonSets.Status,
		wh.ReplicaSets.Status,
		wh.Services.Status,
		wh.PVCs.Status,
	)

	var problems []string
	for _, c := range []ResourceCheck{
		wh.Nodes, wh.Pods, wh.Deployments, wh.StatefulSets,
		wh.DaemonSets, wh.ReplicaSets, wh.Services, wh.PVCs,
	} {
		if c.Status != "HEALTHY" {
			problems = append(problems, fmt.Sprintf("%s(%d not ready)", c.Kind, c.NotReady))
		}
	}

	if len(problems) == 0 {
		wh.Message = "All Viya workloads, services and volumes are ready"
	} else {
		wh.Message = "Issues detected: " + strings.Join(problems, ", ")
	}

	return wh
}

func checkNodeReadiness(kubeconfig string, exclusions []string) ResourceCheck {
	check := ResourceCheck{Kind: "Nodes"}

	var nodes nodeList
	if err := kubectlJSON(kubeconfig, &nodes, "get", "nodes"); err != nil {
		return errorCheck("Nodes", err.Error())
	}

	for _, n := range nodes.Items {
		name := n.Metadata.Name
		if isExcluded(name, exclusions) {
			check.Excluded++
			continue
		}
		check.Total++

		ready := false
		reason := "Ready condition not found"
		for _, c := range n.Status.Conditions {
			if strings.EqualFold(c.Type, "Ready") {
				ready = strings.EqualFold(c.Status, "True")
				reason = c.Reason
				break
			}
		}

		if ready {
			check.Ready++
		} else {
			check.NotReady++
			check.Failures = append(check.Failures, ResourceFailure{
				Name:   name,
				Status: "NotReady",
				Reason: reason,
			})
		}
	}

	return finalizeCheck(check)
}

func checkNamespacePods(namespace, kubeconfig string, exclusions []string) ResourceCheck {
	check := ResourceCheck{Kind: "Pods"}

	var pods podList
	if err := kubectlJSON(kubeconfig, &pods, "get", "pods", "-n", namespace); err != nil {
		return errorCheck("Pods", err.Error())
	}

	// A pod owned by a Job (or a sas-deployment-operator-reconcile pod, which
	// is recreated each reconcile loop without a Job owner) that has at least
	// one Succeeded sibling should not fail the check for its earlier retries.
	succeededGroups := make(map[string]bool)
	for _, p := range pods.Items {
		if !strings.EqualFold(p.Status.Phase, "Succeeded") {
			continue
		}
		if group := podGroupKey(p.Metadata.Name, p.Metadata.Labels, p.Metadata.OwnerReferences); group != "" {
			succeededGroups[group] = true
		}
	}

	for _, p := range pods.Items {
		name := p.Metadata.Name
		if isExcluded(name, exclusions) {
			check.Excluded++
			continue
		}
		check.Total++

		phase := p.Status.Phase
		group := podGroupKey(name, p.Metadata.Labels, p.Metadata.OwnerReferences)

		switch {
		case strings.EqualFold(phase, "Succeeded"):
			check.Ready++

		case strings.EqualFold(phase, "Running"):
			ready := false
			reason := ""
			for _, c := range p.Status.Conditions {
				if strings.EqualFold(c.Type, "Ready") {
					ready = strings.EqualFold(c.Status, "True")
					reason = c.Reason
					break
				}
			}
			if ready {
				check.Ready++
			} else {
				check.NotReady++
				check.Failures = append(check.Failures, ResourceFailure{
					Name:   name,
					Status: phase,
					Reason: reason,
				})
			}

		case strings.EqualFold(phase, "Failed") && group != "" && succeededGroups[group]:
			// A completed Job (or reconcile loop) can leave failed retry
			// attempts behind once a later attempt succeeds - not a failure.
			check.Ready++

		default:
			// Pending, Failed (with no successful sibling), Unknown or empty phase
			check.NotReady++
			check.Failures = append(check.Failures, ResourceFailure{
				Name:   name,
				Status: phase,
				Reason: p.Status.Reason,
			})
		}
	}

	return finalizeCheck(check)
}

// podGroupKey identifies the Job (via ownerReferences or the standard batch
// job-name label) or sas-deployment-operator-reconcile group a pod belongs
// to, so a later Succeeded attempt can excuse earlier Failed siblings.
// Returns "" for pods that are not part of any such retry group.
func podGroupKey(name string, labels map[string]string, owners []podOwnerRef) string {
	for _, o := range owners {
		if strings.EqualFold(o.Kind, "Job") {
			return "job:" + o.Name
		}
	}
	if jobName := labels["batch.kubernetes.io/job-name"]; jobName != "" {
		return "job:" + jobName
	}
	if jobName := labels["job-name"]; jobName != "" {
		return "job:" + jobName
	}
	if strings.Contains(name, "sas-deployment-operator-reconcile") {
		return "reconcile:sas-deployment-operator"
	}
	return ""
}

func checkDeployments(namespace, kubeconfig string, exclusions []string) ResourceCheck {
	check := ResourceCheck{Kind: "Deployments"}

	var deps deploymentList
	if err := kubectlJSON(kubeconfig, &deps, "get", "deployments", "-n", namespace); err != nil {
		return errorCheck("Deployments", err.Error())
	}

	for _, d := range deps.Items {
		name := d.Metadata.Name
		if isExcluded(name, exclusions) {
			check.Excluded++
			continue
		}
		check.Total++

		desired := d.Spec.Replicas
		healthy := d.Status.ReadyReplicas == desired &&
			d.Status.AvailableReplicas == desired &&
			d.Status.UnavailableReplicas == 0

		if healthy {
			check.Ready++
		} else {
			check.NotReady++
			check.Failures = append(check.Failures, ResourceFailure{
				Name:   name,
				Status: "NotReady",
				Details: fmt.Sprintf("desired=%d ready=%d available=%d unavailable=%d",
					desired, d.Status.ReadyReplicas, d.Status.AvailableReplicas, d.Status.UnavailableReplicas),
			})
		}
	}

	return finalizeCheck(check)
}

func checkStatefulSets(namespace, kubeconfig string, exclusions []string) ResourceCheck {
	check := ResourceCheck{Kind: "StatefulSets"}

	var sets statefulSetList
	if err := kubectlJSON(kubeconfig, &sets, "get", "statefulsets", "-n", namespace); err != nil {
		return errorCheck("StatefulSets", err.Error())
	}

	for _, s := range sets.Items {
		name := s.Metadata.Name
		if isExcluded(name, exclusions) {
			check.Excluded++
			continue
		}
		check.Total++

		desired := s.Spec.Replicas
		if s.Status.ReadyReplicas == desired {
			check.Ready++
		} else {
			check.NotReady++
			check.Failures = append(check.Failures, ResourceFailure{
				Name:    name,
				Status:  "NotReady",
				Details: fmt.Sprintf("desired=%d ready=%d", desired, s.Status.ReadyReplicas),
			})
		}
	}

	return finalizeCheck(check)
}

func checkDaemonSets(namespace, kubeconfig string, exclusions []string) ResourceCheck {
	check := ResourceCheck{Kind: "DaemonSets"}

	var sets daemonSetList
	if err := kubectlJSON(kubeconfig, &sets, "get", "daemonsets", "-n", namespace); err != nil {
		return errorCheck("DaemonSets", err.Error())
	}

	for _, d := range sets.Items {
		name := d.Metadata.Name
		if isExcluded(name, exclusions) {
			check.Excluded++
			continue
		}
		check.Total++

		healthy := d.Status.NumberReady == d.Status.DesiredNumberScheduled &&
			d.Status.NumberUnavailable == 0 &&
			d.Status.NumberMisscheduled == 0

		if healthy {
			check.Ready++
		} else {
			check.NotReady++
			check.Failures = append(check.Failures, ResourceFailure{
				Name:   name,
				Status: "NotReady",
				Details: fmt.Sprintf("desired=%d ready=%d unavailable=%d misscheduled=%d",
					d.Status.DesiredNumberScheduled, d.Status.NumberReady,
					d.Status.NumberUnavailable, d.Status.NumberMisscheduled),
			})
		}
	}

	return finalizeCheck(check)
}

func checkReplicaSets(namespace, kubeconfig string, exclusions []string) ResourceCheck {
	check := ResourceCheck{Kind: "ReplicaSets"}

	var sets replicaSetList
	if err := kubectlJSON(kubeconfig, &sets, "get", "replicasets", "-n", namespace); err != nil {
		return errorCheck("ReplicaSets", err.Error())
	}

	for _, r := range sets.Items {
		name := r.Metadata.Name
		if isExcluded(name, exclusions) {
			check.Excluded++
			continue
		}

		// Scaled-down replica sets are expected and are not counted.
		if r.Status.Replicas == 0 {
			continue
		}
		check.Total++

		healthy := r.Status.Replicas == r.Status.ReadyReplicas &&
			r.Status.Replicas == r.Status.AvailableReplicas

		if healthy {
			check.Ready++
		} else {
			check.NotReady++
			check.Failures = append(check.Failures, ResourceFailure{
				Name:   name,
				Status: "NotReady",
				Details: fmt.Sprintf("desired=%d ready=%d available=%d",
					r.Status.Replicas, r.Status.ReadyReplicas, r.Status.AvailableReplicas),
			})
		}
	}

	return finalizeCheck(check)
}

func checkServicesAndEndpoints(namespace, kubeconfig string, exclusions []string) ResourceCheck {
	check := ResourceCheck{Kind: "Services/Endpoints"}

	var services serviceList
	if err := kubectlJSON(kubeconfig, &services, "get", "services", "-n", namespace); err != nil {
		return errorCheck("Services/Endpoints", err.Error())
	}

	epMap, err := collectEndpointState(kubeconfig, namespace)
	if err != nil {
		return errorCheck("Services/Endpoints", err.Error())
	}

	for _, svc := range services.Items {
		name := svc.Metadata.Name
		if isExcluded(name, exclusions) {
			check.Excluded++
			continue
		}
		// ExternalName services have no endpoints by design.
		if strings.EqualFold(svc.Spec.Type, "ExternalName") {
			check.Excluded++
			continue
		}
		check.Total++

		state, ok := epMap[name]
		switch {
		case !ok || !state.exists:
			check.NotReady++
			check.Failures = append(check.Failures, ResourceFailure{
				Name:   name,
				Status: "NoEndpoints",
				Reason: "no endpoints object found for service",
			})
		case state.addresses == 0:
			// Endpoints/EndpointSlice objects exist but zero ready addresses -
			// this must not be silently counted as Ready.
			reason := "endpoints have no ready addresses"
			if !state.hasSubsets {
				reason = "endpoints object has no subsets"
			}
			check.NotReady++
			check.Failures = append(check.Failures, ResourceFailure{
				Name:    name,
				Status:  "NoReadyAddresses",
				Reason:  reason,
				Details: fmt.Sprintf("ready=0 notReady=%d", state.notReady),
			})
		case state.notReady > 0:
			check.NotReady++
			check.Failures = append(check.Failures, ResourceFailure{
				Name:    name,
				Status:  "EndpointsNotReady",
				Details: fmt.Sprintf("ready=%d notReady=%d", state.addresses, state.notReady),
			})
		default:
			check.Ready++
		}
	}

	return finalizeCheck(check)
}

// endpointState is the ready/not-ready address tally for one service,
// collected from either EndpointSlice or the legacy Endpoints API.
type endpointState struct {
	exists     bool
	hasSubsets bool
	addresses  int
	notReady   int
}

// collectEndpointState prefers EndpointSlice (the Endpoints API is deprecated
// as of Kubernetes 1.33) and falls back to the legacy Endpoints API for older
// clusters or when EndpointSlice access is unavailable.
func collectEndpointState(kubeconfig, namespace string) (map[string]endpointState, error) {
	if state, err := collectEndpointSliceState(kubeconfig, namespace); err == nil {
		return state, nil
	}
	return collectLegacyEndpointState(kubeconfig, namespace)
}

func collectEndpointSliceState(kubeconfig, namespace string) (map[string]endpointState, error) {
	var slices endpointSliceList
	if err := kubectlJSON(kubeconfig, &slices, "get", "endpointslices", "-n", namespace); err != nil {
		return nil, err
	}

	result := make(map[string]endpointState)
	for _, s := range slices.Items {
		svcName := s.Metadata.Labels["kubernetes.io/service-name"]
		if svcName == "" {
			continue
		}
		state := result[svcName]
		state.exists = true
		for _, ep := range s.Endpoints {
			state.hasSubsets = true
			// Per the EndpointSlice API, a nil ready condition means ready.
			ready := ep.Conditions.Ready == nil || *ep.Conditions.Ready
			if ready {
				state.addresses += len(ep.Addresses)
			} else {
				state.notReady += len(ep.Addresses)
			}
		}
		result[svcName] = state
	}
	return result, nil
}

func collectLegacyEndpointState(kubeconfig, namespace string) (map[string]endpointState, error) {
	var endpoints endpointsList
	if err := kubectlJSON(kubeconfig, &endpoints, "get", "endpoints", "-n", namespace); err != nil {
		return nil, err
	}

	result := make(map[string]endpointState)
	for _, e := range endpoints.Items {
		state := endpointState{exists: true}
		for _, s := range e.Subsets {
			state.hasSubsets = true
			state.addresses += len(s.Addresses)
			state.notReady += len(s.NotReadyAddresses)
		}
		result[e.Metadata.Name] = state
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Deployment metadata validation
// ---------------------------------------------------------------------------

func checkDeploymentMetadata(namespace, kubeconfig string) DeploymentMetadataHealth {
	meta := DeploymentMetadataHealth{}

	var maps configMapList
	if err := kubectlJSON(kubeconfig, &maps, "get", "configmaps", "-n", namespace); err != nil {
		meta.Status = "UNHEALTHY"
		meta.Message = "Failed to read config maps: " + err.Error()
		return meta
	}
	meta.ConfigMapsFound = len(maps.Items)

	for _, cm := range maps.Items {
		if strings.HasPrefix(cm.Metadata.Name, "sas-deployment-metadata") {
			meta.CadenceName = cm.Data["SAS_CADENCE_NAME"]
			meta.CadenceVersion = cm.Data["SAS_CADENCE_VERSION"]
			meta.CadenceRelease = cm.Data["SAS_CADENCE_RELEASE"]
			break
		}
	}

	var secrets secretList
	if err := kubectlJSON(kubeconfig, &secrets, "get", "secrets", "-n", namespace); err != nil {
		meta.Status = "DEGRADED"
		meta.Message = "Cadence read, but secrets could not be listed: " + err.Error()
		return meta
	}
	meta.SecretsFound = len(secrets.Items)

	for _, s := range secrets.Items {
		if s.Metadata.Name == "sas-consul-client" {
			if _, ok := s.Data["CONSUL_TOKEN"]; ok {
				meta.ConsulTokenPresent = true
			}
			break
		}
	}

	switch {
	case meta.CadenceName == "" || meta.CadenceVersion == "":
		meta.Status = "UNHEALTHY"
		meta.Message = "sas-deployment-metadata cadence information not found"
	case !meta.ConsulTokenPresent:
		meta.Status = "DEGRADED"
		meta.Message = "CONSUL_TOKEN not found in sas-consul-client secret"
	default:
		meta.Status = "HEALTHY"
		meta.Message = fmt.Sprintf("Cadence %s %s validated, consul token present",
			meta.CadenceName, meta.CadenceVersion)
	}

	return meta
}

// ---------------------------------------------------------------------------
// SAS Viya application validation
// ---------------------------------------------------------------------------

// newViyaHTTPClient builds the HTTPS client used for SAS Viya API checks.
// TLS verification is only skipped when insecureSkipVerify is explicitly
// requested (--insecure-skip-tls-verify); callers may also supply a CA bundle
// via caCertPath (--ca-cert) for self-signed or private CAs. timeout bounds
// every individual call (--viya-api-timeout); a non-positive value falls
// back to viyaHTTPTimeout.
func newViyaHTTPClient(insecureSkipVerify bool, caCertPath string, timeout time.Duration) (*http.Client, error) {
	tlsConfig := &tls.Config{InsecureSkipVerify: insecureSkipVerify}

	if caCertPath != "" {
		caCert, err := os.ReadFile(caCertPath)
		if err != nil {
			return nil, fmt.Errorf("unable to read CA cert %s: %w", caCertPath, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("no valid certificates found in %s", caCertPath)
		}
		tlsConfig.RootCAs = pool
	}

	if timeout <= 0 {
		timeout = viyaHTTPTimeout
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: tlsConfig},
	}, nil
}

// firstNonEmpty returns the first non-blank (after trimming) value, or "".
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// normalizeViyaURL reduces a user-supplied URL or bare hostname to scheme +
// host with no trailing slash, path, query or fragment. A missing scheme
// defaults to https.
func normalizeViyaURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.TrimRight(raw, "/")
	}
	return u.Scheme + "://" + u.Host
}

// LoadViyaAPIConfig resolves the SAS Viya URL/username/password using
// precedence: CLI flag, environment variable, interactive prompt. Leaving
// the URL blank (press Enter to skip) disables API checks entirely.
func LoadViyaAPIConfig(urlFlag, userFlag string, apiTimeout time.Duration) ViyaAPIConfig {
	cfg := ViyaAPIConfig{APITimeout: apiTimeout}

	cfg.BaseURL = firstNonEmpty(urlFlag, os.Getenv("VIYA_URL"))
	if cfg.BaseURL == "" {
		cfg.BaseURL = promptForInput("Enter SAS Viya URL, for example https://viya.example.com, or press Enter to skip: ")
	}
	if cfg.BaseURL == "" {
		return cfg
	}
	cfg.BaseURL = normalizeViyaURL(cfg.BaseURL)

	cfg.Username = firstNonEmpty(userFlag, os.Getenv("VIYA_USER"))
	if cfg.Username == "" {
		cfg.Username = promptForInput("Enter SAS Viya username: ")
	}

	cfg.Password = os.Getenv("VIYA_PASSWORD")
	if cfg.Password == "" {
		cfg.Password = promptForViyaPassword()
	}

	cfg.Enabled = cfg.Username != "" && cfg.Password != ""
	return cfg
}

// promptForViyaPassword reads the Viya password without echoing it to the
// terminal or leaving it in shell scrollback. term.ReadPassword requires a
// real TTY, so non-interactive runs (CI, piped input) must set VIYA_PASSWORD
// instead. The password is never printed.
func promptForViyaPassword() string {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "Non-interactive session detected - set the VIYA_PASSWORD environment variable to run Viya API checks")
		return ""
	}
	fmt.Print("Enter SAS Viya password (input hidden): ")
	passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not read password securely: %v\n", err)
		return ""
	}
	return strings.TrimSpace(string(passwordBytes))
}

// printAPIProgress prints (without a trailing newline) a status line before
// a potentially slow Viya API call and flushes it immediately, so a long
// call (SASLogon, a compute session retry loop, etc.) never looks like a
// hang. Pair with printAPIOutcome once the call returns.
func printAPIProgress(msg string) {
	fmt.Printf("  %s %s%s%s", SymbolInfo, ColorBlue, msg, ColorReset)
	_ = os.Stdout.Sync()
}

// printAPIOutcome completes the line started by printAPIProgress.
func printAPIOutcome(status string) {
	fmt.Printf(" %s%s%s\n", getStatusColor(status), getStatusSymbol(status), ColorReset)
	_ = os.Stdout.Sync()
}

func checkStatuses(checks []ViyaAPICheck) []string {
	statuses := make([]string, len(checks))
	for i, c := range checks {
		statuses[i] = c.Status
	}
	return statuses
}

func checkViyaAPIHealth(cfg ViyaAPIConfig, namespace, kubeconfig string, strictRouting bool) ViyaAPIHealth {
	api := ViyaAPIHealth{BaseURL: cfg.BaseURL}

	if !cfg.Enabled {
		api.Status = "SKIPPED"
		api.Message = "Viya API validation skipped (no URL or credentials provided)"
		return api
	}
	api.Executed = true

	client, err := newViyaHTTPClient(cfg.InsecureSkipTLSVerify, cfg.CACertPath, cfg.APITimeout)
	if err != nil {
		api.Status = "UNHEALTHY"
		api.Message = "Unable to configure HTTPS client: " + err.Error()
		return api
	}

	printAPIProgress("Authenticating with SASLogon...")
	token, tokenCheck := viyaAuthenticate(client, cfg)
	printAPIOutcome(tokenCheck.Status)
	api.Checks = append(api.Checks, tokenCheck)
	if token == "" {
		api.Status = "UNHEALTHY"
		api.Message = "SASLogon authentication failed - remaining API checks skipped"
		return api
	}

	printAPIProgress("Checking FQDN accessibility...")
	fqdnCheck := viyaSimpleGet(client, cfg.BaseURL, token, "FQDN Accessibility", "")
	printAPIOutcome(fqdnCheck.Status)
	api.Checks = append(api.Checks, fqdnCheck)

	// HTTP routes (Ingress and/or Contour HTTPProxy) are discovered from the
	// cluster, so no sasdeployment.yaml is required to validate them.
	printAPIProgress("Discovering HTTP routes...")
	routingCheck := checkViyaHTTPRouting(client, namespace, kubeconfig, cfg, token, strictRouting)
	printAPIOutcome(routingCheck.Status)
	api.Checks = append(api.Checks, routingCheck)

	printAPIProgress("Checking compute contexts...")
	contexts, c := viyaCountCollection(client, cfg.BaseURL, token, "Compute Contexts", "/compute/contexts")
	printAPIOutcome(c.Status)
	api.Inventory.ComputeContexts = contexts
	api.Checks = append(api.Checks, c)

	// viyaComputeSession prints its own per-attempt progress.
	api.Checks = append(api.Checks, viyaComputeSession(client, cfg.BaseURL, token, cfg.Username))

	printAPIProgress("Checking CAS, CASLIBs...")
	servers, caslibs, casChecks := viyaCASValidation(client, cfg.BaseURL, token)
	printAPIOutcome(worstStatus(checkStatuses(casChecks)...))
	api.Inventory.CASServers = servers
	api.Inventory.CASLibs = caslibs
	api.Checks = append(api.Checks, casChecks...)

	printAPIProgress("Checking Quality Knowledge Bases...")
	qkbs, c := viyaCountCollection(client, cfg.BaseURL, token, "Quality Knowledge Base", "/dataQuality/qkbs")
	printAPIOutcome(c.Status)
	api.Inventory.QKBs = qkbs
	api.Checks = append(api.Checks, c)

	printAPIProgress("Checking reports...")
	reports, c := viyaCountCollection(client, cfg.BaseURL, token, "Reports", "/reports/reports")
	printAPIOutcome(c.Status)
	api.Inventory.Reports = reports
	api.Checks = append(api.Checks, c)

	printAPIProgress("Checking folders...")
	folders, c := viyaCountCollection(client, cfg.BaseURL, token, "Folders", "/folders/folders")
	printAPIOutcome(c.Status)
	api.Inventory.Folders = folders
	api.Checks = append(api.Checks, c)

	printAPIProgress("Checking users...")
	users, c := viyaCountCollection(client, cfg.BaseURL, token, "Identities - Users", "/identities/users")
	printAPIOutcome(c.Status)
	api.Inventory.Users = users
	api.Checks = append(api.Checks, c)

	printAPIProgress("Checking groups...")
	groups, c := viyaCountCollection(client, cfg.BaseURL, token, "Identities - Groups", "/identities/groups")
	printAPIOutcome(c.Status)
	api.Inventory.Groups = groups
	api.Checks = append(api.Checks, c)

	failed, degraded := 0, 0
	for _, chk := range api.Checks {
		switch chk.Status {
		case "UNHEALTHY":
			failed++
		case "DEGRADED":
			degraded++
		}
	}

	switch {
	case failed > 0:
		api.Status = "UNHEALTHY"
		api.Message = fmt.Sprintf("%d Viya API check(s) failed, %d degraded", failed, degraded)
	case degraded > 0:
		api.Status = "DEGRADED"
		api.Message = fmt.Sprintf("%d Viya API check(s) degraded", degraded)
	default:
		api.Status = "HEALTHY"
		api.Message = "All SAS Viya application API checks passed"
	}

	return api
}

func viyaAuthenticate(client *http.Client, cfg ViyaAPIConfig) (string, ViyaAPICheck) {
	check := ViyaAPICheck{Name: "SASLogon Authentication"}

	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("username", cfg.Username)
	form.Set("password", cfg.Password)

	req, err := http.NewRequest(http.MethodPost,
		cfg.BaseURL+"/SASLogon/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		check.Status = "UNHEALTHY"
		check.Message = err.Error()
		return "", check
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth("sas.ec", "")

	resp, err := client.Do(req)
	if err != nil {
		check.Status = "UNHEALTHY"
		check.Message = "SASLogon request failed: " + err.Error()
		return "", check
	}
	defer resp.Body.Close()

	check.HTTPStatus = resp.StatusCode
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode > 399 {
		check.Status = "UNHEALTHY"
		check.Message = fmt.Sprintf("SASLogon returned status %d", resp.StatusCode)
		return "", check
	}

	var payload struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.AccessToken == "" {
		check.Status = "UNHEALTHY"
		check.Message = "No access token returned by SASLogon"
		return "", check
	}

	check.Status = "HEALTHY"
	check.Message = "Access token obtained successfully"

	tokenType := payload.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}
	return tokenType + " " + payload.AccessToken, check
}

func viyaRequest(client *http.Client, method, endpoint, token, accept string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", token)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	return client.Do(req)
}

func viyaSimpleGet(client *http.Client, baseURL, token, name, path string) ViyaAPICheck {
	check := ViyaAPICheck{Name: name}

	resp, err := viyaRequest(client, http.MethodGet, baseURL+path, token, "", nil)
	if err != nil {
		check.Status = "UNHEALTHY"
		check.Message = err.Error()
		return check
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	check.HTTPStatus = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode <= 399 {
		check.Status = "HEALTHY"
		check.Message = fmt.Sprintf("Reachable (status %d)", resp.StatusCode)
	} else {
		check.Status = "UNHEALTHY"
		check.Message = fmt.Sprintf("Unexpected status %d", resp.StatusCode)
	}
	return check
}

// sasCollectionLink is one entry in a SAS collection response's "links".
type sasCollectionLink struct {
	Rel  string `json:"rel"`
	Href string `json:"href"`
}

// sasCollectionPage is the subset of a SAS collection response needed to
// count items across pages: item bodies themselves are never inspected here.
type sasCollectionPage struct {
	Count *int                `json:"count"`
	Items []json.RawMessage   `json:"items"`
	Links []sasCollectionLink `json:"links"`
}

func findNextLink(links []sasCollectionLink) string {
	for _, l := range links {
		if strings.EqualFold(l.Rel, "next") {
			return l.Href
		}
	}
	return ""
}

// resolveHref turns a (possibly relative) link href from a SAS collection
// response into an absolute URL against baseURL.
func resolveHref(baseURL, href string) string {
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	if strings.HasPrefix(href, "/") {
		return baseURL + href
	}
	return baseURL + "/" + href
}

// paginateCollectionCount fetches a SAS collection endpoint's total item
// count. It prefers the response's "count" field (countSourceField); when
// that field is absent, a single limit=1 (or default-limit) page cannot be
// trusted as the true total, so it paginates with start/limit=1000 - following
// the API's "next" link when the server advertises one - for up to
// maxCountPages pages (countSourcePaginated).
func paginateCollectionCount(client *http.Client, baseURL, token, path string) (count int, source string, httpStatus int, err error) {
	endpoint := fmt.Sprintf("%s%s?start=0&limit=%d", baseURL, path, countPageSize)
	resp, reqErr := viyaRequest(client, http.MethodGet, endpoint, token, "application/vnd.sas.collection+json", nil)
	if reqErr != nil {
		return 0, countSourceUnknown, 0, reqErr
	}
	defer resp.Body.Close()
	httpStatus = resp.StatusCode
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode > 399 {
		return 0, countSourceUnknown, httpStatus, fmt.Errorf("GET %s returned status %d", path, resp.StatusCode)
	}

	var page sasCollectionPage
	if unmarshalErr := json.Unmarshal(body, &page); unmarshalErr != nil {
		return 0, countSourceUnknown, httpStatus, fmt.Errorf("unable to parse response: %w", unmarshalErr)
	}

	if page.Count != nil {
		return *page.Count, countSourceField, httpStatus, nil
	}

	total := len(page.Items)
	current := page
	for pages := 1; pages < maxCountPages; pages++ {
		var nextURL string
		if nextHref := findNextLink(current.Links); nextHref != "" {
			nextURL = resolveHref(baseURL, nextHref)
		} else if len(current.Items) == countPageSize {
			// Server advertises no "next" link but the page was full -
			// keep paginating via start/limit until a short page appears.
			nextURL = fmt.Sprintf("%s%s?start=%d&limit=%d", baseURL, path, pages*countPageSize, countPageSize)
		} else {
			break
		}

		r, reqErr := viyaRequest(client, http.MethodGet, nextURL, token, "application/vnd.sas.collection+json", nil)
		if reqErr != nil {
			break
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode < 200 || r.StatusCode > 399 {
			break
		}
		var p sasCollectionPage
		if unmarshalErr := json.Unmarshal(b, &p); unmarshalErr != nil {
			break
		}
		total += len(p.Items)
		current = p
	}

	return total, countSourcePaginated, httpStatus, nil
}

// viyaCountCollection issues a GET and returns the number of items reported,
// preferring the authoritative "count" field and falling back to pagination.
func viyaCountCollection(client *http.Client, baseURL, token, name, path string) (int, ViyaAPICheck) {
	check := ViyaAPICheck{Name: name}

	count, source, httpStatus, err := paginateCollectionCount(client, baseURL, token, path)
	check.HTTPStatus = httpStatus
	check.CountSource = source
	if err != nil {
		check.Status = "UNHEALTHY"
		check.Message = err.Error()
		return 0, check
	}

	check.Count = count
	switch {
	case source == countSourceField && count > 0:
		check.Status = "HEALTHY"
		check.Message = fmt.Sprintf("%d item(s) found", count)
	case source == countSourceField:
		check.Status = "DEGRADED"
		check.Message = "Endpoint reachable but returned no items"
	case count > 0:
		check.Status = "DEGRADED"
		check.Message = fmt.Sprintf("%d item(s) found (paginated count - endpoint did not return a 'count' field)", count)
	default:
		check.Status = "DEGRADED"
		check.Message = "Endpoint reachable but returned no items (paginated - endpoint did not return a 'count' field)"
	}
	return count, check
}

// maxErrorBodyBytes bounds how much of a non-2xx response body is captured
// for diagnostics.
const maxErrorBodyBytes = 4096

// sasErrorBody covers the common shapes of a SAS REST API error response,
// including the variant nested under an "error" object.
type sasErrorBody struct {
	Message     string      `json:"message"`
	Details     interface{} `json:"details"`
	ErrorCode   int         `json:"errorCode"`
	Remediation string      `json:"remediation"`
	Err         *struct {
		Message     string      `json:"message"`
		Details     interface{} `json:"details"`
		ErrorCode   int         `json:"errorCode"`
		Remediation string      `json:"remediation"`
	} `json:"error"`
}

// parseSASError extracts message/details/errorCode/remediation from a SAS
// error body and returns a sanitized, single-line, length-bounded summary.
// If the body isn't a recognizable SAS error shape, the raw body is
// sanitized and returned instead so no diagnostic information is lost.
func parseSASError(body []byte) string {
	var e sasErrorBody
	if err := json.Unmarshal(body, &e); err != nil {
		return sanitizeErrorText(string(body))
	}

	msg, code, remediation, details := e.Message, e.ErrorCode, e.Remediation, e.Details
	if e.Err != nil {
		if msg == "" {
			msg = e.Err.Message
		}
		if code == 0 {
			code = e.Err.ErrorCode
		}
		if remediation == "" {
			remediation = e.Err.Remediation
		}
		if details == nil {
			details = e.Err.Details
		}
	}

	var parts []string
	if msg != "" {
		parts = append(parts, "message="+msg)
	}
	if code != 0 {
		parts = append(parts, fmt.Sprintf("errorCode=%d", code))
	}
	if details != nil {
		if b, err := json.Marshal(details); err == nil {
			parts = append(parts, "details="+string(b))
		}
	}
	if remediation != "" {
		parts = append(parts, "remediation="+remediation)
	}
	if len(parts) == 0 {
		return sanitizeErrorText(string(body))
	}
	return sanitizeErrorText(strings.Join(parts, "; "))
}

// sanitizeErrorText strips control characters and truncates so diagnostic
// text is safe to print to the console and embed in the JSON report.
func sanitizeErrorText(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case r < 32:
			return -1
		default:
			return r
		}
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > maxErrorBodyBytes {
		s = s[:maxErrorBodyBytes] + "...(truncated)"
	}
	return s
}

// isRetryableComputeStatus reports whether an HTTP status from the compute
// session create call should be retried - only transient gateway errors, not
// e.g. a 500 (usually a real server-side problem worth surfacing immediately).
func isRetryableComputeStatus(status int) bool {
	return status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
}

// viyaComputeSession starts a compute session, verifies its state and always
// cleans up a session it created. username tailors diagnostics: sasboot
// commonly lacks the POSIX identity the compute service requires, so a
// failure for that user is reported as DEGRADED (not UNHEALTHY) with
// remediation guidance instead of failing the whole run.
func viyaComputeSession(client *http.Client, baseURL, token, username string) ViyaAPICheck {
	check := ViyaAPICheck{Name: "Compute Session Startup"}

	degradeForSasboot := func(reason string) ViyaAPICheck {
		check.Status = "DEGRADED"
		check.Message = reason + " (user 'sasboot' typically lacks the POSIX identity required by the compute service - retry with a regular LDAP/SCIM user that has compute access)"
		return check
	}

	// Locate a SAS Studio compute context.
	contextID, contextName := "", ""
	for _, creator := range []string{"sas.SASStudio", "sas.studio"} {
		endpoint := fmt.Sprintf("%s/compute/contexts?filter=%s",
			baseURL, url.QueryEscape(fmt.Sprintf(`contains(createdBy,"%s")`, creator)))

		resp, err := viyaRequest(client, http.MethodGet, endpoint, token,
			"application/vnd.sas.collection+json", nil)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode > 399 {
			continue
		}

		var payload struct {
			Items []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"items"`
		}
		if err := json.Unmarshal(body, &payload); err == nil && len(payload.Items) > 0 {
			contextID = payload.Items[0].ID
			contextName = payload.Items[0].Name
			break
		}
	}

	if contextID != "" {
		check.ComputeContext = &ComputeContextRef{Name: contextName, ID: contextID}
	}
	if contextID == "" {
		if strings.EqualFold(username, "sasboot") {
			return degradeForSasboot("No compute context found for sas.SASStudio or sas.studio")
		}
		check.Status = "UNHEALTHY"
		check.Message = "No compute context found for sas.SASStudio or sas.studio"
		return check
	}

	// Create a session. Some SAS builds reject a nil body with the
	// compute.session.request content type, so send an empty JSON object.
	// Only network errors and 502/503/504 are retried - other statuses (e.g.
	// 500) are reported immediately with the parsed error body so the real
	// cause is visible instead of being retried away.
	sessionID := ""
	var lastStatus int
	var lastErrDetail string
	for attempt := 1; attempt <= computeMaxRetries; attempt++ {
		printAPIProgress(fmt.Sprintf("Starting compute session (attempt %d/%d)...", attempt, computeMaxRetries))

		endpoint := fmt.Sprintf("%s/compute/contexts/%s/sessions", baseURL, contextID)
		req, reqErr := http.NewRequest(http.MethodPost, endpoint, strings.NewReader("{}"))
		if reqErr != nil {
			printAPIOutcome("UNHEALTHY")
			check.Status = "UNHEALTHY"
			check.Message = reqErr.Error()
			return check
		}
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/vnd.sas.compute.session.request+json")
		req.Header.Set("Accept", "application/vnd.sas.compute.session+json")

		resp, err := client.Do(req)
		if err != nil {
			lastErrDetail = sanitizeErrorText(err.Error())
			printAPIOutcome("DEGRADED")
			if attempt < computeMaxRetries {
				time.Sleep(5 * time.Second)
				continue
			}
			break
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		resp.Body.Close()
		lastStatus = resp.StatusCode

		if resp.StatusCode >= 200 && resp.StatusCode <= 399 {
			var payload struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(body, &payload); err == nil && payload.ID != "" {
				sessionID = payload.ID
				printAPIOutcome("HEALTHY")
				break
			}
			lastErrDetail = "Session response did not contain an id"
			printAPIOutcome("UNHEALTHY")
			break
		}

		lastErrDetail = parseSASError(body)
		printAPIOutcome("DEGRADED")
		if isRetryableComputeStatus(resp.StatusCode) && attempt < computeMaxRetries {
			time.Sleep(5 * time.Second)
			continue
		}
		break
	}

	check.HTTPStatus = lastStatus
	if sessionID == "" {
		check.ErrorDetail = lastErrDetail
		reason := fmt.Sprintf("Unable to create compute session (last status %d): %s", lastStatus, lastErrDetail)
		if strings.EqualFold(username, "sasboot") {
			return degradeForSasboot(reason)
		}
		check.Status = "UNHEALTHY"
		check.Message = reason
		return check
	}

	// Verify state, then always clean up the session that was created.
	state := ""
	for attempt := 1; attempt <= computeMaxRetries; attempt++ {
		endpoint := fmt.Sprintf("%s/compute/sessions/%s/state", baseURL, sessionID)
		resp, err := viyaRequest(client, http.MethodGet, endpoint, token, "", nil)
		if err != nil {
			if attempt < computeMaxRetries {
				time.Sleep(5 * time.Second)
			}
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		state = strings.ToLower(strings.TrimSpace(string(body)))
		if state == "running" || state == "idle" {
			break
		}
		if attempt < computeMaxRetries {
			time.Sleep(5 * time.Second)
		}
	}

	// Cleanup regardless of state result.
	delResp, err := viyaRequest(client, http.MethodDelete,
		fmt.Sprintf("%s/compute/sessions/%s", baseURL, sessionID), token, "", nil)
	if err == nil {
		io.Copy(io.Discard, delResp.Body)
		delResp.Body.Close()
	}

	if state == "running" || state == "idle" {
		check.Status = "HEALTHY"
		check.Message = fmt.Sprintf("Compute session started (state=%s) and stopped successfully", state)
	} else {
		check.Status = "UNHEALTHY"
		check.Message = fmt.Sprintf("Compute session did not reach running/idle state (state=%s)", state)
	}
	return check
}

// viyaCASValidation validates CAS servers, their state and their caslibs.
func viyaCASValidation(client *http.Client, baseURL, token string) (int, int, []ViyaAPICheck) {
	var checks []ViyaAPICheck

	serverCheck := ViyaAPICheck{Name: "CAS Servers Defined"}
	resp, err := viyaRequest(client, http.MethodGet, baseURL+"/casManagement/servers", token,
		"application/vnd.sas.collection+json", nil)
	if err != nil {
		serverCheck.Status = "UNHEALTHY"
		serverCheck.Message = err.Error()
		return 0, 0, append(checks, serverCheck)
	}
	body, _ := io.ReadAll(resp.Body)
	serverCheck.HTTPStatus = resp.StatusCode
	resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 399 {
		serverCheck.Status = "UNHEALTHY"
		serverCheck.Message = fmt.Sprintf("casManagement returned status %d", resp.StatusCode)
		return 0, 0, append(checks, serverCheck)
	}

	var payload struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		serverCheck.Status = "DEGRADED"
		serverCheck.Message = "Unable to parse CAS server list"
		return 0, 0, append(checks, serverCheck)
	}

	serverCount := len(payload.Items)
	serverCheck.Count = serverCount
	if serverCount == 0 {
		serverCheck.Status = "UNHEALTHY"
		serverCheck.Message = "No CAS servers defined"
		return 0, 0, append(checks, serverCheck)
	}
	serverCheck.Status = "HEALTHY"
	serverCheck.Message = fmt.Sprintf("%d CAS server(s) defined", serverCount)
	checks = append(checks, serverCheck)

	totalCaslibs := 0
	for _, server := range payload.Items {
		// Server state
		stateCheck := ViyaAPICheck{Name: fmt.Sprintf("CAS Server State (%s)", server.Name)}
		sResp, err := viyaRequest(client, http.MethodGet,
			fmt.Sprintf("%s/casManagement/servers/%s/state", baseURL, server.Name), token, "", nil)
		if err != nil {
			stateCheck.Status = "UNHEALTHY"
			stateCheck.Message = err.Error()
		} else {
			sBody, _ := io.ReadAll(sResp.Body)
			stateCheck.HTTPStatus = sResp.StatusCode
			sResp.Body.Close()

			state := strings.ToLower(strings.TrimSpace(string(sBody)))
			if state == "running" {
				stateCheck.Status = "HEALTHY"
				stateCheck.Message = "Server is running"
			} else {
				stateCheck.Status = "UNHEALTHY"
				stateCheck.Message = fmt.Sprintf("Server state is '%s'", state)
			}
		}
		checks = append(checks, stateCheck)

		// Caslibs - shares paginateCollectionCount with viyaCountCollection so
		// a missing "count" field is handled the same way (paginate rather
		// than trust a single default-limit page).
		libCheck := ViyaAPICheck{Name: fmt.Sprintf("CASLIBs (%s)", server.Name)}
		count, source, httpStatus, err := paginateCollectionCount(client, baseURL, token,
			fmt.Sprintf("/casManagement/servers/%s/caslibs", server.Name))
		libCheck.HTTPStatus = httpStatus
		libCheck.CountSource = source
		if err != nil {
			libCheck.Status = "UNHEALTHY"
			libCheck.Message = err.Error()
			checks = append(checks, libCheck)
			continue
		}

		totalCaslibs += count
		libCheck.Count = count
		switch {
		case count > 0 && source == countSourceField:
			libCheck.Status = "HEALTHY"
			libCheck.Message = fmt.Sprintf("%d caslib(s) loaded", count)
		case count > 0:
			libCheck.Status = "HEALTHY"
			libCheck.Message = fmt.Sprintf("%d caslib(s) loaded (paginated count - no 'count' field returned)", count)
		default:
			libCheck.Status = "UNHEALTHY"
			libCheck.Message = "No caslibs loaded"
		}
		checks = append(checks, libCheck)
	}

	return serverCount, totalCaslibs, checks
}

// routingTarget pairs a discovered host+path with the routing resource that
// produced it (Ingress rule or HTTPProxy virtualhost+route), so multi-tenant
// deployments are probed against the correct FQDN regardless of which
// routing mechanism the cluster uses.
type routingTarget struct {
	Host   string
	Path   string
	Source string // "ingress" or "httpproxy"
}

// httpProxyCondition mirrors one entry of a Contour HTTPProxy route or
// include's "conditions" list; only the path-prefix condition is needed here.
type httpProxyCondition struct {
	Prefix string `json:"prefix"`
}

// httpProxyList mirrors `kubectl get httpproxies.projectcontour.io -o json`
// (projectcontour.io/v1), keeping only the fields needed to build routes and
// evaluate resource health.
type httpProxyList struct {
	Items []httpProxyItem `json:"items"`
}

type httpProxyItem struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		VirtualHost *struct {
			FQDN string `json:"fqdn"`
		} `json:"virtualhost"`
		Routes []struct {
			Conditions []httpProxyCondition `json:"conditions"`
			Services   []struct {
				Name string `json:"name"`
				Port int    `json:"port"`
			} `json:"services"`
		} `json:"routes"`
		Includes []struct {
			Name       string               `json:"name"`
			Namespace  string               `json:"namespace"`
			Conditions []httpProxyCondition `json:"conditions"`
		} `json:"includes"`
	} `json:"spec"`
	Status struct {
		CurrentStatus string `json:"currentStatus"`
		Description   string `json:"description"`
	} `json:"status"`
}

// isMissingCRDError reports whether a kubectl error indicates the resource
// type/CRD simply isn't registered on the cluster (not using Contour), as
// opposed to a real failure that should be surfaced.
func isMissingCRDError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "doesn't have a resource type") ||
		strings.Contains(msg, "the server could not find the requested resource") ||
		strings.Contains(msg, "no matches for kind")
}

// fetchHTTPProxies lists Contour HTTPProxy resources in namespace. A missing
// CRD is not an error - it simply yields an empty list so ingress-only
// clusters are unaffected.
func fetchHTTPProxies(kubeconfig, namespace string) ([]httpProxyItem, error) {
	var list httpProxyList
	if err := kubectlJSON(kubeconfig, &list, "get", "httpproxies.projectcontour.io", "-n", namespace); err != nil {
		if isMissingCRDError(err) {
			return nil, nil
		}
		return nil, err
	}
	return list.Items, nil
}

// normalizeRoutingPrefix ensures a path starts with "/" and has no trailing
// slash, while preserving "/" itself as a valid path (a bare TrimSuffix would
// otherwise reduce it to "").
func normalizeRoutingPrefix(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// combineRoutingPrefix joins a parent HTTPProxy's prefix with a child
// route/include prefix, the same way Contour composes prefixes when
// resolving spec.includes.
func combineRoutingPrefix(parent, child string) string {
	if child == "" {
		return normalizeRoutingPrefix(parent)
	}
	if parent == "" || parent == "/" {
		return normalizeRoutingPrefix(child)
	}
	return normalizeRoutingPrefix(strings.TrimSuffix(parent, "/") + "/" + strings.TrimPrefix(child, "/"))
}

func firstPrefixCondition(conditions []httpProxyCondition) string {
	for _, c := range conditions {
		if c.Prefix != "" {
			return c.Prefix
		}
	}
	return ""
}

// resolveHTTPProxyRoutes walks each root HTTPProxy (one that defines a
// virtualhost) and follows spec.includes[] recursively, combining parent and
// child prefixes, to build the full set of routable paths. It also tallies
// HTTPProxy resource status (Valid is healthy; Invalid/Orphaned is unhealthy).
func resolveHTTPProxyRoutes(items []httpProxyItem) (targets []routingTarget, valid, invalid, orphaned int) {
	byName := make(map[string]httpProxyItem, len(items))
	for _, it := range items {
		byName[it.Metadata.Namespace+"/"+it.Metadata.Name] = it
	}

	for _, it := range items {
		switch strings.ToLower(strings.TrimSpace(it.Status.CurrentStatus)) {
		case "valid":
			valid++
		case "invalid":
			invalid++
		case "orphaned":
			orphaned++
		}
	}

	for _, root := range items {
		if root.Spec.VirtualHost == nil || root.Spec.VirtualHost.FQDN == "" {
			continue
		}
		// visited is scoped per root traversal and keyed by namespace/name,
		// so an include cycle (A includes B includes A) terminates instead
		// of recursing forever.
		visited := make(map[string]bool)
		walkHTTPProxy(root, root.Spec.VirtualHost.FQDN, "", byName, visited, &targets)
	}
	return targets, valid, invalid, orphaned
}

func walkHTTPProxy(item httpProxyItem, fqdn, parentPrefix string, byName map[string]httpProxyItem, visited map[string]bool, targets *[]routingTarget) {
	key := item.Metadata.Namespace + "/" + item.Metadata.Name
	if visited[key] {
		return
	}
	visited[key] = true

	for _, route := range item.Spec.Routes {
		prefix := combineRoutingPrefix(parentPrefix, firstPrefixCondition(route.Conditions))
		*targets = append(*targets, routingTarget{Host: fqdn, Path: prefix, Source: "httpproxy"})
	}

	for _, inc := range item.Spec.Includes {
		ns := inc.Namespace
		if ns == "" {
			ns = item.Metadata.Namespace
		}
		child, ok := byName[ns+"/"+inc.Name]
		if !ok {
			// Referenced HTTPProxy wasn't returned by the namespace-scoped
			// list (e.g. lives in another namespace) - skip rather than fail.
			continue
		}
		childPrefix := combineRoutingPrefix(parentPrefix, firstPrefixCondition(inc.Conditions))
		walkHTTPProxy(child, fqdn, childPrefix, byName, visited, targets)
	}
}

// checkViyaHTTPRouting discovers HTTP routes from both Kubernetes Ingress and
// Contour HTTPProxy resources and probes each one. It supports Ingress,
// HTTPProxy, or both, so clusters that use Contour instead of an Ingress
// controller are still validated instead of reporting "no paths found".
// 401/403 responses are informational (a non-admin test user is expected to
// be denied some paths) unless strictRouting is set, and every discovered
// path is probed against its own host, falling back to cfg.BaseURL.
func checkViyaHTTPRouting(client *http.Client, namespace, kubeconfig string, cfg ViyaAPIConfig, token string, strictRouting bool) ViyaAPICheck {
	check := ViyaAPICheck{Name: "HTTP Routing Endpoints"}

	if !cfg.Enabled || token == "" {
		check.Status = "SKIPPED"
		check.Message = "HTTP routing validation skipped (Viya API checks disabled)"
		return check
	}

	exclusions := loadExclusions()
	seen := make(map[string]bool)
	var targets []routingTarget

	var ingresses ingressList
	ingressErr := kubectlJSON(kubeconfig, &ingresses, "get", "ingress", "-n", namespace)
	if ingressErr == nil {
		for _, ing := range ingresses.Items {
			if isExcluded(ing.Metadata.Name, exclusions) {
				continue
			}
			for _, rule := range ing.Spec.Rules {
				for _, p := range rule.HTTP.Paths {
					path := p.Path
					// Strip regex capture groups, e.g. "/SASStudio(/|$)(.*)"
					if idx := strings.Index(path, "("); idx > 0 {
						path = path[:idx]
					}
					path = normalizeRoutingPrefix(path)
					key := "ingress|" + rule.Host + "|" + path
					if seen[key] {
						continue
					}
					seen[key] = true
					targets = append(targets, routingTarget{Host: rule.Host, Path: path, Source: "ingress"})
				}
			}
		}
	}
	ingressCount := len(targets)

	proxies, proxyErr := fetchHTTPProxies(kubeconfig, namespace)
	var httpProxyValid, httpProxyInvalid, httpProxyOrphaned int
	if proxyErr == nil && len(proxies) > 0 {
		proxyTargets, valid, invalid, orphaned := resolveHTTPProxyRoutes(proxies)
		httpProxyValid, httpProxyInvalid, httpProxyOrphaned = valid, invalid, orphaned
		for _, t := range proxyTargets {
			key := "httpproxy|" + t.Host + "|" + t.Path
			if seen[key] {
				continue
			}
			seen[key] = true
			targets = append(targets, t)
		}
	}
	httpProxyRouteCount := len(targets) - ingressCount

	// Only fail outright if both discovery mechanisms errored; either one
	// alone succeeding (e.g. HTTPProxy CRD absent) is a normal configuration.
	if ingressErr != nil && proxyErr != nil {
		check.Status = "DEGRADED"
		check.Message = fmt.Sprintf("Unable to list Ingress (%v) or HTTPProxy (%v) resources", ingressErr, proxyErr)
		return check
	}

	routingType := "none"
	switch {
	case ingressCount > 0 && httpProxyRouteCount > 0:
		routingType = "both"
	case ingressCount > 0:
		routingType = "ingress"
	case httpProxyRouteCount > 0:
		routingType = "httpproxy"
	}

	summary := &RoutingSummary{
		RoutingType:       routingType,
		IngressPaths:      ingressCount,
		HTTPProxyRoutes:   httpProxyRouteCount,
		HTTPProxyValid:    httpProxyValid,
		HTTPProxyInvalid:  httpProxyInvalid,
		HTTPProxyOrphaned: httpProxyOrphaned,
	}
	check.Routing = summary

	if len(targets) == 0 {
		check.Status = "DEGRADED"
		check.Message = "No ingress or HTTPProxy routes found to validate"
		return check
	}

	scheme := "https"
	if u, parseErr := url.Parse(cfg.BaseURL); parseErr == nil && u.Scheme != "" {
		scheme = u.Scheme
	}

	failures := 0
	authIssues := 0

	for _, t := range targets {
		target := cfg.BaseURL + t.Path
		// Use the route's own host when present - required for multi-tenant
		// deployments where paths resolve under distinct FQDNs.
		if t.Host != "" {
			target = scheme + "://" + t.Host + t.Path
		}

		resp, err := viyaRequest(client, http.MethodGet, target, token, "", nil)
		if err != nil {
			failures++
			continue
		}
		io.Copy(io.Discard, resp.Body)
		code := resp.StatusCode
		resp.Body.Close()

		switch {
		case code >= 200 && code <= 399:
			// healthy
		case code == 401 || code == 403:
			// A non-admin test user is expected to be denied some paths -
			// informational only, unless --strict-routing was requested.
			authIssues++
		default:
			failures++
		}
	}

	summary.TargetsProbed = len(targets)
	summary.AuthIssues = authIssues
	summary.Failures = failures
	check.Count = len(targets)

	invalidOrOrphaned := httpProxyInvalid + httpProxyOrphaned
	switch {
	case failures > 0 || invalidOrOrphaned > 0:
		check.Status = "UNHEALTHY"
		check.Message = fmt.Sprintf("%d of %d route(s) failed (%d auth-restricted, %d invalid/orphaned HTTPProxy)",
			failures, len(targets), authIssues, invalidOrOrphaned)
	case authIssues > 0 && strictRouting:
		check.Status = "DEGRADED"
		check.Message = fmt.Sprintf("All %d route(s) reachable, %d returned 401/403 (--strict-routing)",
			len(targets), authIssues)
	case authIssues > 0:
		check.Status = "HEALTHY"
		check.Message = fmt.Sprintf("All %d route(s) reachable, %d returned 401/403 (informational)",
			len(targets), authIssues)
	default:
		check.Status = "HEALTHY"
		check.Message = fmt.Sprintf("All %d route(s) reachable (%s routing)", len(targets), routingType)
	}

	return check
}

// ---------------------------------------------------------------------------
// Console rendering for the new sections
// ---------------------------------------------------------------------------

func displayWorkloadResults(wh WorkloadHealth) {
	fmt.Printf("%s%s┌─ VIYA WORKLOAD CHECK %s%s\n", ColorBold, ColorWhite, strings.Repeat("─", 56), ColorReset)

	color := getStatusColor(wh.Status)
	symbol := getStatusSymbol(wh.Status)

	fmt.Printf("│ %s Status:%s     %s%s %s%s\n", ColorBold, ColorReset, color, symbol, wh.Status, ColorReset)
	fmt.Printf("│ %s Namespace:%s  %s\n", ColorBold, ColorReset, wh.Namespace)
	fmt.Printf("│ %s Message:%s    %s\n", ColorBold, ColorReset, wh.Message)
	fmt.Printf("│\n")
	fmt.Printf("│ %s Resources:%s\n", ColorBold, ColorReset)

	for _, c := range []ResourceCheck{
		wh.Nodes, wh.Pods, wh.Deployments, wh.StatefulSets,
		wh.DaemonSets, wh.ReplicaSets, wh.Services, wh.PVCs,
	} {
		cColor := getStatusColor(c.Status)
		cSymbol := getStatusSymbol(c.Status)
		fmt.Printf("│   %s%s%s %-24s %d/%d ready", cColor, cSymbol, ColorReset, c.Kind, c.Ready, c.Total)
		if c.Excluded > 0 {
			fmt.Printf(" (%d excluded)", c.Excluded)
		}
		fmt.Printf("\n")

		limit := len(c.Failures)
		if limit > 5 {
			limit = 5
		}
		for i := 0; i < limit; i++ {
			f := c.Failures[i]
			detail := f.Details
			if detail == "" {
				detail = f.Reason
			}
			fmt.Printf("│       • %s (%s) %s\n", f.Name, f.Status, detail)
		}
		if len(c.Failures) > limit {
			fmt.Printf("│       • ... and %d more\n", len(c.Failures)-limit)
		}
	}

	fmt.Printf("%s└%s%s\n\n", ColorWhite, strings.Repeat("─", 79), ColorReset)
}

func displayDeploymentMetadataResults(meta DeploymentMetadataHealth) {
	fmt.Printf("%s%s┌─ DEPLOYMENT METADATA CHECK %s%s\n", ColorBold, ColorWhite, strings.Repeat("─", 50), ColorReset)

	color := getStatusColor(meta.Status)
	symbol := getStatusSymbol(meta.Status)

	fmt.Printf("│ %s Status:%s          %s%s %s%s\n", ColorBold, ColorReset, color, symbol, meta.Status, ColorReset)
	fmt.Printf("│ %s Cadence Name:%s    %s\n", ColorBold, ColorReset, meta.CadenceName)
	fmt.Printf("│ %s Cadence Version:%s %s\n", ColorBold, ColorReset, meta.CadenceVersion)
	if meta.CadenceRelease != "" {
		fmt.Printf("│ %s Cadence Release:%s %s\n", ColorBold, ColorReset, meta.CadenceRelease)
	}
	fmt.Printf("│ %s Consul Token:%s    %t\n", ColorBold, ColorReset, meta.ConsulTokenPresent)
	fmt.Printf("│ %s ConfigMaps:%s      %d\n", ColorBold, ColorReset, meta.ConfigMapsFound)
	fmt.Printf("│ %s Secrets:%s         %d\n", ColorBold, ColorReset, meta.SecretsFound)
	fmt.Printf("│ %s Message:%s         %s\n", ColorBold, ColorReset, meta.Message)
	fmt.Printf("%s└%s%s\n\n", ColorWhite, strings.Repeat("─", 79), ColorReset)
}

func displayViyaAPIResults(api ViyaAPIHealth) {
	fmt.Printf("%s%s┌─ VIYA APPLICATION CHECK %s%s\n", ColorBold, ColorWhite, strings.Repeat("─", 53), ColorReset)

	color := getStatusColor(api.Status)
	symbol := getStatusSymbol(api.Status)

	fmt.Printf("│ %s Status:%s   %s%s %s%s\n", ColorBold, ColorReset, color, symbol, api.Status, ColorReset)
	fmt.Printf("│ %s URL:%s      %s\n", ColorBold, ColorReset, api.BaseURL)
	fmt.Printf("│ %s Message:%s  %s\n", ColorBold, ColorReset, api.Message)

	if len(api.Checks) > 0 {
		fmt.Printf("│\n")
		fmt.Printf("│ %s API Checks:%s\n", ColorBold, ColorReset)
		for _, c := range api.Checks {
			cColor := getStatusColor(c.Status)
			cSymbol := getStatusSymbol(c.Status)
			fmt.Printf("│   %s%s%s %-32s %s\n", cColor, cSymbol, ColorReset, c.Name, c.Message)
			if c.Routing != nil {
				r := c.Routing
				fmt.Printf("│       routing type: %s | ingress paths: %d | httpproxy routes: %d | probed: %d\n",
					r.RoutingType, r.IngressPaths, r.HTTPProxyRoutes, r.TargetsProbed)
				fmt.Printf("│       httpproxy status: valid=%d invalid=%d orphaned=%d\n",
					r.HTTPProxyValid, r.HTTPProxyInvalid, r.HTTPProxyOrphaned)
			}
			if c.ComputeContext != nil {
				fmt.Printf("│       compute context: %s (%s)\n", c.ComputeContext.Name, c.ComputeContext.ID)
			}
			if c.ErrorDetail != "" {
				fmt.Printf("│       error detail: %s\n", c.ErrorDetail)
			}
		}

		fmt.Printf("│\n")
		fmt.Printf("│ %s Content Inventory (restore baseline):%s\n", ColorBold, ColorReset)
		fmt.Printf("│   Compute contexts: %d\n", api.Inventory.ComputeContexts)
		fmt.Printf("│   CAS servers:      %d\n", api.Inventory.CASServers)
		fmt.Printf("│   CASLIBs:          %d\n", api.Inventory.CASLibs)
		fmt.Printf("│   QKBs:             %d\n", api.Inventory.QKBs)
		fmt.Printf("│   Reports:          %d\n", api.Inventory.Reports)
		fmt.Printf("│   Folders:          %d\n", api.Inventory.Folders)
		fmt.Printf("│   Users:            %d\n", api.Inventory.Users)
		fmt.Printf("│   Groups:           %d\n", api.Inventory.Groups)
	}

	fmt.Printf("%s└%s%s\n\n", ColorWhite, strings.Repeat("─", 79), ColorReset)
}

// generateViyaValidationRecommendations produces guidance for the new checks.
func generateViyaValidationRecommendations(wh WorkloadHealth, meta DeploymentMetadataHealth, api ViyaAPIHealth) []string {
	var recs []string

	if wh.Status == "UNHEALTHY" {
		recs = append(recs, "Viya workloads are not fully ready - inspect failing pods with 'kubectl describe' and review events")
	}
	if wh.Services.NotReady > 0 {
		recs = append(recs, "Some services have missing or not-ready endpoints - verify the backing pods are running")
	}
	if wh.PVCs.NotReady > 0 {
		recs = append(recs, "Unbound PVCs detected - verify the storage class and provisioner before using this deployment")
	}
	if meta.Status == "UNHEALTHY" {
		recs = append(recs, "Deployment metadata is missing - confirm the Viya deployment completed successfully")
	}
	if api.Status == "UNHEALTHY" {
		recs = append(recs, "SAS Viya application APIs are failing - validate SASLogon, HTTP routing (Ingress/HTTPProxy) and CAS availability")
	}
	if api.Executed && api.Inventory.Reports == 0 {
		recs = append(recs, "No reports returned - if this follows a restore, investigate content service data integrity")
	}

	return recs
}
