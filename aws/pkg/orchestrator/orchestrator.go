// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sas-institute/viya-aws-dr-automation/pkg/aws"
	"github.com/sas-institute/viya-aws-dr-automation/pkg/config"
	"github.com/sas-institute/viya-aws-dr-automation/pkg/kubernetes"
	"github.com/sas-institute/viya-aws-dr-automation/pkg/state"
	"github.com/sas-institute/viya-aws-dr-automation/pkg/validator"
	"github.com/sas-institute/viya-aws-dr-automation/pkg/velero"
)

// Orchestrator coordinates all DR operations
type Orchestrator struct {
	config     *config.Config
	stateStore *state.StateStore
	validator  *validator.Validator
	ctx        context.Context
}

// New creates a new orchestrator
func New(cfg *config.Config) (*Orchestrator, error) {
	stateStore := state.NewStateStore("state.json")
	validator := validator.NewValidator(cfg)

	return &Orchestrator{
		config:     cfg,
		stateStore: stateStore,
		validator:  validator,
		ctx:        context.Background(),
	}, nil
}

// Execute executes the specified operation
func (o *Orchestrator) Execute(operation string) error {
	switch operation {
	case "validate":
		return o.Validate()
	case "aws-setup":
		return o.Setup()
	case "backup":
		return o.Backup()
	case "restore":
		return o.Restore()
	case "cleanup":
		return o.Cleanup()
	default:
		return fmt.Errorf("unknown operation: %s", operation)
	}
}

// ExecuteSteps executes specific setup steps (Azure-compatible interface)
func (o *Orchestrator) ExecuteSteps(steps string) error {
	fmt.Printf("\n=== Starting Step-by-Step Setup ===\n")
	fmt.Printf("Steps to execute: %s\n\n", steps)

	// Load state
	state, err := o.stateStore.Load()
	if err != nil {
		return err
	}

	state.ClusterType = o.config.ClusterType
	state.ClusterName = o.config.ClusterName

	// Parse steps
	stepList := parseSteps(steps)

	// Always validate first (unless explicitly excluded)
	needsValidation := true
	for _, step := range stepList {
		if step == "validate" {
			needsValidation = false
			break
		}
	}

	if needsValidation {
		fmt.Println("Step 0: Validating prerequisites...")
		if err := o.validator.ValidateAll(o.ctx); err != nil {
			o.stateStore.AddOperation(state, "validate", "failed", "Prerequisite validation", err)
			o.stateStore.Save(state)
			return err
		}
		o.stateStore.AddOperation(state, "validate", "success", "Prerequisite validation", nil)
	}

	// Execute requested steps
	for _, step := range stepList {
		switch step {
		case "aws":
			if err := o.executeAWSSteps(state); err != nil {
				return err
			}
		case "kubernetes":
			if err := o.executeKubernetesSteps(state); err != nil {
				return err
			}
		case "velero":
			if err := o.executeVeleroSteps(state); err != nil {
				return err
			}
		case "all":
			// Execute all steps - same as Setup()
			return o.Setup()
		default:
			return fmt.Errorf("unknown step: %s (valid steps: aws, kubernetes, velero, all)", step)
		}
	}

	// Save final state
	if err := o.stateStore.Save(state); err != nil {
		return err
	}

	fmt.Println("\n=== Steps completed successfully! ===")
	return nil
}

// parseSteps parses comma-separated step list
func parseSteps(steps string) []string {
	if steps == "" || steps == "all" {
		return []string{"all"}
	}

	var stepList []string
	for _, step := range splitAndTrim(steps, ",") {
		stepList = append(stepList, step)
	}
	return stepList
}

// splitAndTrim splits string by delimiter and trims whitespace
func splitAndTrim(s, delimiter string) []string {
	parts := []string{}
	for _, part := range splitString(s, delimiter) {
		trimmed := trimSpace(part)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return parts
}

// splitString splits a string by delimiter
func splitString(s, delimiter string) []string {
	if s == "" {
		return []string{}
	}
	result := []string{}
	current := ""
	for i := 0; i < len(s); i++ {
		if i+len(delimiter) <= len(s) && s[i:i+len(delimiter)] == delimiter {
			result = append(result, current)
			current = ""
			i += len(delimiter) - 1
		} else {
			current += string(s[i])
		}
	}
	result = append(result, current)
	return result
}

// trimSpace removes leading and trailing whitespace
func trimSpace(s string) string {
	start := 0
	end := len(s)

	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}

	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}

	return s[start:end]
}

// executeAWSSteps executes AWS resource creation steps
func (o *Orchestrator) executeAWSSteps(state *state.State) error {
	fmt.Println("\n=== AWS Resources Setup ===")

	// Get OIDC Provider ID
	fmt.Println("Getting EKS OIDC provider...")
	eksManager, err := aws.NewEKSManager(o.ctx, o.config.EKSClusterRegion)
	if err != nil {
		return err
	}

	oidcID, err := eksManager.GetOIDCProvider(o.ctx, o.config.ClusterName)
	if err != nil {
		o.stateStore.AddOperation(state, "get_oidc", "failed", "Get OIDC provider", err)
		o.stateStore.Save(state)
		return err
	}
	o.config.OIDCID = oidcID
	o.stateStore.SetResource(state, "oidc_id", oidcID)
	o.stateStore.AddOperation(state, "get_oidc", "success", "Get OIDC provider", nil)

	// Create S3 bucket
	fmt.Println("Creating S3 bucket...")
	s3Manager, err := aws.NewS3Manager(o.ctx, o.config.VeleroBucketRegion)
	if err != nil {
		return err
	}

	if err := s3Manager.CreateBucket(o.ctx, o.config.VeleroBucketName, o.config.VeleroBucketRegion); err != nil {
		o.stateStore.AddOperation(state, "create_s3", "failed", "Create S3 bucket", err)
		o.stateStore.Save(state)
		return err
	}
	o.stateStore.SetResource(state, "s3_bucket", o.config.VeleroBucketName)
	o.stateStore.AddOperation(state, "create_s3", "success", "Create S3 bucket", nil)

	// Create IAM roles
	fmt.Println("Creating IAM roles...")
	if err := o.createIAMRoles(state); err != nil {
		return err
	}

	// Install EBS CSI Driver
	fmt.Println("Installing EBS CSI Driver...")
	if err := o.installEBSCSI(state); err != nil {
		return err
	}

	fmt.Println("✓ AWS resources setup completed")
	return nil
}

