// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Global variable to store the kubeconfig path for kubectl commands
var configuredKubeconfigPath string

// runKubectl executes kubectl with the configured kubeconfig file
func runKubectl(args ...string) *exec.Cmd {
	cmd := exec.Command("kubectl", args...)
	if configuredKubeconfigPath != "" {
		// Set KUBECONFIG for this specific command
		cmd.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", configuredKubeconfigPath))
	}
	return cmd
}

func ApplySnapshotClass(filePath string) error {
	cmd := runKubectl("apply", "-f", filePath)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

func VerifyVolumeSnapshotClasses() error {
	cmd := runKubectl("get", "volumesnapshotclasses.snapshot.storage.k8s.io")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

func CheckKubernetes(env Environment, state *State) error {
	fmt.Println("\n    Starting Kubernetes setup...")
	fmt.Println("=====================================")

	step := "Apply Azure Disk SnapshotClass"
	UpdateStepState(state, step, "IN-PROGRESS")
	fmt.Printf("Applying Azure Disk snapshot class from: configs/snapshot_classes/azure-disk-snapshot-class.yaml\n")
	if err := ApplySnapshotClass("configs/snapshot_classes/azure-disk-snapshot-class.yaml"); err != nil {
		UpdateStepState(state, step, "FAILED")
		return fmt.Errorf("failed to apply azure-disk-snapshot-class: %v", err)
	}
	fmt.Printf("   Applied VolumeSnapshotClass: azure-disk-snapshot-class\n")
	UpdateStepState(state, step, "SUCCESS")

	step = "Apply Azure NFS SnapshotClass"
	UpdateStepState(state, step, "IN-PROGRESS")
	fmt.Printf("    Applying Azure NFS snapshot class from: configs/snapshot_classes/azure-nfs-snapshot-class.yaml\n")
	if err := ApplySnapshotClass("configs/snapshot_classes/azure-nfs-snapshot-class.yaml"); err != nil {
		UpdateStepState(state, step, "FAILED")
		return fmt.Errorf("failed to apply azure-nfs-snapshot-class: %v", err)
	}
	fmt.Printf("   Applied VolumeSnapshotClass: azure-nfs-snapshot-class\n")
	UpdateStepState(state, step, "SUCCESS")

	step = "Verify VolumeSnapshotClasses"
	UpdateStepState(state, step, "IN-PROGRESS")
	fmt.Printf("   Verifying VolumeSnapshotClasses are available...\n")
	if err := VerifyVolumeSnapshotClasses(); err != nil {
		UpdateStepState(state, step, "FAILED")
		return fmt.Errorf("failed to verify VolumeSnapshotClasses: %v", err)
	}
	fmt.Printf("   VolumeSnapshotClasses are available in cluster\n")
	UpdateStepState(state, step, "SUCCESS")

	fmt.Println("\n   Kubernetes connectivity and snapshot classes configured successfully!")
	fmt.Println("   For Velero node-agent configuration, run: go run *.go --steps=velero")
	fmt.Println("================================================================")
	return nil
}

// CheckAzureLogin verifies Azure CLI authentication
func CheckAzureLogin() error {
	cmd := exec.Command("az", "account", "show")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Azure CLI not authenticated. Run 'az login' first")
	}
	return nil
}

// CheckKubeconfig verifies kubectl configuration
func CheckKubeconfig(kubeconfigPath string) error {
	// Use the configured kubeconfig path from environment_details.go
	// This takes priority over KUBECONFIG environment variable

	// Handle tilde expansion for home directory
	if strings.HasPrefix(kubeconfigPath, "~/") {
		homeDir, _ := os.UserHomeDir()
		kubeconfigPath = filepath.Join(homeDir, kubeconfigPath[2:])
	}

	// Check if the configured kubeconfig file exists
	if _, err := os.Stat(kubeconfigPath); os.IsNotExist(err) {
		// If configured path doesn't exist, fall back to KUBECONFIG environment variable
		if kubeconfigEnv := os.Getenv("KUBECONFIG"); kubeconfigEnv != "" {
			fmt.Printf("    Configured kubeconfig not found: %s\n", kubeconfigPath)
			fmt.Printf("Falling back to KUBECONFIG environment variable: %s\n", kubeconfigEnv)
			if _, err := os.Stat(kubeconfigEnv); os.IsNotExist(err) {
				return fmt.Errorf("kubeconfig not found at KUBECONFIG path: %s", kubeconfigEnv)
			}
			configuredKubeconfigPath = kubeconfigEnv
			// Also set environment variable so cleanup functions can access it
			os.Setenv("CONFIGURED_KUBECONFIG_PATH", kubeconfigEnv)
			return nil
		}
		return fmt.Errorf("kubeconfig not found at configured path: %s. Update KubeconfigPath in environment_details.go or set KUBECONFIG environment variable", kubeconfigPath)
	}

	// Store the configured path globally for kubectl commands
	configuredKubeconfigPath = kubeconfigPath
	// Also set environment variable so cleanup functions can access it
	os.Setenv("CONFIGURED_KUBECONFIG_PATH", kubeconfigPath)
	fmt.Printf("   Using configured kubeconfig: %s\n", kubeconfigPath)
	return nil
}

// CheckClusterAccess verifies kubectl can connect to cluster
func CheckClusterAccess() error {
	cmd := runKubectl("cluster-info")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cannot access Kubernetes cluster. Check kubectl configuration")
	}
	return nil
}

// isVeleroInstalled checks if Velero namespace and components exist
func isVeleroInstalled() bool {
	// Check if Velero namespace exists
	cmd := runKubectl("get", "namespace", "velero")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return false
	}

	// Check if Velero deployment exists
	cmd = runKubectl("get", "deployment", "velero", "-n", "velero")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return false
	}

	// Check if node-agent daemonset exists (required for patching)
	cmd = runKubectl("get", "daemonset", "node-agent", "-n", "velero")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// executePermissionBackup runs the permission backup script for the specified namespace
func executePermissionBackup(namespace string) error {
	fmt.Printf("   Executing permission backup script for namespace: %s\n", namespace)

	// Get the script path relative to the current working directory
	scriptPath := "./scripts/backup-permissions.sh"

	// Check if the script exists
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return fmt.Errorf("permission backup script not found: %s", scriptPath)
	}

	// Create the command to run the script
	cmd := exec.Command("bash", scriptPath)

	// Set environment variables for the script
	env := GetEnvironment()
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("NAMESPACE=%s", namespace),
		fmt.Sprintf("STORAGE_CLASS=%s", env.NFSStorageClass),
	)

	// Set the kubeconfig if we have one configured
	if configuredKubeconfigPath != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("KUBECONFIG=%s", configuredKubeconfigPath))
	}

	// Run the script and capture output
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("permission backup script failed: %v\nOutput: %s", err, string(output))
	}

	fmt.Printf("   Permission backup script output:\n%s\n", string(output))
	return nil
}

// executePermissionRestore runs the permission restore script for the specified namespace
func executePermissionRestore(namespace string) error {
	fmt.Printf("   Executing permission restore script for namespace: %s\n", namespace)

	// Get the script path relative to the current working directory
	scriptPath := "./scripts/restore-permissions.sh"

	// Check if the script exists
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return fmt.Errorf("permission restore script not found: %s", scriptPath)
	}

	// Create the command to run the script
	cmd := exec.Command("bash", scriptPath)

	// Set environment variables for the script
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("NAMESPACE=%s", namespace),
	)

	// Set the kubeconfig if we have one configured
	if configuredKubeconfigPath != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("KUBECONFIG=%s", configuredKubeconfigPath))
	}

	// Run the script and capture output
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("permission restore script failed: %v\nOutput: %s", err, string(output))
	}

	fmt.Printf("   Permission restore script output:\n%s\n", string(output))
	return nil
}

