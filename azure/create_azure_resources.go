// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// getConfiguredKubeconfigPath gets the globally configured kubeconfig path
// This function accesses the same variable used in check_kubernetes_connectivity.go
func getConfiguredKubeconfigPath() string {
	// This will be set by the CheckKubeconfig function in check_kubernetes_connectivity.go
	// We need to access it from the other package, so we'll use environment variable as bridge
	return os.Getenv("CONFIGURED_KUBECONFIG_PATH")
}

// runKubectlCleanup executes kubectl with the configured kubeconfig file for cleanup operations
func runKubectlCleanup(args ...string) *exec.Cmd {
	cmd := exec.Command("kubectl", args...)
	// Use the global kubeconfig path if it was set during validation
	if configuredKubeconfigPath != "" {
		cmd.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", configuredKubeconfigPath))
	}
	return cmd
}

func RunCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

func CreateAzureResources(env Environment, state *State) error {
	env.Print()
	fmt.Println("\nStarting Azure resource creation...")
	fmt.Println("===========================================")

	step := "Create Azure Resource Groups"
	UpdateStepState(state, step, "IN-PROGRESS")
	fmt.Printf("Creating resource group: %s\n", env.VeleroBlobRG)
	if err := RunCommand("az", "group", "create", "--name", env.VeleroBlobRG, "--location", env.Location); err != nil {
		UpdateStepState(state, step, "FAILED")
		return fmt.Errorf("failed to create blob RG: %v", err)
	}
	fmt.Printf("Created resource group: %s\n", env.VeleroBlobRG)

	fmt.Printf("Creating resource group: %s\n", env.VeleroSnapRG)
	if err := RunCommand("az", "group", "create", "--name", env.VeleroSnapRG, "--location", env.Location); err != nil {
		UpdateStepState(state, step, "FAILED")
		return fmt.Errorf("failed to create snapshot RG: %v", err)
	}
	fmt.Printf("Created resource group: %s\n", env.VeleroSnapRG)
	UpdateStepState(state, step, "SUCCESS")

	step = "Create Storage Account"
	UpdateStepState(state, step, "IN-PROGRESS")
	fmt.Printf("Creating storage account: %s (Standard_GRS)\n", env.VeleroStoreName)
	if err := RunCommand("az", "storage", "account", "create",
		"--name", env.VeleroStoreName,
		"--resource-group", env.VeleroBlobRG,
		"--sku", "Standard_GRS",
		"--encryption-services", "blob"); err != nil {
		UpdateStepState(state, step, "FAILED")
		return fmt.Errorf("failed to create storage account: %v", err)
	}
	fmt.Printf("Created storage account: %s in %s\n", env.VeleroStoreName, env.VeleroBlobRG)
	UpdateStepState(state, step, "SUCCESS")

	step = "Create Storage Container"
	UpdateStepState(state, step, "IN-PROGRESS")
	fmt.Printf("Creating storage container: %s\n", env.VeleroStoreName)
	if err := RunCommand("az", "storage", "container", "create",
		"--name", env.VeleroStoreName,
		"--account-name", env.VeleroStoreName); err != nil {
		UpdateStepState(state, step, "FAILED")
		return fmt.Errorf("failed to create container: %v", err)
	}
	fmt.Printf("Created storage container: %s in storage account %s\n", env.VeleroStoreName, env.VeleroStoreName)
	UpdateStepState(state, step, "SUCCESS")

	step = "Create Service Principal"
	UpdateStepState(state, step, "IN-PROGRESS")
	fmt.Printf("Creating service principal: %s\n", env.VeleroServicePrincipalName)
	if err := createServicePrincipalWithCredentials(env); err != nil {
		UpdateStepState(state, step, "FAILED")
		return fmt.Errorf("failed to create SP: %v", err)
	}
	fmt.Printf("Created service principal: %s\n", env.VeleroServicePrincipalName)
	UpdateStepState(state, step, "SUCCESS")

	step = "Assign Role Permissions"
	UpdateStepState(state, step, "IN-PROGRESS")
	fmt.Println("Assigning RBAC roles to service principal...")
	if err := assignServicePrincipalRoles(env); err != nil {
		UpdateStepState(state, step, "FAILED")
		return fmt.Errorf("failed to assign roles: %v", err)
	}
	UpdateStepState(state, step, "SUCCESS")

	step = "Create Velero Credentials File"
	UpdateStepState(state, step, "IN-PROGRESS")
	fmt.Println("Creating Velero credential files...")
	if err := createVeleroCredentialsFile(env); err != nil {
		UpdateStepState(state, step, "FAILED")
		return fmt.Errorf("failed to create Velero credentials: %v", err)
	}
	UpdateStepState(state, step, "SUCCESS")

	fmt.Println("\nAzure resource creation completed successfully!")
	fmt.Println("===========================================")
	return nil
}

