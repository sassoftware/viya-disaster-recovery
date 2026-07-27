package main

import (
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
)

type operationStatus struct {
	Component string
	Phase     string
	Action    string
	Status    string
	ExitCode  int
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

	describeRow := operationStatus{Component: "Velero Backup", Phase: "Backup Phase", Action: "Described", Status: "Success", ExitCode: 0}
	if _, err := r.Output("velero", "backup", "describe", name, "--details", "--namespace", cfg.VeleroNamespace); err != nil {
		describeRow.Status = "Failed"
		describeRow.ExitCode = extractExitCode(err)
		report = append(report, describeRow)
		printOperationReport(report)
		printDRFinalSummary(permissionBackupStatus, restorePermissionStatus, overallOperationStatus(report))
		return err
	}
	report = append(report, describeRow)

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

	describeRow := operationStatus{Component: "Velero Restore", Phase: "Restore Phase", Action: "Described", Status: "Success", ExitCode: 0}
	if _, err := r.Output("velero", "restore", "describe", restore, "--details", "--namespace", cfg.VeleroNamespace); err != nil {
		describeRow.Status = "Failed"
		describeRow.ExitCode = extractExitCode(err)
		report = append(report, describeRow)
		printOperationReport(report)
		printDRFinalSummary(permissionBackupStatus, restorePermissionStatus, overallOperationStatus(report))
		return err
	}
	report = append(report, describeRow)

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