// executeIngressUpdate runs the update-ingress.sh script to patch ingress hostnames
// after a Velero restore so they point to the DR cluster's nginx LoadBalancer address.
func executeIngressUpdate(namespace string) error {
	fmt.Printf("   Executing ingress update script for namespace: %s\n", namespace)

	scriptPath := "./scripts/update-ingress.sh"

	// Check if the script exists
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return fmt.Errorf("ingress update script not found: %s", scriptPath)
	}

	cmd := exec.Command("bash", scriptPath)

	// Pass NAMESPACE and optionally KUBECONFIG to the script
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("NAMESPACE=%s", namespace),
	)
	if configuredKubeconfigPath != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("KUBECONFIG=%s", configuredKubeconfigPath))
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ingress update script failed: %v\nOutput: %s", err, string(output))
	}

	fmt.Printf("   Ingress update script output:\n%s\n", string(output))
	return nil
}

// InstallVelero installs Velero using the Azure credentials and configuration
func InstallVelero(env Environment) error {
	fmt.Println("\n   Installing Velero in Kubernetes Cluster")
	fmt.Println("=========================================")

	// Check cluster type - for restore clusters, credentials should already be set up
	if env.ClusterType == "restore" {
		fmt.Printf("   Setting up Velero on RESTORE cluster\n")
		fmt.Printf("   (Credentials and kubeconfig already configured)\n")
	} else {
		fmt.Printf("   Setting up Velero on SOURCE cluster\n")
	}

	// Check if Velero is already installed
	if isVeleroInstalled() {
		fmt.Printf("    Velero is already installed\n")
		fmt.Printf("   Skipping installation, proceeding to verification\n")
		return nil
	}

	// Check if velero CLI is available
	fmt.Printf("   Checking Velero CLI availability...\n")
	cmd := exec.Command("velero", "version", "--client-only")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Velero CLI not found. Please install Velero CLI first: https://velero.io/docs/main/basic-install/")
	}
	fmt.Printf("   Velero CLI detected\n")

	// Check if credentials file exists (use restore path for restore cluster)
	var credsPath string
	if env.ClusterType == "restore" {
		credsPath = "./configs/restore/azure-velero-credentials"
	} else {
		credsPath = "./credentials/azure-velero-credentials"
	}

	if _, err := os.Stat(credsPath); os.IsNotExist(err) {
		return fmt.Errorf("credentials file not found: %s. Run azure step first", credsPath)
	}
	fmt.Printf("   Azure credentials file found: %s\n", credsPath)

	// Install Velero with Azure provider
	fmt.Printf("   Installing Velero with Azure provider...\n")

	// IMPORTANT: Two different resource groups for different purposes:
	// 1. Backup Location: Uses VeleroBlobRG (for backup metadata storage)
	// 2. Snapshot Location: Uses MC resource group (where AKS disks are located)
	snapshotResourceGroup := env.SourceMCResourceGroup
	fmt.Printf(" Backup location (metadata): %s\n", env.VeleroBlobRG)
	fmt.Printf(" Snapshot location (disks): %s\n", snapshotResourceGroup)

	installArgs := []string{
		"install",
		"--provider", "azure",
		"--plugins", "velero/velero-plugin-for-microsoft-azure:v1.13.0",
		"--features=EnableCSI",
		"--bucket", env.VeleroStoreName,
		"--secret-file", credsPath,
		"--backup-location-config",
		fmt.Sprintf("resourceGroup=%s,storageAccount=%s,subscriptionId=%s",
			env.VeleroBlobRG, env.VeleroStoreName, env.SubscriptionID),
		"--snapshot-location-config",
		fmt.Sprintf("apiTimeout=15m,subscriptionId=%s,resourceGroup=%s",
			env.SubscriptionID, env.VeleroSnapRG),
		"--use-node-agent",
		"--default-snapshot-move-data",
	}

	// Use configured kubeconfig
	installCmd := exec.Command("velero", installArgs...)
	if configuredKubeconfigPath != "" {
		installCmd.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", configuredKubeconfigPath))
	}

	output, err := installCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to install Velero: %v\nOutput: %s", err, string(output))
	}

	fmt.Printf("   Velero Installation Output:\n%s\n", string(output))

	// Wait for Velero pods to be ready
	fmt.Printf("   Waiting for Velero pods to be ready...\n")
	for i := 0; i < 30; i++ { // Wait up to 5 minutes
		time.Sleep(10 * time.Second)
		if isVeleroInstalled() {
			cmd := runKubectl("get", "pods", "-n", "velero", "--no-headers")
			output, err := cmd.Output()
			if err == nil {
				outputStr := string(output)
				if strings.Contains(outputStr, "Running") && !strings.Contains(outputStr, "Pending") && !strings.Contains(outputStr, "ContainerCreating") {
					fmt.Printf("   Velero pods are ready\n")
					break
				}
			}
		}
		fmt.Printf("   Still waiting... (%d/30)\n", i+1)
	}

	// Final verification
	if !isVeleroInstalled() {
		return fmt.Errorf("Velero installation completed but pods are not ready. Check manually with: kubectl get pods -n velero")
	}

	fmt.Printf("   Velero installation completed successfully!\n")

	// Validate backup location after installation
	fmt.Printf("   Validating backup storage location...\n")
	if err := validateBackupLocationAfterInstall(); err != nil {
		fmt.Printf("    Warning: Backup location validation failed: %v\n", err)
		fmt.Printf("   This may affect backup operations. Check Velero logs for details.\n")
		// Don't return error as installation was successful, just warn about backup location
	} else {
		fmt.Printf("   Backup storage location is available and ready\n")
	}

	return nil
}

// VerifyVeleroPods checks if Velero pods are running and configures node agent
func VerifyVeleroPods(env Environment) error {
	fmt.Println("\n   Verifying Velero Installation and Configuration")
	fmt.Println("================================================")

	// First check if Velero is installed
	if !isVeleroInstalled() {
		return fmt.Errorf("Velero is not installed. Please install Velero first")
	}

	fmt.Printf("   Velero namespace detected\n")

	// Check if Velero pods are running
	cmd := runKubectl("get", "pods", "-n", "velero", "--no-headers")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get Velero pods: %v", err)
	}

	if len(output) == 0 {
		return fmt.Errorf("no Velero pods found in velero namespace")
	}

	// Check for Running status
	outputStr := string(output)
	if !strings.Contains(outputStr, "Running") {
		return fmt.Errorf("Velero pods are not in Running state:\n%s", outputStr)
	}

	fmt.Printf("   All Velero pods are running successfully\n")

	// Display pod details
	cmd = runKubectl("get", "pods", "-n", "velero", "-o", "wide")
	output, err = cmd.CombinedOutput()
	if err == nil {
		fmt.Printf("   Velero Pods:\n%s\n", string(output))
	}

	// Configure node-agent based on cluster type
	fmt.Println("\n   Configuring Velero Node-Agent for SAS Workloads")
	fmt.Println("===================================================")

	if env.ClusterType == "restore" {
		fmt.Printf("   Applying RESTORE cluster configuration (with priority class)...\n")
		if err := PatchVeleroNodeAgentForRestore(); err != nil {
			return fmt.Errorf("failed to configure node-agent for restore cluster: %v", err)
		}
	} else {
		fmt.Printf("   Applying SOURCE cluster configuration...\n")
		fmt.Printf("   Patching Velero node-agent with dynamic tolerations...\n")
		if err := PatchVeleroNodeAgent(); err != nil {
			return fmt.Errorf("failed to patch node-agent: %v", err)
		}
	}

	fmt.Println("\n   Velero verification and configuration completed successfully!")
	fmt.Println("=============================================================")
	return nil
}

