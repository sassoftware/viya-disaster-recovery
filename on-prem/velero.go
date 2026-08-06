package main

import (
	"bufio"
	"bytes"
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
	bslReadyTimeout             = 2 * time.Minute
	bslReadyPollInterval        = 5 * time.Second
	restorePostInstallWait      = 50 * time.Second
	restoreBackupFetchAttempts  = 5
	restoreBackupFetchInterval  = 8 * time.Second
)

type operationStatus struct {
	Component string
	Phase     string
	Action    string
	Status    string
	ExitCode  int
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
	args := buildVeleroInstallArgs(cfg, credPath)
	if err := r.Run("velero", args...); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			fmt.Println("Velero appears to be already installed; continuing")
		} else {
			return err
		}
	}
	return r.Run("velero", "backup-location", "get", "--namespace", cfg.VeleroNamespace)
}

func buildVeleroInstallArgs(cfg *Config, credPath string) []string {
	return []string{
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
		printBackupFinalSummary(permissionBackupStatus, restorePermissionStatus, overallOperationStatus(report))
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
		printBackupFinalSummary(permissionBackupStatus, restorePermissionStatus, overallOperationStatus(report))
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
		printBackupFinalSummary(permissionBackupStatus, restorePermissionStatus, overallOperationStatus(report))
		return err
	}
	report = append(report, monitorRow)
	printVeleroFinalSummary(backupResult)

	printOperationReport(report)
	printBackupFinalSummary(permissionBackupStatus, restorePermissionStatus, overallOperationStatus(report))
	return nil
}

func createRestore(cfg *Config, r Runner) error {
	report := make([]operationStatus, 0, 7)
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
		printRestoreFinalSummary(restorePermissionStatus, overallOperationStatus(report))
		return err
	}
	report = append(report, preflightRow)

	warmupRow := operationStatus{Component: "Backup Discovery Warmup", Phase: "Restore Phase", Action: "Waited", Status: "Success", ExitCode: 0}
	fmt.Printf("Waiting %d seconds for backup synchronization...\n", int(restorePostInstallWait.Seconds()))
	time.Sleep(restorePostInstallWait)
	report = append(report, warmupRow)

	discoveryRow := operationStatus{Component: "Backup Discovery", Phase: "Restore Phase", Action: "Fetched", Status: "Success", ExitCode: 0}
	fmt.Println("Fetching available backups...")
	availableBackups, err := fetchVeleroBackupsWithRetry(cfg, r, restoreBackupFetchAttempts, restoreBackupFetchInterval)
	if err != nil {
		discoveryRow.Status = "Failed"
		discoveryRow.ExitCode = extractExitCode(err)
		report = append(report, discoveryRow)
		printOperationReport(report)
		printRestoreFinalSummary(restorePermissionStatus, overallOperationStatus(report))
		return err
	}
	report = append(report, discoveryRow)

	selectionRow := operationStatus{Component: "Backup Selection", Phase: "Restore Phase", Action: "Selected", Status: "Success", ExitCode: 0}
	backup, err := selectBackupForRestore(cfg, availableBackups)
	if err != nil {
		selectionRow.Status = "Failed"
		selectionRow.ExitCode = extractExitCode(err)
		report = append(report, selectionRow)
		printOperationReport(report)
		printRestoreFinalSummary(restorePermissionStatus, overallOperationStatus(report))
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
		printRestoreFinalSummary(restorePermissionStatus, overallOperationStatus(report))
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
		printRestoreFinalSummary(restorePermissionStatus, overallOperationStatus(report))
		return err
	}
	report = append(report, monitorRow)
	printVeleroFinalSummary(restoreResult)

	restorePermRow, err := runPermissionRestoreScript(cfg, r)
	report = append(report, restorePermRow)
	restorePermissionStatus = restorePermRow.Status
	if err != nil {
		printOperationReport(report)
		printRestoreFinalSummary(restorePermissionStatus, overallOperationStatus(report))
		return fmt.Errorf("restore completed but permission restore failed: %w", err)
	}

	printOperationReport(report)
	printRestoreFinalSummary(restorePermissionStatus, overallOperationStatus(report))
	return nil
}

func ensureVeleroReadyForRestore(cfg *Config, r Runner) error {
	if _, err := exec.LookPath("velero"); err != nil {
		return fmt.Errorf("velero CLI not found in PATH: %w", err)
	}

	credPath, err := ensureExistingVeleroCredentialsFile(cfg)
	if err != nil {
		return err
	}
	fmt.Printf("Credential file detected: %s\n", credPath)

	if err := setupVeleroForRestore(cfg, r, credPath); err != nil {
		return fmt.Errorf("failed to configure Velero for restore: %w", err)
	}

	fmt.Println("Waiting for Velero deployment readiness...")
	if err := waitForVeleroDeploymentReady(cfg, r, 3*time.Minute, bslReadyPollInterval); err != nil {
		return fmt.Errorf("Velero deployment not ready: %w", err)
	}

	fmt.Println("Waiting for BackupStorageLocation availability...")
	if err := waitForBackupStorageLocationAvailable(cfg, r, bslReadyTimeout, bslReadyPollInterval); err != nil {
		return fmt.Errorf("BackupStorageLocation not available: %w", err)
	}

	return nil
}

