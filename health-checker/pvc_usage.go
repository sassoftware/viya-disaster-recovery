package main

// pvc_usage.go
//
// PVC capacity and on-disk usage validation. Extends the bind-status-only
// check in viya_health_checker.go with requested/provisioned size, storage
// class, access modes and (optionally) actual disk usage collected via
// `kubectl exec ... df`. Intended to be run before a Velero backup and
// compared against a post-restore run to detect data loss.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// PVCUsageOptions controls whether exec-based usage collection runs and the
// thresholds used to classify usage percentage.
type PVCUsageOptions struct {
	CollectUsage    bool
	WarnPercent     float64
	CriticalPercent float64
}

// PVCDetail captures capacity, binding and (optionally) usage for one PVC.
type PVCDetail struct {
	Name            string   `json:"name"`
	Phase           string   `json:"phase"`
	RequestedSize   string   `json:"requested_size,omitempty"`
	ProvisionedSize string   `json:"provisioned_size,omitempty"`
	StorageClass    string   `json:"storage_class,omitempty"`
	AccessModes     []string `json:"access_modes,omitempty"`
	VolumeName      string   `json:"volume_name,omitempty"`
	UsagePod        string   `json:"usage_pod,omitempty"`
	UsageContainer  string   `json:"usage_container,omitempty"`
	MountPath       string   `json:"mount_path,omitempty"`
	UsedSize        string   `json:"used_size,omitempty"`
	// UsedPercent has no omitempty: 0% usage is a meaningful, real value and
	// must not be indistinguishable from "not collected" in the JSON report.
	UsedPercent float64 `json:"used_percent"`
	Status      string  `json:"status"`
	Message     string  `json:"message,omitempty"`
}

// PVCUsageHealth aggregates the per-PVC capacity/usage results.
type PVCUsageHealth struct {
	Status         string      `json:"status"`
	Message        string      `json:"message"`
	Namespace      string      `json:"namespace"`
	UsageCollected bool        `json:"usage_collected"`
	Total          int         `json:"total"`
	Healthy        int         `json:"healthy"`
	Degraded       int         `json:"degraded"`
	Unhealthy      int         `json:"unhealthy"`
	Unknown        int         `json:"unknown"`
	PVCs           []PVCDetail `json:"pvcs"`
}

// pvcDetailList mirrors `kubectl get pvc -o json`, including the capacity,
// request and storage-class fields the bind-status-only check ignores.
type pvcDetailList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			AccessModes      []string `json:"accessModes"`
			StorageClassName string   `json:"storageClassName"`
			VolumeName       string   `json:"volumeName"`
			Resources        struct {
				Requests struct {
					Storage string `json:"storage"`
				} `json:"requests"`
			} `json:"resources"`
		} `json:"spec"`
		Status struct {
			Phase    string `json:"phase"`
			Capacity struct {
				Storage string `json:"storage"`
			} `json:"capacity"`
		} `json:"status"`
	} `json:"items"`
}

// podMountList mirrors `kubectl get pods -o json`, capturing just enough of
// spec/status to match a PVC claim name to a running, ready mounting pod.
type podMountList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Volumes []struct {
				Name                  string `json:"name"`
				PersistentVolumeClaim *struct {
					ClaimName string `json:"claimName"`
				} `json:"persistentVolumeClaim,omitempty"`
			} `json:"volumes"`
			Containers []struct {
				Name         string `json:"name"`
				VolumeMounts []struct {
					Name      string `json:"name"`
					MountPath string `json:"mountPath"`
				} `json:"volumeMounts"`
			} `json:"containers"`
		} `json:"spec"`
		Status struct {
			Phase             string `json:"phase"`
			ContainerStatuses []struct {
				Name  string `json:"name"`
				Ready bool   `json:"ready"`
			} `json:"containerStatuses"`
		} `json:"status"`
	} `json:"items"`
}

type pvcMountInfo struct {
	Pod       string
	Container string
	MountPath string
}