// PatchVeleroNodeAgent patches the node-agent daemonset with dynamic node pool tolerations
func PatchVeleroNodeAgent() error {
	// Check if Velero namespace exists
	if err := checkVeleroNamespace(); err != nil {
		return fmt.Errorf("Velero namespace check failed: %v", err)
	}

	// Check if node-agent daemonset exists
	if err := checkNodeAgentExists(); err != nil {
		return fmt.Errorf("node-agent daemonset check failed: %v", err)
	}

	// Discover all node pools and create dynamic tolerations
	tolerations, err := createDynamicTolerations()
	if err != nil {
		return fmt.Errorf("failed to create dynamic tolerations: %v", err)
	}

	// Apply the patch with dynamic tolerations
	patchJSON := fmt.Sprintf(`{
		"spec": {
			"template": {
				"spec": {
					"tolerations": %s
				}
			}
		}
	}`, tolerations)

	cmd := runKubectl("patch", "daemonset", "node-agent", "-n", "velero", "--type=merge", "-p", patchJSON)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to patch node-agent daemonset: %v", err)
	}

	// Verify the patch was applied
	return verifyNodeAgentTolerations()
}

// PatchVeleroNodeAgentForRestore patches the node-agent daemonset for restore clusters with priority class and tolerations
func PatchVeleroNodeAgentForRestore() error {
	fmt.Println("   Configuring Velero Node-Agent for RESTORE cluster...")

	// Step 1: Apply priority class for restore cluster
	fmt.Printf("   Creating high-priority class for node-agent...\n")
	if err := applyVeleroPriorityClass(); err != nil {
		return fmt.Errorf("failed to apply priority class: %v", err)
	}

	// Step 2: Check if Velero namespace exists
	if err := checkVeleroNamespace(); err != nil {
		return fmt.Errorf("Velero namespace check failed: %v", err)
	}

	// Step 3: Check if node-agent daemonset exists
	if err := checkNodeAgentExists(); err != nil {
		return fmt.Errorf("node-agent daemonset check failed: %v", err)
	}

	// Step 4: Discover all node pools and create dynamic tolerations
	tolerations, err := createDynamicTolerations()
	if err != nil {
		return fmt.Errorf("failed to create dynamic tolerations: %v", err)
	}

	// Step 5: Apply comprehensive patch with both tolerations AND priority class
	fmt.Printf(" Applying comprehensive patch with priority class and tolerations...\n")
	patchJSON := fmt.Sprintf(`{
		"spec": {
			"template": {
				"spec": {
					"priorityClassName": "velero-node-agent-priority",
					"tolerations": %s
				}
			}
		}
	}`, tolerations)

	cmd := runKubectl("patch", "daemonset", "node-agent", "-n", "velero", "--type=merge", "-p", patchJSON)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to patch node-agent daemonset with priority class: %v\nOutput: %s", err, string(output))
	}

	fmt.Printf("   Node-agent patched with priority class and tolerations\n")

	// Step 6: Verify the comprehensive patch was applied
	if err := verifyNodeAgentRestoreConfiguration(); err != nil {
		return fmt.Errorf("failed to verify restore configuration: %v", err)
	}

	fmt.Printf(" Node-agent successfully configured for restore cluster!\n")
	return nil
}

// applyVeleroPriorityClass creates the high-priority class for Velero node-agent
func applyVeleroPriorityClass() error {
	priorityClassPath := "configs/restore/velero-priorityclass.yaml"

	// Check if priority class file exists
	if _, err := os.Stat(priorityClassPath); os.IsNotExist(err) {
		return fmt.Errorf("priority class file not found: %s", priorityClassPath)
	}

	// Apply the priority class
	cmd := runKubectl("apply", "-f", priorityClassPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to apply priority class: %v\nOutput: %s", err, string(output))
	}

	fmt.Printf("   Priority class 'velero-node-agent-priority' created\n")
	fmt.Printf(" Output: %s\n", string(output))

	// Verify priority class was created
	cmd = runKubectl("get", "priorityclass", "velero-node-agent-priority")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("priority class verification failed: %v", err)
	}

	fmt.Printf("   Priority class verified successfully\n")
	return nil
}

// verifyNodeAgentRestoreConfiguration verifies both priority class and tolerations are applied
func verifyNodeAgentRestoreConfiguration() error {
	// Check priority class
	cmd := runKubectl("get", "daemonset", "node-agent", "-n", "velero", "-o", "jsonpath={.spec.template.spec.priorityClassName}")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to verify priority class: %v", err)
	}

	priorityClass := strings.TrimSpace(string(output))
	if priorityClass != "velero-node-agent-priority" {
		return fmt.Errorf("priority class not set correctly. Expected: velero-node-agent-priority, Got: %s", priorityClass)
	}
	fmt.Printf("   Priority class verified: %s\n", priorityClass)

	// Check tolerations
	cmd = runKubectl("get", "daemonset", "node-agent", "-n", "velero", "-o", "jsonpath={.spec.template.spec.tolerations}")
	output, err = cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to verify tolerations: %v", err)
	}

	tolerationsStr := string(output)
	if len(tolerationsStr) == 0 || tolerationsStr == "null" {
		return fmt.Errorf("no tolerations found in node-agent daemonset")
	}
	fmt.Printf("   Tolerations verified and applied\n")

	// Show comprehensive daemonset configuration
	fmt.Printf(" Node-agent configuration summary:\n")
	cmd = runKubectl("get", "daemonset", "node-agent", "-n", "velero", "-o", "wide")
	if summaryOutput, err := cmd.CombinedOutput(); err == nil {
		fmt.Printf("%s\n", string(summaryOutput))
	}

	return nil
}

// checkVeleroNamespace verifies Velero namespace exists
func checkVeleroNamespace() error {
	cmd := runKubectl("get", "namespace", "velero")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Velero namespace not found. Ensure Velero is installed")
	}
	return nil
}

// checkNodeAgentExists verifies node-agent daemonset exists
func checkNodeAgentExists() error {
	cmd := runKubectl("get", "daemonset", "node-agent", "-n", "velero")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("node-agent daemonset not found. Ensure Velero with node-agent is installed")
	}
	return nil
}

// verifyNodeAgentTolerations verifies the tolerations were applied correctly
func verifyNodeAgentTolerations() error {
	cmd := runKubectl("get", "daemonset", "node-agent", "-n", "velero", "-o", "jsonpath={.spec.template.spec.tolerations}")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to verify tolerations: %v", err)
	}

	// Check for tolerations were applied
	tolerationsStr := string(output)
	if len(tolerationsStr) == 0 || tolerationsStr == "null" {
		return fmt.Errorf("no tolerations found in node-agent daemonset")
	}

	fmt.Println("   Node-agent daemonset successfully patched with dynamic node pool tolerations")
	return nil
}

// createDynamicTolerations discovers node pools and creates appropriate tolerations
func createDynamicTolerations() (string, error) {
	fmt.Println(" Creating dynamic tolerations for node-agent...")

	// Get all node pool labels and taints
	nodePools, err := discoverNodePools()
	if err != nil {
		return "", fmt.Errorf("failed to discover node pools: %v", err)
	}

	// Create base tolerations for common node pool patterns
	var tolerations []map[string]interface{}

	// Add SAS Viya 4 specific tolerations
	fmt.Println(" Adding SAS Viya 4 workload tolerations:")
	sasWorkloads := []string{"stateless", "stateful", "cas", "compute"}
	for _, workload := range sasWorkloads {
		tolerations = append(tolerations, map[string]interface{}{
			"key":      "workload.sas.com/class",
			"operator": "Equal",
			"value":    workload,
			"effect":   "NoSchedule",
		})
		fmt.Printf("      workload.sas.com/class=%s (NoSchedule)\n", workload)
	}

	// Add discovered node pool tolerations (excluding system pools)
	userNodePools := 0
	for _, nodePool := range nodePools {
		if !isSystemNodePool(nodePool) {
			// Add toleration for this specific node pool
			tolerations = append(tolerations, map[string]interface{}{
				"key":      "agentpool",
				"operator": "Equal",
				"value":    nodePool,
				"effect":   "NoSchedule",
			})
			userNodePools++
		}
	}

	if userNodePools > 0 {
		fmt.Printf(" Added node pool tolerations for %d user node pools\n", userNodePools)
	}

	// Add generic node-pool toleration as fallback
	tolerations = append(tolerations, map[string]interface{}{
		"key":      "node-pool",
		"operator": "Exists",
		"effect":   "NoSchedule",
	})
	fmt.Println(" Added generic node-pool toleration (fallback)")

	fmt.Printf(" Total tolerations created: %d\n", len(tolerations))

	// Convert to JSON
	jsonBytes, err := json.Marshal(tolerations)
	if err != nil {
		return "", fmt.Errorf("failed to marshal tolerations: %v", err)
	}

	return string(jsonBytes), nil
}