func waitForVeleroDeploymentReady(cfg *Config, r Runner, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := runCommandCapture(r, "kubectl", "-n", cfg.VeleroNamespace, "get", "deployment", "velero"); err == nil {
			if _, err := runCommandCapture(r, "kubectl", "-n", cfg.VeleroNamespace,
				"wait", "--for=condition=Available", "deployment/velero", "--timeout=30s"); err == nil {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for Velero deployment to become ready", timeout)
		}
		time.Sleep(interval)
	}
}

func ensureExistingVeleroCredentialsFile(cfg *Config) (string, error) {
	credPath := filepath.Join(cfg.CredentialsDir, cfg.CredentialsFile)
	info, err := os.Stat(credPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("existing Velero credentials file is required for restore but was not found at %s", credPath)
		}
		return "", fmt.Errorf("unable to access existing Velero credentials file %s: %w", credPath, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("credentials path is a directory, expected file: %s", credPath)
	}
	content, err := os.ReadFile(credPath)
	if err != nil {
		return "", fmt.Errorf("unable to read existing Velero credentials file %s: %w", credPath, err)
	}
	if err := validateVeleroCredentialsContent(content); err != nil {
		return "", fmt.Errorf("invalid Velero credentials file %s: %w", credPath, err)
	}
	return credPath, nil
}

func setupVeleroForRestore(cfg *Config, r Runner, credPath string) error {
	if err := os.Setenv("KUBECONFIG", cfg.KubeconfigPath); err != nil {
		return err
	}

	args := buildVeleroInstallArgs(cfg, credPath)
	// Show the exact command shape used by restore preflight for easier troubleshooting.
	fmt.Printf("Restore preflight Velero install command: velero %s\n", strings.Join(args, " "))
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
	locations, err := listBackupStorageLocations(cfg, r)
	if err != nil {
		return err
	}
	if len(locations) == 0 {
		return fmt.Errorf("no Velero backup storage locations found in namespace %s", cfg.VeleroNamespace)
	}

	hasAvailable := false
	problems := make([]string, 0, len(locations))
	fmt.Println("Backup storage locations:")
	for _, loc := range locations {
		message := loc.Message
		if message == "" {
			message = "N/A"
		}
		fmt.Printf("- %s: phase=%s message=%s\n", loc.Name, emptyAsNA(loc.Phase), message)
		if strings.EqualFold(loc.Phase, "Available") {
			hasAvailable = true
			continue
		}
		problem := fmt.Sprintf("%s phase=%s", loc.Name, loc.Phase)
		if strings.TrimSpace(loc.Message) != "" {
			problem = fmt.Sprintf("%s message=%s", problem, strings.TrimSpace(loc.Message))
		}
		problems = append(problems, problem)
	}

	if !hasAvailable {
		details := strings.Join(problems, "; ")
		if strings.Contains(strings.ToLower(details), "metadata") || strings.Contains(strings.ToLower(details), "imds") || strings.Contains(strings.ToLower(details), "ec2") {
			return fmt.Errorf("no accessible Velero backup storage location found: %s. remediation: verify restore credential file and secret binding (BackupStorageLocation spec.credential.name) to avoid IMDS fallback", details)
		}
		return fmt.Errorf("no accessible Velero backup storage location found: %s", details)
	}
	return nil
}

type backupStorageLocationStatus struct {
	Name    string
	Phase   string
	Message string
}

func listBackupStorageLocations(cfg *Config, r Runner) ([]backupStorageLocationStatus, error) {
	// Use plain velero CLI output; -o json reads raw CRD and may lag behind velero's reconciled state.
	out, err := runCommandCapture(r, "velero", "backup-location", "get", "--namespace", cfg.VeleroNamespace)
	if err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "no backup") || strings.Contains(errLower, "not found") {
			return nil, nil
		}
		return nil, fmt.Errorf("unable to query backup storage locations: %w", err)
	}

	// Tabular output: NAME  PROVIDER  BUCKET/PREFIX  PHASE  LAST VALIDATED  ACCESS MODE  DEFAULT
	lines := strings.Split(strings.TrimSpace(out), "\n")
	locations := make([]backupStorageLocationStatus, 0, len(lines))
	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		lower := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(lower, "no ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		locations = append(locations, backupStorageLocationStatus{
			Name:  fields[0],
			Phase: fields[3],
		})
	}
	return locations, nil
}

