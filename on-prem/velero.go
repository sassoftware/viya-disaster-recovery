package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	permissionBackupScriptPath  = "scripts/backup_permission.sh"
	permissionRestoreScriptPath = "scripts/restore_permission.sh"
	permissionBackupPVC         = "viya-permission-backup"
	veleroPollInterval          = 20 * time.Second
	veleroCredentialSecretName  = "cloud-credentials"
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

type veleroBackupSummary struct {
	Name        string
	Phase       string
	Created     string
	Completed   string
	Warnings    string
	Errors      string
	StorageName string
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
	report := make([]operationStatus, 0, 6)
	permissionBackupStatus := "Skipped"
	restorePermissionStatus := "Skipped"

	if cfg.ClusterType != "restore" {
		return fmt.Errorf("restore requires CLUSTER_TYPE=restore")
	}
	if err := os.Setenv("KUBECONFIG", cfg.KubeconfigPath); err != nil {
		return err
	}

	preflightRow := operationStatus{Component: "Velero Preflight", Phase: "Restore Phase", Action: "Validated", Status: "Success", ExitCode: 0}
	if err := ensureVeleroReadyForRestore(cfg, r); err != nil {
		preflightRow.Status = "Failed"
		preflightRow.ExitCode = extractExitCode(err)
		report = append(report, preflightRow)
		printOperationReport(report)
		printDRFinalSummary(permissionBackupStatus, restorePermissionStatus, overallOperationStatus(report))
		return err
	}
	report = append(report, preflightRow)

	availableBackups, err := fetchVeleroBackups(cfg, r)
	selectionRow := operationStatus{Component: "Backup Selection", Phase: "Restore Phase", Action: "Selected", Status: "Success", ExitCode: 0}
	if err != nil {
		selectionRow.Status = "Failed"
		selectionRow.ExitCode = extractExitCode(err)
		report = append(report, selectionRow)
		printOperationReport(report)
		printDRFinalSummary(permissionBackupStatus, restorePermissionStatus, overallOperationStatus(report))
		return err
	}

	backup, err := selectBackupForRestore(cfg, availableBackups)
	if err != nil {
		selectionRow.Status = "Failed"
		selectionRow.ExitCode = extractExitCode(err)
		report = append(report, selectionRow)
		printOperationReport(report)
		printDRFinalSummary(permissionBackupStatus, restorePermissionStatus, overallOperationStatus(report))
		return err
	}
	report = append(report, selectionRow)

	restore := cfg.RestoreName
	if strings.TrimSpace(restore) == "" || restore == "auto" {
		restore = "viya-restore-" + time.Now().Format("20060102-150405")
	}
	args := []string{"restore", "create", restore,
		"--from-backup", backup.Name,
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
	state.BackupName = backup.Name
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

func ensureVeleroReadyForRestore(cfg *Config, r Runner) error {
	if _, err := exec.LookPath("velero"); err != nil {
		return fmt.Errorf("velero CLI not found in PATH: %w", err)
	}
	credPath, credContent, err := ensureExistingVeleroCredentialsFile(cfg)
	if err != nil {
		return err
	}
	fmt.Printf("Credential file detected: %s\n", credPath)

	if err := ensureVeleroCoreInstalled(cfg, r); err != nil {
		fmt.Println("Velero core components are not ready; attempting automatic setup...")
		if err := setupVeleroForRestore(cfg, r); err != nil {
			return fmt.Errorf("failed to install/configure Velero automatically: %w", err)
		}
		if err := ensureVeleroCoreInstalled(cfg, r); err != nil {
			return fmt.Errorf("Velero setup completed but core components are still not ready: %w", err)
		}
	}

	action, err := syncVeleroCredentialSecretFromFile(cfg, r, credPath, credContent)
	if err != nil {
		return err
	}
	fmt.Printf("Velero credentials secret %s/%s: %s\n", cfg.VeleroNamespace, veleroCredentialSecretName, action)

	if err := ensureBackupStorageLocationAccessible(cfg, r); err != nil {
		return err
	}

	if _, err := fetchVeleroBackups(cfg, r); err != nil {
		return err
	}

	fmt.Println("Velero preflight checks passed")
	return nil
}

func ensureExistingVeleroCredentialsFile(cfg *Config) (string, []byte, error) {
	credPath := filepath.Join(cfg.CredentialsDir, cfg.CredentialsFile)
	info, err := os.Stat(credPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, fmt.Errorf("existing Velero credentials file is required for restore but was not found at %s", credPath)
		}
		return "", nil, fmt.Errorf("unable to access existing Velero credentials file %s: %w", credPath, err)
	}
	if info.IsDir() {
		return "", nil, fmt.Errorf("credentials path is a directory, expected file: %s", credPath)
	}
	content, err := os.ReadFile(credPath)
	if err != nil {
		return "", nil, fmt.Errorf("unable to read existing Velero credentials file %s: %w", credPath, err)
	}
	if err := validateVeleroCredentialsContent(content); err != nil {
		return "", nil, fmt.Errorf("invalid Velero credentials file %s: %w", credPath, err)
	}
	return credPath, content, nil
}

func setupVeleroForRestore(cfg *Config, r Runner) error {
	if err := os.Setenv("KUBECONFIG", cfg.KubeconfigPath); err != nil {
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
		"--use-volume-snapshots=true",
		"--backup-location-config", fmt.Sprintf("region=minio,s3ForcePathStyle=true,s3Url=%s", cfg.LibrefsEndpoint),
		"--no-secret",
		"--namespace", cfg.VeleroNamespace,
	}
	if err := r.Run("velero", args...); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			fmt.Println("Velero appears to be already installed; continuing")
		} else {
			return err
		}
	}

	return nil
}