// discoverNodePools discovers all node pools in the cluster
func discoverNodePools() ([]string, error) {
	cmd := runKubectl("get", "nodes", "-o", "jsonpath={.items[*].metadata.labels.agentpool}")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get node pools: %v", err)
	}

	nodePoolsStr := strings.TrimSpace(string(output))
	if nodePoolsStr == "" {
		// Fallback: try kubernetes.azure.com/agentpool label
		cmd = runKubectl("get", "nodes", "-o", "jsonpath={.items[*].metadata.labels['kubernetes\\.azure\\.com/agentpool']}")
		output, err = cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("failed to get node pools with fallback label: %v", err)
		}
		nodePoolsStr = strings.TrimSpace(string(output))
	}

	if nodePoolsStr == "" {
		fmt.Println("  No node pool labels found, using generic tolerations only")
		return []string{}, nil
	}

	// Split and deduplicate
	nodePools := strings.Fields(nodePoolsStr)
	uniqueNodePools := make(map[string]bool)
	var result []string

	for _, pool := range nodePools {
		if pool != "" && !uniqueNodePools[pool] {
			uniqueNodePools[pool] = true
			result = append(result, pool)
		}
	}

	fmt.Printf(" Discovered node pools: %v\n", result)

	// Show which pools will be included/excluded
	var includedPools []string
	var excludedPools []string
	for _, pool := range result {
		if isSystemNodePool(pool) {
			excludedPools = append(excludedPools, pool)
		} else {
			includedPools = append(includedPools, pool)
		}
	}

	if len(excludedPools) > 0 {
		fmt.Printf("   Excluded system pools: %v\n", excludedPools)
	}
	if len(includedPools) > 0 {
		fmt.Printf("   Including user pools: %v\n", includedPools)
	}

	return result, nil
}

// isSystemNodePool checks if a node pool is a system node pool that should be excluded
func isSystemNodePool(nodePool string) bool {
	systemPools := []string{"system", "nodepool1", "agentpool"}
	for _, systemPool := range systemPools {
		if nodePool == systemPool {
			return true
		}
	}
	return false
}

// ExecuteBackup orchestrates the complete backup process with validations
func ExecuteBackup() error {
	fmt.Println("\n    Starting Velero Backup Process")
	fmt.Println("==================================")

	// Show kubeconfig information for debugging
	kubeconfigInfo := configuredKubeconfigPath
	if kubeconfigInfo == "" {
		kubeconfigInfo = "default kubectl configuration"
	}
	fmt.Printf("   Using kubeconfig: %s\n", kubeconfigInfo)

	// Step 1: Validate Velero Installation
	fmt.Printf("   Validating Velero installation...\n")
	if err := validateVeleroForBackup(); err != nil {
		return fmt.Errorf("Velero validation failed: %v", err)
	}
	fmt.Printf("   Velero installation validated\n")

	// Step 2: Check Backup Location
	fmt.Printf("   Checking backup location availability...\n")
	if err := validateBackupLocation(); err != nil {
		return fmt.Errorf("backup location validation failed: %v", err)
	}
	fmt.Printf("   Backup location is available\n")

	// Step 3: Get Viya Namespace from User
	fmt.Printf("   Getting Viya namespace information...\n")
	namespace, err := promptForNamespace()
	if err != nil {
		return fmt.Errorf("failed to get namespace: %v", err)
	}
	fmt.Printf("   Using namespace: %s\n", namespace)

	// Step 4: Validate Namespace Exists
	if err := validateNamespaceExists(namespace); err != nil {
		return fmt.Errorf("namespace validation failed: %v", err)
	}
	fmt.Printf("   Namespace %s exists in cluster\n", namespace)

	// Step 5: Apply CSI NFS controller patch to extend snapshot timeout
	// NFS snapshots are full copy operations that can take much longer than the
	// default csi-snapshotter timeout. Patch it to 30m before taking any snapshots.
	fmt.Printf("   Patching CSI NFS controller snapshotter timeout (30m)...\n")
	if err := applyCSINFSControllerPatch(); err != nil {
		// Non-fatal: warn but do not abort — cluster may not use NFS CSI
		fmt.Printf("   Warning: CSI NFS controller patch failed (non-fatal): %v\n", err)
	} else {
		fmt.Printf("   CSI NFS controller patch applied\n")
	}

	// Step 6: Execute Permission Backup
	fmt.Printf("   Running permission backup for NFS volumes...\n")
	if err := executePermissionBackup(namespace); err != nil {
		return fmt.Errorf("permission backup failed: %v", err)
	}
	fmt.Printf("   Permission backup completed successfully\n")

	// Step 8: Create Backup Configuration
	fmt.Printf("   Creating backup configuration...\n")
	backupFile, backupName, err := createBackupConfig(namespace)
	if err != nil {
		return fmt.Errorf("failed to create backup config: %v", err)
	}
	fmt.Printf("   Created backup config: %s\n", backupFile)
	fmt.Printf("   Backup name: %s\n", backupName)

	// Step 9: Execute Backup
	fmt.Printf("   Starting backup process...\n")
	if err := applyBackupConfig(backupFile); err != nil {
		return fmt.Errorf("backup execution failed: %v", err)
	}

	// Step 10: Monitor Backup Progress
	fmt.Printf("   Monitoring backup progress...\n")
	if err := monitorBackupProgress(backupName); err != nil {
		return fmt.Errorf("backup monitoring failed: %v", err)
	}

	fmt.Println("\n   Backup process completed successfully!")
	fmt.Println("=====================================")
	return nil
}

// validateVeleroForBackup checks if Velero and node-agent are properly installed
func validateVeleroForBackup() error {
	// Check if Velero is installed
	if !isVeleroInstalled() {
		return fmt.Errorf("Velero is not installed. Run: go run *.go --steps=velero")
	}

	// Check if Velero pods are running
	cmd := runKubectl("get", "pods", "-n", "velero", "--no-headers")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get Velero pods (check kubeconfig): %v", err)
	}

	outputStr := string(output)
	if !strings.Contains(outputStr, "Running") {
		return fmt.Errorf("Velero pods are not running:\n%s", outputStr)
	}

	// Check if node-agent is running
	cmd = runKubectl("get", "daemonset", "node-agent", "-n", "velero", "-o", "jsonpath={.status.numberReady}")
	output, err = cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check node-agent status: %v", err)
	}

	if strings.TrimSpace(string(output)) == "0" {
		return fmt.Errorf("node-agent daemonset has no ready pods")
	}

	fmt.Printf("      Velero pods: Running\n")
	fmt.Printf("      Node-agent: Ready\n")
	return nil
}

// validateBackupLocation checks if backup location is available
func validateBackupLocation() error {
	// Check backup storage locations
	cmd := runKubectl("get", "backupstoragelocations", "-n", "velero", "-o", "jsonpath={.items[*].status.phase}")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get backup storage locations: %v", err)
	}

	phases := strings.TrimSpace(string(output))
	if phases == "" {
		return fmt.Errorf("no backup storage locations found")
	}

	if !strings.Contains(phases, "Available") {
		return fmt.Errorf("backup storage location is not available. Status: %s", phases)
	}

	// Get location details
	cmd = runKubectl("get", "backupstoragelocations", "-n", "velero", "-o", "wide")
	output, err = cmd.CombinedOutput()
	if err == nil {
		fmt.Printf("   Backup Storage Locations:\n%s\n", string(output))
	}

	return nil
}