// createServicePrincipalWithCredentials creates SP and saves credentials to file
func createServicePrincipalWithCredentials(env Environment) error {
	// Validate user permissions before attempting to create service principal
	if err := validateAzurePermissions(env); err != nil {
		return fmt.Errorf("permission validation failed: %v", err)
	}

	// Check if service principal already exists
	if exists, existingSPID, err := checkServicePrincipalExists(env.VeleroServicePrincipalName); err != nil {
		return fmt.Errorf("failed to check existing service principal: %v", err)
	} else if exists {
		fmt.Printf("Service principal '%s' already exists (ID: %s)\n", env.VeleroServicePrincipalName, existingSPID)
		fmt.Printf("Deleting existing service principal to recreate with correct permissions...\n")
		if err := deleteExistingServicePrincipal(env.VeleroServicePrincipalName); err != nil {
			return fmt.Errorf("failed to delete existing service principal: %v", err)
		}
	}

	fmt.Printf("Creating service principal '%s' with Contributor role on subscription: %s\n", env.VeleroServicePrincipalName, env.SubscriptionID)

	cmd := exec.Command("az", "ad", "sp", "create-for-rbac",
		"--name", env.VeleroServicePrincipalName,
		"--role", "Contributor",
		"--scopes", "/subscriptions/"+env.SubscriptionID,
		"--sdk-auth")
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("Service principal creation failed with error: %v\n", err)
		fmt.Printf("Raw output: %s\n", string(output))
		return fmt.Errorf("failed to create service principal: %v\nOutput: %s", err, string(output))
	}

	fmt.Printf("Raw Azure CLI output:\n%s\n", string(output))

	// Filter out warning messages and extract JSON
	jsonOutput := extractJSONFromAzureOutput(string(output))
	if jsonOutput == "" {
		return fmt.Errorf("no valid JSON found in Azure CLI output")
	}

	fmt.Printf("Extracted JSON:\n%s\n", jsonOutput)

	// Validate that we got valid JSON before saving
	var testCredentials map[string]interface{}
	if err := json.Unmarshal([]byte(jsonOutput), &testCredentials); err != nil {
		fmt.Printf("Invalid JSON after extraction: %s\n", jsonOutput)
		return fmt.Errorf("received invalid JSON from service principal creation: %v", err)
	}

	// Check that required fields are present
	requiredFields := []string{"clientId", "clientSecret", "tenantId", "subscriptionId"}
	for _, field := range requiredFields {
		if _, exists := testCredentials[field]; !exists {
			return fmt.Errorf("missing required field '%s' in service principal credentials", field)
		}
	}

	// Save credentials to file (using the clean JSON)
	credentialsPath := "credentials/credentials-velero.json"
	os.MkdirAll("credentials", 0755)
	if err := os.WriteFile(credentialsPath, []byte(jsonOutput), 0600); err != nil {
		return fmt.Errorf("failed to save credentials: %v", err)
	}

	fmt.Printf("Successfully created service principal: %s\n", env.VeleroServicePrincipalName)
	fmt.Printf("Saved service principal credentials to: %s\n", credentialsPath)

	// Verify the saved file can be read and parsed
	savedData, err := os.ReadFile(credentialsPath)
	if err != nil {
		return fmt.Errorf("failed to verify saved credentials file: %v", err)
	}

	var verifyCredentials map[string]interface{}
	if err := json.Unmarshal(savedData, &verifyCredentials); err != nil {
		return fmt.Errorf("saved credentials file contains invalid JSON: %v", err)
	}

	clientId, _ := verifyCredentials["clientId"].(string)
	fmt.Printf("Verified service principal credentials - Client ID: %s\n", clientId)

	return nil
}

// extractJSONFromAzureOutput filters out warning messages and extracts valid JSON
func extractJSONFromAzureOutput(output string) string {
	lines := strings.Split(output, "\n")
	var jsonLines []string
	inJSON := false

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)

		// Skip warning lines
		if strings.HasPrefix(trimmedLine, "WARNING:") {
			continue
		}

		// Skip empty lines before JSON starts
		if !inJSON && trimmedLine == "" {
			continue
		}

		// Start of JSON object
		if !inJSON && strings.HasPrefix(trimmedLine, "{") {
			inJSON = true
			jsonLines = append(jsonLines, line)
			continue
		}

		// Inside JSON object
		if inJSON {
			jsonLines = append(jsonLines, line)
			// End of JSON object
			if strings.HasPrefix(trimmedLine, "}") {
				break
			}
		}
	}

	if len(jsonLines) == 0 {
		return ""
	}

	return strings.Join(jsonLines, "\n")
}

