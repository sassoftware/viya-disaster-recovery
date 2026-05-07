// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package validator

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/sas-institute/viya-aws-dr-automation/pkg/aws"
	"github.com/sas-institute/viya-aws-dr-automation/pkg/config"
	"github.com/sas-institute/viya-aws-dr-automation/pkg/kubernetes"
)

// Validator validates environment prerequisites
type Validator struct {
	config *config.Config
}

// NewValidator creates a new validator
func NewValidator(cfg *config.Config) *Validator {
	return &Validator{
		config: cfg,
	}
}

// ValidateAll validates all prerequisites
func (v *Validator) ValidateAll(ctx context.Context) error {
	fmt.Println("Validating environment prerequisites...")

	checks := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"AWS CLI", v.validateAWSCLI},
		{"Kubectl", v.validateKubectl},
		{"Eksctl", v.validateEksctl},
		{"Velero CLI", v.validateVeleroCLI},
		{"Kubeconfig", v.validateKubeconfig},
		{"Kubernetes Connection", v.validateK8sConnection},
		{"AWS Credentials", v.validateAWSCredentials},
		{"EKS Cluster", v.validateEKSCluster},
	}

	for _, check := range checks {
		fmt.Printf("  ✓ Checking %s...\n", check.name)
		if err := check.fn(ctx); err != nil {
			return fmt.Errorf("%s validation failed: %w", check.name, err)
		}
	}

	fmt.Println("All prerequisite checks passed!")
	return nil
}

// validateAWSCLI validates AWS CLI is installed
func (v *Validator) validateAWSCLI(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "aws", "--version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("AWS CLI not found. Please install AWS CLI")
	}
	return nil
}

// validateKubectl validates kubectl is installed
func (v *Validator) validateKubectl(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "kubectl", "version", "--client")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl not found. Please install kubectl")
	}
	return nil
}

// validateEksctl validates eksctl is installed
func (v *Validator) validateEksctl(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "eksctl", "version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("eksctl not found. Please install eksctl from https://eksctl.io")
	}
	return nil
}

// validateVeleroCLI validates Velero CLI is installed
func (v *Validator) validateVeleroCLI(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "velero", "version", "--client-only")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Velero CLI not found. Please install Velero CLI")
	}
	return nil
}

// validateKubeconfig validates kubeconfig file exists
func (v *Validator) validateKubeconfig(ctx context.Context) error {
	if _, err := os.Stat(v.config.KubeconfigPath); os.IsNotExist(err) {
		return fmt.Errorf("kubeconfig file not found: %s", v.config.KubeconfigPath)
	}
	return nil
}

// validateK8sConnection validates connection to Kubernetes cluster
func (v *Validator) validateK8sConnection(ctx context.Context) error {
	client, err := kubernetes.NewK8sClient(v.config.KubeconfigPath)
	if err != nil {
		return err
	}

	if err := client.TestConnection(ctx); err != nil {
		return err
	}

	version, err := client.GetServerVersion(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("    Connected to Kubernetes %s\n", version)
	return nil
}

// validateAWSCredentials validates AWS credentials are configured
func (v *Validator) validateAWSCredentials(ctx context.Context) error {
	stsManager, err := aws.NewSTSManager(ctx, v.config.Region)
	if err != nil {
		return err
	}

	identity, err := stsManager.GetCallerIdentity(ctx)
	if err != nil {
		return fmt.Errorf("failed to get AWS caller identity. Please configure AWS credentials")
	}

	fmt.Printf("    AWS Account: %s\n", identity.Account)

	// Update config with account ID if not set
	if v.config.AccountID == "" {
		v.config.AccountID = identity.Account
	}

	return nil
}

// validateEKSCluster validates EKS cluster exists and is accessible
func (v *Validator) validateEKSCluster(ctx context.Context) error {
	eksManager, err := aws.NewEKSManager(ctx, v.config.EKSClusterRegion)
	if err != nil {
		return err
	}

	exists, err := eksManager.ClusterExists(ctx, v.config.ClusterName)
	if err != nil {
		return err
	}

	if !exists {
		return fmt.Errorf("EKS cluster '%s' not found", v.config.ClusterName)
	}

	info, err := eksManager.GetClusterInfo(ctx, v.config.ClusterName)
	if err != nil {
		return err
	}

	fmt.Printf("    EKS Cluster: %s (version %s, status: %s)\n",
		info.Name, info.Version, info.Status)

	return nil
}