// validateBackupLocationAfterInstall checks backup location and provides detailed error messages
func validateBackupLocationAfterInstall() error {
	fmt.Printf("   Waiting for backup storage location to initialize...\n")

	// Wait up to 2 minutes for backup location to be ready
	maxWaitTime := 2 * time.Minute
	checkInterval := 10 * time.Second
	startTime := time.Now()

	for time.Since(startTime) < maxWaitTime {
		// Check backup storage locations
		cmd := runKubectl("get", "backupstoragelocations", "-n", "velero", "-o", "jsonpath={.items[*].status.phase}")
		output, err := cmd.Output()
		if err != nil {
			fmt.Printf("   Checking backup locations... (attempt failed: %v)\n", err)
			time.Sleep(checkInterval)
			continue
		}

		phases := strings.TrimSpace(string(output))
		elapsed := time.Since(startTime).Round(time.Second)

		if phases == "" {
			fmt.Printf("   No backup storage locations found yet... (Elapsed: %v)\n", elapsed)
			time.Sleep(checkInterval)
			continue
		}

		if strings.Contains(phases, "Available") {
			// Get location details for confirmation
			cmd = runKubectl("get", "backupstoragelocations", "-n", "velero", "-o", "wide")
			locationOutput, _ := cmd.CombinedOutput()
			fmt.Printf("   Backup Storage Locations:\n%s\n", string(locationOutput))
			return nil
		}

		// If not available, show current status and continue waiting
		fmt.Printf("   Backup location status: %s (Elapsed: %v)\n", phases, elapsed)
		time.Sleep(checkInterval)
	}

	// If we get here, backup location is not available after waiting
	fmt.Printf("   Backup storage location failed to become available after %v\n", maxWaitTime)

	// Get detailed backup storage location information
	fmt.Printf("   Current backup storage location status:\n")
	cmd := runKubectl("get", "backupstoragelocations", "-n", "velero", "-o", "yaml")
	bslOutput, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("    Failed to get backup storage location details: %v\n", err)
	} else {
		fmt.Printf("%s\n", string(bslOutput))
	}

	// Get Velero server logs for troubleshooting
	fmt.Printf("   Velero server logs (last 20 lines):\n")
	cmd = runKubectl("logs", "-n", "velero", "-l", "component=velero", "--tail=20")
	logsOutput, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("    Failed to get Velero logs: %v\n", err)
		return fmt.Errorf("backup storage location not available and unable to retrieve logs")
	} else {
		fmt.Printf("%s\n", string(logsOutput))
		return fmt.Errorf("backup storage location not available - check Velero logs above for details")
	}
}

// promptForNamespace asks user for the Viya namespace name
func promptForNamespace() (string, error) {
	fmt.Print("   Enter the Viya namespace name (default: viya): ")
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read input: %v", err)
	}

	namespace := strings.TrimSpace(input)
	if namespace == "" {
		namespace = "viya"
	}

	return namespace, nil
}

// validateNamespaceExists checks if the specified namespace exists in the cluster
func validateNamespaceExists(namespace string) error {
	cmd := runKubectl("get", "namespace", namespace)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Get list of available namespaces to help user
		namespacesCmd := runKubectl("get", "namespaces", "--no-headers", "-o", "custom-columns=NAME:.metadata.name")
		namespacesOutput, nsErr := namespacesCmd.Output()
		var namespacesList string
		if nsErr == nil {
			namespacesList = fmt.Sprintf("\nAvailable namespaces:\n%s", string(namespacesOutput))
		}
		return fmt.Errorf("namespace '%s' does not exist in cluster: %v\nOutput: %s%s", namespace, err, string(output), namespacesList)
	}
	return nil
}

// createBackupConfig creates a backup YAML file with the specified namespace and returns the file path and backup name
func createBackupConfig(namespace string) (string, string, error) {
	// Read the template
	templatePath := "configs/backup/backup-template.yaml"
	templateContent, err := os.ReadFile(templatePath)
	if err != nil {
		return "", "", fmt.Errorf("failed to read backup template: %v", err)
	}

	// Create timestamp for both backup name and file name
	timestamp := time.Now().Format("20060102-150405")
	backupName := fmt.Sprintf("sas-viya4-backup-%s", timestamp)

	// Replace placeholders with actual values
	backupContent := strings.ReplaceAll(string(templateContent), "NAMESPACE_PLACEHOLDER", namespace)
	backupContent = strings.ReplaceAll(backupContent, "TIMESTAMP_PLACEHOLDER", timestamp)

	// Create timestamped backup file
	backupFileName := fmt.Sprintf("sas-viya4-backup-%s-%s.yaml", namespace, timestamp)
	backupFilePath := filepath.Join("configs/backup", backupFileName)

	// Write the backup file
	if err := os.WriteFile(backupFilePath, []byte(backupContent), 0644); err != nil {
		return "", "", fmt.Errorf("failed to write backup config: %v", err)
	}

	return backupFilePath, backupName, nil
}

// applyBackupConfig applies the backup configuration to start the backup
func applyBackupConfig(backupFile string) error {
	fmt.Printf("   Applying backup configuration: %s\n", backupFile)

	// Show which kubeconfig is being used for kubectl apply
	kubeconfigInfo := configuredKubeconfigPath
	if kubeconfigInfo == "" {
		kubeconfigInfo = "default kubectl configuration"
	}
	fmt.Printf("   Using kubeconfig for backup: %s\n", kubeconfigInfo)

	cmd := runKubectl("apply", "-f", backupFile)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to apply backup config: %v\nOutput: %s", err, string(output))
	}

	fmt.Printf("   Backup started successfully\n")
	fmt.Printf("   Output: %s\n", string(output))
	return nil
}

// monitorBackupProgress monitors the backup until completion
func monitorBackupProgress(backupName string) error {
	fmt.Printf("   Monitoring backup: %s\n", backupName)

	maxWaitTime := 60 * time.Minute
	checkInterval := 30 * time.Second
	startTime := time.Now()

	for time.Since(startTime) < maxWaitTime {
		// Get backup status
		cmd := runKubectl("get", "backup", backupName, "-n", "velero", "-o", "jsonpath={.status.phase}")
		output, err := cmd.Output()
		if err != nil {
			fmt.Printf("    Failed to check backup status: %v\n", err)
			time.Sleep(checkInterval)
			continue
		}

		phase := strings.TrimSpace(string(output))
		elapsed := time.Since(startTime).Round(time.Second)

		switch phase {
		case "Completed":
			fmt.Printf("   Backup completed successfully! (Time: %v)\n", elapsed)
			return printBackupDetails(backupName)
		case "Failed", "PartiallyFailed":
			fmt.Printf("   Backup failed with status: %s (Time: %v)\n", phase, elapsed)
			return printBackupDetails(backupName)
		case "InProgress", "New":
			fmt.Printf("   Backup in progress... Status: %s (Elapsed: %v)\n", phase, elapsed)
		default:
			fmt.Printf("   Backup status: %s (Elapsed: %v)\n", phase, elapsed)
		}

		time.Sleep(checkInterval)
	}

	return fmt.Errorf("backup monitoring timed out after %v", maxWaitTime)
}

// printBackupDetails shows detailed information about the backup
func printBackupDetails(backupName string) error {
	fmt.Printf("\n📊 Backup Details:\n")
	fmt.Printf("==================\n")

	// Get backup details
	cmd := runKubectl("describe", "backup", backupName, "-n", "velero")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get backup details: %v", err)
	}

	fmt.Printf("%s\n", string(output))
	return nil
}

