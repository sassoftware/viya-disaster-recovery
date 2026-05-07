// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package velero

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// VeleroManager handles Velero operations
type VeleroManager struct {
	namespace      string
	kubeconfigPath string
}

// VeleroConfig holds Velero installation configuration
type VeleroConfig struct {
	BucketName        string
	Bucket            string
	Region            string
	CredentialsFile   string
	PluginVersion     string
	SnapshotLocations []string
	BackupLocations   []string
	UseCSI            bool
	UseNodeAgent      bool
	UseIRSA           bool
	IAMRoleARN        string
}

// BackupOptions holds backup configuration options
type BackupOptions struct {
	IncludeNamespaces []string
	ExcludeNamespaces []string
	SnapshotVolumes   bool
	TTL               string
	Wait              bool
}

// RestoreOptions holds restore configuration options
type RestoreOptions struct {
	RestorePVs bool
	Wait       bool
}

// NewVeleroManager creates a new Velero manager
func NewVeleroManager(namespace string, kubeconfigPath string) *VeleroManager {
	return &VeleroManager{
		namespace:      namespace,
		kubeconfigPath: kubeconfigPath,
	}
}

// Install installs Velero using the CLI
func (m *VeleroManager) Install(ctx context.Context, config *VeleroConfig) error {
	fmt.Println("\n=== Installing Velero ===")

	// Check if velero CLI is installed
	if _, err := exec.LookPath("velero"); err != nil {
		return fmt.Errorf("velero CLI not found. Please install from https://velero.io/docs/main/basic-install/")
	}

	// Use BucketName if Bucket is not set
	bucket := config.Bucket
	if bucket == "" {
		bucket = config.BucketName
	}

	args := []string{
		"install",
		"--provider", "aws",
		"--plugins", fmt.Sprintf("velero/velero-plugin-for-aws:%s", config.PluginVersion),
		"--bucket", bucket,
		"--backup-location-config", fmt.Sprintf("region=%s", config.Region),
		"--snapshot-location-config", fmt.Sprintf("region=%s", config.Region),
		"--namespace", m.namespace,
	}

	// If using IRSA, add service account annotation
	if config.UseIRSA && config.IAMRoleARN != "" {
		args = append(args, "--sa-annotations", fmt.Sprintf("eks.amazonaws.com/role-arn=%s", config.IAMRoleARN))
		args = append(args, "--no-secret")
	} else if config.CredentialsFile != "" {
		args = append(args, "--secret-file", config.CredentialsFile)
	}

	// Enable volume snapshots
	args = append(args, "--use-volume-snapshots=true")

	// Enable CSI features
	if config.UseCSI {
		args = append(args, "--features=EnableCSI,EnableKopiaPreserveOwnershipAndPermissions")
	}

	// Enable node agent (restic/kopia)
	if config.UseNodeAgent {
		args = append(args, "--use-node-agent")
		args = append(args, "--privileged-node-agent")
	}

	// Enable snapshot data movement
	args = append(args, "--default-snapshot-move-data")

	// Fall back to filesystem backup for volumes that don't support CSI snapshots (e.g. NFS)
	args = append(args, "--default-volumes-to-fs-backup")

	cmd := exec.CommandContext(ctx, "velero", args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+m.kubeconfigPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Printf("Running: velero %s\n", strings.Join(args, " "))

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to install Velero: %w", err)
	}

	fmt.Println("✓ Velero installation completed")
	return nil
}

// VerifyInstallation verifies that Velero is properly installed and running
func (m *VeleroManager) VerifyInstallation(ctx context.Context) error {
	fmt.Println("\n=== Verifying Velero Installation ===")

	// Check Velero pods
	fmt.Println("Checking Velero pods...")
	checkPodsCmd := exec.CommandContext(ctx, "kubectl", "get", "pods", "-n", m.namespace)
	checkPodsCmd.Env = append(os.Environ(), "KUBECONFIG="+m.kubeconfigPath)
	checkPodsCmd.Stdout = os.Stdout
	checkPodsCmd.Stderr = os.Stderr

	if err := checkPodsCmd.Run(); err != nil {
		return fmt.Errorf("failed to get Velero pods: %w", err)
	}

	// Check backup location
	fmt.Println("\nChecking Velero backup location...")
	checkLocationCmd := exec.CommandContext(ctx, "velero", "backup-location", "get", "--namespace", m.namespace)
	checkLocationCmd.Env = append(os.Environ(), "KUBECONFIG="+m.kubeconfigPath)
	checkLocationCmd.Stdout = os.Stdout
	checkLocationCmd.Stderr = os.Stderr

	if err := checkLocationCmd.Run(); err != nil {
		return fmt.Errorf("failed to get backup location: %w", err)
	}

	fmt.Println("\n✓ Velero installation verified successfully")
	return nil
}