func ensureVeleroCoreInstalled(cfg *Config, r Runner) error {
	if err := ensureVeleroCRDs(r); err != nil {
		return err
	}
	if err := ensureVeleroDeployment(cfg, r); err != nil {
		return err
	}
	return nil
}

func syncVeleroCredentialSecretFromFile(cfg *Config, r Runner, credPath string, credContent []byte) (string, error) {
	secretExists, currentContent, err := getVeleroCredentialSecretContent(cfg, r)
	if err != nil {
		return "", err
	}
	if secretExists && bytes.Equal(currentContent, credContent) {
		return "reused", nil
	}

	manifest, err := runCommandCapture(r,
		"kubectl", "-n", cfg.VeleroNamespace,
		"create", "secret", "generic", veleroCredentialSecretName,
		"--from-file=cloud="+credPath,
		"--dry-run=client",
		"-o", "yaml",
	)
	if err != nil {
		return "", fmt.Errorf("failed to build Velero credentials secret manifest from %s: %w", credPath, err)
	}

	tmp, err := os.CreateTemp("", "velero-credentials-secret-*.yaml")
	if err != nil {
		return "", fmt.Errorf("failed to prepare temporary manifest for Velero credentials secret: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(manifest); err != nil {
		tmp.Close()
		return "", fmt.Errorf("failed to write Velero credentials secret manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("failed to finalize Velero credentials secret manifest: %w", err)
	}

	if _, err := runCommandCapture(r, "kubectl", "apply", "-f", tmp.Name()); err != nil {
		return "", fmt.Errorf("failed to apply Velero credentials secret from %s: %w", credPath, err)
	}

	if secretExists {
		return "updated", nil
	}
	return "created", nil
}

func getVeleroCredentialSecretContent(cfg *Config, r Runner) (bool, []byte, error) {
	out, err := runCommandCapture(r, "kubectl", "-n", cfg.VeleroNamespace, "get", "secret", veleroCredentialSecretName, "-o", "json")
	if err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "notfound") || strings.Contains(errLower, "not found") {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("failed to check Velero credentials secret %s/%s: %w", cfg.VeleroNamespace, veleroCredentialSecretName, err)
	}

	type secretJSON struct {
		Data map[string]string `json:"data"`
	}
	var parsed secretJSON
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return false, nil, fmt.Errorf("failed to parse Velero credentials secret output: %w", err)
	}
	encoded, ok := parsed.Data["cloud"]
	if !ok {
		return true, nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return false, nil, fmt.Errorf("failed to decode Velero credentials secret cloud data: %w", err)
	}
	return true, decoded, nil
}

func validateVeleroCredentialsContent(content []byte) error {
	text := strings.TrimSpace(string(content))
	if text == "" {
		return fmt.Errorf("file is empty")
	}
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "aws_access_key_id") {
		return fmt.Errorf("missing aws_access_key_id")
	}
	if !strings.Contains(lower, "aws_secret_access_key") {
		return fmt.Errorf("missing aws_secret_access_key")
	}
	return nil
}