// executeKubernetesSteps executes Kubernetes configuration steps
func (o *Orchestrator) executeKubernetesSteps(state *state.State) error {
	fmt.Println("\n=== Kubernetes Configuration ===")

	// Install CSI Snapshot Controller
	fmt.Println("Installing CSI Snapshot Controller...")
	if err := o.installCSISnapshotController(state); err != nil {
		return err
	}

	// Create VolumeSnapshotClasses
	fmt.Println("Creating VolumeSnapshotClasses...")
	if err := o.applyVolumeSnapshotClasses(state); err != nil {
		return err
	}

	fmt.Println("✓ Kubernetes configuration completed")
	return nil
}

// executeVeleroSteps executes Velero installation steps
func (o *Orchestrator) executeVeleroSteps(state *state.State) error {
	fmt.Println("\n=== Velero Installation ===")

	// Install Velero
	fmt.Println("Installing Velero...")
	if err := o.installVelero(state); err != nil {
		return err
	}

	// Patch node-agent tolerations
	fmt.Println("Patching node-agent tolerations...")
	if err := o.patchNodeAgentTolerations(state); err != nil {
		return err
	}

	fmt.Println("✓ Velero installation completed")
	return nil
}

// Validate validates the environment
func (o *Orchestrator) Validate() error {
	return o.validator.ValidateAll(o.ctx)
}

// Setup sets up AWS resources for DR
func (o *Orchestrator) Setup() error {
	fmt.Println("\n=== Starting DR Setup ===")

	// Load state
	state, err := o.stateStore.Load()
	if err != nil {
		return err
	}

	state.ClusterType = o.config.ClusterType
	state.ClusterName = o.config.ClusterName

	// Step 1: Validate prerequisites
	fmt.Println("Step 1: Validating prerequisites...")
	if err := o.validator.ValidateAll(o.ctx); err != nil {
		o.stateStore.AddOperation(state, "validate", "failed", "Prerequisite validation", err)
		o.stateStore.Save(state)
		return err
	}
	o.stateStore.AddOperation(state, "validate", "success", "Prerequisite validation", nil)

	// Step 2: Get OIDC Provider ID
	fmt.Println("\nStep 2: Retrieving EKS OIDC provider...")
	eksManager, err := aws.NewEKSManager(o.ctx, o.config.EKSClusterRegion)
	if err != nil {
		return err
	}

	oidcID, err := eksManager.GetOIDCProvider(o.ctx, o.config.ClusterName)
	if err != nil {
		o.stateStore.AddOperation(state, "get_oidc", "failed", "Get OIDC provider", err)
		o.stateStore.Save(state)
		return err
	}
	o.config.OIDCID = oidcID
	o.stateStore.SetResource(state, "oidc_id", oidcID)
	o.stateStore.AddOperation(state, "get_oidc", "success", "Get OIDC provider", nil)

	// Step 3: Create S3 bucket
	fmt.Println("\nStep 3: Creating S3 bucket for Velero...")
	s3Manager, err := aws.NewS3Manager(o.ctx, o.config.VeleroBucketRegion)
	if err != nil {
		return err
	}

	if err := s3Manager.CreateBucket(o.ctx, o.config.VeleroBucketName, o.config.VeleroBucketRegion); err != nil {
		o.stateStore.AddOperation(state, "create_s3", "failed", "Create S3 bucket", err)
		o.stateStore.Save(state)
		return err
	}
	o.stateStore.SetResource(state, "s3_bucket", o.config.VeleroBucketName)
	o.stateStore.AddOperation(state, "create_s3", "success", "Create S3 bucket", nil)

	// Step 4: Create IAM roles
	fmt.Println("\nStep 4: Creating IAM roles...")
	if err := o.createIAMRoles(state); err != nil {
		return err
	}

	// Step 5: Install EBS CSI Driver
	fmt.Println("\nStep 5: Installing EBS CSI Driver...")
	if err := o.installEBSCSI(state); err != nil {
		return err
	}

	// Step 6: Install CSI Snapshot Controller
	fmt.Println("\nStep 6: Installing CSI Snapshot Controller...")
	if err := o.installCSISnapshotController(state); err != nil {
		return err
	}

	// Step 7: Create VolumeSnapshotClasses
	fmt.Println("\nStep 7: Creating VolumeSnapshotClasses...")
	if err := o.applyVolumeSnapshotClasses(state); err != nil {
		return err
	}

	// Step 8: Install Velero
	fmt.Println("\nStep 8: Installing Velero...")
	if err := o.installVelero(state); err != nil {
		return err
	}

	// Step 9: Patch node-agent tolerations
	fmt.Println("\nStep 9: Patching Velero node-agent with SAS tolerations...")
	if err := o.patchNodeAgentTolerations(state); err != nil {
		return err
	}

	// Save final state
	if err := o.stateStore.Save(state); err != nil {
		return err
	}

	fmt.Println("\n=== Setup completed successfully! ===")
	return nil
}