// CreateBackup renders configs/backup/backup-template.yaml and applies it via kubectl
func (m *VeleroManager) CreateBackup(ctx context.Context, backupName string, options *BackupOptions) error {
	fmt.Printf("\n=== Creating Velero Backup: %s ===\n", backupName)

	// Determine namespace
	viyaNamespace := ""
	if len(options.IncludeNamespaces) > 0 {
		viyaNamespace = options.IncludeNamespaces[0]
	}

	// Read backup template from configs/backup/backup-template.yaml
	templatePath := "configs/backup/backup-template.yaml"
	templateBytes, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("failed to read backup template %s: %w", templatePath, err)
	}

	// Replace placeholders
	backupYAML := string(templateBytes)
	backupYAML = strings.ReplaceAll(backupYAML, "BACKUP_NAME_PLACEHOLDER", backupName)
	backupYAML = strings.ReplaceAll(backupYAML, "NAMESPACE_PLACEHOLDER", viyaNamespace)

	fmt.Printf("Applying backup manifest for namespace '%s'...\n", viyaNamespace)

	// Apply via kubectl
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+m.kubeconfigPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start kubectl: %w", err)
	}
	if _, err := stdin.Write([]byte(backupYAML)); err != nil {
		stdin.Close()
		return fmt.Errorf("failed to write backup YAML: %w", err)
	}
	stdin.Close()

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("failed to apply backup: %w", err)
	}

	fmt.Printf("✓ Backup '%s' applied\n", backupName)

	// Wait for backup to complete if requested
	if options.Wait {
		if err := m.WaitForBackup(ctx, backupName, 30*time.Minute); err != nil {
			return err
		}
	}

	fmt.Println("✓ Backup created successfully")
	return nil
}

// CreateRestore creates a Velero restore
func (m *VeleroManager) CreateRestore(ctx context.Context, restoreName, backupName string, options *RestoreOptions) error {
	fmt.Printf("\n=== Creating Velero Restore: %s from backup %s ===\n", restoreName, backupName)

	args := []string{
		"restore", "create", restoreName,
		"--from-backup", backupName,
		"--namespace", m.namespace,
	}

	if !options.RestorePVs {
		args = append(args, "--restore-volumes=false")
	}

	if options.Wait {
		args = append(args, "--wait")
	}

	cmd := exec.CommandContext(ctx, "velero", args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+m.kubeconfigPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Printf("Running: velero %s\n", strings.Join(args, " "))

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create restore: %w", err)
	}

	fmt.Println("✓ Restore created successfully")
	return nil
}

// ListBackups lists all available backups
func (m *VeleroManager) ListBackups(ctx context.Context) ([]string, error) {
	// Use kubectl to list backups — more reliable than velero CLI on restore clusters
	// where the velero context/namespace flag may behave differently.
	// Retry to allow backup metadata to sync from S3 after Velero install.
	var lastErr error
	for attempt := 1; attempt <= 5; attempt++ {
		if attempt > 1 {
			fmt.Printf("   Retrying backup list (attempt %d/5, waiting 15s for S3 sync)...\n", attempt)
			time.Sleep(15 * time.Second)
		}

		cmd := exec.CommandContext(ctx, "kubectl", "get", "backup",
			"-n", m.namespace,
			"-o", `jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}`)
		cmd.Env = append(os.Environ(), "KUBECONFIG="+m.kubeconfigPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			lastErr = fmt.Errorf("kubectl get backup failed: %w\nOutput: %s", err, string(output))
			continue
		}

		backups := []string{}
		for _, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				backups = append(backups, line)
			}
		}

		if len(backups) > 0 {
			return backups, nil
		}
		lastErr = fmt.Errorf("no backups found yet (metadata still syncing from S3)")
	}

	return nil, lastErr
}

