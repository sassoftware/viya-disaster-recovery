// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Config represents the environment configuration
type Config struct {
	// AWS Configuration
	Region    string
	AccountID string

	// S3 Configuration
	VeleroBucketName   string
	VeleroBucketRegion string

	// Kubernetes Configuration
	KubeconfigPath string

	// EKS Configuration
	ClusterName      string
	EKSClusterRegion string

	// Viya Configuration
	ViyaDeploymentType string
	ViyaNamespace      string
	ClusterType        string

	// IAM Configuration
	EBSRoleName          string
	EBSPolicyName        string
	VeleroRoleName       string
	VeleroServiceAccount string
	VeleroIAMUser        string // IAM user for Velero (instead of IRSA)
	UseIAMUser           bool   // Use IAM user instead of IRSA
	InstallEBSCSIAddon   bool   // Install EBS CSI addon

	// Velero Configuration
	VeleroNamespace     string
	VeleroVersion       string
	VeleroPluginVersion string
	SnapshotClassName   string

	// EBS CSI Configuration
	EBSCSIDriverVersion  string
	EBSCSIVersion        string
	EBSCSIServiceAccount string
	EBSCSINamespace      string

	// Backup/Restore Configuration
	BackupNamePrefix        string
	RestoreNamePrefix       string
	BackupTTL               string
	BackupSnapshotVolumes   bool
	BackupWait              bool
	BackupExcludeNamespaces string
	RestoreWait             bool
	RestorePVs              bool

	// NFS/EFS Storage Class — used to detect EFS/NFS PVCs and create the permission backup PVC
	// Matches NFS_STORAGE_CLASS in environment.properties (same as Azure pattern)
	NFSStorageClass string

	// Advanced Configuration
	DebugMode      bool
	SkipValidation bool

	// Runtime data (auto-detected)
	OIDCID string
}

// LoadConfig loads configuration from properties file
func LoadConfig(filepath string) (*Config, error) {
	config := &Config{
		// Set defaults
		VeleroBucketRegion:      "us-east-1",
		EKSClusterRegion:        "us-east-1",
		Region:                  "us-east-1",
		ViyaDeploymentType:      "nmt",
		ClusterType:             "source",
		EBSPolicyName:           "AmazonEBSCSIDriverPolicy",
		VeleroServiceAccount:    "velero",
		VeleroIAMUser:           "velero-primary",
		UseIAMUser:              true,
		InstallEBSCSIAddon:      true,
		VeleroNamespace:         "velero",
		VeleroVersion:           "v1.17.0",
		VeleroPluginVersion:     "v1.13.0",
		SnapshotClassName:       "csi-aws-vsc",
		EBSCSIDriverVersion:     "v1.52.1-eksbuild.1",
		EBSCSIVersion:           "v1.52.1-eksbuild.1",
		EBSCSIServiceAccount:    "ebs-csi-controller-sa",
		EBSCSINamespace:         "kube-system",
		BackupNamePrefix:        "viya-backup",
		RestoreNamePrefix:       "viya-restore",
		BackupTTL:               "720h",
		BackupSnapshotVolumes:   true,
		BackupWait:              true,
		BackupExcludeNamespaces: "kube-system,kube-public,kube-node-lease,velero",
		RestoreWait:             true,
		RestorePVs:              true,
		DebugMode:               false,
		SkipValidation:          false,
		NFSStorageClass:         "sas",
	}

	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse key=value
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid line %d: %s", lineNum, line)
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Skip empty values
		if value == "" {
			continue
		}

		// Map to config fields
		switch key {
		case "REGION":
			config.Region = value
		case "ACCOUNT_ID":
			config.AccountID = value
		case "VELERO_BUCKET_NAME":
			config.VeleroBucketName = value
		case "VELERO_BUCKET_REGION":
			config.VeleroBucketRegion = value
		case "KUBECONFIG_PATH":
			config.KubeconfigPath = value
		case "CLUSTER_NAME":
			config.ClusterName = value
		case "EKS_CLUSTER_REGION":
			config.EKSClusterRegion = value
		case "VIYA_DEPLOYMENT_TYPE":
			config.ViyaDeploymentType = value
		case "VIYA_NAMESPACE":
			config.ViyaNamespace = value
		case "CLUSTER_TYPE":
			config.ClusterType = value
		case "EBS_ROLE_NAME":
			config.EBSRoleName = value
		case "EBS_POLICY_NAME":
			config.EBSPolicyName = value
		case "VELERO_ROLE_NAME":
			config.VeleroRoleName = value
		case "VELERO_SERVICE_ACCOUNT":
			config.VeleroServiceAccount = value
		case "VELERO_IAM_USER":
			config.VeleroIAMUser = value
		case "USE_IAM_USER":
			config.UseIAMUser = strings.ToLower(value) == "true"
		case "INSTALL_EBS_CSI_ADDON":
			config.InstallEBSCSIAddon = strings.ToLower(value) == "true"
		case "VELERO_NAMESPACE":
			config.VeleroNamespace = value
		case "VELERO_VERSION":
			config.VeleroVersion = value
		case "VELERO_PLUGIN_VERSION":
			config.VeleroPluginVersion = value
		case "SNAPSHOT_CLASS_NAME":
			config.SnapshotClassName = value
		case "EBS_CSI_DRIVER_VERSION":
			config.EBSCSIDriverVersion = value
		case "EBS_CSI_VERSION":
			config.EBSCSIVersion = value
		case "EBS_CSI_SERVICE_ACCOUNT":
			config.EBSCSIServiceAccount = value
		case "EBS_CSI_NAMESPACE":
			config.EBSCSINamespace = value
		case "BACKUP_NAME_PREFIX":
			config.BackupNamePrefix = value
		case "RESTORE_NAME_PREFIX":
			config.RestoreNamePrefix = value
		case "BACKUP_TTL":
			config.BackupTTL = value
		case "BACKUP_SNAPSHOT_VOLUMES":
			config.BackupSnapshotVolumes = strings.ToLower(value) == "true"
		case "BACKUP_WAIT":
			config.BackupWait = strings.ToLower(value) == "true"
		case "BACKUP_EXCLUDE_NAMESPACES":
			config.BackupExcludeNamespaces = value
		case "RESTORE_WAIT":
			config.RestoreWait = strings.ToLower(value) == "true"
		case "RESTORE_PVS":
			config.RestorePVs = strings.ToLower(value) == "true"
		case "DEBUG_MODE":
			config.DebugMode = strings.ToLower(value) == "true"
		case "SKIP_VALIDATION":
			config.SkipValidation = strings.ToLower(value) == "true"
		case "NFS_STORAGE_CLASS":
			config.NFSStorageClass = value
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading config file: %w", err)
	}

	return config, nil
}

