// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !health
// +build !health

package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/sas-institute/viya-aws-dr-automation/pkg/config"
	"github.com/sas-institute/viya-aws-dr-automation/pkg/orchestrator"
)

const (
	version = "1.0.0"
)

func main() {
	// Command line flags (Azure-compatible interface)
	stepsFlag := flag.String("steps", "", "Comma-separated list of steps to run: aws,kubernetes,velero or all")
	backupFlag := flag.Bool("backup", false, "Execute Velero backup process for Viya namespace")
	restoreFlag := flag.Bool("restore", false, "Execute Velero restore process from existing backup")
	cleanupFlag := flag.Bool("cleanup", false, "Delete all resources created by this automation")
	validateFlag := flag.Bool("validate", false, "Validate environment and prerequisites")
	configPath := flag.String("config", "environment.properties", "Path to environment configuration file")
	showVersion := flag.Bool("version", false, "Show version information")
	help := flag.Bool("help", false, "Show help message")

	// Legacy flag support (deprecated but still supported)
	operation := flag.String("operation", "", "DEPRECATED: Use --steps, --backup, --restore, --cleanup, or --validate instead")

	flag.Parse()

	if *showVersion {
		fmt.Printf("SAS Viya4 AWS Disaster Recovery Automation v%s\n", version)
		os.Exit(0)
	}

	if *help {
		printHelp()
		os.Exit(0)
	}

	// Determine operation mode
	var operationMode string

	if *backupFlag {
		operationMode = "backup"
	} else if *restoreFlag {
		operationMode = "restore"
	} else if *cleanupFlag {
		operationMode = "cleanup"
	} else if *validateFlag {
		operationMode = "validate"
	} else if *stepsFlag != "" {
		operationMode = "aws-setup"
	} else if *operation != "" {
		// Legacy support
		operationMode = *operation
		fmt.Printf("Warning: -operation flag is deprecated. Use --steps, --backup, --restore, --cleanup, or --validate instead\n\n")
	} else {
		// No flags specified - default to help
		printHelp()
		os.Exit(0)
	}

	// Load configuration
	fmt.Println("Loading environment configuration...")
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Configuration validation failed: %v\n", err)
		os.Exit(1)
	}

	// Additional validation for backup/restore operations
	if operationMode == "backup" && cfg.ClusterType != "source" {
		fmt.Fprintf(os.Stderr, "Error: Backup operations require CLUSTER_TYPE=source in environment.properties\n")
		os.Exit(1)
	}
	if operationMode == "restore" && cfg.ClusterType != "restore" {
		fmt.Fprintf(os.Stderr, "Error: Restore operations require CLUSTER_TYPE=restore in environment.properties\n")
		os.Exit(1)
	}

	// Create orchestrator
	orch, err := orchestrator.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing orchestrator: %v\n", err)
		os.Exit(1)
	}

	// Handle steps-based execution
	if operationMode == "aws-setup" && *stepsFlag != "" {
		fmt.Printf("Executing steps: %s\n", *stepsFlag)
		if err := orch.ExecuteSteps(*stepsFlag); err != nil {
			fmt.Fprintf(os.Stderr, "Steps execution failed: %v\n", err)
			os.Exit(1)
		}
	} else {
		// Execute single operation
		fmt.Printf("Executing operation: %s\n", operationMode)
		if err := orch.Execute(operationMode); err != nil {
			fmt.Fprintf(os.Stderr, "Operation failed: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Println("✓ Operation completed successfully!")
}

func printHelp() {
	fmt.Print(`
SAS Viya4 AWS Disaster Recovery Automation

Usage:
  ./aws-viya4-dr [options]

Options:
  --steps string
        Comma-separated list of setup steps to run.
        Available steps: aws, kubernetes, velero, all
          aws        - Create IAM roles, S3 bucket, install EBS CSI driver
          kubernetes - Install CSI snapshot controller and VolumeSnapshotClasses
          velero     - Install Velero with CSI support and patch node-agent tolerations
          all        - Run all steps in sequence (default)

  --backup
        Execute Velero backup process (prompts for Viya namespace)
        Requires CLUSTER_TYPE=source in environment.properties

  --restore
        Execute Velero restore process from an existing backup
        Requires CLUSTER_TYPE=restore in environment.properties

  --cleanup
        Delete all AWS resources created by this automation

  --validate
        Validate environment and prerequisites only

  --config string
        Path to environment configuration file (default: "environment.properties")

  --version
        Show version information

  --help
        Show this help message

Examples:
  # Full setup — AWS resources + Kubernetes config + Velero installation
  ./aws-viya4-dr --steps=all

  # Run individual steps
  ./aws-viya4-dr --steps=aws
  ./aws-viya4-dr --steps=kubernetes
  ./aws-viya4-dr --steps=velero

  # Run combined steps
  ./aws-viya4-dr --steps=aws,kubernetes
  ./aws-viya4-dr --steps=aws,kubernetes,velero

  # Validate environment prerequisites
  ./aws-viya4-dr --validate

  # Run backup on source cluster (CLUSTER_TYPE=source)
  ./aws-viya4-dr --backup

  # Run restore on target cluster (CLUSTER_TYPE=restore)
  ./aws-viya4-dr --restore

  # Clean up all resources
  ./aws-viya4-dr --cleanup

  # Use a custom config file
  ./aws-viya4-dr --config custom-env.properties --steps=all

Notes:
  - Set CLUSTER_TYPE=source in environment.properties for backup operations
  - Set CLUSTER_TYPE=restore in environment.properties for restore operations
  - The --steps flag can combine multiple steps: --steps=aws,kubernetes,velero

For more information, see README.md
`)
}