// Backup creates a Velero backup
func (o *Orchestrator) Backup() error {
	if !o.config.IsSourceCluster() {
		return fmt.Errorf("backup operation can only be performed on source cluster")
	}

	fmt.Println("\n=== Starting Backup ===")

	state, err := o.stateStore.Load()
	if err != nil {
		return err
	}

	// Prompt user for Viya namespace
	viyaNamespace := o.config.ViyaNamespace
	if viyaNamespace == "" {
		fmt.Print("Enter the Viya namespace to backup: ")
		fmt.Scanln(&viyaNamespace)
	} else {
		fmt.Printf("Using Viya namespace from config: %s\n", viyaNamespace)
		fmt.Printf("Press Enter to confirm or type a different namespace: ")
		var input string
		fmt.Scanln(&input)
		if input != "" {
			viyaNamespace = input
		}
	}
	if viyaNamespace == "" {
		return fmt.Errorf("Viya namespace cannot be empty")
	}
	fmt.Printf("Backing up namespace: %s\n", viyaNamespace)

	// Create backup name with timestamp
	backupName := fmt.Sprintf("%s-%s", o.config.BackupNamePrefix, time.Now().Format("20060102-150405"))

	// Save the namespace to state so restore can reference it
	o.stateStore.SetResource(state, "viya_namespace", viyaNamespace)

	// Step: Backup NFS/EFS volume permissions before Velero snapshot
	fmt.Println("\nSTEP: Backing up EFS/NFS volume permissions...")
	fmt.Println("================================================")
	if err := o.executePermissionBackup(viyaNamespace); err != nil {
		return fmt.Errorf("permission backup failed: %v", err)
	}
	fmt.Println("   Permission backup completed successfully")

	veleroMgr := velero.NewVeleroManager(o.config.VeleroNamespace, o.config.KubeconfigPath)

	options := &velero.BackupOptions{
		IncludeNamespaces: []string{viyaNamespace},
		SnapshotVolumes:   o.config.BackupSnapshotVolumes,
		Wait:              o.config.BackupWait,
		TTL:               o.config.BackupTTL,
	}

	if err := veleroMgr.CreateBackup(o.ctx, backupName, options); err != nil {
		o.stateStore.AddOperation(state, "backup", "failed", fmt.Sprintf("Create backup %s", backupName), err)
		o.stateStore.Save(state)
		return err
	}

	o.stateStore.SetResource(state, "last_backup", backupName)
	o.stateStore.AddOperation(state, "backup", "success", fmt.Sprintf("Create backup %s", backupName), nil)
	o.stateStore.Save(state)

	fmt.Printf("\n=== Backup '%s' completed successfully! ===\n", backupName)
	return nil
}