// Validate validates the configuration
func (c *Config) Validate() error {
	var errors []string

	// Required fields
	if c.ClusterName == "" {
		errors = append(errors, "CLUSTER_NAME is required")
	}
	if c.VeleroBucketName == "" {
		errors = append(errors, "VELERO_BUCKET_NAME is required")
	}
	if c.KubeconfigPath == "" {
		errors = append(errors, "KUBECONFIG_PATH is required")
	}
	if c.EBSRoleName == "" {
		errors = append(errors, "EBS_ROLE_NAME is required")
	}
	if c.VeleroRoleName == "" {
		errors = append(errors, "VELERO_ROLE_NAME is required")
	}

	// Validate deployment type
	if c.ViyaDeploymentType != "nmt" && c.ViyaDeploymentType != "mt" {
		errors = append(errors, "VIYA_DEPLOYMENT_TYPE must be 'nmt' or 'mt'")
	}

	// Validate cluster type
	if c.ClusterType != "source" && c.ClusterType != "restore" {
		errors = append(errors, "CLUSTER_TYPE must be 'source' or 'restore'")
	}

	// Check if kubeconfig exists
	if _, err := os.Stat(c.KubeconfigPath); os.IsNotExist(err) {
		errors = append(errors, fmt.Sprintf("KUBECONFIG_PATH does not exist: %s", c.KubeconfigPath))
	}

	if len(errors) > 0 {
		return fmt.Errorf("configuration validation failed:\n  - %s", strings.Join(errors, "\n  - "))
	}

	return nil
}

// IsSourceCluster returns true if this is a source (backup) cluster
func (c *Config) IsSourceCluster() bool {
	return c.ClusterType == "source"
}

// IsRestoreCluster returns true if this is a restore cluster
func (c *Config) IsRestoreCluster() bool {
	return c.ClusterType == "restore"
}

// IsDebugEnabled returns true if debug mode is enabled
func (c *Config) IsDebugEnabled() bool {
	return c.DebugMode
}