// setupRestoreClusterCredentials copies credentials from source to restore folder
// This is required because:
// 1. Restore cluster needs same Azure storage access as source cluster
// 2. Velero installation requires credentials file for Azure provider configuration
// 3. Same service principal ensures access to backup storage and snapshot locations
func setupRestoreClusterCredentials() error {
	fmt.Printf("   Copying Azure service principal credentials for restore cluster access...\n")

	sourceCredsPath := "./credentials/azure-velero-credentials"
	targetCredsPath := "./configs/restore/azure-velero-credentials"

	// Check if source credentials exist
	if _, err := os.Stat(sourceCredsPath); os.IsNotExist(err) {
		return fmt.Errorf("source credentials file not found: %s. Please run azure setup first on source cluster", sourceCredsPath)
	}

	// Create restore directory if it doesn't exist
	if err := os.MkdirAll("./configs/restore", 0755); err != nil {
		return fmt.Errorf("failed to create restore directory: %v", err)
	}

	// Copy credentials file using io.Copy for better reliability
	sourceFile, err := os.Open(sourceCredsPath)
	if err != nil {
		return fmt.Errorf("failed to open source credentials: %v", err)
	}
	defer sourceFile.Close()

	targetFile, err := os.Create(targetCredsPath)
	if err != nil {
		return fmt.Errorf("failed to create restore credentials file: %v", err)
	}
	defer targetFile.Close()

	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		return fmt.Errorf("failed to copy credentials: %v", err)
	}

	fmt.Printf("   Azure credentials copied to: %s\n", targetCredsPath)
	fmt.Printf("   Restore cluster will use same Azure storage account and service principal as source cluster\n")
	return nil
}

// promptAndSetRestoreKubeconfig prompts user for restore cluster kubeconfig
func promptAndSetRestoreKubeconfig() error {
	fmt.Printf("   Setting up kubeconfig for RESTORE cluster...\n")
	fmt.Printf("   Please provide the admin kubeconfig path for the restore/target AKS cluster.\n")
	fmt.Printf("   This should be different from the source cluster kubeconfig.\n")
	fmt.Print("   Enter the restore cluster kubeconfig path: ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read input: %v", err)
	}

	kubeconfigPath := strings.TrimSpace(input)
	if kubeconfigPath == "" {
		return fmt.Errorf("kubeconfig path cannot be empty")
	}

	// Handle tilde expansion for home directory
	if strings.HasPrefix(kubeconfigPath, "~/") {
		homeDir, _ := os.UserHomeDir()
		kubeconfigPath = filepath.Join(homeDir, kubeconfigPath[2:])
	}

	// Check if the kubeconfig file exists
	if _, err := os.Stat(kubeconfigPath); os.IsNotExist(err) {
		return fmt.Errorf("kubeconfig not found at path: %s", kubeconfigPath)
	}

	// Test connectivity to the restore cluster
	fmt.Printf("   Testing connectivity to restore cluster...\n")
	testCmd := exec.Command("kubectl", "cluster-info")
	testCmd.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))
	output, err := testCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to connect to restore cluster: %v\nOutput: %s", err, string(output))
	}

	// Set the global kubeconfig path for restore cluster
	configuredKubeconfigPath = kubeconfigPath
	os.Setenv("CONFIGURED_KUBECONFIG_PATH", kubeconfigPath)

	fmt.Printf("   Restore cluster kubeconfig configured: %s\n", kubeconfigPath)
	fmt.Printf("   Connectivity to restore cluster verified\n")
	return nil
}

// ExecuteRestore orchestrates the complete restore process with automatic Velero installation
func ExecuteRestore() error {
	env := GetEnvironment()

	fmt.Println("\n   Starting Complete Velero Restore Process")
	fmt.Println("==========================================")

	// Validate environment for restore operations
	if err := env.ValidateViyaDeploymentType("Velero restore"); err != nil {
		return fmt.Errorf("environment validation failed: %v", err)
	}

	if env.ClusterType != "restore" {
		return fmt.Errorf("ClusterType is set to '%s' but restore mode requires 'restore'. Please update environment_details.go", env.ClusterType)
	}

	// Step 1: Setup restore cluster credentials (required for Velero installation)
	fmt.Println("\nSTEP 1: Setting up Azure credentials for restore cluster")
	fmt.Println("=========================================================")
	if err := setupRestoreClusterCredentials(); err != nil {
		return fmt.Errorf("failed to setup restore credentials: %v", err)
	}

	// Step 2: Configure restore cluster kubeconfig access
	fmt.Println("\nSTEP 2: Configuring restore cluster access")
	fmt.Println("===========================================")
	if err := promptAndSetRestoreKubeconfig(); err != nil {
		return fmt.Errorf("failed to configure restore cluster kubeconfig: %v", err)
	}

	// Step 3: Install Kubernetes prerequisites on restore cluster
	fmt.Println("\nSTEP 3: Installing Kubernetes snapshot classes on restore cluster")
	fmt.Println("==================================================================")
	state := LoadState()
	if err := CheckKubernetes(env, &state); err != nil {
		return fmt.Errorf("failed to setup Kubernetes prerequisites: %v", err)
	}

	// Step 4: Install Velero on restore cluster with same storage configuration
	fmt.Println("\nSTEP 4: Installing Velero on restore cluster with same storage configuration")
	fmt.Println("============================================================================")
	if err := InstallVelero(env); err != nil {
		return fmt.Errorf("failed to install Velero on restore cluster: %v", err)
	}

	// Step 4.5: Apply Velero priority class and patch node-agent DaemonSet
	fmt.Println("\nSTEP 4.5: Configuring Velero node-agent priority class")
	fmt.Println("======================================================")
	if err := applyVeleroNodeAgentPriorityClass(); err != nil {
		return fmt.Errorf("failed to configure Velero node-agent priority class: %v", err)
	}

	// Step 5: Verify Velero installation and configure for restore operations
	fmt.Println("\nSTEP 5: Verifying Velero installation and configuring for restore operations")
	fmt.Println("=============================================================================")
	if err := VerifyVeleroPods(env); err != nil {
		return fmt.Errorf("failed to verify Velero installation: %v", err)
	}

	// Step 6: Validate backup location accessibility from restore cluster
	fmt.Println("\nSTEP 6: Validating backup storage accessibility from restore cluster")
	fmt.Println("====================================================================")
	if err := validateBackupLocation(); err != nil {
		return fmt.Errorf("backup location not accessible from restore cluster: %v", err)
	}

	// Step 7: List available backups and prompt for selection
	fmt.Println("\nSTEP 7: Retrieving available backups from Azure storage")
	fmt.Println("======================================================")
	backups, err := listAvailableBackups()
	if err != nil {
		return fmt.Errorf("failed to list backups: %v", err)
	}

	var backupName string
	if len(backups) == 0 {
		fmt.Printf("   No backups found via automatic sync. This can happen if:\n")
		fmt.Printf("   - Backup metadata hasn't synced yet from Azure storage\n")
		fmt.Printf("   - No backups exist in the Azure storage location\n")
		fmt.Printf("   \n")
		fmt.Printf("   You can manually enter a backup name if you know it exists.\n")
		fmt.Print("   Enter backup name manually (or press Enter to abort): ")

		reader := bufio.NewReader(os.Stdin)
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read backup name: %v", err)
		}

		backupName = strings.TrimSpace(input)
		if backupName == "" {
			return fmt.Errorf("no backup name provided and no backups found automatically")
		}

		fmt.Printf("   Using manually entered backup: %s\n", backupName)
	} else {
		fmt.Printf("   Available backups:\n")
		for i, backup := range backups {
			fmt.Printf("   %d. %s\n", i+1, backup)
		}

		selectedBackup, err := promptForBackupName(backups)
		if err != nil {
			return fmt.Errorf("failed to select backup: %v", err)
		}
		backupName = selectedBackup
		fmt.Printf("   Selected backup: %s\n", backupName)
	}

	// Step 7.5: Apply CSI NFS Controller patch before restore
	fmt.Println("\nSTEP 7.5: Applying CSI NFS controller patch for restore operations")
	fmt.Println("===================================================================")
	if err := applyCSINFSControllerPatch(); err != nil {
		return fmt.Errorf("failed to apply CSI NFS controller patch: %v", err)
	}

	// Step 8: Create and apply restore configuration
	fmt.Println("\nSTEP 8: Creating and applying restore configuration")
	fmt.Println("===================================================")
	fmt.Printf("   Creating restore configuration for backup: %s\n", backupName)
	restoreConfigPath, restoreName, err := createRestoreConfig(backupName)
	if err != nil {
		return fmt.Errorf("failed to create restore config: %v", err)
	}

	fmt.Printf("   Applying restore configuration: %s\n", restoreName)
	if err := applyRestoreConfig(restoreConfigPath); err != nil {
		return fmt.Errorf("failed to apply restore config: %v", err)
	}

	// Step 9: Monitor restore progress
	fmt.Println("\nSTEP 9: Monitoring restore progress")
	fmt.Println("===================================")
	fmt.Printf("   Monitoring restore progress for: %s\n", restoreName)
	if err := monitorRestoreProgress(restoreName); err != nil {
		return fmt.Errorf("restore monitoring failed: %v", err)
	}

	// Step 10: Execute Permission Restore (NEW STEP)
	fmt.Println("\nSTEP 10: Executing permission restore for NFS volumes")
	fmt.Println("====================================================")

	// Get the namespace from user for permission restore
	namespace, err := promptForNamespace()
	if err != nil {
		return fmt.Errorf("failed to get namespace for permission restore: %v", err)
	}

	// Validate that the namespace exists in the restore cluster
	if err := validateNamespaceExists(namespace); err != nil {
		return fmt.Errorf("namespace validation failed for permission restore: %v", err)
	}

	fmt.Printf("   Running permission restore for namespace: %s\n", namespace)
	if err := executePermissionRestore(namespace); err != nil {
		return fmt.Errorf("permission restore failed: %v", err)
	}
	fmt.Printf("   Permission restore completed successfully\n")

	// Step 11: Print final restore details
	fmt.Println("\nSTEP 11: Restore completed successfully!")
	fmt.Println("========================================")
	fmt.Println("\n⚠️  MANUAL ACTION REQUIRED:")
	fmt.Println("   Run the ingress update script to point ingress to the DR cluster:")
	fmt.Printf("   cd azure && ./scripts/update-ingress.sh\n")
	fmt.Println("   (or set NEW_INGRESS_HOST=<fqdn> ./scripts/update-ingress.sh for non-interactive)")
	if err := printRestoreDetails(restoreName); err != nil {
		fmt.Printf("   Warning: Failed to print restore details: %v\n", err)
	}

	return nil
}

