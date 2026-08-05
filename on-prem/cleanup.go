package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const cleanupNamespaceDeleteTimeout = 180 * time.Second

func cleanupDestroyResources(cfg *Config, r Runner) error {
	fmt.Println("Starting HPOS cleanup...")

	veleroUninstallStatus := "Success"
	namespaceDeleteStatus := "Success"

	if err := os.Setenv("KUBECONFIG", cfg.KubeconfigPath); err != nil {
		return fmt.Errorf("failed to set KUBECONFIG for cleanup: %w", err)
	}

	if _, err := r.Output("kubectl", "cluster-info"); err != nil {
		return fmt.Errorf("unable to access cluster during cleanup: %w", err)
	}

	if _, err := r.Output("velero", "uninstall", "--namespace", cfg.VeleroNamespace, "--force"); err != nil {
		if isNotFoundError(err.Error()) {
			veleroUninstallStatus = "Skipped"
		} else {
			veleroUninstallStatus = "Failed"
			fmt.Printf("Warning: Velero uninstall failed: %v\n", err)
		}
	}

	if _, err := r.Output("kubectl", "delete", "namespace", cfg.VeleroNamespace, "--ignore-not-found"); err != nil {
		namespaceDeleteStatus = "Failed"
		return fmt.Errorf("failed to delete namespace %s: %w", cfg.VeleroNamespace, err)
	}

	fmt.Printf("\nWaiting for namespace %q to be deleted...\n", cfg.VeleroNamespace)
	if err := waitForNamespaceDeletion(cfg, r, cleanupNamespaceDeleteTimeout); err != nil {
		namespaceDeleteStatus = "Failed"
		return fmt.Errorf("failed waiting for namespace %s deletion: %w", cfg.VeleroNamespace, err)
	}
	fmt.Println("Velero namespace deleted successfully.")

	fmt.Println("\n=================================")
	fmt.Println("HPOS Cleanup Complete")
	fmt.Println("=================================")
	fmt.Printf("Velero uninstalled: %s\n", veleroUninstallStatus)
	fmt.Printf("Namespace deleted: %s\n", namespaceDeleteStatus)
	fmt.Println("=================================")

	return nil
}

func isNotFoundError(message string) bool {
	msg := strings.ToLower(message)
	patterns := []string{
		"not found",
		"no resources found",
		"the server doesn't have a resource type",
		"the server could not find the requested resource",
		"no matches for kind",
		"nosuchbucket",
		"404",
		"status code: 404",
		"cannot find",
	}
	for _, p := range patterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

func waitForNamespaceDeletion(cfg *Config, r Runner, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		_, err := runCommandCapture(r, "kubectl", "get", "namespace", cfg.VeleroNamespace)
		if err != nil {
			if isNotFoundError(err.Error()) {
				return nil
			}
			return err
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s", timeout)
		}
		time.Sleep(5 * time.Second)
	}
}