func ensureVeleroCRDs(r Runner) error {
	crds := []string{"backups.velero.io", "restores.velero.io", "backupstoragelocations.velero.io"}
	for _, crd := range crds {
		if _, err := runCommandCapture(r, "kubectl", "get", "crd", crd); err != nil {
			return fmt.Errorf("required Velero CRD %s is missing: %w", crd, err)
		}
	}
	return nil
}

func ensureVeleroDeployment(cfg *Config, r Runner) error {
	if _, err := runCommandCapture(r, "kubectl", "-n", cfg.VeleroNamespace, "get", "deployment", "velero"); err != nil {
		return fmt.Errorf("velero deployment not found in namespace %s: %w", cfg.VeleroNamespace, err)
	}
	if _, err := runCommandCapture(r, "kubectl", "-n", cfg.VeleroNamespace, "wait", "--for=condition=Available", "deployment/velero", "--timeout=180s"); err != nil {
		return fmt.Errorf("velero deployment is not available: %w", err)
	}
	return nil
}

func ensureBackupStorageLocationAccessible(cfg *Config, r Runner) error {
	out, err := runCommandCapture(r, "velero", "backup-location", "get", "--namespace", cfg.VeleroNamespace, "-o", "json")
	if err != nil {
		return fmt.Errorf("unable to query Velero backup storage locations: %w", err)
	}

	type bslList struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Phase   string `json:"phase"`
				Message string `json:"message"`
			} `json:"status"`
		} `json:"items"`
	}

	var parsed bslList
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return fmt.Errorf("failed to parse backup storage location output: %w", err)
	}
	if len(parsed.Items) == 0 {
		return fmt.Errorf("no Velero backup storage locations found in namespace %s", cfg.VeleroNamespace)
	}

	hasAvailable := false
	problems := make([]string, 0, len(parsed.Items))
	fmt.Println("Backup storage locations:")
	for _, item := range parsed.Items {
		phase := strings.TrimSpace(item.Status.Phase)
		message := strings.TrimSpace(item.Status.Message)
		if message == "" {
			message = "N/A"
		}
		fmt.Printf("- %s: phase=%s message=%s\n", item.Metadata.Name, emptyAsNA(phase), message)
		if strings.EqualFold(phase, "Available") {
			hasAvailable = true
			continue
		}
		problem := fmt.Sprintf("%s phase=%s", item.Metadata.Name, phase)
		if strings.TrimSpace(item.Status.Message) != "" {
			problem = fmt.Sprintf("%s message=%s", problem, strings.TrimSpace(item.Status.Message))
		}
		problems = append(problems, problem)
	}

	if !hasAvailable {
		return fmt.Errorf("no accessible Velero backup storage location found: %s", strings.Join(problems, "; "))
	}
	return nil
}

func fetchVeleroBackups(cfg *Config, r Runner) ([]veleroBackupSummary, error) {
	out, err := runCommandCapture(r, "velero", "backup", "get", "--namespace", cfg.VeleroNamespace, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("unable to fetch Velero backups: %w", err)
	}

	type backupList struct {
		Items []struct {
			Metadata struct {
				Name              string `json:"name"`
				CreationTimestamp string `json:"creationTimestamp"`
			} `json:"metadata"`
			Spec struct {
				StorageLocation string `json:"storageLocation"`
			} `json:"spec"`
			Status struct {
				Phase               string      `json:"phase"`
				Warnings            interface{} `json:"warnings"`
				Errors              interface{} `json:"errors"`
				StartTimestamp      string      `json:"startTimestamp"`
				CompletionTimestamp string      `json:"completionTimestamp"`
			} `json:"status"`
		} `json:"items"`
	}

	var parsed backupList
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse Velero backup list: %w", err)
	}
	if len(parsed.Items) == 0 {
		return nil, fmt.Errorf("no Velero backups found in storage")
	}

	backups := make([]veleroBackupSummary, 0, len(parsed.Items))
	for _, item := range parsed.Items {
		created := strings.TrimSpace(item.Metadata.CreationTimestamp)
		if created == "" {
			created = strings.TrimSpace(item.Status.StartTimestamp)
		}
		backups = append(backups, veleroBackupSummary{
			Name:        item.Metadata.Name,
			Phase:       strings.TrimSpace(item.Status.Phase),
			Created:     created,
			Completed:   strings.TrimSpace(item.Status.CompletionTimestamp),
			Warnings:    strings.TrimSpace(fmt.Sprint(item.Status.Warnings)),
			Errors:      strings.TrimSpace(fmt.Sprint(item.Status.Errors)),
			StorageName: strings.TrimSpace(item.Spec.StorageLocation),
		})
	}

	printAvailableBackups(backups)
	return backups, nil
}