// listAvailableBackups gets list of backups from Velero with sync from Azure storage
func listAvailableBackups() ([]string, error) {
	fmt.Printf("   Syncing backup metadata from Azure storage to restore cluster...\n")

	// Force Velero to sync backup metadata from Azure storage
	// This is crucial for restore clusters that need to discover existing backups
	syncCmd := runKubectl("annotate", "backupstoragelocation", "default", "-n", "velero",
		"velero.io/backup-sync-trigger="+"backup-sync-"+time.Now().Format("20060102-150405"))
	_, syncErr := syncCmd.CombinedOutput()
	if syncErr != nil {
		fmt.Printf("   Warning: Failed to trigger backup sync: %v\n", syncErr)
		fmt.Printf("   Attempting to list existing backups anyway...\n")
	} else {
		fmt.Printf("   Backup sync triggered successfully\n")

		// Wait for initial sync to complete (increased timing for Azure storage)
		fmt.Printf("   Waiting for backup metadata sync to complete (30 seconds)...\n")
		time.Sleep(30 * time.Second)
	}

	// Try to get backups from the cluster
	cmd := runKubectl("get", "backups", "-n", "velero", "--no-headers", "-o", "custom-columns=NAME:.metadata.name")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get backups: %v", err)
	}

	backupList := strings.TrimSpace(string(output))
	if backupList == "" {
		// If no backups found, wait longer for Azure storage sync
		fmt.Printf("   No backups found yet, waiting longer for Azure storage sync (45 seconds)...\n")
		time.Sleep(45 * time.Second)

		// Try again after extended wait
		cmd = runKubectl("get", "backups", "-n", "velero", "--no-headers", "-o", "custom-columns=NAME:.metadata.name")
		output, err = cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("failed to get backups after extended wait: %v", err)
		}
		backupList = strings.TrimSpace(string(output))

		// If still no backups, try one more time with maximum wait
		if backupList == "" {
			fmt.Printf("   Still no backups found, trying final sync attempt (60 seconds)...\n")

			// Trigger sync again in case the first one didn't work
			syncCmd2 := runKubectl("annotate", "backupstoragelocation", "default", "-n", "velero",
				"velero.io/backup-sync-trigger="+"backup-sync-retry-"+time.Now().Format("20060102-150405"), "--overwrite")
			syncCmd2.CombinedOutput() // Ignore errors for retry

			time.Sleep(60 * time.Second)

			cmd = runKubectl("get", "backups", "-n", "velero", "--no-headers", "-o", "custom-columns=NAME:.metadata.name")
			output, err = cmd.Output()
			if err != nil {
				return nil, fmt.Errorf("failed to get backups after maximum wait: %v", err)
			}
			backupList = strings.TrimSpace(string(output))
		}
	}

	if backupList == "" {
		return []string{}, nil
	}

	backups := strings.Split(backupList, "\n")
	var validBackups []string
	for _, backup := range backups {
		backup = strings.TrimSpace(backup)
		if backup != "" {
			validBackups = append(validBackups, backup)
		}
	}

	fmt.Printf("   Successfully found %d backup(s) after sync\n", len(validBackups))
	return validBackups, nil
}

// promptForBackupName asks user to select a backup for restore
func promptForBackupName(availableBackups []string) (string, error) {
	fmt.Print("   Enter the backup name to restore from: ")
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read input: %v", err)
	}

	backupName := strings.TrimSpace(input)
	if backupName == "" {
		return "", fmt.Errorf("backup name cannot be empty")
	}

	// Validate that the backup exists
	backupExists := false
	for _, backup := range availableBackups {
		if backup == backupName {
			backupExists = true
			break
		}
	}

	if !backupExists {
		return "", fmt.Errorf("backup '%s' not found in available backups", backupName)
	}

	return backupName, nil
}

// createRestoreConfig creates a restore YAML file with the specified backup name
func createRestoreConfig(backupName string) (string, string, error) {
	// Read the template
	templatePath := "configs/restore/restore-template.yaml"
	templateContent, err := os.ReadFile(templatePath)
	if err != nil {
		return "", "", fmt.Errorf("failed to read restore template: %v", err)
	}

	// Create timestamp for restore name and file name
	timestamp := time.Now().Format("20060102-150405")
	restoreName := fmt.Sprintf("restore-%s-%s", backupName, timestamp)

	// Replace placeholders with actual values
	restoreContent := strings.ReplaceAll(string(templateContent), "BACKUP_NAME_PLACEHOLDER", backupName)
	restoreContent = strings.ReplaceAll(restoreContent, "RESTORE_NAME_PLACEHOLDER", restoreName)

	// Create timestamped restore file
	restoreFileName := fmt.Sprintf("restore-%s-%s.yaml", backupName, timestamp)
	restoreFilePath := filepath.Join("configs/restore", restoreFileName)

	// Write the restore file
	if err := os.WriteFile(restoreFilePath, []byte(restoreContent), 0644); err != nil {
		return "", "", fmt.Errorf("failed to write restore config: %v", err)
	}

	return restoreFilePath, restoreName, nil
}

// applyRestoreConfig applies the restore configuration to start the restore
func applyRestoreConfig(restoreFile string) error {
	fmt.Printf("   Applying restore configuration: %s\n", restoreFile)

	// Show which kubeconfig is being used for kubectl apply
	kubeconfigInfo := configuredKubeconfigPath
	if kubeconfigInfo == "" {
		kubeconfigInfo = "default kubectl configuration"
	}
	fmt.Printf("   Using kubeconfig for restore: %s\n", kubeconfigInfo)

	cmd := runKubectl("apply", "-f", restoreFile)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to apply restore config: %v\nOutput: %s", err, string(output))
	}

	fmt.Printf("   Restore started successfully\n")
	fmt.Printf("   Output: %s\n", string(output))
	return nil
}