// Restore restores from a Velero backup
func (o *Orchestrator) Restore() error {
	if !o.config.IsRestoreCluster() {
		return fmt.Errorf("restore operation can only be performed on restore cluster (set CLUSTER_TYPE=restore in environment.properties)")
	}

	fmt.Println("\n   Starting Complete Velero Restore Process")
	fmt.Println("==========================================")

	state, err := o.stateStore.Load()
	if err != nil {
		return err
	}
	state.ClusterType = o.config.ClusterType
	state.ClusterName = o.config.ClusterName

	// ── STEP 1: Validate prerequisites ──────────────────────────────────────
	fmt.Println("\nSTEP 1: Validating prerequisites")
	fmt.Println("=================================")
	if err := o.validator.ValidateAll(o.ctx); err != nil {
		o.stateStore.AddOperation(state, "validate", "failed", "Prerequisite validation", err)
		o.stateStore.Save(state)
		return fmt.Errorf("prerequisite validation failed: %v", err)
	}
	o.stateStore.AddOperation(state, "validate", "success", "Prerequisite validation", nil)
	fmt.Println("   ✓ Prerequisites validated")

	// ── STEP 2: Verify existing IAM credentials are available in state ───────
	fmt.Println("\nSTEP 2: Verifying IAM credentials from state")
	fmt.Println("============================================")
	// For restore we never create new IAM users/keys — the IAM user and
	// access keys were already created on the source cluster and stored in
	// state.json.  Just verify they are present before we try to install Velero.
	if o.config.UseIAMUser {
		if _, exists := o.stateStore.GetResource(state, "velero_access_key_id"); !exists {
			return fmt.Errorf("velero_access_key_id not found in state.json\n" +
				"   Ensure state.json from the source cluster is copied to the aws/ directory.\n" +
				"   The IAM user and keys are created during --backup on the source cluster.")
		}
		if _, exists := o.stateStore.GetResource(state, "velero_secret_access_key"); !exists {
			return fmt.Errorf("velero_secret_access_key not found in state.json\n" +
				"   Ensure state.json from the source cluster is copied to the aws/ directory.")
		}
		fmt.Printf("   ✓ IAM user credentials found in state (user: %s)\n", o.config.VeleroIAMUser)
	} else {
		if _, exists := o.stateStore.GetResource(state, "velero_role_arn"); !exists {
			return fmt.Errorf("velero_role_arn not found in state.json\n" +
				"   Ensure state.json from the source cluster is copied to the aws/ directory.")
		}
		fmt.Println("   ✓ Velero IRSA role ARN found in state")
	}

	// ── STEP 3: Install CSI Snapshot Controller (installs CRDs) ─────────────
	fmt.Println("\nSTEP 3: Installing CSI Snapshot Controller on DR cluster")
	fmt.Println("=========================================================")
	if err := o.installCSISnapshotController(state); err != nil {
		return fmt.Errorf("CSI snapshot controller installation failed: %v", err)
	}
	fmt.Println("   ✓ CSI Snapshot Controller installed")

	// ── STEP 4: Apply VolumeSnapshotClasses on DR cluster ────────────────────
	fmt.Println("\nSTEP 4: Applying VolumeSnapshotClasses on DR cluster")
	fmt.Println("=====================================================")
	if err := o.applyVolumeSnapshotClasses(state); err != nil {
		return fmt.Errorf("failed to apply VolumeSnapshotClasses: %v", err)
	}
	fmt.Println("   ✓ VolumeSnapshotClasses applied")

	// ── STEP 5: Install Velero on DR cluster with same S3 configuration ──────
	fmt.Println("\nSTEP 5: Installing Velero on DR cluster with same S3 backup location")
	fmt.Println("======================================================================")
	if err := o.installVelero(state); err != nil {
		return fmt.Errorf("Velero installation failed: %v", err)
	}
	fmt.Println("   ✓ Velero installed")

	// ── STEP 6: Patch node-agent with SAS tolerations ────────────────────────
	fmt.Println("\nSTEP 6: Patching Velero node-agent with SAS tolerations")
	fmt.Println("=========================================================")
	if err := o.patchNodeAgentTolerations(state); err != nil {
		return fmt.Errorf("node-agent patch failed: %v", err)
	}
	fmt.Println("   ✓ node-agent patched")

	// ── STEP 7: Verify Velero and validate backup storage location ───────────
	fmt.Println("\nSTEP 7: Verifying Velero and backup storage location accessibility")
	fmt.Println("===================================================================")
	veleroMgr := velero.NewVeleroManager(o.config.VeleroNamespace, o.config.KubeconfigPath)
	if err := veleroMgr.VerifyInstallation(o.ctx); err != nil {
		return fmt.Errorf("Velero verification failed: %v", err)
	}
	fmt.Println("   ✓ Velero verified and backup location is accessible")

	// ── STEP 8: List available backups and prompt for selection ──────────────
	fmt.Println("\nSTEP 8: Retrieving available backups from S3")
	fmt.Println("=============================================")
	fmt.Println("   Syncing backup metadata from S3 to restore cluster...")

	backups, err := veleroMgr.ListBackups(o.ctx)
	if err != nil || len(backups) == 0 {
		fmt.Printf("   ⚠ Could not list backups automatically: %v\n", err)
		fmt.Println("   This can happen if backup metadata hasn't synced yet from S3.")
		fmt.Println("   You can manually enter a backup name if you know it exists.")
	}

	var selectedBackup string
	if len(backups) > 0 {
		fmt.Printf("\n   Available backups (%d found):\n", len(backups))
		for i, b := range backups {
			fmt.Printf("   %d. %s\n", i+1, b)
		}
		fmt.Printf("\n   Enter backup name to restore (or press Enter to use latest '%s'): ", backups[len(backups)-1])
		var input string
		fmt.Scanln(&input)
		input = strings.TrimSpace(input)
		if input == "" {
			selectedBackup = backups[len(backups)-1]
		} else {
			selectedBackup = input
		}
	} else {
		// Fall back to state or manual entry
		if lastBackup, exists := o.stateStore.GetResource(state, "last_backup"); exists && lastBackup != "" {
			fmt.Printf("   Enter backup name manually (or press Enter to use '%s' from state): ", lastBackup)
			var input string
			fmt.Scanln(&input)
			input = strings.TrimSpace(input)
			if input == "" {
				selectedBackup = lastBackup
			} else {
				selectedBackup = input
			}
		} else {
			fmt.Print("   Enter backup name manually (or press Enter to abort): ")
			var input string
			fmt.Scanln(&input)
			selectedBackup = strings.TrimSpace(input)
			if selectedBackup == "" {
				return fmt.Errorf("no backup name provided and no backups found automatically")
			}
		}
	}
	fmt.Printf("   ✓ Selected backup: %s\n", selectedBackup)

	// ── STEP 9: Prompt for namespace and execute Velero restore ──────────────
	fmt.Println("\nSTEP 9: Creating and applying Velero restore")
	fmt.Println("=============================================")

	viyaNamespace := o.config.ViyaNamespace
	if ns, exists := o.stateStore.GetResource(state, "viya_namespace"); exists && ns != "" && viyaNamespace == "" {
		viyaNamespace = ns
	}
	if viyaNamespace != "" {
		fmt.Printf("   Using Viya namespace from config: %s\n", viyaNamespace)
		fmt.Print("   Press Enter to confirm or type a different namespace: ")
		var input string
		fmt.Scanln(&input)
		if strings.TrimSpace(input) != "" {
			viyaNamespace = strings.TrimSpace(input)
		}
	} else {
		fmt.Print("   Enter the Viya namespace to restore into: ")
		fmt.Scanln(&viyaNamespace)
		viyaNamespace = strings.TrimSpace(viyaNamespace)
	}
	if viyaNamespace == "" {
		return fmt.Errorf("Viya namespace cannot be empty")
	}
	o.stateStore.SetResource(state, "viya_namespace", viyaNamespace)
	o.stateStore.Save(state)

	restoreName := fmt.Sprintf("%s-%s", o.config.RestoreNamePrefix, time.Now().Format("20060102-150405"))

	options := &velero.RestoreOptions{
		RestorePVs: true,
		Wait:       true,
	}

	if err := veleroMgr.CreateRestore(o.ctx, restoreName, selectedBackup, options); err != nil {
		o.stateStore.AddOperation(state, "restore", "failed",
			fmt.Sprintf("Create restore %s from backup %s", restoreName, selectedBackup), err)
		o.stateStore.Save(state)
		return fmt.Errorf("Velero restore failed: %v", err)
	}

	o.stateStore.SetResource(state, "last_restore", restoreName)
	o.stateStore.AddOperation(state, "restore", "success",
		fmt.Sprintf("Create restore %s from backup %s", restoreName, selectedBackup), nil)
	o.stateStore.Save(state)
	fmt.Printf("   ✓ Restore '%s' completed\n", restoreName)

	// ── STEP 10: Restore EFS/NFS volume permissions ──────────────────────────
	fmt.Println("\nSTEP 10: Restoring EFS/NFS volume permissions")
	fmt.Println("==============================================")
	if err := o.executePermissionRestore(viyaNamespace); err != nil {
		return fmt.Errorf("permission restore failed: %v", err)
	}
	fmt.Println("   ✓ Permission restore completed successfully")

	// ── STEP 11: Done ────────────────────────────────────────────────────────
	fmt.Println("\nSTEP 11: Restore completed successfully!")
	fmt.Println("========================================")
	fmt.Printf("   Restore name : %s\n", restoreName)
	fmt.Printf("   Namespace    : %s\n", viyaNamespace)
	fmt.Printf("   From backup  : %s\n", selectedBackup)
	fmt.Println("\n   Verify with:")
	fmt.Println("     velero restore get")
	fmt.Printf("     kubectl get pods -n %s\n", viyaNamespace)
	fmt.Printf("     kubectl get pvc  -n %s\n", viyaNamespace)
	fmt.Printf("     kubectl get ingress -n %s\n", viyaNamespace)
	fmt.Println("\n   Once pods are healthy, run the ingress DNS cutover manually:")
	fmt.Printf("     NAMESPACE=%s ./scripts/update-ingress.sh\n", viyaNamespace)
	fmt.Println("     (use --rollback flag if you need to revert)")

	return nil
}