// validateAzurePermissions checks if the current user has sufficient permissions
func validateAzurePermissions(env Environment) error {
	fmt.Printf("Validating Azure permissions for subscription: %s\n", env.SubscriptionID)

	// Check if user is logged in
	cmd := exec.Command("az", "account", "show")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("not logged into Azure CLI. Please run 'az login' first")
	}

	// Parse the account info to get current user
	var accountInfo map[string]interface{}
	if err := json.Unmarshal(output, &accountInfo); err != nil {
		return fmt.Errorf("failed to parse account info: %v", err)
	}

	currentUser, ok := accountInfo["user"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("could not determine current user from Azure CLI")
	}

	userName, _ := currentUser["name"].(string)
	userType, _ := currentUser["type"].(string)

	fmt.Printf("Current Azure user: %s (type: %s)\n", userName, userType)

	// Check if user has Owner or User Access Administrator role at subscription level
	fmt.Printf("Checking role assignments for subscription...\n")
	cmd = exec.Command("az", "role", "assignment", "list",
		"--assignee", userName,
		"--scope", "/subscriptions/"+env.SubscriptionID,
		"--query", "[].{role:roleDefinitionName,scope:scope}",
		"-o", "json")

	roleOutput, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("Warning: Could not check role assignments directly. Attempting service principal creation...\n")
		fmt.Printf("If creation fails, ensure you have Owner or User Access Administrator role.\n")
		return nil // Don't fail here, let the actual SP creation attempt provide the error
	}

	var roleAssignments []map[string]interface{}
	if err := json.Unmarshal(roleOutput, &roleAssignments); err != nil {
		fmt.Printf("Warning: Could not parse role assignments. Proceeding with service principal creation...\n")
		return nil
	}

	hasRequiredRole := false
	requiredRoles := []string{"Owner", "User Access Administrator"}

	for _, assignment := range roleAssignments {
		roleName, _ := assignment["role"].(string)
		for _, requiredRole := range requiredRoles {
			if roleName == requiredRole {
				fmt.Printf("✓ Found required role: %s\n", roleName)
				hasRequiredRole = true
				break
			}
		}
		if hasRequiredRole {
			break
		}
	}

	if !hasRequiredRole {
		fmt.Printf("Warning: Could not verify Owner or User Access Administrator role.\n")
		fmt.Printf("Required permissions for service principal creation:\n")
		fmt.Printf("  - Owner role on subscription, OR\n")
		fmt.Printf("  - User Access Administrator + Application Administrator roles\n")
		fmt.Printf("Proceeding with creation attempt...\n")
	}

	return nil
}

// checkServicePrincipalExists checks if a service principal with the given name already exists
func checkServicePrincipalExists(spName string) (bool, string, error) {
	cmd := exec.Command("az", "ad", "sp", "list",
		"--display-name", spName,
		"--query", "[0].{appId:appId,displayName:displayName}",
		"-o", "json")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, "", fmt.Errorf("failed to check existing service principal: %v", err)
	}

	var spInfo map[string]interface{}
	if err := json.Unmarshal(output, &spInfo); err != nil {
		// If unmarshal fails, likely means no SP found (empty result)
		return false, "", nil
	}

	if spInfo["appId"] != nil {
		appId, _ := spInfo["appId"].(string)
		return true, appId, nil
	}

	return false, "", nil
}

// deleteExistingServicePrincipal removes an existing service principal
func deleteExistingServicePrincipal(spName string) error {
	cmd := exec.Command("az", "ad", "sp", "list",
		"--display-name", spName,
		"--query", "[0].appId",
		"-o", "tsv")

	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get service principal ID: %v", err)
	}

	appID := strings.TrimSpace(string(output))
	if appID == "" {
		return fmt.Errorf("service principal not found: %s", spName)
	}

	cmd = exec.Command("az", "ad", "sp", "delete", "--id", appID)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to delete service principal: %v", err)
	}

	fmt.Printf("Successfully deleted existing service principal: %s\n", spName)
	return nil
}

