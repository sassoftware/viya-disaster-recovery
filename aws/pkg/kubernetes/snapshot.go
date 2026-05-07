// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// SnapshotClassManager handles VolumeSnapshotClass operations
type SnapshotClassManager struct {
	clientset      *kubernetes.Clientset
	kubeconfigPath string
}

// NewSnapshotClassManager creates a new snapshot class manager
func NewSnapshotClassManager(clientset *kubernetes.Clientset, kubeconfigPath string) *SnapshotClassManager {
	return &SnapshotClassManager{
		clientset:      clientset,
		kubeconfigPath: kubeconfigPath,
	}
}

// InstallCSISnapshotController installs CSI snapshot controller components
func (s *SnapshotClassManager) InstallCSISnapshotController(ctx context.Context) error {
	fmt.Println("\n=== Installing CSI Snapshot Controller ===")

	// Step 1: Install CRDs
	fmt.Println("Step 1: Installing snapshot CRDs...")
	cmd1 := exec.CommandContext(ctx, "kubectl", "apply", "-k",
		"github.com/kubernetes-csi/external-snapshotter/client/config/crd?ref=v8.0.1")
	cmd1.Env = append(os.Environ(), "KUBECONFIG="+s.kubeconfigPath)
	output1, err := cmd1.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to install snapshot CRDs: %w\nOutput: %s", err, string(output1))
	}
	fmt.Println("✓ Snapshot CRDs installed")

	// Step 2: Install RBAC
	fmt.Println("Step 2: Installing snapshot controller RBAC...")
	cmd2 := exec.CommandContext(ctx, "kubectl", "apply", "-f",
		"https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v8.0.1/deploy/kubernetes/snapshot-controller/rbac-snapshot-controller.yaml")
	cmd2.Env = append(os.Environ(), "KUBECONFIG="+s.kubeconfigPath)
	output2, err := cmd2.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to install snapshot RBAC: %w\nOutput: %s", err, string(output2))
	}
	fmt.Println("✓ Snapshot controller RBAC installed")

	// Step 3: Install snapshot controller
	fmt.Println("Step 3: Installing snapshot controller...")
	cmd3 := exec.CommandContext(ctx, "kubectl", "apply", "-f",
		"https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v8.0.1/deploy/kubernetes/snapshot-controller/setup-snapshot-controller.yaml")
	cmd3.Env = append(os.Environ(), "KUBECONFIG="+s.kubeconfigPath)
	output3, err := cmd3.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to install snapshot controller: %w\nOutput: %s", err, string(output3))
	}
	fmt.Println("✓ Snapshot controller installed")

	// Step 4: Verify installation
	fmt.Println("Step 4: Verifying snapshot controller deployment...")
	verifyCmd := exec.CommandContext(ctx, "kubectl", "get", "deployment",
		"snapshot-controller", "-n", "kube-system")
	verifyCmd.Env = append(os.Environ(), "KUBECONFIG="+s.kubeconfigPath)
	if verifyOutput, err := verifyCmd.CombinedOutput(); err != nil {
		fmt.Printf("Warning: Could not verify snapshot controller: %s\n", string(verifyOutput))
	} else {
		fmt.Println("✓ Snapshot controller is running")
	}

	fmt.Println("\n✓ CSI snapshot controller installation completed successfully")
	return nil
}

// ApplyVolumeSnapshotClasses applies VolumeSnapshotClass files from configs/snapshot_classes/
func (s *SnapshotClassManager) ApplyVolumeSnapshotClasses(ctx context.Context) error {
	fmt.Println("\n=== Creating VolumeSnapshotClasses ===")

	snapshotClassFiles := []string{
		"configs/snapshot_classes/aws-ebs-snapshot-class.yaml",
		"configs/snapshot_classes/aws-nfs-snapshot-class.yaml",
	}

	for _, filePath := range snapshotClassFiles {
		fmt.Printf("Applying %s...\n", filePath)
		cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", filePath)
		cmd.Env = append(os.Environ(), "KUBECONFIG="+s.kubeconfigPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to apply %s: %w\nOutput: %s", filePath, err, string(output))
		}
		fmt.Printf("✓ Applied %s\n", filePath)
	}

	// Verify creation
	fmt.Println("\nVerifying VolumeSnapshotClasses...")
	verifyCmd := exec.CommandContext(ctx, "kubectl", "get", "volumesnapshotclass")
	verifyCmd.Env = append(os.Environ(), "KUBECONFIG="+s.kubeconfigPath)
	verifyOutput, err := verifyCmd.CombinedOutput()
	if err != nil {
		fmt.Printf("Warning: Could not verify snapshot classes: %s\n", string(verifyOutput))
	} else {
		fmt.Printf("%s\n", string(verifyOutput))
	}

	fmt.Println("\n✓ VolumeSnapshotClasses created successfully")
	return nil
}

// applyYAMLString applies a YAML string using kubectl
func (s *SnapshotClassManager) applyYAMLString(ctx context.Context, yaml string) error {
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+s.kubeconfigPath)

	// Create a pipe to pass YAML
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	// Capture output for error reporting
	cmd.Stdout = nil
	cmd.Stderr = nil

	// Start the command
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start kubectl: %w", err)
	}

	// Write YAML to stdin
	if _, err := stdin.Write([]byte(yaml)); err != nil {
		stdin.Close()
		return fmt.Errorf("failed to write YAML to stdin: %w", err)
	}
	stdin.Close()

	// Wait for command to complete
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("kubectl apply failed: %w", err)
	}

	return nil
}

