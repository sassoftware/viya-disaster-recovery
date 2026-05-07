// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

// EBSCSIManager handles EBS CSI driver setup
type EBSCSIManager struct {
	iamClient      *iam.Client
	eksClient      *eks.Client
	config         aws.Config
	kubeconfigPath string
}

// NewEBSCSIManager creates a new EBS CSI manager
func NewEBSCSIManager(ctx context.Context, region string, kubeconfigPath string) (*EBSCSIManager, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &EBSCSIManager{
		iamClient:      iam.NewFromConfig(cfg),
		eksClient:      eks.NewFromConfig(cfg),
		config:         cfg,
		kubeconfigPath: kubeconfigPath,
	}, nil
}

// GetOIDCProviderID retrieves the OIDC provider ID from EKS cluster
func (e *EBSCSIManager) GetOIDCProviderID(ctx context.Context, clusterName string) (string, error) {
	input := &eks.DescribeClusterInput{
		Name: aws.String(clusterName),
	}

	result, err := e.eksClient.DescribeCluster(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to describe cluster: %w", err)
	}

	if result.Cluster == nil || result.Cluster.Identity == nil || result.Cluster.Identity.Oidc == nil {
		return "", fmt.Errorf("OIDC issuer not found for cluster")
	}

	issuer := aws.ToString(result.Cluster.Identity.Oidc.Issuer)
	// Extract ID from https://oidc.eks.region.amazonaws.com/id/{ID}
	parts := strings.Split(issuer, "/id/")
	if len(parts) != 2 {
		return "", fmt.Errorf("unexpected OIDC issuer format: %s", issuer)
	}

	return parts[1], nil
}

// CreateEBSCSITrustPolicy creates the IAM trust policy for EBS CSI driver
func (e *EBSCSIManager) CreateEBSCSITrustPolicy(accountID, region, oidcID, namespace, serviceAccount string) (string, error) {
	trustPolicy := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{
				"Effect": "Allow",
				"Principal": map[string]string{
					"Federated": fmt.Sprintf("arn:aws:iam::%s:oidc-provider/oidc.eks.%s.amazonaws.com/id/%s", accountID, region, oidcID),
				},
				"Action": []string{
					"sts:TagSession",
					"sts:AssumeRoleWithWebIdentity",
				},
				"Condition": map[string]interface{}{
					"StringEquals": map[string]string{
						fmt.Sprintf("oidc.eks.%s.amazonaws.com/id/%s:sub", region, oidcID): fmt.Sprintf("system:serviceaccount:%s:%s", namespace, serviceAccount),
					},
					"StringLike": map[string]string{
						fmt.Sprintf("oidc.eks.%s.amazonaws.com/id/%s:aud", region, oidcID): "sts.amazonaws.com",
					},
				},
			},
			{
				"Effect": "Allow",
				"Principal": map[string]string{
					"Service": "pods.eks.amazonaws.com",
				},
				"Action": "sts:AssumeRole",
			},
		},
	}

	policyJSON, err := json.MarshalIndent(trustPolicy, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal trust policy: %w", err)
	}

	return string(policyJSON), nil
}

// CreateOrUpdateEBSCSIRole creates or updates the IAM role for EBS CSI driver
func (e *EBSCSIManager) CreateOrUpdateEBSCSIRole(ctx context.Context, roleName, accountID, region, oidcID, namespace, serviceAccount string) (string, error) {
	trustPolicy, err := e.CreateEBSCSITrustPolicy(accountID, region, oidcID, namespace, serviceAccount)
	if err != nil {
		return "", err
	}

	// Check if role exists
	getRoleInput := &iam.GetRoleInput{
		RoleName: aws.String(roleName),
	}

	roleExists := false
	roleARN := ""

	getRoleOutput, err := e.iamClient.GetRole(ctx, getRoleInput)
	if err == nil {
		roleExists = true
		roleARN = aws.ToString(getRoleOutput.Role.Arn)
	}

	if roleExists {
		// Update existing role trust policy
		fmt.Printf("Updating trust policy for existing role: %s\n", roleName)
		updateInput := &iam.UpdateAssumeRolePolicyInput{
			RoleName:       aws.String(roleName),
			PolicyDocument: aws.String(trustPolicy),
		}

		_, err = e.iamClient.UpdateAssumeRolePolicy(ctx, updateInput)
		if err != nil {
			return "", fmt.Errorf("failed to update role trust policy: %w", err)
		}
	} else {
		// Create new role
		fmt.Printf("Creating new IAM role: %s\n", roleName)
		createInput := &iam.CreateRoleInput{
			RoleName:                 aws.String(roleName),
			AssumeRolePolicyDocument: aws.String(trustPolicy),
			Description:              aws.String("IAM role for EBS CSI driver"),
		}

		createOutput, err := e.iamClient.CreateRole(ctx, createInput)
		if err != nil {
			return "", fmt.Errorf("failed to create role: %w", err)
		}
		roleARN = aws.ToString(createOutput.Role.Arn)
	}

	return roleARN, nil
}