// deleteServicePrincipal removes the service principal
func deleteServicePrincipal(env Environment) error {
	fmt.Printf("Deleting service principal: %s...\n", env.VeleroServicePrincipalName)

	// Method 1: Try to find service principal by display name
	fmt.Printf("Searching for service principal by display name...\n")
	searchCmd := exec.Command("az", "ad", "sp", "list",
		"--display-name", env.VeleroServicePrincipalName,
		"--query", "[].{appId:appId,id:id,displayName:displayName}",
		"-o", "json")
	searchOutput, searchErr := searchCmd.CombinedOutput()

	if searchErr != nil {
		fmt.Printf("Failed to search by display name: %v\n", searchErr)
		fmt.Printf("Search output: %s\n", string(searchOutput))
	} else {
		fmt.Printf("Search by display name result: %s\n", string(searchOutput))

		var spList []map[string]interface{}
		if json.Unmarshal(searchOutput, &spList) == nil && len(spList) > 0 {
			// Found service principal(s) by display name
			for _, spInfo := range spList {
				appId, _ := spInfo["appId"].(string)
				objectId, _ := spInfo["id"].(string)
				displayName, _ := spInfo["displayName"].(string)

				fmt.Printf("Found service principal by display name:\n")
				fmt.Printf("  Display Name: %s\n", displayName)
				fmt.Printf("  App ID: %s\n", appId)
				fmt.Printf("  Object ID: %s\n", objectId)

				// Try to delete using App ID
				if err := deleteServicePrincipalByAppId(appId); err != nil {
					fmt.Printf("Failed to delete by App ID, trying Object ID: %v\n", err)
					if err := deleteServicePrincipalByObjectId(objectId); err != nil {
						fmt.Printf("Failed to delete by Object ID: %v\n", err)
						continue
					}
				}

				// Verify deletion
				if verifyServicePrincipalDeleted(appId) {
					fmt.Printf("✓ Service principal '%s' successfully deleted from Azure AD\n", env.VeleroServicePrincipalName)
					return nil
				}
			}
		}
	}

	// Method 2: Try to find by app registration name (sometimes different from SP display name)
	fmt.Printf("Searching for app registration by name...\n")
	appCmd := exec.Command("az", "ad", "app", "list",
		"--display-name", env.VeleroServicePrincipalName,
		"--query", "[].{appId:appId,displayName:displayName}",
		"-o", "json")
	appOutput, appErr := appCmd.CombinedOutput()

	if appErr != nil {
		fmt.Printf("Failed to search app registrations: %v\n", appErr)
		fmt.Printf("App search output: %s\n", string(appOutput))
	} else {
		fmt.Printf("App registration search result: %s\n", string(appOutput))

		var appList []map[string]interface{}
		if json.Unmarshal(appOutput, &appList) == nil && len(appList) > 0 {
			for _, appInfo := range appList {
				appId, _ := appInfo["appId"].(string)
				displayName, _ := appInfo["displayName"].(string)

				fmt.Printf("Found app registration:\n")
				fmt.Printf("  Display Name: %s\n", displayName)
				fmt.Printf("  App ID: %s\n", appId)

				// Try to delete service principal by this App ID
				if err := deleteServicePrincipalByAppId(appId); err != nil {
					fmt.Printf("Failed to delete service principal for app ID %s: %v\n", appId, err)
					continue
				}

				// Also try to delete the app registration itself
				fmt.Printf("Attempting to delete app registration: %s\n", appId)
				deleteAppCmd := exec.Command("az", "ad", "app", "delete", "--id", appId)
				if deleteAppErr := deleteAppCmd.Run(); deleteAppErr != nil {
					fmt.Printf("Failed to delete app registration: %v\n", deleteAppErr)
				} else {
					fmt.Printf("Deleted app registration: %s\n", appId)
				}

				// Verify deletion
				if verifyServicePrincipalDeleted(appId) {
					fmt.Printf("✓ Service principal '%s' successfully deleted from Azure AD\n", env.VeleroServicePrincipalName)
					return nil
				}
			}
		}
	}

	// Method 3: Check if we can find it in the credentials file
	fmt.Printf("Attempting to find service principal from credentials file...\n")
	if appIdFromFile := getAppIdFromCredentialsFile(); appIdFromFile != "" {
		fmt.Printf("Found App ID in credentials file: %s\n", appIdFromFile)

		if err := deleteServicePrincipalByAppId(appIdFromFile); err != nil {
			fmt.Printf("Failed to delete using credentials file App ID: %v\n", err)
		} else if verifyServicePrincipalDeleted(appIdFromFile) {
			fmt.Printf("✓ Service principal successfully deleted using credentials file App ID\n")
			return nil
		}
	}

	// If we get here, we couldn't find or delete the service principal
	fmt.Printf("Service principal '%s' not found or already deleted\n", env.VeleroServicePrincipalName)
	return nil
}

// deleteServicePrincipalByAppId deletes a service principal using its App ID
func deleteServicePrincipalByAppId(appId string) error {
	fmt.Printf("Attempting to delete service principal using App ID: %s\n", appId)
	deleteCmd := exec.Command("az", "ad", "sp", "delete", "--id", appId)
	deleteOutput, deleteErr := deleteCmd.CombinedOutput()

	if deleteErr != nil {
		fmt.Printf("Failed to delete service principal using App ID: %v\n", deleteErr)
		fmt.Printf("Delete command output: %s\n", string(deleteOutput))
		return deleteErr
	}

	fmt.Printf("Successfully deleted service principal using App ID: %s\n", appId)
	return nil
}

// deleteServicePrincipalByObjectId deletes a service principal using its Object ID
func deleteServicePrincipalByObjectId(objectId string) error {
	fmt.Printf("Attempting to delete service principal using Object ID: %s\n", objectId)
	deleteCmd := exec.Command("az", "ad", "sp", "delete", "--id", objectId)
	deleteOutput, deleteErr := deleteCmd.CombinedOutput()

	if deleteErr != nil {
		fmt.Printf("Failed to delete service principal using Object ID: %v\n", deleteErr)
		fmt.Printf("Delete command output: %s\n", string(deleteOutput))
		return deleteErr
	}

	fmt.Printf("Successfully deleted service principal using Object ID: %s\n", objectId)
	return nil
}

// verifyServicePrincipalDeleted checks if a service principal is actually deleted
func verifyServicePrincipalDeleted(appId string) bool {
	fmt.Printf("Verifying service principal deletion for App ID: %s\n", appId)
	verifyCmd := exec.Command("az", "ad", "sp", "show", "--id", appId, "-o", "json")
	verifyOutput, verifyErr := verifyCmd.CombinedOutput()

	if verifyErr != nil {
		// Expected error messages when SP is deleted
		errorMsg := string(verifyOutput)
		if strings.Contains(errorMsg, "does not exist") ||
			strings.Contains(errorMsg, "not found") ||
			strings.Contains(errorMsg, "could not be found") ||
			strings.Contains(verifyErr.Error(), "does not exist") {
			fmt.Printf("✓ Verification confirmed: service principal no longer exists\n")
			return true
		} else {
			fmt.Printf("Warning: Unexpected verification error: %v\n", verifyErr)
			fmt.Printf("Verification output: %s\n", errorMsg)
			return false
		}
	} else {
		// Service principal still exists
		fmt.Printf("⚠ Warning: Service principal still exists in Azure AD\n")
		fmt.Printf("Verification output: %s\n", string(verifyOutput))
		return false
	}
}

