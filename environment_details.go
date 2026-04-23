// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Environment struct {
	SubscriptionID             string
	VeleroStoreName            string
	VeleroBlobRG               string
	VeleroSnapRG               string
	Location                   string
	KubeconfigPath             string
	ViyaDeploymentType         string // "nmt" for Non-Multi-Tenant, "mt" for Multi-Tenant
	ClusterType                string // "source" for source cluster, "restore" for restore/target cluster
	VeleroServicePrincipalName string // Name for the Velero service principal
	// MC_ resource groups for AKS clusters (format: MC_{rg}_{cluster-name}_{region})
	SourceMCResourceGroup string
	// Set this manually if Azure CLI is not available
	ManualSubscriptionID string
	// Storage class used for the NFS permission backup PVC
	NFSStorageClass string
}

func GetEnvironment() Environment {
	// Load configuration from properties file
	props := loadEnvironmentProperties()

	// Get subscription ID (either from Azure CLI or manual configuration)
	subscriptionID := getSubscriptionIDWithValidation(props["MANUAL_SUBSCRIPTION_ID"])

	return Environment{
		SubscriptionID:             subscriptionID,
		VeleroStoreName:            getPropertyWithDefault(props, "VELERO_STORE_NAME", "psvelerostore"),
		VeleroBlobRG:               getPropertyWithDefault(props, "VELERO_BLOB_RG", "psvelerostorerg"),
		VeleroSnapRG:               getPropertyWithDefault(props, "VELERO_SNAP_RG", "psvelerosnapsrg"),
		Location:                   getPropertyWithDefault(props, "LOCATION", "eastus"),
		KubeconfigPath:             getPropertyWithDefault(props, "KUBECONFIG_PATH", "/u/qstauto/.kube/auto/pscloudrestore-aks-cxwaknyb.hcp.eastus.azmk8s.io/kubeconfig"),
		ViyaDeploymentType:         getPropertyWithDefault(props, "VIYA_DEPLOYMENT_TYPE", "nmt"),
		ClusterType:                getPropertyWithDefault(props, "CLUSTER_TYPE", "source"),
		VeleroServicePrincipalName: getPropertyWithDefault(props, "VELERO_SERVICE_PRINCIPAL_NAME", "velero-sp"),
		SourceMCResourceGroup:      getPropertyWithDefault(props, "SOURCE_MC_RESOURCE_GROUP", "MC_pscloudrestore-rg_pscloudrestore-aks_eastus"),
		ManualSubscriptionID:       getPropertyWithDefault(props, "MANUAL_SUBSCRIPTION_ID", ""),
		NFSStorageClass:            getPropertyWithDefault(props, "NFS_STORAGE_CLASS", "sas"),
	}
}

// loadEnvironmentProperties reads configuration from environment.properties file
func loadEnvironmentProperties() map[string]string {
	props := make(map[string]string)

	// Try to find the properties file
	propertiesFile := "environment.properties"
	if _, err := os.Stat(propertiesFile); os.IsNotExist(err) {
		// Try in the same directory as the executable
		execDir := "."
		if len(os.Args) > 0 {
			if absExecDir, err := filepath.Abs(filepath.Dir(os.Args[0])); err == nil {
				execDir = absExecDir
			} else {
				fmt.Printf("Warning: Could not determine executable directory: %v\n", err)
			}
		}

		propertiesFile = filepath.Join(execDir, "environment.properties")
		if _, err := os.Stat(propertiesFile); os.IsNotExist(err) {
			fmt.Printf("Warning: environment.properties file not found in current directory or executable directory.\n")
			fmt.Printf("Using default values. Create environment.properties file for custom configuration.\n\n")
			return props
		}
	}

	file, err := os.Open(propertiesFile)
	if err != nil {
		fmt.Printf("Warning: Could not open environment.properties: %v\n", err)
		fmt.Printf("Using default values.\n\n")
		return props
	}
	defer file.Close()

	fmt.Printf("Loading configuration from: %s\n", propertiesFile)

	scanner := bufio.NewScanner(file)
	lineNumber := 0
	loadedCount := 0

	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse key=value pairs
		if strings.Contains(line, "=") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				key := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])

				// Strip inline comments (e.g. KEY=value # comment -> value)
				if commentIdx := strings.Index(value, " #"); commentIdx != -1 {
					value = strings.TrimSpace(value[:commentIdx])
				}

				// Validate key format (only allow alphanumeric and underscore)
				if key != "" && isValidPropertyKey(key) {
					if value != "" {
						props[key] = value
						fmt.Printf("   Loaded: %s = %s\n", key, value)
						loadedCount++
					} else {
						fmt.Printf("   Skipped empty value for key: %s\n", key)
					}
				} else {
					fmt.Printf("Warning: Invalid property key format at line %d: %s\n", lineNumber, key)
				}
			} else {
				fmt.Printf("Warning: Could not parse property at line %d: %s\n", lineNumber, line)
			}
		} else {
			fmt.Printf("Warning: Invalid property format at line %d (missing '='): %s\n", lineNumber, line)
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Printf("Warning: Error reading properties file: %v\n", err)
	}

	fmt.Printf("Successfully loaded %d properties from configuration file\n\n", loadedCount)
	return props
}