// monitorRestoreProgress monitors the restore until completion
func monitorRestoreProgress(restoreName string) error {
	fmt.Printf("   Monitoring restore: %s\n", restoreName)

	maxWaitTime := 45 * time.Minute // Restores can take longer than backups
	checkInterval := 30 * time.Second
	startTime := time.Now()

	for time.Since(startTime) < maxWaitTime {
		// Get restore status
		cmd := runKubectl("get", "restore", restoreName, "-n", "velero", "-o", "jsonpath={.status.phase}")
		output, err := cmd.Output()
		if err != nil {
			fmt.Printf("    Failed to check restore status: %v\n", err)
			time.Sleep(checkInterval)
			continue
		}

		phase := strings.TrimSpace(string(output))
		elapsed := time.Since(startTime).Round(time.Second)

		switch phase {
		case "Completed":
			fmt.Printf("   Restore completed successfully! (Time: %v)\n", elapsed)
			return printRestoreDetails(restoreName)
		case "Failed", "PartiallyFailed":
			fmt.Printf("   Restore failed with status: %s (Time: %v)\n", phase, elapsed)
			return printRestoreDetails(restoreName)
		case "InProgress", "New":
			fmt.Printf("   Restore in progress... Status: %s (Elapsed: %v)\n", phase, elapsed)
		default:
			fmt.Printf("   Restore status: %s (Elapsed: %v)\n", phase, elapsed)
		}

		time.Sleep(checkInterval)
	}

	return fmt.Errorf("restore monitoring timed out after %v", maxWaitTime)
}

// printRestoreDetails shows detailed information about the restore
func printRestoreDetails(restoreName string) error {
	fmt.Printf("\n📊 Restore Details:\n")
	fmt.Printf("===================\n")

	// Get restore details
	cmd := runKubectl("describe", "restore", restoreName, "-n", "velero")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get restore details: %v", err)
	}

	fmt.Printf("%s\n", string(output))
	return nil
}

// applyCSINFSControllerPatch applies the CSI NFS controller patch to extend snapshot timeout.
// Applied before both backup and restore to prevent DeadlineExceeded on large NFS volumes.
func applyCSINFSControllerPatch() error {
	fmt.Printf("   Patching CSI NFS controller snapshotter timeout to 4800s (default is 1200s)...\n")

	// Create the patch JSON — mirrors:
	// kubectl patch deployment csi-nfs-controller -n kube-system --type=strategic \
	//   -p='{"spec":{"template":{"spec":{"containers":[{"name":"csi-snapshotter",
	//         "args":["-v=5","-csi-address=$(ADDRESS)","--leader-election","--timeout=4800s"]}]}}}}'
	patchData := `{
		"spec": {
			"template": {
				"spec": {
					"containers": [
						{
							"name": "csi-snapshotter",
							"args": [
								"-v=5",
								"-csi-address=$(ADDRESS)",
								"--leader-election",
								"--timeout=4800s"
							]
						}
					]
				}
			}
		}
	}`

	// Apply the strategic merge patch
	cmd := runKubectl("patch", "deployment", "csi-nfs-controller",
		"-n", "kube-system",
		"--type=strategic",
		"-p", patchData)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if the error is because the deployment doesn't exist
		if strings.Contains(string(output), "not found") {
			fmt.Printf("   Warning: CSI NFS controller deployment not found in kube-system namespace\n")
			fmt.Printf("   This may be normal if using a different CSI driver or deployment name\n")
			fmt.Printf("   Continuing with restore process...\n")
			return nil
		}
		return fmt.Errorf("failed to patch CSI NFS controller: %v\nOutput: %s", err, string(output))
	}

	fmt.Printf("   CSI NFS controller patched successfully\n")
	fmt.Printf("   csi-snapshotter timeout set to 4800s (prevents DeadlineExceeded on large NFS volumes)\n")

	// Verify the patch was applied
	fmt.Printf("   Verifying patch was applied...\n")
	verifyCmd := runKubectl("get", "deployment", "csi-nfs-controller",
		"-n", "kube-system",
		"-o", "jsonpath={.spec.template.spec.containers[?(@.name=='csi-snapshotter')].args}")

	verifyOutput, verifyErr := verifyCmd.Output()
	if verifyErr != nil {
		fmt.Printf("   Warning: Could not verify patch application: %v\n", verifyErr)
	} else {
		args := string(verifyOutput)
		if strings.Contains(args, "timeout=4800s") {
			fmt.Printf("   ✅ Patch verified: CSI snapshotter timeout set to 4800s\n")
		} else {
			fmt.Printf("   ⚠️  Warning: Patch may not have applied correctly. Args: %s\n", args)
		}
	}

	return nil
}

// applyVeleroNodeAgentPriorityClass applies priority class and patches node-agent DaemonSet
func applyVeleroNodeAgentPriorityClass() error {
	fmt.Printf("   Applying Velero node-agent priority class...\n")

	// Apply the priority class from the YAML file
	priorityClassPath := "configs/restore/velero-priorityclass.yaml"
	if _, err := os.Stat(priorityClassPath); os.IsNotExist(err) {
		return fmt.Errorf("priority class file not found: %s", priorityClassPath)
	}

	cmd := runKubectl("apply", "-f", priorityClassPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to apply priority class: %v\nOutput: %s", err, string(output))
	}

	fmt.Printf("   ✅ Priority class 'velero-node-agent-priority' applied successfully\n")

	// Wait a moment for the priority class to be available
	time.Sleep(2 * time.Second)

	// Check if node-agent DaemonSet exists
	fmt.Printf("   Checking for Velero node-agent DaemonSet...\n")
	checkCmd := runKubectl("get", "daemonset", "node-agent", "-n", "velero")
	_, checkErr := checkCmd.Output()

	if checkErr != nil {
		fmt.Printf("   Warning: node-agent DaemonSet not found yet\n")
		fmt.Printf("   This is normal immediately after Velero installation\n")
		fmt.Printf("   Waiting 30 seconds for node-agent to be created...\n")
		time.Sleep(30 * time.Second)

		// Check again after waiting
		checkCmd = runKubectl("get", "daemonset", "node-agent", "-n", "velero")
		_, checkErr = checkCmd.Output()
		if checkErr != nil {
			return fmt.Errorf("node-agent DaemonSet still not found after waiting: %v", checkErr)
		}
	}

	fmt.Printf("   Found node-agent DaemonSet, applying priority class patch...\n")

	// Patch the node-agent DaemonSet to use the priority class
	patchCmd := runKubectl("patch", "daemonset", "node-agent", "-n", "velero",
		"--type=json",
		"-p=[{\"op\": \"add\", \"path\": \"/spec/template/spec/priorityClassName\", \"value\":\"velero-node-agent-priority\"}]")

	patchOutput, patchErr := patchCmd.CombinedOutput()
	if patchErr != nil {
		return fmt.Errorf("failed to patch node-agent DaemonSet: %v\nOutput: %s", patchErr, string(patchOutput))
	}

	fmt.Printf("   ✅ node-agent DaemonSet patched with priority class successfully\n")

	// Verify the patch was applied
	fmt.Printf("   Verifying priority class configuration...\n")
	verifyCmd := runKubectl("get", "daemonset", "node-agent", "-n", "velero",
		"-o", "jsonpath={.spec.template.spec.priorityClassName}")

	verifyOutput, verifyErr := verifyCmd.Output()
	if verifyErr != nil {
		fmt.Printf("   Warning: Could not verify priority class configuration: %v\n", verifyErr)
	} else {
		priorityClass := strings.TrimSpace(string(verifyOutput))
		if priorityClass == "velero-node-agent-priority" {
			fmt.Printf("   ✅ Verification successful: node-agent using priority class '%s'\n", priorityClass)
		} else {
			fmt.Printf("   ⚠️  Warning: Expected 'velero-node-agent-priority', got '%s'\n", priorityClass)
		}
	}

	// Show DaemonSet status
	fmt.Printf("   Current Velero DaemonSets status:\n")
	statusCmd := runKubectl("get", "daemonsets", "-n", "velero")
	statusOutput, statusErr := statusCmd.CombinedOutput()
	if statusErr != nil {
		fmt.Printf("   Warning: Could not get DaemonSet status: %v\n", statusErr)
	} else {
		fmt.Printf("%s\n", string(statusOutput))
	}

	return nil
}