// checkPVCUsage lists every PVC in the namespace and, when enabled, collects
// on-disk usage from the first running/ready pod that mounts each claim.
func checkPVCUsage(namespace, kubeconfig string, opts PVCUsageOptions) PVCUsageHealth {
	health := PVCUsageHealth{Namespace: namespace, UsageCollected: opts.CollectUsage}

	var claims pvcDetailList
	if err := kubectlJSON(kubeconfig, &claims, "get", "pvc", "-n", namespace); err != nil {
		health.Status = "UNHEALTHY"
		health.Message = "Failed to list PVCs: " + err.Error()
		return health
	}

	var mounts map[string]pvcMountInfo
	if opts.CollectUsage {
		mounts = findPVCMountPods(namespace, kubeconfig)
	}

	for _, c := range claims.Items {
		detail := PVCDetail{
			Name:            c.Metadata.Name,
			Phase:           c.Status.Phase,
			RequestedSize:   c.Spec.Resources.Requests.Storage,
			ProvisionedSize: c.Status.Capacity.Storage,
			StorageClass:    c.Spec.StorageClassName,
			AccessModes:     c.Spec.AccessModes,
			VolumeName:      c.Spec.VolumeName,
		}

		if !strings.EqualFold(c.Status.Phase, "Bound") {
			detail.Status = "UNHEALTHY"
			detail.Message = fmt.Sprintf("PVC is not bound (phase=%s)", c.Status.Phase)
			health.Unhealthy++
			health.Total++
			health.PVCs = append(health.PVCs, detail)
			continue
		}

		if !opts.CollectUsage {
			detail.Status = "UNKNOWN"
			detail.Message = "Usage collection disabled (rerun with --pvc-usage)"
			health.Unknown++
			health.Total++
			health.PVCs = append(health.PVCs, detail)
			continue
		}

		mount, ok := mounts[c.Metadata.Name]
		if !ok {
			detail.Status = "UNKNOWN"
			detail.Message = "No running pod currently mounts this PVC"
			health.Unknown++
			health.Total++
			health.PVCs = append(health.PVCs, detail)
			continue
		}

		detail.UsagePod = mount.Pod
		detail.UsageContainer = mount.Container
		detail.MountPath = mount.MountPath

		usedBytes, totalBytes, percent, err := collectPVCUsage(namespace, kubeconfig, mount)
		if err != nil {
			detail.Status = "UNKNOWN"
			detail.Message = "Failed to collect usage: " + err.Error()
			health.Unknown++
			health.Total++
			health.PVCs = append(health.PVCs, detail)
			continue
		}

		detail.UsedSize = formatBytes(usedBytes)
		if detail.ProvisionedSize == "" && totalBytes > 0 {
			detail.ProvisionedSize = formatBytes(totalBytes)
		}
		detail.UsedPercent = percent

		switch {
		case percent >= opts.CriticalPercent:
			detail.Status = "UNHEALTHY"
			detail.Message = fmt.Sprintf("Usage %.1f%% >= critical threshold %.1f%%", percent, opts.CriticalPercent)
			health.Unhealthy++
		case percent >= opts.WarnPercent:
			detail.Status = "DEGRADED"
			detail.Message = fmt.Sprintf("Usage %.1f%% >= warning threshold %.1f%%", percent, opts.WarnPercent)
			health.Degraded++
		default:
			detail.Status = "HEALTHY"
			detail.Message = fmt.Sprintf("Usage %.1f%% is within thresholds", percent)
			health.Healthy++
		}
		health.Total++
		health.PVCs = append(health.PVCs, detail)
	}

	switch {
	case health.Unhealthy > 0:
		health.Status = "UNHEALTHY"
	case health.Degraded > 0:
		health.Status = "DEGRADED"
	case health.Total > 0 && health.Healthy == 0:
		// Every PVC is UNKNOWN (usage collection disabled or no mounting
		// pod) - report that plainly rather than a misleading HEALTHY.
		health.Status = "UNKNOWN"
	default:
		health.Status = "HEALTHY"
	}
	health.Message = fmt.Sprintf("%d PVC(s): %d healthy, %d degraded, %d unhealthy, %d unknown",
		health.Total, health.Healthy, health.Degraded, health.Unhealthy, health.Unknown)

	return health
}

// pvcUsageToResourceCheck adapts the consolidated PVC capacity/usage result to
// the ResourceCheck shape used by WorkloadHealth, so there is a single PVC
// section instead of two checks that could disagree (item 8 consolidation:
// checkPersistentVolumeClaims has been removed in favor of this superset).
func pvcUsageToResourceCheck(h PVCUsageHealth) ResourceCheck {
	check := ResourceCheck{
		Kind:     "PersistentVolumeClaims",
		Total:    h.Total,
		Ready:    h.Healthy,
		NotReady: h.Unhealthy + h.Degraded,
		Excluded: h.Unknown,
		Status:   h.Status,
		Message:  h.Message,
	}
	for _, p := range h.PVCs {
		if p.Status == "HEALTHY" || p.Status == "UNKNOWN" {
			continue
		}
		check.Failures = append(check.Failures, ResourceFailure{
			Name:    p.Name,
			Status:  p.Status,
			Details: p.Message,
		})
	}
	return check
}