func waitForBackupStorageLocationAvailable(cfg *Config, r Runner, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastDetails string

	for {
		locations, err := listBackupStorageLocations(cfg, r)
		if err == nil {
			if len(locations) == 0 {
				lastDetails = fmt.Sprintf("no BackupStorageLocation found in namespace %s", cfg.VeleroNamespace)
			} else {
				hasAvailable := false
				problems := make([]string, 0, len(locations))
				for _, loc := range locations {
					if strings.EqualFold(loc.Phase, "Available") {
						hasAvailable = true
						break
					}
					problem := fmt.Sprintf("%s phase=%s", loc.Name, emptyAsNA(loc.Phase))
					if loc.Message != "" {
						problem = fmt.Sprintf("%s message=%s", problem, loc.Message)
					}
					problems = append(problems, problem)
				}

				if hasAvailable {
					fmt.Println("BackupStorageLocation Available.")
					return nil
				}
				lastDetails = strings.Join(problems, "; ")
			}
		} else {
			lastDetails = err.Error()
		}

		if time.Now().After(deadline) {
			if lastDetails == "" {
				lastDetails = "timed out waiting for BackupStorageLocation"
			}
			return fmt.Errorf("timed out after %s waiting for BackupStorageLocation to become Available: %s", timeout, lastDetails)
		}

		time.Sleep(interval)
	}
}

func fetchVeleroBackups(cfg *Config, r Runner) ([]veleroBackupSummary, error) {
	// Plain text: avoids CRD status lag that -o json can expose
	out, err := runCommandCapture(r, "velero", "backup", "get", "--namespace", cfg.VeleroNamespace)
	if err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "no backup") || strings.Contains(errLower, "not found") {
			return nil, fmt.Errorf("no Velero backups found in storage")
		}
		return nil, fmt.Errorf("unable to fetch Velero backups: %w", err)
	}
	backups := parseVeleroBackupsText(out)
	if len(backups) == 0 {
		return nil, fmt.Errorf("no Velero backups found in storage")
	}

	fmt.Printf("Found %d backup(s).\n", len(backups))
	printAvailableBackups(backups)
	return backups, nil
}

func fetchVeleroBackupsWithRetry(cfg *Config, r Runner, attempts int, interval time.Duration) ([]veleroBackupSummary, error) {
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		backups, err := fetchVeleroBackups(cfg, r)
		if err == nil {
			return backups, nil
		}
		lastErr = err

		if attempt == attempts {
			break
		}

		fmt.Printf("No backups discovered yet (attempt %d/%d). Retrying in %s...\n", attempt, attempts, interval)
		time.Sleep(interval)
		fmt.Println("Fetching available backups...")
	}

	return nil, fmt.Errorf("unable to discover Velero backups after %d attempt(s): %w", attempts, lastErr)
}

// parseVeleroBackupsText parses tabular output of `velero backup get`.
// Columns: NAME STATUS ERRORS WARNINGS CREATED(date time tz) EXPIRES STORAGE_LOCATION SELECTOR
func parseVeleroBackupsText(out string) []veleroBackupSummary {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	backups := make([]veleroBackupSummary, 0, len(lines))
	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		lower := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(lower, "no ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		created := fields[4]
		if len(fields) >= 6 {
			created = fields[4] + " " + fields[5]
		}
		backups = append(backups, veleroBackupSummary{
			Name:     fields[0],
			Phase:    fields[1],
			Errors:   fields[2],
			Warnings: fields[3],
			Created:  created,
		})
	}
	return backups
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
		out, err := runCommandCapture(r, "velero", resourceType, "get", "--namespace", cfg.VeleroNamespace)
		if err != nil {
			result.Duration = time.Since(start)
			return result, err
		}

		phase, errors, warnings := parseVeleroGetStatus(out, name)
		if phase != "" {
			result.Phase = phase
		}
		if errors != "" {
			result.Errors = errors
		}
		if warnings != "" {
			result.Warnings = warnings
		}

		result.Duration = time.Since(start)
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

// parseVeleroGetStatus extracts phase, errors, warnings from `velero backup|restore get` tabular output.
func parseVeleroGetStatus(out, name string) (phase, errors, warnings string) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return
	}
	header := lines[0]
	upper := strings.ToUpper(header)

	statusIdx := strings.Index(upper, "STATUS")
	errorsIdx := strings.Index(upper, "ERRORS")
	warningsIdx := strings.Index(upper, "WARNINGS")

	if statusIdx < 0 {
		return
	}

	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != name {
			continue
		}
		phase = colValue(line, statusIdx, errorsIdx)
		if errorsIdx >= 0 {
			errors = colValue(line, errorsIdx, warningsIdx)
		}
		if warningsIdx >= 0 {
			warnings = colValue(line, warningsIdx, -1)
		}
		return
	}
	return
}

func colValue(line string, start, end int) string {
	if start < 0 || start >= len(line) {
		return ""
	}
	s := line[start:]
	if end > start && end < len(line) {
		s = line[start:end]
	}
	return strings.TrimSpace(s)
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

func printBackupFinalSummary(permissionBackupStatus, restorePermissionStatus, overallStatus string) {
	fmt.Println("\nFinal Summary:")
	fmt.Printf("- Permission Backup: %s\n", permissionBackupStatus)
	fmt.Printf("- Restore Permissions: %s\n", restorePermissionStatus)
	fmt.Printf("- Overall DR Operation: %s\n", overallStatus)
}

func printRestoreFinalSummary(restorePermissionStatus, overallStatus string) {
	fmt.Println("\nFinal Summary:")
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