// isValidPropertyKey validates that property key contains only valid characters
func isValidPropertyKey(key string) bool {
	if key == "" {
		return false
	}

	for _, char := range key {
		if !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '.') {
			return false
		}
	}
	return true
}

// getPropertyWithDefault returns property value or default if not found/empty
func getPropertyWithDefault(props map[string]string, key, defaultValue string) string {
	if value, exists := props[key]; exists && value != "" {
		return value
	}
	return defaultValue
}

// getSubscriptionIDWithValidation checks Azure CLI and gets subscription ID with fallback options
func getSubscriptionIDWithValidation(manualSubscriptionID string) string {
	// First check if manual subscription ID is provided
	if manualSubscriptionID != "" {
		fmt.Printf("   Using manual subscription ID from configuration: %s\n", manualSubscriptionID)
		return manualSubscriptionID
	}

	// Check if Azure CLI is installed
	if !isAzureCLIInstalled() {
		fmt.Println("  Azure CLI (az) is not installed or not in PATH")
		fmt.Println("")
		fmt.Println("Options:")
		fmt.Println("1. Install Azure CLI: https://docs.microsoft.com/en-us/cli/azure/install-azure-cli")
		fmt.Println("2. Set MANUAL_SUBSCRIPTION_ID in environment.properties file")
		fmt.Println("3. Enter your subscription ID manually below")
		fmt.Println("")
		return promptForSubscriptionID()
	}

	// Try to get subscription ID from Azure CLI
	subscriptionID := getSubscriptionIDFromAzureCLI()
	if subscriptionID == "" {
		fmt.Println("  Failed to get subscription ID from Azure CLI")
		fmt.Println("Please run 'az login' first, or set MANUAL_SUBSCRIPTION_ID in environment.properties file:")
		return promptForSubscriptionID()
	}

	fmt.Printf("   Using subscription from Azure CLI: %s\n", subscriptionID)
	return subscriptionID
}