// findPVCMountPods maps claim name -> the first running pod/container/mountPath
// found mounting it, preferring containers reported ready.
func findPVCMountPods(namespace, kubeconfig string) map[string]pvcMountInfo {
	result := make(map[string]pvcMountInfo)

	var pods podMountList
	if err := kubectlJSON(kubeconfig, &pods, "get", "pods", "-n", namespace); err != nil {
		return result
	}

	for _, p := range pods.Items {
		if !strings.EqualFold(p.Status.Phase, "Running") {
			continue
		}

		readyContainers := make(map[string]bool)
		for _, cs := range p.Status.ContainerStatuses {
			readyContainers[cs.Name] = cs.Ready
		}

		volumeToClaim := make(map[string]string)
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				volumeToClaim[v.Name] = v.PersistentVolumeClaim.ClaimName
			}
		}

		for _, c := range p.Spec.Containers {
			if !readyContainers[c.Name] {
				continue
			}
			for _, vm := range c.VolumeMounts {
				claim, ok := volumeToClaim[vm.Name]
				if !ok {
					continue
				}
				if _, exists := result[claim]; exists {
					continue
				}
				result[claim] = pvcMountInfo{
					Pod:       p.Metadata.Name,
					Container: c.Name,
					MountPath: vm.MountPath,
				}
			}
		}
	}

	return result
}

// collectPVCUsage runs `df -B1` inside the mounting pod/container and parses the result.
func collectPVCUsage(namespace, kubeconfig string, mount pvcMountInfo) (usedBytes, totalBytes int64, percent float64, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), kubectlTimeout)
	defer cancel()

	args := []string{"exec", "-n", namespace, mount.Pod, "-c", mount.Container, "--", "df", "-B1", mount.MountPath}
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	if kubeconfig != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	}

	out, execErr := cmd.Output()
	if execErr != nil {
		return 0, 0, 0, fmt.Errorf("df exec failed: %w", execErr)
	}

	return parseDfOutput(string(out))
}

// parseDfOutput reads the numeric fields from the end of the output so that a
// long filesystem name wrapping onto its own line does not shift the columns.
func parseDfOutput(output string) (usedBytes, totalBytes int64, percent float64, err error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		return 0, 0, 0, fmt.Errorf("unexpected df output: %q", output)
	}

	var fields []string
	for _, line := range lines[1:] {
		fields = append(fields, strings.Fields(line)...)
	}
	if len(fields) < 5 {
		return 0, 0, 0, fmt.Errorf("unable to parse df fields: %q", output)
	}

	n := len(fields)
	usePercentStr := strings.TrimSuffix(fields[n-2], "%")
	usedStr := fields[n-4]
	totalStr := fields[n-5]

	usedBytes, uErr := strconv.ParseInt(usedStr, 10, 64)
	totalBytes, tErr := strconv.ParseInt(totalStr, 10, 64)
	if uErr != nil || tErr != nil {
		return 0, 0, 0, fmt.Errorf("unable to parse df numeric fields: %q", output)
	}

	percent, pErr := strconv.ParseFloat(usePercentStr, 64)
	if pErr != nil && totalBytes > 0 {
		percent = float64(usedBytes) / float64(totalBytes) * 100
	}

	return usedBytes, totalBytes, percent, nil
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// generatePVCUsageRecommendations produces guidance based on PVC capacity/usage results.
func generatePVCUsageRecommendations(h PVCUsageHealth) []string {
	var recs []string
	if h.Unhealthy > 0 {
		recs = append(recs, "One or more PVCs are unbound or over the critical usage threshold - free space or expand the volume before backup/restore")
	}
	if h.Degraded > 0 {
		recs = append(recs, "One or more PVCs are approaching the usage warning threshold - monitor disk usage")
	}
	if !h.UsageCollected {
		recs = append(recs, "PVC usage collection was skipped - rerun with --pvc-usage to capture on-disk usage before a Velero backup")
	}
	return recs
}

// displayPVCUsageResults renders the PVC capacity/usage table to the console.
func displayPVCUsageResults(health PVCUsageHealth) {
	fmt.Printf("%s%s┌─ PVC CAPACITY & USAGE CHECK %s%s\n", ColorBold, ColorWhite, strings.Repeat("─", 49), ColorReset)

	color := getStatusColor(health.Status)
	symbol := getStatusSymbol(health.Status)

	fmt.Printf("│ %s Status:%s  %s%s %s%s\n", ColorBold, ColorReset, color, symbol, health.Status, ColorReset)
	fmt.Printf("│ %s Message:%s %s\n", ColorBold, ColorReset, health.Message)
	fmt.Printf("│\n")

	for _, p := range health.PVCs {
		pColor := getStatusColor(p.Status)
		pSymbol := getStatusSymbol(p.Status)
		usage := "n/a"
		if p.UsedSize != "" {
			usage = fmt.Sprintf("%s / %s (%.1f%%)", p.UsedSize, p.ProvisionedSize, p.UsedPercent)
		}
		fmt.Printf("│   %s%s%s %-30s req=%-8s prov=%-8s class=%-15s usage=%s\n",
			pColor, pSymbol, ColorReset, p.Name, p.RequestedSize, p.ProvisionedSize, p.StorageClass, usage)
		if p.Message != "" && p.Status != "HEALTHY" {
			fmt.Printf("│       • %s\n", p.Message)
		}
	}

	fmt.Printf("%s└%s%s\n\n", ColorWhite, strings.Repeat("─", 79), ColorReset)
}