// getAppIdFromCredentialsFile extracts App ID from the credentials file
func getAppIdFromCredentialsFile() string {
	credentialsPath := "credentials/credentials-velero.json"
	credData, err := os.ReadFile(credentialsPath)
	if err != nil {
		fmt.Printf("Could not read credentials file: %v\n", err)
		return ""
	}

	var credentials map[string]interface{}
	if err := json.Unmarshal(credData, &credentials); err != nil {
		fmt.Printf("Could not parse credentials file: %v\n", err)
		return ""
	}

	appId, _ := credentials["clientId"].(string)
	return appId
}

// assignServicePrincipalRoles assigns required roles to the service principal
func assignServicePrincipalRoles(env Environment) error {
	// Read credentials to get client ID
	credentialsPath := "credentials/credentials-velero.json"
	credData, err := os.ReadFile(credentialsPath)
	if err != nil {
		return fmt.Errorf("failed to read credentials: %v", err)
	}

	var credentials map[string]interface{}
	if err := json.Unmarshal(credData, &credentials); err != nil {
		return fmt.Errorf("failed to parse credentials: %v", err)
	}

	clientID, ok := credentials["clientId"].(string)
	if !ok {
		return fmt.Errorf("clientId not found in credentials")
	}

	fmt.Printf("Service Principal ID: %s\n", clientID)
	fmt.Println("Assigning Contributor role to the following scopes:")

	// Role assignments matching your instructions
	roleAssignments := []struct {
		name  string
		scope string
	}{
		{"Blob Resource Group", "/subscriptions/" + env.SubscriptionID + "/resourceGroups/" + env.VeleroBlobRG},
		{"Snapshot Resource Group", "/subscriptions/" + env.SubscriptionID + "/resourceGroups/" + env.VeleroSnapRG},
		{"Source AKS MC_ Resource Group", "/subscriptions/" + env.SubscriptionID + "/resourceGroups/" + env.SourceMCResourceGroup},
	}

	for i, assignment := range roleAssignments {
		fmt.Printf("   %d. %s: %s\n", i+1, assignment.name, assignment.scope)
		if err := RunCommand("az", "role", "assignment", "create",
			"--assignee", clientID,
			"--role", "Contributor",
			"--scope", assignment.scope); err != nil {
			return fmt.Errorf("failed to assign role for %s: %v", assignment.name, err)
		}
		fmt.Printf("      Assigned Contributor role\n")
	}

	fmt.Printf("Successfully assigned Contributor role to %d scopes\n", len(roleAssignments))
	return nil
}

// createVeleroCredentialsFile creates Velero credentials file in environment format
func createVeleroCredentialsFile(env Environment) error {
	// Read the JSON credentials file
	credentialsPath := "credentials/credentials-velero.json"
	credData, err := os.ReadFile(credentialsPath)
	if err != nil {
		return fmt.Errorf("failed to read credentials: %v", err)
	}

	var credentials map[string]interface{}
	if err := json.Unmarshal(credData, &credentials); err != nil {
		return fmt.Errorf("failed to parse credentials: %v", err)
	}

	// Extract values
	subscriptionID := env.SubscriptionID
	tenantID, _ := credentials["tenantId"].(string)
	clientID, _ := credentials["clientId"].(string)
	clientSecret, _ := credentials["clientSecret"].(string)
	resourceGroup := env.VeleroBlobRG
	cloudName := "AzurePublicCloud"

	// Create environment format credentials
	envCredentials := fmt.Sprintf(`AZURE_SUBSCRIPTION_ID=%s
AZURE_TENANT_ID=%s
AZURE_CLIENT_ID=%s
AZURE_CLIENT_SECRET=%s
AZURE_RESOURCE_GROUP=%s
AZURE_CLOUD_NAME=%s
`,
		subscriptionID, tenantID, clientID, clientSecret, resourceGroup, cloudName)

	// Save to azure-velero-credentials file
	veleroCredsPath := "credentials/azure-velero-credentials"
	if err := os.WriteFile(veleroCredsPath, []byte(envCredentials), 0600); err != nil {
		return err
	}

	// Show detailed output about credential files
	fmt.Println("Created credential files:")
	fmt.Printf("   1. JSON format: %s\n", credentialsPath)
	fmt.Printf("      - For Azure SDK and programmatic access\n")
	fmt.Printf("      - Contains: clientId, clientSecret, tenantId, subscriptionId\n")
	fmt.Printf("   2. Environment format: %s\n", veleroCredsPath)
	fmt.Printf("      - For Velero CLI installation\n")
	fmt.Printf("      - Contains: AZURE_* environment variables\n")
	fmt.Printf("   3. File permissions: 0600 (readable only by owner)\n")
	fmt.Printf("\nUse '%s' for Velero installation\n", veleroCredsPath)

	return nil
}