// WaitForBackup waits for a backup to complete
func (m *VeleroManager) WaitForBackup(ctx context.Context, backupName string, timeout time.Duration) error {
	fmt.Printf("Waiting for backup %s to complete...\n", backupName)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := m.getBackupStatus(ctx, backupName)
		if err != nil {
			return err
		}

		fmt.Printf("Backup status: %s\n", status)

		if status == "Completed" || status == "PartiallyFailed" {
			if status == "PartiallyFailed" {
				fmt.Println("⚠ Backup completed with partial failures")
			} else {
				fmt.Println("✓ Backup completed successfully")
			}
			return nil
		}

		if status == "Failed" {
			return fmt.Errorf("backup failed with status: %s", status)
		}

		time.Sleep(10 * time.Second)
	}

	return fmt.Errorf("timeout waiting for backup to complete")
}

// WaitForRestore waits for a restore to complete
func (m *VeleroManager) WaitForRestore(ctx context.Context, restoreName string, timeout time.Duration) error {
	fmt.Printf("Waiting for restore %s to complete...\n", restoreName)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := m.getRestoreStatus(ctx, restoreName)
		if err != nil {
			return err
		}

		fmt.Printf("Restore status: %s\n", status)

		if status == "Completed" {
			fmt.Println("✓ Restore completed successfully")
			return nil
		}

		if status == "Failed" || status == "PartiallyFailed" {
			return fmt.Errorf("restore failed with status: %s", status)
		}

		time.Sleep(10 * time.Second)
	}

	return fmt.Errorf("timeout waiting for restore to complete")
}

// getBackupStatus gets the status of a backup using kubectl jsonpath for exact top-level phase
func (m *VeleroManager) getBackupStatus(ctx context.Context, backupName string) (string, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "backup", backupName,
		"-n", m.namespace,
		"-o", "jsonpath={.status.phase}")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+m.kubeconfigPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get backup status: %w", err)
	}

	status := strings.TrimSpace(string(output))
	if status == "" {
		return "Unknown", nil
	}
	return status, nil
}

// getRestoreStatus gets the status of a restore using kubectl jsonpath for exact top-level phase
func (m *VeleroManager) getRestoreStatus(ctx context.Context, restoreName string) (string, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "restore", restoreName,
		"-n", m.namespace,
		"-o", "jsonpath={.status.phase}")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+m.kubeconfigPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get restore status: %w", err)
	}

	status := strings.TrimSpace(string(output))
	if status == "" {
		return "Unknown", nil
	}
	return status, nil
}

// PatchNodeAgentTolerations patches the node-agent DaemonSet with SAS Viya tolerations
func (m *VeleroManager) PatchNodeAgentTolerations(ctx context.Context) error {
	fmt.Println("\n=== Patching node-agent tolerations ===")

	patch := `{
  "spec": {
    "template": {
      "spec": {
        "tolerations": [
          {
            "key": "workload.sas.com/class",
            "operator": "Equal",
            "value": "stateless",
            "effect": "NoSchedule"
          },
          {
            "key": "workload.sas.com/class",
            "operator": "Equal",
            "value": "stateful",
            "effect": "NoSchedule"
          },
          {
            "key": "workload.sas.com/class",
            "operator": "Equal",
            "value": "cas",
            "effect": "NoSchedule"
          },
          {
            "key": "workload.sas.com/class",
            "operator": "Equal",
            "value": "compute",
            "effect": "NoSchedule"
          },
          {
            "key": "workload.sas.com/class",
            "operator": "Equal",
            "value": "master",
            "effect": "NoSchedule"
          }
        ]
      }
    }
  }
}`

	cmd := exec.CommandContext(ctx, "kubectl", "patch", "daemonset", "node-agent",
		"-n", m.namespace,
		"--type=merge",
		"-p", patch,
	)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+m.kubeconfigPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to patch node-agent tolerations: %w\nOutput: %s", err, string(output))
	}

	fmt.Println("✓ node-agent tolerations patched with SAS Viya workload classes")
	return nil
}

// Uninstall uninstalls Velero
func (m *VeleroManager) Uninstall(ctx context.Context) error {
	fmt.Printf("\n=== Uninstalling Velero from namespace %s ===\n", m.namespace)

	// Check if velero CLI is installed
	if _, err := exec.LookPath("velero"); err != nil {
		return fmt.Errorf("velero CLI not found")
	}

	// Uninstall using velero CLI
	cmd := exec.CommandContext(ctx, "velero", "uninstall", "--namespace", m.namespace, "--force")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+m.kubeconfigPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to uninstall Velero: %w", err)
	}

	fmt.Printf("✓ Velero successfully uninstalled from namespace: %s\n", m.namespace)
	return nil
}