// Cleanup cleans up AWS resources
func (o *Orchestrator) Cleanup() error {
	fmt.Println("\n=== Starting Cleanup ===")
	fmt.Println("WARNING: This will delete all DR resources!")
	fmt.Println("The following resources will be deleted:")
	fmt.Println("  1. Velero deployment and namespace")
	fmt.Println("  2. CSI Snapshot Controller")
	fmt.Println("  3. VolumeSnapshotClasses")
	fmt.Println("  4. EBS CSI Driver")
	fmt.Println("  5. IAM Roles (EBS CSI and Velero)")
	fmt.Println("  6. IAM Policies (Velero custom policy)")
	fmt.Println("  7. S3 Bucket (must be empty)")
	fmt.Println("\nThis operation cannot be undone.")
	fmt.Print("\nType 'yes' to confirm deletion: ")

	var confirmation string
	fmt.Scanln(&confirmation)

	if confirmation != "yes" {
		fmt.Println("Cleanup cancelled.")
		return nil
	}

	// Load state to get resource ARNs
	st, err := o.stateStore.Load()
	if err != nil {
		fmt.Printf("Warning: Could not load state: %v\n", err)
		st = &state.State{Operations: []state.Operation{}, Resources: make(map[string]string)}
	}

	var errors []error

	// Step 1: Uninstall Velero
	fmt.Println("\nStep 1: Uninstalling Velero...")
	if err := o.uninstallVelero(); err != nil {
		fmt.Printf("  ⚠ Warning: %v\n", err)
		errors = append(errors, fmt.Errorf("velero uninstall: %w", err))
	} else {
		fmt.Println("  ✓ Velero uninstalled")
	}

	// Step 2: Delete VolumeSnapshotClasses
	fmt.Println("\nStep 2: Deleting VolumeSnapshotClasses...")
	if err := o.deleteVolumeSnapshotClasses(); err != nil {
		fmt.Printf("  ⚠ Warning: %v\n", err)
		errors = append(errors, fmt.Errorf("volumesnapshotclasses: %w", err))
	} else {
		fmt.Println("  ✓ VolumeSnapshotClasses deleted")
	}

	// Step 3: Uninstall CSI Snapshot Controller
	fmt.Println("\nStep 3: Uninstalling CSI Snapshot Controller...")
	if err := o.uninstallCSISnapshotController(); err != nil {
		fmt.Printf("  ⚠ Warning: %v\n", err)
		errors = append(errors, fmt.Errorf("csi snapshot controller: %w", err))
	} else {
		fmt.Println("  ✓ CSI Snapshot Controller uninstalled")
	}

	// Step 4: Uninstall EBS CSI Driver
	fmt.Println("\nStep 4: Uninstalling EBS CSI Driver...")
	if err := o.uninstallEBSCSI(); err != nil {
		fmt.Printf("  ⚠ Warning: %v\n", err)
		errors = append(errors, fmt.Errorf("ebs csi driver: %w", err))
	} else {
		fmt.Println("  ✓ EBS CSI Driver uninstalled")
	}

	// Step 5: Delete IAM Roles and Policies
	fmt.Println("\nStep 5: Deleting IAM roles and policies...")
	if err := o.deleteIAMResources(st); err != nil {
		fmt.Printf("  ⚠ Warning: %v\n", err)
		errors = append(errors, fmt.Errorf("iam resources: %w", err))
	} else {
		fmt.Println("  ✓ IAM resources deleted")
	}

	// Step 6: Delete S3 Bucket (only if empty)
	fmt.Println("\nStep 6: Deleting S3 bucket...")
	if err := o.deleteS3Bucket(); err != nil {
		fmt.Printf("  ⚠ Warning: %v\n", err)
		fmt.Println("  Note: Bucket must be empty before deletion. Use 'aws s3 rm s3://bucket-name --recursive' to empty it.")
		errors = append(errors, fmt.Errorf("s3 bucket: %w", err))
	} else {
		fmt.Println("  ✓ S3 bucket deleted")
	}

	// Step 7: Clear state file
	fmt.Println("\nStep 7: Clearing state file...")
	emptyState := &state.State{Operations: []state.Operation{}, Resources: make(map[string]string)}
	if err := o.stateStore.Save(emptyState); err != nil {
		fmt.Printf("  ⚠ Warning: Could not clear state: %v\n", err)
	} else {
		fmt.Println("  ✓ State file cleared")
	}

	if len(errors) > 0 {
		fmt.Println("\n=== Cleanup completed with warnings ===")
		fmt.Println("Some resources could not be deleted:")
		for _, err := range errors {
			fmt.Printf("  - %v\n", err)
		}
		return fmt.Errorf("cleanup completed with %d warnings", len(errors))
	}

	fmt.Println("\n=== Cleanup completed successfully! ===")
	return nil
}

// Helper methods