// CleanupResources deletes all resources created by the automation
func CleanupResources(env Environment) error {
	// Additional validation within cleanup function
	fmt.Printf("Final validation for cleanup operations...\n")
	if err := env.ValidateViyaDeploymentType("Resource cleanup"); err != nil {
		return fmt.Errorf("cleanup validation failed: %v", err)
	}

	fmt.Println("Starting cleanup of all resources created by automation...")
	fmt.Printf("Deployment Type: %s\n", env.ViyaDeploymentType)
	fmt.Println("This will delete:")
	fmt.Printf("- Velero installation (namespace, deployments, daemonsets)\n")
	fmt.Printf("- Service Principal: %s\n", env.VeleroServicePrincipalName)
	fmt.Printf("- Storage Account: %s\n", env.VeleroStoreName)
	fmt.Printf("- Resource Groups: %s and %s\n", env.VeleroBlobRG, env.VeleroSnapRG)
	fmt.Printf("- Volume Snapshot Classes: azure-disk-snapshot-class, azure-nfs-snapshot-class\n")
	fmt.Printf("- Local credential files and backup configurations\n")
	fmt.Println()

	// Confirmation prompt
	fmt.Print("Are you sure you want to delete ALL resources? Type 'yes' to continue: ")
	reader := bufio.NewReader(os.Stdin)
	confirmation, _ := reader.ReadString('\n')
	confirmation = strings.TrimSpace(strings.ToLower(confirmation))

	if confirmation != "yes" {
		fmt.Println("Cleanup cancelled.")
		return nil
	}

	state := LoadState()

	// Delete Velero Installation first
	step := "Delete Velero Installation"
	UpdateStepState(&state, step, "IN-PROGRESS")
	fmt.Printf("Uninstalling Velero...\n")
	if err := deleteVeleroInstallation(env); err != nil {
		fmt.Printf("Warning: Failed to delete Velero installation: %v\n", err)
		UpdateStepState(&state, step, "FAILED")
	} else {
		UpdateStepState(&state, step, "SUCCESS")
	}

	// Delete Service Principal
	step = "Delete Service Principal"
	UpdateStepState(&state, step, "IN-PROGRESS")
	if err := deleteServicePrincipal(env); err != nil {
		fmt.Printf("Warning: Failed to delete service principal: %v\n", err)
		UpdateStepState(&state, step, "FAILED")
	} else {
		UpdateStepState(&state, step, "SUCCESS")
	}

	// Delete Storage Resources (storage account deletion will delete container too)
	step = "Delete Storage Resources"
	UpdateStepState(&state, step, "IN-PROGRESS")
	if err := deleteStorageAccount(env); err != nil {
		fmt.Printf("Warning: Failed to delete storage account: %v\n", err)
		UpdateStepState(&state, step, "FAILED")
	} else {
		UpdateStepState(&state, step, "SUCCESS")
	}

	// Delete Resource Groups (this will delete all contained resources)
	step = "Delete Resource Groups"
	UpdateStepState(&state, step, "IN-PROGRESS")
	if err := deleteResourceGroups(env); err != nil {
		fmt.Printf("Warning: Failed to delete resource groups: %v\n", err)
		UpdateStepState(&state, step, "FAILED")
	} else {
		UpdateStepState(&state, step, "SUCCESS")
	}

	// Delete Kubernetes Resources
	step = "Delete Kubernetes Resources"
	UpdateStepState(&state, step, "IN-PROGRESS")
	if err := deleteKubernetesResources(env); err != nil {
		fmt.Printf("Warning: Failed to delete Kubernetes resources: %v\n", err)
		UpdateStepState(&state, step, "FAILED")
	} else {
		UpdateStepState(&state, step, "SUCCESS")
	}

	// Delete Local Files
	step = "Delete Local Files"
	UpdateStepState(&state, step, "IN-PROGRESS")
	if err := deleteLocalFiles(); err != nil {
		fmt.Printf("Warning: Failed to delete local files: %v\n", err)
		UpdateStepState(&state, step, "FAILED")
	} else {
		UpdateStepState(&state, step, "SUCCESS")
	}

	fmt.Println("\nCleanup process completed!")
	fmt.Println("===============================")
	fmt.Println("Velero installation: Removed")
	fmt.Println("Service Principal: Deleted")
	fmt.Println("Storage Resources: Deleted")
	fmt.Println("Resource Groups: Deletion started")
	fmt.Println("VolumeSnapshotClasses: Removed")
	fmt.Println("Local Files: Cleaned up")
	fmt.Println("===============================")
	fmt.Println("Note: Resource group deletions run in background and may take several minutes to complete")

	return nil
}

// deleteStorageAccount removes the storage account
func deleteStorageAccount(env Environment) error {
	fmt.Printf("Deleting storage account: %s...\n", env.VeleroStoreName)

	cmd := exec.Command("az", "storage", "account", "delete",
		"--name", env.VeleroStoreName,
		"--resource-group", env.VeleroBlobRG,
		"--yes")
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("Failed to delete storage account: %v\nOutput: %s\n", err, string(output))
		return err
	}

	fmt.Printf("Deleted storage account: %s\n", env.VeleroStoreName)
	return nil
}

