package main

// main.go
//
// Entry point for the SAS Viya Health Checker. Prompts for kubeconfig/namespace,
// runs the original checks (readiness pod, CAS, PostgreSQL, Velero - defined in
// legacy_checks.go) plus the post-installation checks (workload/metadata/API -
// defined in viya_health_checker.go, and PVC capacity/usage - defined in
// pvc_usage.go), then renders a console report and a timestamped JSON report.

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	Version = "1.0.0"
	AppName = "SAS Viya Health Checker"

	// ANSI color codes for CLI output
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorCyan   = "\033[36m"
	ColorWhite  = "\033[37m"
	ColorBold   = "\033[1m"

	// Symbols for visual indicators
	SymbolCheck = "✓"
	SymbolWarn  = "⚠"
	SymbolError = "✗"
	SymbolInfo  = "ℹ"
)

// HealthReport is the top-level JSON report produced by a run.
type HealthReport struct {
	Summary         Summary                  `json:"summary"`
	SystemInfo      SystemInfo               `json:"system_info"`
	ReadinessCheck  ReadinessHealth          `json:"readiness_check"`
	CASCheck        CASHealth                `json:"cas_check"`
	PostgreSQLCheck PostgreSQLHealth         `json:"postgresql_check"`
	VeleroCheck     VeleroHealth             `json:"velero_check"`
	WorkloadCheck   WorkloadHealth           `json:"workload_check"`
	MetadataCheck   DeploymentMetadataHealth `json:"metadata_check"`
	ViyaAPICheck    ViyaAPIHealth            `json:"viya_api_check"`
	PVCUsageCheck   PVCUsageHealth           `json:"pvc_usage_check"`
	Recommendations []string                 `json:"recommendations,omitempty"`
}

type Summary struct {
	OverallStatus   string    `json:"overall_status"`
	HealthScore     string    `json:"health_score"`
	ChecksExecuted  int       `json:"checks_executed"`
	ChecksPassed    int       `json:"checks_passed"`
	ChecksWarning   int       `json:"checks_warning"`
	ChecksFailed    int       `json:"checks_failed"`
	Timestamp       time.Time `json:"timestamp"`
	ExecutionTimeMs int64     `json:"execution_time_ms"`
}

type SystemInfo struct {
	Namespace      string `json:"namespace"`
	KubeconfigPath string `json:"kubeconfig_path"`
	ToolVersion    string `json:"tool_version"`
	CheckerName    string `json:"checker_name"`
}