func (o *Orchestrator) createIAMRoles(state *state.State) error {
	iamManager, err := aws.NewIAMManager(o.ctx, o.config.Region)
	if err != nil {
		return err
	}

	// Create EBS CSI IAM role if addon installation is enabled
	if o.config.InstallEBSCSIAddon {
		fmt.Println("  Creating EBS CSI IAM role...")
		ebsTrustPolicy, err := iamManager.CreateEBSCSITrustPolicy(
			o.config.AccountID, o.config.OIDCID, o.config.EKSClusterRegion,
			o.config.EBSCSINamespace, o.config.EBSCSIServiceAccount)
		if err != nil {
			return err
		}

		ebsRoleARN, err := iamManager.CreateRole(o.ctx, o.config.EBSRoleName, ebsTrustPolicy,
			"IAM role for EBS CSI driver")
		if err != nil {
			o.stateStore.AddOperation(state, "create_ebs_role", "failed", "Create EBS CSI role", err)
			return err
		}

		// Attach EBS CSI policy
		ebsPolicyARN := fmt.Sprintf("arn:aws:iam::aws:policy/service-role/%s", o.config.EBSPolicyName)
		if err := iamManager.AttachPolicy(o.ctx, o.config.EBSRoleName, ebsPolicyARN); err != nil {
			return err
		}

		o.stateStore.SetResource(state, "ebs_role_arn", ebsRoleARN)
		o.stateStore.AddOperation(state, "create_ebs_role", "success", "Create EBS CSI role", nil)
	}

	if o.config.UseIAMUser {
		// Use IAM User approach for Velero
		fmt.Println("  Creating Velero IAM user...")

		// Create IAM user
		if err := iamManager.CreateIAMUser(o.ctx, o.config.VeleroIAMUser); err != nil {
			o.stateStore.AddOperation(state, "create_velero_user", "failed", "Create Velero IAM user", err)
			return err
		}

		// Create Velero policy document
		veleroPolicy := o.getVeleroPolicy()

		// Attach inline policy to user
		policyName := fmt.Sprintf("%s-policy", o.config.VeleroIAMUser)
		if err := iamManager.PutUserPolicy(o.ctx, o.config.VeleroIAMUser, policyName, veleroPolicy); err != nil {
			return err
		}

		// Create access key
		accessKeyID, secretAccessKey, err := iamManager.CreateAccessKey(o.ctx, o.config.VeleroIAMUser)
		if err != nil {
			o.stateStore.AddOperation(state, "create_access_key", "failed", "Create access key", err)
			return err
		}

		// Store credentials
		o.stateStore.SetResource(state, "velero_access_key_id", accessKeyID)
		o.stateStore.SetResource(state, "velero_secret_access_key", secretAccessKey)
		o.stateStore.SetResource(state, "velero_iam_user", o.config.VeleroIAMUser)
		o.stateStore.AddOperation(state, "create_velero_user", "success", "Create Velero IAM user", nil)

		fmt.Printf("✓ Created IAM user and access key\n")
		fmt.Printf("\n=== IMPORTANT: Save these credentials ===\n")
		fmt.Printf("AWS Access Key ID: %s\n", accessKeyID)
		fmt.Printf("AWS Secret Access Key: %s\n", secretAccessKey)
		fmt.Printf("=========================================\n\n")

		return nil
	}

	// IRSA approach for Velero (create IAM role)
	fmt.Println("  Creating Velero IAM role...")
	veleroTrustPolicy, err := iamManager.CreateVeleroTrustPolicy(
		o.config.AccountID, o.config.OIDCID, o.config.EKSClusterRegion,
		o.config.VeleroNamespace, o.config.VeleroServiceAccount)
	if err != nil {
		return err
	}

	veleroRoleARN, err := iamManager.CreateRole(o.ctx, o.config.VeleroRoleName, veleroTrustPolicy,
		"IAM role for Velero")
	if err != nil {
		o.stateStore.AddOperation(state, "create_velero_role", "failed", "Create Velero role", err)
		return err
	}

	// Create and attach Velero policy
	veleroPolicy := o.getVeleroPolicy()
	veleroPolicyARN, err := iamManager.CreatePolicy(o.ctx,
		fmt.Sprintf("%s-policy", o.config.VeleroRoleName), veleroPolicy, "Velero backup policy")
	if err != nil {
		return err
	}

	if err := iamManager.AttachPolicy(o.ctx, o.config.VeleroRoleName, veleroPolicyARN); err != nil {
		return err
	}

	o.stateStore.SetResource(state, "velero_role_arn", veleroRoleARN)
	o.stateStore.AddOperation(state, "create_velero_role", "success", "Create Velero role", nil)

	return nil
}

func (o *Orchestrator) installVelero(state *state.State) error {
	veleroMgr := velero.NewVeleroManager(o.config.VeleroNamespace, o.config.KubeconfigPath)

	var veleroConfig *velero.VeleroConfig

	if o.config.UseIAMUser {
		// Use IAM user with access keys
		fmt.Println("Configuring Velero with IAM user credentials...")

		accessKeyID, exists := o.stateStore.GetResource(state, "velero_access_key_id")
		if !exists {
			return fmt.Errorf("Velero access key ID not found in state")
		}

		secretAccessKey, exists := o.stateStore.GetResource(state, "velero_secret_access_key")
		if !exists {
			return fmt.Errorf("Velero secret access key not found in state")
		}

		// Create credentials file
		credentialsFile := "aws-velero-credentials"
		credContent := fmt.Sprintf("[default]\naws_access_key_id=%s\naws_secret_access_key=%s\n",
			accessKeyID, secretAccessKey)

		if err := os.WriteFile(credentialsFile, []byte(credContent), 0600); err != nil {
			return fmt.Errorf("failed to create credentials file: %w", err)
		}
		defer os.Remove(credentialsFile)

		fmt.Printf("✓ Created credentials file: %s\n", credentialsFile)

		veleroConfig = &velero.VeleroConfig{
			BucketName:      o.config.VeleroBucketName,
			Region:          o.config.VeleroBucketRegion,
			UseIRSA:         false,
			CredentialsFile: credentialsFile,
			PluginVersion:   o.config.VeleroPluginVersion,
			UseCSI:          true,
			UseNodeAgent:    true,
		}
	} else {
		// Use IRSA
		fmt.Println("Configuring Velero with IRSA...")
		veleroRoleARN, _ := o.stateStore.GetResource(state, "velero_role_arn")

		veleroConfig = &velero.VeleroConfig{
			BucketName:    o.config.VeleroBucketName,
			Region:        o.config.VeleroBucketRegion,
			UseIRSA:       true,
			IAMRoleARN:    veleroRoleARN,
			PluginVersion: o.config.VeleroPluginVersion,
			UseCSI:        true,
			UseNodeAgent:  true,
		}
	}

	if err := veleroMgr.Install(o.ctx, veleroConfig); err != nil {
		o.stateStore.AddOperation(state, "install_velero", "failed", "Install Velero", err)
		return err
	}

	// Wait for Velero pods to be ready
	fmt.Println("\nWaiting for Velero pods to be ready...")
	time.Sleep(10 * time.Second)

	// Verify Velero installation
	if err := veleroMgr.VerifyInstallation(o.ctx); err != nil {
		fmt.Printf("⚠ Warning: Velero verification failed: %v\n", err)
		// Don't fail the installation, just warn
	}

	o.stateStore.AddOperation(state, "install_velero", "success", "Install Velero", nil)
	return nil
}

