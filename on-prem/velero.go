package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	permissionBackupScriptPath  = "scripts/backup_permission.sh"
	permissionRestoreScriptPath = "scripts/restore_permission.sh"
	permissionBackupPVC         = "viya-permission-backup"
	veleroPollInterval          = 20 * time.Second
)

type operationStatus struct {
	Component string
	Phase     string
	Action    string
	Status    string
	ExitCode  int
}

type veleroDescribeSummary struct {
	Phase       string
	Warnings    string
	Errors      string
	StartedAt   *time.Time
	CompletedAt *time.Time
}

type veleroOperationResult struct {
	Operation string
	Name      string
	Phase     string
	Warnings  string
	Errors    string
	Duration  time.Duration
}

func setupVelero(cfg *Config, r Runner) error {
	if err := os.Setenv("KUBECONFIG", cfg.KubeconfigPath); err != nil {
		return err
	}
	credPath, err := writeVeleroCredentials(cfg)
	if err != nil {
		return err
	}
	args := []string{
		"install",
		"--features=EnableCSI",
		"--use-node-agent",
		"--default-snapshot-move-data",
		"--provider", cfg.VeleroProvider,
		"--plugins", cfg.VeleroPlugin,
		"--bucket", cfg.LibrefsBucket,
		"--secret-file", credPath,
		"--use-volume-snapshots=true",
		"--backup-location-config", fmt.Sprintf("region=minio,s3ForcePathStyle=true,s3Url=%s", cfg.LibrefsEndpoint),
		"--namespace", cfg.VeleroNamespace,
	}
	if err := r.Run("velero", args...); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			fmt.Println("Velero appears to be already installed; continuing")
		} else {
			return err
		}
	}
	return r.Run("velero", "backup-location", "get", "--namespace", cfg.VeleroNamespace)
}