func main() {
	startTime := time.Now()

	help := flag.Bool("help", false, "Show help")
	pvcUsage := flag.Bool("pvc-usage", false, "Enable exec-based PVC usage collection (off by default, it is slow)")
	pvcWarn := flag.Float64("pvc-usage-warn", 85, "Percent threshold for DEGRADED PVC usage")
	pvcCritical := flag.Float64("pvc-usage-critical", 95, "Percent threshold for UNHEALTHY PVC usage")
	strictRouting := flag.Bool("strict-routing", false, "Treat HTTP routing (Ingress/HTTPProxy) 401/403 responses as DEGRADED instead of informational")
	insecureSkipTLSVerify := flag.Bool("insecure-skip-tls-verify", false, "Skip TLS certificate verification for SAS Viya API calls (insecure)")
	caCert := flag.String("ca-cert", "", "Path to a CA bundle (PEM) used to verify the SAS Viya API's TLS certificate")
	viyaURLFlag := flag.String("viya-url", "", "SAS Viya base URL for API validation (overrides VIYA_URL env var)")
	viyaUserFlag := flag.String("viya-user", "", "SAS Viya username for API validation (overrides VIYA_USER env var)")
	viyaAPITimeout := flag.Duration("viya-api-timeout", 5*time.Minute, "Timeout for each individual SAS Viya API call")
	flag.Parse()

	if *help {
		printHeader()
		fmt.Printf("%s%s%s v%s\n\n", ColorBold, AppName, ColorReset, Version)
		fmt.Printf("Usage: %s [flags]\n", os.Args[0])
		fmt.Println("This tool will prompt you for kubeconfig path and namespace.")
		fmt.Println("A timestamped JSON report will be automatically generated.")
		flag.PrintDefaults()
		return
	}

	printHeader()

	kubeconfig := promptForInput("Enter kubeconfig file path (or press Enter for default ~/.kube/config): ")
	if kubeconfig == "" {
		kubeconfig = os.Getenv("HOME") + "/.kube/config"
	}

	fmt.Printf("\n%s %sValidating kubeconfig...%s", SymbolInfo, ColorBlue, ColorReset)
	if err := validateKubeconfig(kubeconfig); err != nil {
		fmt.Printf(" %s%s%s\n", ColorRed, SymbolError, ColorReset)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf(" %s%s%s\n", ColorGreen, SymbolCheck, ColorReset)

	namespace := promptForInput("Enter Kubernetes namespace: ")
	if namespace == "" {
		fmt.Fprintf(os.Stderr, "Error: Namespace cannot be empty\n")
		os.Exit(1)
	}

	fmt.Printf("%s %sValidating namespace...%s", SymbolInfo, ColorBlue, ColorReset)
	if err := validateNamespace(namespace, kubeconfig); err != nil {
		fmt.Printf(" %s%s%s\n", ColorRed, SymbolError, ColorReset)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf(" %s%s%s\n", ColorGreen, SymbolCheck, ColorReset)

	fmt.Printf("\n%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n", ColorCyan, ColorReset)
	fmt.Printf("%s%s               EXECUTING SAS VIYA HEALTH CHECKS               %s%s\n", ColorBold, ColorCyan, ColorReset, ColorReset)
	fmt.Printf("%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n\n", ColorCyan, ColorReset)

	report := HealthReport{
		SystemInfo: SystemInfo{
			Namespace:      namespace,
			KubeconfigPath: kubeconfig,
			ToolVersion:    Version,
			CheckerName:    AppName,
		},
	}

	checksExecuted, checksPassed, checksWarning, checksFailed := 0, 0, 0, 0
	// SKIPPED/UNKNOWN checks are intentionally excluded so they neither pass
	// nor lower the health score.
	countStatus := func(status string) {
		if status == "SKIPPED" || status == "UNKNOWN" {
			return
		}
		checksExecuted++
		switch status {
		case "HEALTHY":
			checksPassed++
		case "DEGRADED":
			checksWarning++
		case "UNHEALTHY":
			checksFailed++
		}
	}
	printOutcome := func(status string) {
		fmt.Printf(" %s%s%s\n", getStatusColor(status), getStatusSymbol(status), ColorReset)
	}

	// --- Original checks: readiness, CAS, PostgreSQL, Velero ---

	fmt.Printf("%s %sChecking SAS Readiness Pod...%s", SymbolInfo, ColorBlue, ColorReset)
	report.ReadinessCheck = checkReadinessPod(namespace, kubeconfig)
	countStatus(report.ReadinessCheck.Status)
	printOutcome(report.ReadinessCheck.Status)

	fmt.Printf("%s %sChecking CAS Services...%s", SymbolInfo, ColorBlue, ColorReset)
	report.CASCheck = checkCASHealth(namespace, kubeconfig)
	countStatus(report.CASCheck.Status)
	printOutcome(report.CASCheck.Status)

	fmt.Printf("%s %sChecking PostgreSQL Clusters...%s", SymbolInfo, ColorBlue, ColorReset)
	report.PostgreSQLCheck = checkPostgreSQLHealth(namespace, kubeconfig)
	countStatus(report.PostgreSQLCheck.Status)
	printOutcome(report.PostgreSQLCheck.Status)

	fmt.Printf("%s %sChecking Velero Backups...%s", SymbolInfo, ColorBlue, ColorReset)
	report.VeleroCheck = checkVeleroHealth(namespace, kubeconfig)
	countStatus(report.VeleroCheck.Status)
	printOutcome(report.VeleroCheck.Status)

	// --- Post-installation checks: PVC usage, workload, metadata, application API ---
	// PVC usage is collected first so its result can be adapted into a single
	// PVCs section inside the workload check (item 8 consolidation), instead
	// of running the (possibly slow) exec-based collection twice.

	fmt.Printf("%s %sChecking PVC capacity and usage...%s", SymbolInfo, ColorBlue, ColorReset)
	report.PVCUsageCheck = checkPVCUsage(namespace, kubeconfig, PVCUsageOptions{
		CollectUsage:    *pvcUsage,
		WarnPercent:     *pvcWarn,
		CriticalPercent: *pvcCritical,
	})
	countStatus(report.PVCUsageCheck.Status)
	printOutcome(report.PVCUsageCheck.Status)

	fmt.Printf("%s %sChecking Viya workload health...%s", SymbolInfo, ColorBlue, ColorReset)
	report.WorkloadCheck = checkViyaWorkloadHealth(namespace, kubeconfig, report.PVCUsageCheck)
	countStatus(report.WorkloadCheck.Status)
	printOutcome(report.WorkloadCheck.Status)

	fmt.Printf("%s %sChecking deployment metadata...%s", SymbolInfo, ColorBlue, ColorReset)
	report.MetadataCheck = checkDeploymentMetadata(namespace, kubeconfig)
	countStatus(report.MetadataCheck.Status)
	printOutcome(report.MetadataCheck.Status)

	// Credentials are collected before the progress header prints, so the
	// user isn't left staring at a bare "Checking..." line while prompted.
	viyaCfg := LoadViyaAPIConfig(*viyaURLFlag, *viyaUserFlag, *viyaAPITimeout)
	viyaCfg.InsecureSkipTLSVerify = *insecureSkipTLSVerify
	viyaCfg.CACertPath = *caCert

	fmt.Printf("%s %sChecking SAS Viya application APIs...%s\n", SymbolInfo, ColorBlue, ColorReset)
	report.ViyaAPICheck = checkViyaAPIHealth(viyaCfg, namespace, kubeconfig, *strictRouting)
	countStatus(report.ViyaAPICheck.Status)
	fmt.Printf("%s %sSAS Viya application API checks complete...%s", SymbolInfo, ColorBlue, ColorReset)
	printOutcome(report.ViyaAPICheck.Status)

	fmt.Printf("\n[DEBUG] Final counts - Executed: %d, Passed: %d, Warning: %d, Failed: %d\n\n",
		checksExecuted, checksPassed, checksWarning, checksFailed)

	executionTime := time.Since(startTime).Milliseconds()
	overallStatus := determineOverallStatus(
		report.ReadinessCheck.Status,
		report.CASCheck.Status,
		report.PostgreSQLCheck.Status,
		report.VeleroCheck.Status,
		report.WorkloadCheck.Status,
		report.MetadataCheck.Status,
		report.ViyaAPICheck.Status,
		report.PVCUsageCheck.Status,
	)
	healthScore := calculateHealthScore(checksPassed, checksWarning, checksFailed, checksExecuted)

	report.Summary = Summary{
		OverallStatus:   overallStatus,
		HealthScore:     healthScore,
		ChecksExecuted:  checksExecuted,
		ChecksPassed:    checksPassed,
		ChecksWarning:   checksWarning,
		ChecksFailed:    checksFailed,
		Timestamp:       time.Now(),
		ExecutionTimeMs: executionTime,
	}

	report.Recommendations = append(generateLegacyRecommendations(report),
		append(generateViyaValidationRecommendations(report.WorkloadCheck, report.MetadataCheck, report.ViyaAPICheck),
			generatePVCUsageRecommendations(report.PVCUsageCheck)...)...)
	if len(report.Recommendations) == 0 {
		report.Recommendations = []string{"System appears healthy - continue regular monitoring"}
	}

	displaySummary(report)
	displayLegacyResults(report)
	displayWorkloadResults(report.WorkloadCheck)
	displayDeploymentMetadataResults(report.MetadataCheck)
	displayViyaAPIResults(report.ViyaAPICheck)
	displayPVCUsageResults(report.PVCUsageCheck)
	displayRecommendations(report)

	saveJSONReport(report, namespace)

	if overallStatus == "UNHEALTHY" {
		os.Exit(1)
	}
}

func printHeader() {
	fmt.Printf("\n%s┌─────────────────────────────────────────────────────────────────────────────────┐%s\n", ColorCyan, ColorReset)
	fmt.Printf("%s│%s%s                        SAS VIYA HEALTH CHECKER                        %s%s          │%s\n", ColorCyan, ColorBold, ColorWhite, ColorReset, ColorCyan, ColorReset)
	fmt.Printf("%s│%s                                  v%s                                    %s     │%s\n", ColorCyan, ColorWhite, Version, ColorCyan, ColorReset)
	fmt.Printf("%s└─────────────────────────────────────────────────────────────────────────────────┘%s\n\n", ColorCyan, ColorReset)
}

func displaySummary(report HealthReport) {
	fmt.Printf("\n%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n", ColorCyan, ColorReset)
	fmt.Printf("%s%s                            HEALTH CHECK RESULTS                          %s%s\n", ColorBold, ColorCyan, ColorReset, ColorReset)
	fmt.Printf("%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n\n", ColorCyan, ColorReset)

	fmt.Printf("%s%s┌─ SUMMARY %s%s\n", ColorBold, ColorWhite, strings.Repeat("─", 68), ColorReset)

	statusColor := getStatusColor(report.Summary.OverallStatus)
	statusSymbol := getStatusSymbol(report.Summary.OverallStatus)

	fmt.Printf("│ %s Overall Status:%s %s%s %s %s%s\n", ColorBold, ColorReset, statusColor, statusSymbol, report.Summary.OverallStatus, ColorReset, statusColor)
	fmt.Printf("│ %s Health Score:%s  %s\n", ColorBold, ColorReset, report.Summary.HealthScore)
	fmt.Printf("│ %s Namespace:%s     %s\n", ColorBold, ColorReset, report.SystemInfo.Namespace)
	fmt.Printf("│ %s Timestamp:%s     %s\n", ColorBold, ColorReset, report.Summary.Timestamp.Format("2006-01-02 15:04:05 MST"))
	fmt.Printf("│ %s Execution Time:%s %dms\n", ColorBold, ColorReset, report.Summary.ExecutionTimeMs)
	fmt.Printf("│\n")
	fmt.Printf("│ %s Checks Summary:%s\n", ColorBold, ColorReset)
	fmt.Printf("│   %s%s%s Passed:   %d/%d\n", ColorGreen, SymbolCheck, ColorReset, report.Summary.ChecksPassed, report.Summary.ChecksExecuted)
	fmt.Printf("│   %s%s%s Warning:  %d/%d\n", ColorYellow, SymbolWarn, ColorReset, report.Summary.ChecksWarning, report.Summary.ChecksExecuted)
	fmt.Printf("│   %s%s%s Failed:   %d/%d\n", ColorRed, SymbolError, ColorReset, report.Summary.ChecksFailed, report.Summary.ChecksExecuted)
	fmt.Printf("%s└%s%s\n\n", ColorWhite, strings.Repeat("─", 79), ColorReset)
}

func displayRecommendations(report HealthReport) {
	if len(report.Recommendations) == 0 {
		return
	}
	fmt.Printf("%s%s┌─ RECOMMENDATIONS %s%s\n", ColorBold, ColorWhite, strings.Repeat("─", 60), ColorReset)
	for i, rec := range report.Recommendations {
		fmt.Printf("│ %d. %s\n", i+1, rec)
	}
	fmt.Printf("%s└%s%s\n\n", ColorWhite, strings.Repeat("─", 79), ColorReset)
}

// determineOverallStatus rolls up check statuses; SKIPPED/UNKNOWN never escalate the result.
func determineOverallStatus(statuses ...string) string {
	return worstStatus(statuses...)
}

func calculateHealthScore(passed, warning, failed, total int) string {
	if total == 0 {
		return "N/A"
	}
	score := float64(passed*100+warning*50) / float64(total*100) * 100
	return fmt.Sprintf("%.1f%%", score)
}

func getStatusColor(status string) string {
	switch status {
	case "HEALTHY":
		return ColorGreen
	case "DEGRADED":
		return ColorYellow
	case "UNHEALTHY":
		return ColorRed
	case "SKIPPED", "UNKNOWN":
		return ColorCyan
	default:
		return ColorReset
	}
}

func getStatusSymbol(status string) string {
	switch status {
	case "HEALTHY":
		return SymbolCheck
	case "DEGRADED":
		return SymbolWarn
	case "UNHEALTHY":
		return SymbolError
	case "SKIPPED", "UNKNOWN":
		return SymbolInfo
	default:
		return SymbolInfo
	}
}

func saveJSONReport(report HealthReport, namespace string) {
	timestamp := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("viya-health-check-%s-%s.json", namespace, timestamp)

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s%s Error creating JSON report: %v%s\n", ColorRed, SymbolError, err, ColorReset)
		return
	}
	if err := os.WriteFile(filename, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "%s%s Error saving report: %v%s\n", ColorRed, SymbolError, err, ColorReset)
		return
	}
	fmt.Printf("%s%s Report automatically saved: %s%s%s\n\n", ColorGreen, SymbolCheck, ColorBold, filename, ColorReset)
}

// promptForInput prompts the user for input and returns the trimmed response.
func promptForInput(prompt string) string {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	return strings.TrimSpace(input)
}

func validateKubeconfig(kubeconfig string) error {
	if _, err := os.Stat(kubeconfig); os.IsNotExist(err) {
		return fmt.Errorf("kubeconfig file not found: %s", kubeconfig)
	}

	cmd := exec.Command("kubectl", "cluster-info", "--kubeconfig", kubeconfig)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("invalid kubeconfig or cluster not reachable: %s", kubeconfig)
	}
	return nil
}

func validateNamespace(namespace, kubeconfig string) error {
	cmd := exec.Command("kubectl", "get", "namespace", namespace, "--kubeconfig", kubeconfig)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("namespace '%s' not found or not accessible", namespace)
	}
	return nil
}
