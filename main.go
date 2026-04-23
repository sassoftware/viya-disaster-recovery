// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	stepsFlag := flag.String("steps", "all", "Comma-separated list of steps to run: azure,kubernetes,velero,backup,restore or all")
	cleanupFlag := flag.Bool("cleanup", false, "Delete all resources created by this automation")
	backupFlag := flag.Bool("backup", false, "Execute Velero backup process for Viya namespace")
	restoreFlag := flag.Bool("restore", false, "Execute Velero restore process from existing backup")
	debugFlag := flag.Bool("debug", false, "Enable debug logging")
	flag.Parse()

	// Set debug mode if enabled
	if *debugFlag {
		fmt.Println("🐛 Debug mode enabled")
	}

	// Handle restore mode
	if *restoreFlag {
		env := GetEnvironment()
		
		// Validate environment configuration
		if err := env.ValidateEnvironment(); err != nil {
			fmt.Printf("Configuration Error: %v\n", err)
			fmt.Println("\nPlease check your environment.properties file or update environment_details.go")
			os.Exit(1)
		}
		
		if err := env.ValidateViyaDeploymentType("Velero restore"); err != nil {
			fmt.Println(err)
			fmt.Println("\nNote: Change VIYA_DEPLOYMENT_TYPE to 'nmt' in environment.properties file")
			os.Exit(1)
		}
		
		// Validate cluster type for restore
		if env.ClusterType != "restore" {
			fmt.Printf("Warning: ClusterType is set to '%s' but restore mode requires 'restore'\n", env.ClusterType)
			fmt.Println("Please set CLUSTER_TYPE=restore in environment.properties file")
			fmt.Println("   This ensures proper credential handling and kubeconfig setup for the target cluster")
			os.Exit(1)
		}
		
		// Setup kubeconfig for restore operations
		fmt.Printf("Setting up kubeconfig for restore operations...\n")
		if err := CheckKubeconfig(env.KubeconfigPath); err != nil {
			fmt.Printf("Warning: Default kubeconfig validation failed: %v\n", err)
			fmt.Println("   You will be prompted for the restore cluster kubeconfig during the process")
		}
		
		if err := ExecuteRestore(); err != nil {
			fmt.Println("Restore execution failed:", err)
			os.Exit(1)
		}
		return
	}

	// Handle backup mode
	if *backupFlag {
		env := GetEnvironment()
		
		// Validate environment configuration
		if err := env.ValidateEnvironment(); err != nil {
			fmt.Printf("Configuration Error: %v\n", err)
			fmt.Println("\nPlease check your environment.properties file or update environment_details.go")
			os.Exit(1)
		}
		
		if err := env.ValidateViyaDeploymentType("Velero backup"); err != nil {
			fmt.Println(err)
			fmt.Println("\nNote: Change VIYA_DEPLOYMENT_TYPE to 'nmt' in environment.properties file")
			os.Exit(1)
		}
		
		// Setup kubeconfig and validate connectivity
		if err := CheckKubeconfig(env.KubeconfigPath); err != nil {
			fmt.Printf("Kubeconfig validation failed: %v\n", err)
			os.Exit(1)
		}
		if err := CheckClusterAccess(); err != nil {
			fmt.Printf("Cluster access validation failed: %v\n", err)
			os.Exit(1)
		}

		if err := ExecuteBackup(); err != nil {
			fmt.Println("Backup execution failed:", err)
			os.Exit(1)
		}
		return
	}

	// Handle cleanup mode
	if *cleanupFlag {
		env := GetEnvironment()
		
		// Validate environment configuration
		if err := env.ValidateEnvironment(); err != nil {
			fmt.Printf("Configuration Error: %v\n", err)
			fmt.Println("\nPlease check your environment.properties file")
			os.Exit(1)
		}
		
		fmt.Println("Starting cleanup process...")
		
		if err := CleanupResources(env); err != nil {
			fmt.Printf("Cleanup failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Normal operation mode
	env := GetEnvironment()
	
	// Validate environment configuration for all operations
	if err := env.ValidateEnvironment(); err != nil {
		fmt.Printf("Configuration Error: %v\n", err)
		fmt.Println("\nPlease check your environment.properties file or update environment_details.go")
		os.Exit(1)
	}
	
	// Load or initialize state
	state := LoadState()
	
	// Parse steps to execute
	steps := parseSteps(*stepsFlag)
	
	// Track overall start time
	overallStart := time.Now()
	
	// Execute steps based on user input
	for _, step := range steps {
		stepStart := time.Now()
		
		switch step {
		case "azure":
			fmt.Println("\nStarting Azure Resources Creation...")
			if err := CreateAzureResources(env, &state); err != nil {
				fmt.Printf("Azure resource creation failed: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Azure resources completed in %v\n", time.Since(stepStart))
			
		case "kubernetes":
			fmt.Println("\nStarting Kubernetes Configuration...")
			if err := CheckKubernetes(env, &state); err != nil {
				fmt.Printf("Kubernetes configuration failed: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Kubernetes configuration completed in %v\n", time.Since(stepStart))
			
		case "velero":
			fmt.Println("\nStarting Velero Verification...")
			if !isVeleroInstalled() {
				fmt.Println("Installing Velero...")
				if err := InstallVelero(env); err != nil {
					fmt.Printf("Velero installation failed: %v\n", err)
					os.Exit(1)
				}
			}
			if err := VerifyVeleroPods(env); err != nil {
				fmt.Printf("Velero verification failed: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Velero verification completed in %v\n", time.Since(stepStart))
			
		case "backup":
			// Validate deployment type for backup
			if err := env.ValidateViyaDeploymentType("Velero backup"); err != nil {
				fmt.Println(err)
				fmt.Println("\nNote: Change ViyaDeploymentType to 'nmt' in environment_details.go to enable backup operations")
				os.Exit(1)
			}
			
			fmt.Println("\nStarting Velero Backup...")
			if err := ExecuteBackup(); err != nil {
				fmt.Printf("Velero backup failed: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Velero backup completed in %v\n", time.Since(stepStart))
			
		case "restore":
			// Validate deployment type for restore
			if err := env.ValidateViyaDeploymentType("Velero restore"); err != nil {
				fmt.Println(err)
				fmt.Println("\nNote: Change ViyaDeploymentType to 'nmt' in environment_details.go to enable restore operations")
				os.Exit(1)
			}
			
			// Validate cluster type for restore
			if env.ClusterType != "restore" {
				fmt.Printf("Warning: ClusterType is set to '%s' but restore mode requires 'restore'\n", env.ClusterType)
				fmt.Println("Please set ClusterType to 'restore' in environment_details.go for restore operations")
				fmt.Println("   This ensures proper credential handling and kubeconfig setup for the target cluster")
				os.Exit(1)
			}
			
			fmt.Println("\nStarting Velero Restore...")
			if err := ExecuteRestore(); err != nil {
				fmt.Printf("Velero restore failed: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Velero restore completed in %v\n", time.Since(stepStart))
			
		default:
			fmt.Printf("Unknown step: %s\n", step)
			os.Exit(1)
		}
	}
	
	fmt.Printf("\nAll operations completed successfully in %v!\n", time.Since(overallStart))
}

// parseSteps parses the steps flag and returns a slice of steps to execute
func parseSteps(stepsStr string) []string {
	if stepsStr == "all" {
		return []string{"azure", "kubernetes", "velero"}
	}
	
	steps := strings.Split(stepsStr, ",")
	var result []string
	for _, step := range steps {
		trimmed := strings.TrimSpace(step)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