func (o *Orchestrator) installEBSCSI(state *state.State) error {
	// Skip EBS CSI addon installation if disabled
	if !o.config.InstallEBSCSIAddon {
		fmt.Println("Skipping EBS CSI addon installation (disabled in config)")
		fmt.Println("Please install EBS CSI driver manually following the documentation")
		o.stateStore.AddOperation(state, "install_ebs_csi", "skipped", "Install EBS CSI driver (manual)", nil)
		return nil
	}

	ebsCSIMgr, err := aws.NewEBSCSIManager(o.ctx, o.config.Region, o.config.KubeconfigPath)
	if err != nil {
		return err
	}

	ebsRoleARN, _ := o.stateStore.GetResource(state, "ebs_role_arn")
	if ebsRoleARN == "" {
		return fmt.Errorf("EBS role ARN not found in state")
	}

	if err := ebsCSIMgr.InstallEBSCSIAddon(o.ctx, o.config.ClusterName, ebsRoleARN, o.config.EBSCSIVersion); err != nil {
		o.stateStore.AddOperation(state, "install_ebs_csi", "failed", "Install EBS CSI driver", err)
		return err
	}

	o.stateStore.AddOperation(state, "install_ebs_csi", "success", "Install EBS CSI driver", nil)
	return nil
}

func (o *Orchestrator) installCSISnapshotController(state *state.State) error {
	k8sClient, err := kubernetes.NewK8sClient(o.config.KubeconfigPath)
	if err != nil {
		return err
	}

	snapshotMgr := kubernetes.NewSnapshotClassManager(k8sClient.GetClientset(), o.config.KubeconfigPath)

	if err := snapshotMgr.InstallCSISnapshotController(o.ctx); err != nil {
		o.stateStore.AddOperation(state, "install_csi_snapshot", "failed", "Install CSI snapshot controller", err)
		return err
	}

	o.stateStore.AddOperation(state, "install_csi_snapshot", "success", "Install CSI snapshot controller", nil)
	return nil
}

func (o *Orchestrator) applyVolumeSnapshotClasses(state *state.State) error {
	k8sClient, err := kubernetes.NewK8sClient(o.config.KubeconfigPath)
	if err != nil {
		return err
	}

	snapshotMgr := kubernetes.NewSnapshotClassManager(k8sClient.GetClientset(), o.config.KubeconfigPath)

	if err := snapshotMgr.ApplyVolumeSnapshotClasses(o.ctx); err != nil {
		o.stateStore.AddOperation(state, "create_snapshot_classes", "failed", "Create VolumeSnapshotClasses", err)
		return err
	}

	o.stateStore.AddOperation(state, "create_snapshot_classes", "success", "Create VolumeSnapshotClasses", nil)
	return nil
}

func (o *Orchestrator) patchNodeAgentTolerations(state *state.State) error {
	veleroMgr := velero.NewVeleroManager(o.config.VeleroNamespace, o.config.KubeconfigPath)

	if err := veleroMgr.PatchNodeAgentTolerations(o.ctx); err != nil {
		o.stateStore.AddOperation(state, "patch_node_agent", "failed", "Patch node-agent tolerations", err)
		return err
	}

	o.stateStore.AddOperation(state, "patch_node_agent", "success", "Patch node-agent tolerations", nil)
	return nil
}

// executePermissionBackup runs backup-permissions.sh before the Velero snapshot
func (o *Orchestrator) executePermissionBackup(namespace string) error {
	fmt.Printf("   Executing permission backup script for namespace: %s\n", namespace)

	scriptPath := "./scripts/backup-permissions.sh"
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return fmt.Errorf("permission backup script not found: %s", scriptPath)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("NAMESPACE=%s", namespace),
		fmt.Sprintf("STORAGE_CLASS=%s", o.config.NFSStorageClass),
	)
	if o.config.KubeconfigPath != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("KUBECONFIG=%s", o.config.KubeconfigPath))
	}
	// Stream script output directly to the terminal in real-time
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("permission backup script failed: %v", err)
	}
	return nil
}

// executePermissionRestore runs restore-permissions.sh after the Velero restore
func (o *Orchestrator) executePermissionRestore(namespace string) error {
	fmt.Printf("   Executing permission restore script for namespace: %s\n", namespace)

	scriptPath := "./scripts/restore-permissions.sh"
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return fmt.Errorf("permission restore script not found: %s", scriptPath)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("NAMESPACE=%s", namespace),
	)
	if o.config.KubeconfigPath != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("KUBECONFIG=%s", o.config.KubeconfigPath))
	}
	// Stream script output directly to the terminal in real-time
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("permission restore script failed: %v", err)
	}
	return nil
}