func writeVeleroCredentials(cfg *Config) (string, error) {
	if err := os.MkdirAll(cfg.CredentialsDir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(cfg.CredentialsDir, cfg.CredentialsFile)
	content := fmt.Sprintf(`[default]
aws_access_key_id=%s
aws_secret_access_key=%s
`, cfg.LibrefsAccessKey, cfg.LibrefsSecretKey)
	return path, os.WriteFile(path, []byte(content), 0600)
}

func createBackup(cfg *Config, r Runner) error {
	report := make([]operationStatus, 0, 4)
	permissionBackupStatus := "Skipped"
	restorePermissionStatus := "Skipped"

	if cfg.ClusterType != "source" {
		return fmt.Errorf("backup requires CLUSTER_TYPE=source")
	}
	if err := os.Setenv("KUBECONFIG", cfg.KubeconfigPath); err != nil {
		return err
	}
	permRow, err := runPermissionBackupScript(cfg, r)
	report = append(report, permRow)
	permissionBackupStatus = permRow.Status
	if err != nil {
		printOperationReport(report)
		printDRFinalSummary(permissionBackupStatus, restorePermissionStatus, overallOperationStatus(report))
		return err
	}

	name := cfg.BackupName
	if name == "auto" || strings.TrimSpace(name) == "" {
		name = "viya-full-backup-" + time.Now().Format("20060102-150405")
	}
	args := []string{"backup", "create", name,
		"--include-namespaces", cfg.ViyaNamespace,
		"--include-cluster-resources=true",
		"--csi-snapshot-timeout=4h",
		"--ttl=720h0m0s",
		"--namespace", cfg.VeleroNamespace,
	}
	veleroBackupRow := operationStatus{Component: "Velero Backup", Phase: "Backup Phase", Action: "Started", Status: "Success", ExitCode: 0}
	if _, err := r.Output("velero", args...); err != nil {
		veleroBackupRow.Status = "Failed"
		veleroBackupRow.ExitCode = extractExitCode(err)
		report = append(report, veleroBackupRow)
		printOperationReport(report)
		printDRFinalSummary(permissionBackupStatus, restorePermissionStatus, overallOperationStatus(report))
		return err
	}
	report = append(report, veleroBackupRow)

	state := LoadState()
	state.BackupName = name
	_ = state.Mark("backup", "created")

	monitorRow := operationStatus{Component: "Velero Backup", Phase: "Backup Phase", Action: "Monitored", Status: "Success", ExitCode: 0}
	backupResult, err := monitorVeleroOperation(cfg, r, "backup", name)
	if err != nil {
		monitorRow.Status = "Failed"
		monitorRow.ExitCode = extractExitCode(err)
		report = append(report, monitorRow)
		printVeleroFinalSummary(backupResult)
		printOperationReport(report)
		printDRFinalSummary(permissionBackupStatus, restorePermissionStatus, overallOperationStatus(report))
		return err
	}
	report = append(report, monitorRow)
	printVeleroFinalSummary(backupResult)

	printOperationReport(report)
	printDRFinalSummary(permissionBackupStatus, restorePermissionStatus, overallOperationStatus(report))
	return nil
}

func createRestore(cfg *Config, r Runner) error {
	report := make([]operationStatus, 0, 4)
	permissionBackupStatus := "Skipped"
	restorePermissionStatus := "Skipped"

	if cfg.ClusterType != "restore" {
		return fmt.Errorf("restore requires CLUSTER_TYPE=restore")
	}
	if err := os.Setenv("KUBECONFIG", cfg.KubeconfigPath); err != nil {
		return err
	}
	backup := cfg.BackupName
	if strings.TrimSpace(backup) == "" || backup == "auto" {
		state := LoadState()
		backup = state.BackupName
	}
	if strings.TrimSpace(backup) == "" {
		return fmt.Errorf("BACKUP_NAME is required for restore or state.json must contain backupName")
	}
	restore := cfg.RestoreName
	if strings.TrimSpace(restore) == "" || restore == "auto" {
		restore = "viya-restore-" + time.Now().Format("20060102-150405")
	}
	args := []string{"restore", "create", restore,
		"--from-backup", backup,
		"--include-cluster-resources=true",
		"--namespace", cfg.VeleroNamespace,
	}
	veleroRestoreRow := operationStatus{Component: "Velero Restore", Phase: "Restore Phase", Action: "Started", Status: "Success", ExitCode: 0}
	if _, err := r.Output("velero", args...); err != nil {
		veleroRestoreRow.Status = "Failed"
		veleroRestoreRow.ExitCode = extractExitCode(err)
		report = append(report, veleroRestoreRow)
		printOperationReport(report)
		printDRFinalSummary(permissionBackupStatus, restorePermissionStatus, overallOperationStatus(report))
		return err
	}
	report = append(report, veleroRestoreRow)

	state := LoadState()
	state.RestoreName = restore
	_ = state.Mark("restore", "created")

	monitorRow := operationStatus{Component: "Velero Restore", Phase: "Restore Phase", Action: "Monitored", Status: "Success", ExitCode: 0}
	restoreResult, err := monitorVeleroOperation(cfg, r, "restore", restore)
	if err != nil {
		monitorRow.Status = "Failed"
		monitorRow.ExitCode = extractExitCode(err)
		report = append(report, monitorRow)
		printVeleroFinalSummary(restoreResult)
		printOperationReport(report)
		printDRFinalSummary(permissionBackupStatus, restorePermissionStatus, overallOperationStatus(report))
		return err
	}
	report = append(report, monitorRow)
	printVeleroFinalSummary(restoreResult)

	restorePermRow, err := runPermissionRestoreScript(cfg, r)
	report = append(report, restorePermRow)
	restorePermissionStatus = restorePermRow.Status
	if err != nil {
		printOperationReport(report)
		printDRFinalSummary(permissionBackupStatus, restorePermissionStatus, overallOperationStatus(report))
		return fmt.Errorf("restore completed but permission restore failed: %w", err)
	}

	printOperationReport(report)
	printDRFinalSummary(permissionBackupStatus, restorePermissionStatus, overallOperationStatus(report))
	return nil
}

func monitorVeleroOperation(cfg *Config, r Runner, resourceType, name string) (veleroOperationResult, error) {
	operationLabel := resourceType
	if len(resourceType) > 0 {
		operationLabel = strings.ToUpper(resourceType[:1]) + strings.ToLower(resourceType[1:])
	}
	result := veleroOperationResult{
		Operation: operationLabel,
		Name:      name,
		Phase:     "Unknown",
		Warnings:  "Unknown",
		Errors:    "Unknown",
	}

	start := time.Now()
	pollCount := 0
	for {
		pollCount++
		out, err := runCommandCapture(r, "velero", resourceType, "describe", name, "--details", "--namespace", cfg.VeleroNamespace)
		if err != nil {
			result.Duration = time.Since(start)
			return result, err
		}

		summary := parseVeleroDescribeSummary(out)
		if summary.Phase != "" {
			result.Phase = summary.Phase
		}
		if summary.Warnings != "" {
			result.Warnings = summary.Warnings
		}
		if summary.Errors != "" {
			result.Errors = summary.Errors
		}

		result.Duration = operationElapsed(start, summary.StartedAt, summary.CompletedAt)
		printVeleroProgress(result, pollCount)

		phaseLower := strings.ToLower(strings.TrimSpace(result.Phase))
		switch phaseLower {
		case "completed":
			return result, nil
		case "failed", "partiallyfailed":
			return result, fmt.Errorf("velero %s %s ended in %s", resourceType, name, result.Phase)
		}

		time.Sleep(veleroPollInterval)
	}
}

func runCommandCapture(r Runner, name string, args ...string) (string, error) {
	if r.Debug {
		fmt.Printf("+ %s %s\n", name, strings.Join(args, " "))
	}
	cmd := exec.Command(name, args...)
	cmd.Env = os.Environ()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("command failed: %s %s\nstdout:\n%s\nstderr:\n%s\nerror: %w", name, strings.Join(args, " "), stdout.String(), stderr.String(), err)
	}
	return stdout.String(), nil
}