// deleteResourceGroups removes both resource groups
func deleteResourceGroups(env Environment) error {
	fmt.Printf("Deleting resource groups...\n")

	// Delete blob storage resource group
	fmt.Printf("Deleting resource group: %s...\n", env.VeleroBlobRG)
	cmd1 := exec.Command("az", "group", "delete",
		"--name", env.VeleroBlobRG,
		"--yes", "--no-wait")
	if err := cmd1.Run(); err != nil {
		fmt.Printf("Failed to delete resource group %s: %v\n", env.VeleroBlobRG, err)
		return err
	}
	fmt.Printf("Started deletion of resource group: %s\n", env.VeleroBlobRG)

	// Delete snapshot resource group
	fmt.Printf("Deleting resource group: %s...\n", env.VeleroSnapRG)
	cmd2 := exec.Command("az", "group", "delete",
		"--name", env.VeleroSnapRG,
		"--yes", "--no-wait")
	if err := cmd2.Run(); err != nil {
		fmt.Printf("Failed to delete resource group %s: %v\n", env.VeleroSnapRG, err)
		return err
	}
	fmt.Printf("Started deletion of resource group: %s\n", env.VeleroSnapRG)

	fmt.Printf("Resource group deletions are running in background\n")
	return nil
}

// deleteVeleroInstallation removes Velero namespace and all components
func deleteVeleroInstallation(env Environment) error {
	fmt.Printf("Checking if Velero is installed...\n")

	// Use the globally configured kubeconfig path from check_kubernetes_connectivity.go
	kubeconfigPath := getConfiguredKubeconfigPath()
	if kubeconfigPath == "" {
		// Fallback to environment kubeconfig
		kubeconfigPath = env.KubeconfigPath
		if kubeconfigEnv := os.Getenv("KUBECONFIG"); kubeconfigEnv != "" {
			kubeconfigPath = kubeconfigEnv
		}
	}

	fmt.Printf("Using kubeconfig for Velero cleanup: %s\n", kubeconfigPath)

	// Test cluster connectivity first
	testCmd := exec.Command("kubectl", "cluster-info")
	testCmd.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))
	if err := testCmd.Run(); err != nil {
		fmt.Printf("Warning: Cannot connect to Kubernetes cluster\n")
		fmt.Printf("   Cluster may have been deleted. Skipping Velero uninstallation...\n")
		return nil
	}

	// Check if Velero namespace exists
	cmd := exec.Command("kubectl", "get", "namespace", "velero")
	cmd.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))
	output, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "no such host") || strings.Contains(string(output), "connection refused") {
			fmt.Printf("Cluster unreachable - Velero assumed deleted with cluster\n")
		} else {
			fmt.Printf("Velero namespace not found, skipping Velero uninstallation\n")
		}
		return nil
	}

	fmt.Printf("Found Velero installation, proceeding with uninstallation...\n")

	// Method 1: Try velero uninstall command if available
	fmt.Printf("Attempting Velero CLI uninstall...\n")
	veleroCmd := exec.Command("velero", "uninstall", "--force")
	veleroCmd.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))
	if err := veleroCmd.Run(); err == nil {
		fmt.Printf("Velero uninstalled via CLI\n")
		return nil
	}

	// Method 2: Manual cleanup if velero CLI uninstall fails
	fmt.Printf("Velero CLI uninstall failed, performing manual cleanup...\n")

	// Delete BackupStorageLocations and VolumeSnapshotLocations first
	fmt.Printf("Deleting Velero storage locations...\n")
	cmd1 := exec.Command("kubectl", "delete", "backupstoragelocations.velero.io", "--all", "-n", "velero", "--ignore-not-found")
	cmd1.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))
	cmd1.Run()
	cmd2 := exec.Command("kubectl", "delete", "volumesnapshotlocations.velero.io", "--all", "-n", "velero", "--ignore-not-found")
	cmd2.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))
	cmd2.Run()

	// Delete all Velero custom resources
	fmt.Printf("Deleting Velero custom resources...\n")
	veleroResources := []string{
		"backups.velero.io",
		"restores.velero.io",
		"schedules.velero.io",
		"backuprepositories.velero.io",
		"downloadrequests.velero.io",
		"deletebackuprequests.velero.io",
		"podvolumebackups.velero.io",
		"podvolumerestores.velero.io",
	}

	for _, resource := range veleroResources {
		cmd := exec.Command("kubectl", "delete", resource, "--all", "-n", "velero", "--ignore-not-found")
		cmd.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))
		cmd.Run() // Continue even if some resources don't exist
	}

	// Delete the entire Velero namespace (this removes everything in it)
	fmt.Printf("Deleting Velero namespace...\n")
	cmd = exec.Command("kubectl", "delete", "namespace", "velero", "--ignore-not-found")
	cmd.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to delete Velero namespace: %v", err)
	}

	fmt.Printf("Velero installation removed\n")
	return nil
}