// AttachEBSCSIPolicy attaches the AWS managed EBS CSI driver policy to the role
func (e *EBSCSIManager) AttachEBSCSIPolicy(ctx context.Context, roleName string) error {
	policyARN := "arn:aws:iam::aws:policy/service-role/AmazonEBSCSIDriverPolicy"

	input := &iam.AttachRolePolicyInput{
		RoleName:  aws.String(roleName),
		PolicyArn: aws.String(policyARN),
	}

	_, err := e.iamClient.AttachRolePolicy(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to attach EBS CSI policy: %w", err)
	}

	fmt.Printf("Successfully attached policy: %s\n", policyARN)
	return nil
}

// InstallEBSCSIAddon installs or updates the EBS CSI driver addon using eksctl
func (e *EBSCSIManager) InstallEBSCSIAddon(ctx context.Context, clusterName, roleARN, version string) error {
	fmt.Println("\n=== Installing EBS CSI Driver Addon ===")

	// Check if addon exists and its status
	fmt.Println("Checking existing addon status...")
	describeInput := &eks.DescribeAddonInput{
		ClusterName: aws.String(clusterName),
		AddonName:   aws.String("aws-ebs-csi-driver"),
	}

	addonOutput, err := e.eksClient.DescribeAddon(ctx, describeInput)
	if err == nil && addonOutput.Addon != nil {
		status := string(addonOutput.Addon.Status)
		fmt.Printf("Found existing addon with status: %s\n", status)

		// If addon is in CREATE_FAILED or DEGRADED state, delete it first
		if status == "CREATE_FAILED" || status == "DEGRADED" {
			fmt.Printf("Deleting failed addon (status: %s)...\n", status)
			deleteArgs := []string{
				"delete", "addon",
				"--cluster", clusterName,
				"--name", "aws-ebs-csi-driver",
				"--preserve",
			}

			deleteCmd := exec.CommandContext(ctx, "eksctl", deleteArgs...)
			deleteCmd.Env = append(os.Environ(), "KUBECONFIG="+e.kubeconfigPath)
			deleteCmd.Stdout = os.Stdout
			deleteCmd.Stderr = os.Stderr

			if err := deleteCmd.Run(); err != nil {
				fmt.Printf("Warning: Failed to delete addon (continuing anyway): %v\n", err)
			} else {
				fmt.Println("✓ Deleted failed addon")
			}
		}
	}

	args := []string{
		"create", "addon",
		"--name", "aws-ebs-csi-driver",
		"--cluster", clusterName,
		"--version", version,
		"--service-account-role-arn", roleARN,
		"--force",
	}

	cmd := exec.CommandContext(ctx, "eksctl", args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+e.kubeconfigPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Printf("Running: eksctl %s\n", strings.Join(args, " "))

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to install EBS CSI driver addon: %w", err)
	}

	fmt.Println("✓ EBS CSI driver addon creation initiated")

	// Wait for addon to become active
	fmt.Println("\nWaiting for addon to become active (20 seconds)...")
	time.Sleep(20 * time.Second)

	// Verify installation
	fmt.Println("\nVerifying EBS CSI driver installation...")
	verifyCmd := exec.CommandContext(ctx, "kubectl", "get", "pods",
		"-n", "kube-system",
		"-l", "app.kubernetes.io/name=aws-ebs-csi-driver")
	verifyCmd.Env = append(os.Environ(), "KUBECONFIG="+e.kubeconfigPath)
	verifyCmd.Stdout = os.Stdout
	verifyCmd.Stderr = os.Stderr
	verifyCmd.Run()

	fmt.Println("✓ EBS CSI driver addon is now active")
	return nil
}

// ValidateEBSCSISetup validates that EBS CSI driver is properly configured
func (e *EBSCSIManager) ValidateEBSCSISetup(ctx context.Context, roleName string) error {
	// Check if role exists
	getRoleInput := &iam.GetRoleInput{
		RoleName: aws.String(roleName),
	}

	_, err := e.iamClient.GetRole(ctx, getRoleInput)
	if err != nil {
		return fmt.Errorf("EBS CSI role not found: %w", err)
	}

	// Check if policy is attached
	listPoliciesInput := &iam.ListAttachedRolePoliciesInput{
		RoleName: aws.String(roleName),
	}

	policies, err := e.iamClient.ListAttachedRolePolicies(ctx, listPoliciesInput)
	if err != nil {
		return fmt.Errorf("failed to list attached policies: %w", err)
	}

	policyAttached := false
	for _, policy := range policies.AttachedPolicies {
		if strings.Contains(aws.ToString(policy.PolicyArn), "AmazonEBSCSIDriverPolicy") {
			policyAttached = true
			break
		}
	}

	if !policyAttached {
		return fmt.Errorf("AmazonEBSCSIDriverPolicy not attached to role")
	}

	fmt.Println("✓ EBS CSI IAM role setup validated successfully")
	return nil
}

// UninstallEBSCSI uninstalls the EBS CSI driver addon
func (e *EBSCSIManager) UninstallEBSCSI(ctx context.Context, clusterName, region string) error {
	fmt.Printf("\n=== Uninstalling EBS CSI Driver from cluster %s ===\n", clusterName)

	// Use eksctl to delete the addon
	cmd := exec.CommandContext(ctx, "eksctl", "delete", "addon",
		"--name", "aws-ebs-csi-driver",
		"--cluster", clusterName,
		"--region", region,
	)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+e.kubeconfigPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to uninstall EBS CSI driver: %w", err)
	}

	fmt.Println("✓ EBS CSI driver uninstalled successfully")
	return nil
}