// isAzureCLIInstalled checks if Azure CLI is available
func isAzureCLIInstalled() bool {
	cmd := exec.Command("az", "--version")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// getSubscriptionIDFromAzureCLI retrieves subscription ID from Azure CLI
func getSubscriptionIDFromAzureCLI() string {
	cmd := exec.Command("az", "account", "show", "--query", "id", "-o", "tsv")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// promptForSubscriptionID asks user to enter subscription ID manually
func promptForSubscriptionID() string {
	fmt.Print("Enter your Azure Subscription ID: ")
	reader := bufio.NewReader(os.Stdin)
	subscriptionID, _ := reader.ReadString('\n')
	subscriptionID = strings.TrimSpace(subscriptionID)

	if subscriptionID == "" {
		fmt.Println("  Subscription ID cannot be empty")
		fmt.Println("  Please set MANUAL_SUBSCRIPTION_ID in environment.properties file or run 'az login'")
		os.Exit(1)
	}

	return subscriptionID
}

func (e Environment) Print() {
	fmt.Println("Environment Configuration:")
	fmt.Println("=============================")
	fmt.Printf("Subscription ID: %s\n", e.SubscriptionID)
	fmt.Printf("Location: %s\n", e.Location)
	fmt.Printf("Cluster Type: %s\n", e.ClusterType)
	fmt.Printf("Viya Deployment Type: %s\n", e.ViyaDeploymentType)
	fmt.Printf("Velero Storage Name: %s\n", e.VeleroStoreName)
	fmt.Printf("Velero Blob RG: %s\n", e.VeleroBlobRG)
	fmt.Printf("Velero Snap RG: %s\n", e.VeleroSnapRG)
	fmt.Printf("Kubeconfig Path: %s\n", e.KubeconfigPath)
	fmt.Printf("Source MC Resource Group: %s\n", e.SourceMCResourceGroup)
	fmt.Printf("Velero Service Principal Name: %s\n", e.VeleroServicePrincipalName)
	if e.ManualSubscriptionID != "" {
		fmt.Printf("Manual Subscription ID: %s (configured)\n", e.ManualSubscriptionID)
	}
	fmt.Println("=============================")
}

// ValidateViyaDeploymentType checks if the current deployment type supports the requested operation
func (e Environment) ValidateViyaDeploymentType(operation string) error {
	if e.ViyaDeploymentType == "mt" {
		return fmt.Errorf("  Operation '%s' is not supported for Multi-Tenant (MT) Viya deployments. Only Non-Multi-Tenant (NMT) deployments support Velero backup operations", operation)
	}

	if e.ViyaDeploymentType != "nmt" && e.ViyaDeploymentType != "mt" {
		return fmt.Errorf("  Invalid ViyaDeploymentType: '%s'. Must be 'nmt' (Non-Multi-Tenant) or 'mt' (Multi-Tenant)", e.ViyaDeploymentType)
	}

	fmt.Printf("   Deployment type '%s' supports %s operations\n", e.ViyaDeploymentType, operation)
	return nil
}

// ValidateEnvironment performs comprehensive validation of environment configuration
func (e Environment) ValidateEnvironment() error {
	var errors []string

	// Validate required fields are not empty
	if e.SubscriptionID == "" {
		errors = append(errors, "SubscriptionID cannot be empty")
	}

	if e.VeleroStoreName == "" {
		errors = append(errors, "VeleroStoreName cannot be empty")
	}

	if e.VeleroBlobRG == "" {
		errors = append(errors, "VeleroBlobRG cannot be empty")
	}

	if e.VeleroSnapRG == "" {
		errors = append(errors, "VeleroSnapRG cannot be empty")
	}

	if e.Location == "" {
		errors = append(errors, "Location cannot be empty")
	}

	// Validate ViyaDeploymentType
	if e.ViyaDeploymentType != "nmt" && e.ViyaDeploymentType != "mt" {
		errors = append(errors, fmt.Sprintf("Invalid ViyaDeploymentType: '%s'. Must be 'nmt' or 'mt'", e.ViyaDeploymentType))
	}

	// Validate ClusterType
	if e.ClusterType != "source" && e.ClusterType != "restore" {
		errors = append(errors, fmt.Sprintf("Invalid ClusterType: '%s'. Must be 'source' or 'restore'", e.ClusterType))
	}

	// Validate MC resource groups format
	if e.SourceMCResourceGroup != "" && !strings.HasPrefix(e.SourceMCResourceGroup, "MC_") {
		errors = append(errors, fmt.Sprintf("Invalid SourceMCResourceGroup format: '%s'. Should start with 'MC_'", e.SourceMCResourceGroup))
	}

	// Validate kubeconfig path exists (if not default path)
	if e.KubeconfigPath != "" && e.KubeconfigPath != "/u/qstauto/.kube/auto/pscloudrestore-aks-cxwaknyb.hcp.eastus.azmk8s.io/kubeconfig" {
		// Handle tilde expansion
		kubeconfigPath := e.KubeconfigPath
		if strings.HasPrefix(kubeconfigPath, "~/") {
			if homeDir, err := os.UserHomeDir(); err == nil {
				kubeconfigPath = filepath.Join(homeDir, kubeconfigPath[2:])
			}
		}

		if _, err := os.Stat(kubeconfigPath); os.IsNotExist(err) {
			errors = append(errors, fmt.Sprintf("Kubeconfig file not found: %s", kubeconfigPath))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("Environment validation failed:\n  - %s", strings.Join(errors, "\n  - "))
	}

	return nil
}