func printAvailableBackups(backups []veleroBackupSummary) {
	fmt.Println("\nAvailable Velero backups")
	fmt.Println("# | Name | Phase | Created | Warnings | Errors")
	for i, backup := range backups {
		fmt.Printf("%d | %s | %s | %s | %s | %s\n", i+1, backup.Name, emptyAsNA(backup.Phase), emptyAsNA(backup.Created), emptyAsNA(backup.Warnings), emptyAsNA(backup.Errors))
	}
}

func selectBackupForRestore(cfg *Config, backups []veleroBackupSummary) (veleroBackupSummary, error) {
	preferredName := strings.TrimSpace(cfg.BackupName)
	if preferredName == "" || strings.EqualFold(preferredName, "auto") {
		state := LoadState()
		preferredName = strings.TrimSpace(state.BackupName)
	}

	nameToIndex := make(map[string]int, len(backups))
	for i := range backups {
		nameToIndex[backups[i].Name] = i
	}

	completedIndexes := make([]int, 0, len(backups))
	for i := range backups {
		if strings.EqualFold(backups[i].Phase, "Completed") {
			completedIndexes = append(completedIndexes, i)
		}
	}
	if len(completedIndexes) == 0 {
		return veleroBackupSummary{}, fmt.Errorf("no backups are in Completed phase; cannot start restore")
	}

	stdinInfo, err := os.Stdin.Stat()
	if err != nil {
		return veleroBackupSummary{}, fmt.Errorf("unable to inspect stdin for interactive backup selection: %w", err)
	}
	if stdinInfo.Mode()&os.ModeCharDevice == 0 {
		if preferredName != "" {
			if idx, ok := nameToIndex[preferredName]; ok {
				if strings.EqualFold(backups[idx].Phase, "Completed") {
					fmt.Printf("\nSelected configured backup (non-interactive mode): %s\n", preferredName)
					return backups[idx], nil
				}
				return veleroBackupSummary{}, fmt.Errorf("configured backup %s has phase %s; restore requires Completed backup", preferredName, backups[idx].Phase)
			}
			return veleroBackupSummary{}, fmt.Errorf("configured backup %s not found among available backups", preferredName)
		}
		return veleroBackupSummary{}, fmt.Errorf("interactive backup selection requires a terminal")
	}

	defaultIndex := completedIndexes[0]
	if preferredName != "" {
		if idx, ok := nameToIndex[preferredName]; ok {
			if strings.EqualFold(backups[idx].Phase, "Completed") {
				defaultIndex = idx
				fmt.Printf("\nDefaulting to configured backup: %s\n", preferredName)
			} else {
				fmt.Printf("\nConfigured backup %s has phase %s. Select a Completed backup.\n", preferredName, backups[idx].Phase)
			}
		} else {
			fmt.Printf("\nConfigured backup %s not found; select from available backups.\n", preferredName)
		}
	}

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("\nSelect backup number to restore [%d]: ", defaultIndex+1)
		input, err := reader.ReadString('\n')
		if err != nil {
			return veleroBackupSummary{}, fmt.Errorf("failed to read backup selection: %w", err)
		}
		input = strings.TrimSpace(input)
		if input == "" {
			return backups[defaultIndex], nil
		}

		selectedNumber, err := strconv.Atoi(input)
		if err != nil || selectedNumber < 1 || selectedNumber > len(backups) {
			fmt.Printf("Invalid selection: %s. Enter a number between 1 and %d.\n", input, len(backups))
			continue
		}

		candidate := backups[selectedNumber-1]
		if !strings.EqualFold(candidate.Phase, "Completed") {
			fmt.Printf("Backup %s is in phase %s. Please select a backup in Completed phase.\n", candidate.Name, candidate.Phase)
			continue
		}
		return candidate, nil
	}
}

func emptyAsNA(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "N/A"
	}
	return trimmed
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