// executeIngressUpdate runs update-ingress.sh after permission restore to patch ingress hostnames
func (o *Orchestrator) executeIngressUpdate(namespace string) error {
	fmt.Printf("   Executing ingress update script for namespace: %s\n", namespace)

	scriptPath := "./scripts/update-ingress.sh"
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return fmt.Errorf("ingress update script not found: %s", scriptPath)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("NAMESPACE=%s", namespace),
	)
	if o.config.KubeconfigPath != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("KUBECONFIG=%s", o.config.KubeconfigPath))
	}
	// Stream script output directly to the terminal in real-time
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ingress update script failed: %v", err)
	}
	return nil
}

func (o *Orchestrator) getVeleroPolicy() string {
	return fmt.Sprintf(`{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "ec2:DescribeVolumes",
        "ec2:DescribeSnapshots",
        "ec2:CreateTags",
        "ec2:CreateVolume",
        "ec2:CreateSnapshot",
        "ec2:DeleteSnapshot"
      ],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:DeleteObject",
        "s3:PutObject",
        "s3:PutObjectTagging",
        "s3:AbortMultipartUpload",
        "s3:ListMultipartUploadParts"
      ],
      "Resource": "arn:aws:s3:::%s/*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "s3:ListBucket"
      ],
      "Resource": "arn:aws:s3:::%s"
    }
  ]
}`, o.config.VeleroBucketName, o.config.VeleroBucketName)
}

// Cleanup helper methods

func (o *Orchestrator) uninstallVelero() error {
	veleroMgr := velero.NewVeleroManager(o.config.VeleroNamespace, o.config.KubeconfigPath)
	return veleroMgr.Uninstall(o.ctx)
}

func (o *Orchestrator) deleteVolumeSnapshotClasses() error {
	k8sClient, err := kubernetes.NewK8sClient(o.config.KubeconfigPath)
	if err != nil {
		return err
	}
	snapshotMgr := kubernetes.NewSnapshotClassManager(k8sClient.GetClientset(), o.config.KubeconfigPath)
	return snapshotMgr.DeleteVolumeSnapshotClasses(o.ctx)
}

func (o *Orchestrator) uninstallCSISnapshotController() error {
	k8sClient, err := kubernetes.NewK8sClient(o.config.KubeconfigPath)
	if err != nil {
		return err
	}
	snapshotMgr := kubernetes.NewSnapshotClassManager(k8sClient.GetClientset(), o.config.KubeconfigPath)
	return snapshotMgr.UninstallCSISnapshotController(o.ctx)
}

func (o *Orchestrator) uninstallEBSCSI() error {
	ebsCSIMgr, err := aws.NewEBSCSIManager(o.ctx, o.config.Region, o.config.KubeconfigPath)
	if err != nil {
		return err
	}
	return ebsCSIMgr.UninstallEBSCSI(o.ctx, o.config.ClusterName, o.config.EKSClusterRegion)
}

func (o *Orchestrator) deleteIAMResources(state *state.State) error {
	iamManager, err := aws.NewIAMManager(o.ctx, o.config.Region)
	if err != nil {
		return err
	}

	var errors []error

	// Delete Velero IAM role
	fmt.Printf("  → Deleting Velero IAM role: %s\n", o.config.VeleroRoleName)
	if err := iamManager.DeleteRole(o.ctx, o.config.VeleroRoleName); err != nil {
		fmt.Printf("    ✗ Failed: %v\n", err)
		errors = append(errors, fmt.Errorf("velero role: %w", err))
	} else {
		fmt.Printf("    ✓ Deleted role: %s\n", o.config.VeleroRoleName)
	}

	// Delete Velero custom policy
	veleroPolicyName := fmt.Sprintf("%s-policy", o.config.VeleroRoleName)
	veleroPolicyARN := fmt.Sprintf("arn:aws:iam::%s:policy/%s", o.config.AccountID, veleroPolicyName)
	fmt.Printf("  → Deleting Velero IAM policy: %s\n", veleroPolicyARN)
	if err := iamManager.DeletePolicy(o.ctx, veleroPolicyARN); err != nil {
		fmt.Printf("    ✗ Failed: %v\n", err)
		errors = append(errors, fmt.Errorf("velero policy: %w", err))
	} else {
		fmt.Printf("    ✓ Deleted policy: %s\n", veleroPolicyARN)
	}

	// Delete EBS CSI IAM role
	fmt.Printf("  → Deleting EBS CSI IAM role: %s\n", o.config.EBSRoleName)
	if err := iamManager.DeleteRole(o.ctx, o.config.EBSRoleName); err != nil {
		fmt.Printf("    ✗ Failed: %v\n", err)
		errors = append(errors, fmt.Errorf("ebs role: %w", err))
	} else {
		fmt.Printf("    ✓ Deleted role: %s\n", o.config.EBSRoleName)
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to delete some IAM resources: %v", errors)
	}

	return nil
}

func (o *Orchestrator) deleteS3Bucket() error {
	s3Manager, err := aws.NewS3Manager(o.ctx, o.config.VeleroBucketRegion)
	if err != nil {
		return err
	}

	// Try to empty bucket first
	fmt.Printf("  → Emptying S3 bucket: %s (region: %s)\n", o.config.VeleroBucketName, o.config.VeleroBucketRegion)
	if err := s3Manager.EmptyBucket(o.ctx, o.config.VeleroBucketName); err != nil {
		fmt.Printf("    ✗ Failed to empty bucket: %v\n", err)
		return err
	}
	fmt.Printf("    ✓ Bucket emptied successfully\n")

	// Delete bucket
	fmt.Printf("  → Deleting S3 bucket: %s\n", o.config.VeleroBucketName)
	if err := s3Manager.DeleteBucket(o.ctx, o.config.VeleroBucketName); err != nil {
		fmt.Printf("    ✗ Failed to delete bucket: %v\n", err)
		return err
	}
	fmt.Printf("    ✓ Deleted bucket: %s\n", o.config.VeleroBucketName)
	return nil
}