func parseVeleroDescribeSummary(out string) veleroDescribeSummary {
	summary := veleroDescribeSummary{}
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !strings.Contains(trimmed, ":") {
			continue
		}
		lower := strings.ToLower(trimmed)
		value := valueAfterColon(trimmed)
		switch {
		case strings.HasPrefix(lower, "phase:"):
			summary.Phase = value
		case strings.HasPrefix(lower, "warnings:"):
			summary.Warnings = value
		case strings.HasPrefix(lower, "errors:"):
			summary.Errors = value
		case strings.HasPrefix(lower, "started:"), strings.HasPrefix(lower, "start timestamp:"):
			if ts, ok := parseVeleroTimestamp(value); ok {
				summary.StartedAt = &ts
			}
		case strings.HasPrefix(lower, "completed:"), strings.HasPrefix(lower, "completion timestamp:"):
			if ts, ok := parseVeleroTimestamp(value); ok {
				summary.CompletedAt = &ts
			}
		}
	}

	if summary.Phase == "" {
		summary.Phase = "Unknown"
	}
	if summary.Warnings == "" {
		summary.Warnings = "Unknown"
	}
	if summary.Errors == "" {
		summary.Errors = "Unknown"
	}
	return summary
}

func valueAfterColon(line string) string {
	idx := strings.Index(line, ":")
	if idx == -1 {
		return ""
	}
	return strings.TrimSpace(line[idx+1:])
}

func parseVeleroTimestamp(value string) (time.Time, bool) {
	v := strings.TrimSpace(value)
	if v == "" || strings.EqualFold(v, "n/a") || strings.EqualFold(v, "<none>") {
		return time.Time{}, false
	}

	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05",
	}
	for _, format := range formats {
		if ts, err := time.Parse(format, v); err == nil {
			return ts, true
		}
	}
	return time.Time{}, false
}

func operationElapsed(fallbackStart time.Time, startedAt, completedAt *time.Time) time.Duration {
	start := fallbackStart
	if startedAt != nil {
		start = *startedAt
	}
	end := time.Now()
	if completedAt != nil {
		end = *completedAt
	}
	if end.Before(start) {
		return 0
	}
	return end.Sub(start)
}

func printVeleroProgress(result veleroOperationResult, pollCount int) {
	if pollCount > 1 {
		// Move to the start of the previous progress block and clear it for a live refresh.
		fmt.Print("\033[7A\033[J")
	}
	fmt.Printf("=== Velero %s Progress ===\n", result.Operation)
	fmt.Printf("Name: %s\n", result.Name)
	fmt.Printf("Poll: %d (interval %s)\n", pollCount, veleroPollInterval)
	fmt.Printf("Phase: %s\n", result.Phase)
	fmt.Printf("Elapsed: %s\n", result.Duration.Round(time.Second))
	fmt.Printf("Errors: %s\n", result.Errors)
	fmt.Printf("Warnings: %s\n", result.Warnings)
}

func printVeleroFinalSummary(result veleroOperationResult) {
	fmt.Printf("\nVelero %s Final Summary\n", result.Operation)
	fmt.Printf("- Name: %s\n", result.Name)
	fmt.Printf("- Status: %s\n", result.Phase)
	fmt.Printf("- Duration: %s\n", result.Duration.Round(time.Second))
	fmt.Printf("- Warnings: %s\n", result.Warnings)
	fmt.Printf("- Errors: %s\n", result.Errors)
}