// deleteKubernetesResources removes volume snapshot classes and other Kubernetes resources
func deleteKubernetesResources(env Environment) error {
	fmt.Printf("Deleting VolumeSnapshotClasses...\n")

	// Use the globally configured kubeconfig path from check_kubernetes_connectivity.go
	kubeconfigPath := getConfiguredKubeconfigPath()
	if kubeconfigPath == "" {
		// Fallback to environment kubeconfig
		kubeconfigPath = env.KubeconfigPath
		if kubeconfigEnv := os.Getenv("KUBECONFIG"); kubeconfigEnv != "" {
			kubeconfigPath = kubeconfigEnv
		}
	}

	fmt.Printf("Using kubeconfig for cleanup: %s\n", kubeconfigPath)

	// Test cluster connectivity first
	testCmd := exec.Command("kubectl", "cluster-info")
	testCmd.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))
	if err := testCmd.Run(); err != nil {
		fmt.Printf("Warning: Cannot connect to Kubernetes cluster using kubeconfig: %s\n", kubeconfigPath)
		fmt.Printf("   This may be expected if the cluster has been deleted.\n")
		fmt.Printf("   Skipping Kubernetes resource cleanup...\n")
		return nil // Don't fail cleanup if cluster is unreachable
	}

	// First, list existing VolumeSnapshotClasses to see what we're working with
	cmd := exec.Command("kubectl", "get", "volumesnapshotclasses.snapshot.storage.k8s.io", "-o", "name")
	cmd.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))
	output, err := cmd.Output()
	if err == nil {
		fmt.Printf("Found VolumeSnapshotClasses:\n%s\n", string(output))
	}

	// Declare variables for error checking at function scope
	var cmd1Output, cmd2Output []byte

	// Delete azure-disk-snapshot-class
	fmt.Printf("Deleting azure-disk-snapshot-class...\n")
	cmd1 := exec.Command("kubectl", "delete", "volumesnapshotclass.snapshot.storage.k8s.io",
		"azure-disk-snapshot-class", "--ignore-not-found")
	cmd1.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))
	cmd1Output, err1 := cmd1.CombinedOutput()
	if err1 != nil {
		if strings.Contains(string(cmd1Output), "no such host") || strings.Contains(string(cmd1Output), "connection refused") {
			fmt.Printf("Cluster unreachable - azure-disk-snapshot-class may have been deleted with cluster\n")
		} else {
			fmt.Printf("Error deleting azure-disk-snapshot-class: %v\n", err1)
		}
	} else {
		fmt.Printf("Deleted azure-disk-snapshot-class\n")
	}

	// Delete azure-nfs-snapshot-class
	fmt.Printf("Deleting azure-nfs-snapshot-class...\n")
	cmd2 := exec.Command("kubectl", "delete", "volumesnapshotclass.snapshot.storage.k8s.io",
		"azure-nfs-snapshot-class", "--ignore-not-found")
	cmd2.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))
	cmd2Output, err2 := cmd2.CombinedOutput()
	if err2 != nil {
		if strings.Contains(string(cmd2Output), "no such host") || strings.Contains(string(cmd2Output), "connection refused") {
			fmt.Printf("Cluster unreachable - azure-nfs-snapshot-class may have been deleted with cluster\n")
		} else {
			fmt.Printf("Error deleting azure-nfs-snapshot-class: %v\n", err2)
		}
	} else {
		fmt.Printf("Deleted azure-nfs-snapshot-class\n")
	}

	// Verify deletion
	fmt.Printf("Verifying VolumeSnapshotClass deletion...\n")
	cmd3 := exec.Command("kubectl", "get", "volumesnapshotclasses.snapshot.storage.k8s.io",
		"azure-disk-snapshot-class", "azure-nfs-snapshot-class", "--ignore-not-found")
	cmd3.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))
	output3, err3 := cmd3.CombinedOutput()
	if err3 != nil {
		if strings.Contains(string(output3), "no such host") || strings.Contains(string(output3), "connection refused") {
			fmt.Printf("Cluster unreachable - VolumeSnapshotClasses assumed deleted with cluster\n")
		} else {
			fmt.Printf("Could not verify VolumeSnapshotClass deletion: %v\n", err3)
		}
	} else if len(output3) == 0 {
		fmt.Printf("All VolumeSnapshotClasses successfully deleted\n")
	} else {
		fmt.Printf("Some VolumeSnapshotClasses may still exist:\n%s\n", string(output3))
	}

	// Only return error if both deletions failed AND it's not due to cluster unreachability
	if err1 != nil && err2 != nil {
		if (strings.Contains(string(cmd1Output), "no such host") || strings.Contains(string(cmd1Output), "connection refused")) &&
			(strings.Contains(string(cmd2Output), "no such host") || strings.Contains(string(cmd2Output), "connection refused")) {
			fmt.Printf("Kubernetes cleanup skipped - cluster is unreachable (likely deleted)\n")
			return nil
		}
		return fmt.Errorf("failed to delete VolumeSnapshotClasses: %v, %v", err1, err2)
	}

	return nil
}

// deleteLocalFiles removes credential files, backup configurations, and state
func deleteLocalFiles() error {
	fmt.Printf("Deleting local files and directories...\n")

	// Remove credentials directory
	fmt.Printf("Removing credentials directory...\n")
	if err := os.RemoveAll("credentials"); err != nil {
		fmt.Printf("Error removing credentials directory: %v\n", err)
		return err
	} else {
		fmt.Printf("Deleted credentials directory\n")
	}

	// Remove backup directory
	fmt.Printf("Removing backup configurations...\n")
	if err := os.RemoveAll("backup"); err != nil {
		fmt.Printf("Error removing backup directory: %v\n", err)
	} else {
		fmt.Printf("Deleted backup directory\n")
	}

	// Remove state file
	fmt.Printf("Removing state file...\n")
	if err := os.Remove("state.json"); err != nil && !os.IsNotExist(err) {
		fmt.Printf("Error removing state file: %v\n", err)
		return err
	} else {
		fmt.Printf("Deleted state.json\n")
	}

	fmt.Printf("All local files cleaned up\n")
	return nil
}