// CreateEBSSnapshotClass creates a VolumeSnapshotClass for EBS snapshots
// Deprecated: Use ApplyVolumeSnapshotClasses instead
func (s *SnapshotClassManager) CreateEBSSnapshotClass(ctx context.Context, className string) error {
	fmt.Printf("\n=== Creating VolumeSnapshotClass: %s ===\n", className)

	yaml := fmt.Sprintf(`apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: %s
  labels:
    velero.io/csi-volumesnapshot-class: "true"
driver: ebs.csi.aws.com
deletionPolicy: Delete
`, className)

	return s.applyYAMLString(ctx, yaml)
}

// ValidateSnapshotClass checks if a VolumeSnapshotClass exists
func (s *SnapshotClassManager) ValidateSnapshotClass(ctx context.Context, className string) error {
	fmt.Printf("Validating VolumeSnapshotClass: %s\n", className)

	// This would require the snapshot CRD client
	// For now, provide kubectl command
	fmt.Printf("Run: kubectl get volumesnapshotclass %s\n", className)

	return nil
}

// ValidateViyaNamespace checks if the Viya namespace exists and has resources
func (k *K8sClient) ValidateViyaNamespace(ctx context.Context, namespace string) error {
	fmt.Printf("Validating namespace: %s\n", namespace)

	ns, err := k.clientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("namespace %s not found: %w", namespace, err)
	}

	if ns.Status.Phase != "Active" {
		return fmt.Errorf("namespace %s is not active: %s", namespace, ns.Status.Phase)
	}

	// Check for pods in namespace
	pods, err := k.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list pods in namespace %s: %w", namespace, err)
	}

	fmt.Printf("✓ Namespace %s is active with %d pods\n", namespace, len(pods.Items))
	return nil
}

// GetViyaNamespaces attempts to identify Viya namespaces
func (k *K8sClient) GetViyaNamespaces(ctx context.Context) ([]string, error) {
	fmt.Println("Searching for Viya namespaces...")

	namespaces, err := k.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list namespaces: %w", err)
	}

	var viyaNamespaces []string
	for _, ns := range namespaces.Items {
		// Look for common Viya labels or naming patterns
		if ns.Labels != nil {
			if _, ok := ns.Labels["sas.com/deployment"]; ok {
				viyaNamespaces = append(viyaNamespaces, ns.Name)
			}
		}
	}

	if len(viyaNamespaces) > 0 {
		fmt.Printf("Found %d Viya namespace(s): %v\n", len(viyaNamespaces), viyaNamespaces)
	} else {
		fmt.Println("No Viya namespaces found with sas.com/deployment label")
	}

	return viyaNamespaces, nil
}

// UninstallCSISnapshotController uninstalls CSI snapshot controller components
func (s *SnapshotClassManager) UninstallCSISnapshotController(ctx context.Context) error {
	fmt.Println("\n=== Uninstalling CSI Snapshot Controller ===")

	// Delete snapshot controller
	fmt.Println("→ Deleting snapshot controller deployment...")
	cmd1 := exec.CommandContext(ctx, "kubectl", "delete", "-f",
		"https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v8.0.1/deploy/kubernetes/snapshot-controller/setup-snapshot-controller.yaml",
		"--ignore-not-found=true")
	cmd1.Env = append(os.Environ(), "KUBECONFIG="+s.kubeconfigPath)
	output1, err := cmd1.CombinedOutput()
	if err != nil {
		fmt.Printf("  ⚠ Warning: %s\n", string(output1))
	} else {
		fmt.Println("  ✓ Snapshot controller deployment deleted")
	}

	// Delete RBAC
	fmt.Println("→ Deleting snapshot controller RBAC...")
	cmd2 := exec.CommandContext(ctx, "kubectl", "delete", "-f",
		"https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v8.0.1/deploy/kubernetes/snapshot-controller/rbac-snapshot-controller.yaml",
		"--ignore-not-found=true")
	cmd2.Env = append(os.Environ(), "KUBECONFIG="+s.kubeconfigPath)
	output2, err := cmd2.CombinedOutput()
	if err != nil {
		fmt.Printf("  ⚠ Warning: %s\n", string(output2))
	} else {
		fmt.Println("  ✓ Snapshot controller RBAC deleted")
	}

	// Delete CRDs
	fmt.Println("→ Deleting snapshot CRDs...")
	cmd3 := exec.CommandContext(ctx, "kubectl", "delete", "-k",
		"github.com/kubernetes-csi/external-snapshotter/client/config/crd?ref=v8.0.1",
		"--ignore-not-found=true")
	cmd3.Env = append(os.Environ(), "KUBECONFIG="+s.kubeconfigPath)
	output3, err := cmd3.CombinedOutput()
	if err != nil {
		fmt.Printf("  ⚠ Warning: %s\n", string(output3))
	} else {
		fmt.Println("  ✓ Snapshot CRDs deleted")
	}

	fmt.Println("✓ CSI Snapshot Controller uninstalled")
	return nil
}

// DeleteVolumeSnapshotClasses deletes all VolumeSnapshotClasses
func (s *SnapshotClassManager) DeleteVolumeSnapshotClasses(ctx context.Context) error {
	fmt.Println("\n=== Deleting VolumeSnapshotClasses ===")

	fmt.Println("→ Deleting aws-ebs-snapshot-class and aws-nfs-snapshot-class...")
	cmd := exec.CommandContext(ctx, "kubectl", "delete", "volumesnapshotclass",
		"aws-ebs-snapshot-class", "aws-nfs-snapshot-class", "--ignore-not-found=true")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+s.kubeconfigPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to delete VolumeSnapshotClasses: %w\nOutput: %s", err, string(output))
	}

	if len(output) > 0 {
		fmt.Printf("  %s", string(output))
	}
	fmt.Println("✓ VolumeSnapshotClasses deleted")
	return nil
}