func runPermissionBackupScript(cfg *Config, r Runner) (operationStatus, error) {
	row := operationStatus{
		Component: "Permission Backup Script",
		Phase:     "Backup Phase",
		Action:    "Executed",
		Status:    "Success",
		ExitCode:  0,
	}

	if err := validateScriptPrerequisites(permissionBackupScriptPath); err != nil {
		row.Status = "Failed"
		row.ExitCode = 127
		return row, err
	}
	if strings.TrimSpace(cfg.ViyaNamespace) == "" {
		row.Status = "Failed"
		row.ExitCode = 2
		return row, fmt.Errorf("VIYA_NAMESPACE is required for permission backup script")
	}
	if strings.TrimSpace(cfg.NFSStorageClass) == "" {
		row.Status = "Failed"
		row.ExitCode = 2
		return row, fmt.Errorf("NFS_STORAGE_CLASS is required for permission backup script")
	}

	if out, err := r.Output("bash", permissionBackupScriptPath, cfg.ViyaNamespace, cfg.NFSStorageClass); err != nil {
		if strings.TrimSpace(out) != "" {
			fmt.Println(out)
		}
		row.Status = "Failed"
		row.ExitCode = extractExitCode(err)
		return row, fmt.Errorf("permission backup script failed (exit code %d): %w", row.ExitCode, err)
	}
	return row, nil
}

func runPermissionRestoreScript(cfg *Config, r Runner) (operationStatus, error) {
	row := operationStatus{
		Component: "Permission Restore",
		Phase:     "Restore Phase",
		Action:    "Executed",
		Status:    "Success",
		ExitCode:  0,
	}

	if err := validateScriptPrerequisites(permissionRestoreScriptPath); err != nil {
		row.Status = "Failed"
		row.ExitCode = 127
		return row, err
	}
	if strings.TrimSpace(cfg.ViyaNamespace) == "" {
		row.Status = "Failed"
		row.ExitCode = 2
		return row, fmt.Errorf("VIYA_NAMESPACE is required for permission restore script")
	}

	if err := os.Setenv("BACKUP_PVC", permissionBackupPVC); err != nil {
		row.Status = "Failed"
		row.ExitCode = 1
		return row, fmt.Errorf("failed setting BACKUP_PVC for permission restore script: %w", err)
	}
	defer os.Unsetenv("BACKUP_PVC")

	if out, err := r.Output("bash", permissionRestoreScriptPath, cfg.ViyaNamespace); err != nil {
		if strings.TrimSpace(out) != "" {
			fmt.Println(out)
		}
		row.Status = "Failed"
		row.ExitCode = extractExitCode(err)
		return row, fmt.Errorf("permission restore script failed (exit code %d): %w", row.ExitCode, err)
	}
	return row, nil
}

func validateScriptPrerequisites(scriptPath string) error {
	info, err := os.Stat(scriptPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("script not found: %s", scriptPath)
		}
		return fmt.Errorf("unable to stat script %s: %w", scriptPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("script path is a directory, expected file: %s", scriptPath)
	}
	if info.Mode()&0111 == 0 {
		return fmt.Errorf("script is not executable: %s", scriptPath)
	}
	return nil
}

func extractExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	if strings.Contains(strings.ToLower(err.Error()), "permission denied") {
		return 126
	}
	if strings.Contains(strings.ToLower(err.Error()), "not found") {
		return 127
	}
	return 1
}

func printOperationReport(report []operationStatus) {
	fmt.Println("\nCleanup/Status Summary")
	fmt.Println("Component | Phase | Action | Status | Exit Code")
	for _, row := range report {
		fmt.Printf("%s | %s | %s | %s | %d\n", row.Component, row.Phase, row.Action, row.Status, row.ExitCode)
	}
}

func printDRFinalSummary(permissionBackupStatus, restorePermissionStatus, overallStatus string) {
	fmt.Println("\nFinal Summary:")
	fmt.Printf("- Permission Backup: %s\n", permissionBackupStatus)
	fmt.Printf("- Restore Permissions: %s\n", restorePermissionStatus)
	fmt.Printf("- Overall DR Operation: %s\n", overallStatus)
}

func overallOperationStatus(report []operationStatus) string {
	failed := 0
	succeeded := 0
	for _, row := range report {
		switch row.Status {
		case "Failed":
			failed++
		case "Success":
			succeeded++
		}
	}
	if failed == 0 {
		return "Success"
	}
	if succeeded == 0 {
		return "Failed"
	}
	return "Partial Success"
}
