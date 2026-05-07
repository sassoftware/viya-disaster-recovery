// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// IAMManager handles IAM operations
type IAMManager struct {
	client    *iam.Client
	stsClient *sts.Client
	cfg       aws.Config
}

// NewIAMManager creates a new IAM manager
func NewIAMManager(ctx context.Context, region string) (*IAMManager, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &IAMManager{
		client:    iam.NewFromConfig(cfg),
		stsClient: sts.NewFromConfig(cfg),
		cfg:       cfg,
	}, nil
}

// CreateEBSCSITrustPolicy creates trust policy for EBS CSI driver
func (m *IAMManager) CreateEBSCSITrustPolicy(accountID, oidcID, region, namespace, serviceAccount string) (string, error) {
	trustPolicy := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{
				"Effect": "Allow",
				"Principal": map[string]interface{}{
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
				"Principal": map[string]interface{}{
					"Service": "pods.eks.amazonaws.com",
				},
				"Action": "sts:AssumeRole",
			},
		},
	}

	policyDoc, err := json.MarshalIndent(trustPolicy, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal trust policy: %w", err)
	}

	return string(policyDoc), nil
}

// CreateVeleroTrustPolicy creates trust policy for Velero
func (m *IAMManager) CreateVeleroTrustPolicy(accountID, oidcID, region, namespace, serviceAccount string) (string, error) {
	trustPolicy := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{
				"Effect": "Allow",
				"Principal": map[string]interface{}{
					"Federated": fmt.Sprintf("arn:aws:iam::%s:oidc-provider/oidc.eks.%s.amazonaws.com/id/%s", accountID, region, oidcID),
				},
				"Action": "sts:AssumeRoleWithWebIdentity",
				"Condition": map[string]interface{}{
					"StringEquals": map[string]string{
						fmt.Sprintf("oidc.eks.%s.amazonaws.com/id/%s:sub", region, oidcID): fmt.Sprintf("system:serviceaccount:%s:%s", namespace, serviceAccount),
						fmt.Sprintf("oidc.eks.%s.amazonaws.com/id/%s:aud", region, oidcID): "sts.amazonaws.com",
					},
				},
			},
		},
	}

	policyDoc, err := json.MarshalIndent(trustPolicy, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal trust policy: %w", err)
	}

	return string(policyDoc), nil
}

// CreateRole creates an IAM role with trust policy
func (m *IAMManager) CreateRole(ctx context.Context, roleName, trustPolicy, description string) (string, error) {
	// Check if role already exists
	exists, roleArn, err := m.RoleExists(ctx, roleName)
	if err != nil {
		return "", err
	}
	if exists {
		fmt.Printf("IAM role '%s' already exists: %s\n", roleName, roleArn)
		return roleArn, nil
	}

	// Create role
	output, err := m.client.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(trustPolicy),
		Description:              aws.String(description),
	})
	if err != nil {
		return "", fmt.Errorf("failed to create IAM role: %w", err)
	}

	fmt.Printf("Created IAM role: %s\n", *output.Role.Arn)
	return *output.Role.Arn, nil
}

// AttachPolicy attaches a managed policy to a role
func (m *IAMManager) AttachPolicy(ctx context.Context, roleName, policyArn string) error {
	_, err := m.client.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
		RoleName:  aws.String(roleName),
		PolicyArn: aws.String(policyArn),
	})
	if err != nil {
		return fmt.Errorf("failed to attach policy: %w", err)
	}

	fmt.Printf("Attached policy %s to role %s\n", policyArn, roleName)
	return nil
}

// CreatePolicy creates an IAM policy
func (m *IAMManager) CreatePolicy(ctx context.Context, policyName, policyDocument, description string) (string, error) {
	// Check if policy already exists
	exists, policyArn, err := m.PolicyExists(ctx, policyName)
	if err != nil {
		return "", err
	}
	if exists {
		fmt.Printf("IAM policy '%s' already exists: %s\n", policyName, policyArn)
		return policyArn, nil
	}

	output, err := m.client.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String(policyName),
		PolicyDocument: aws.String(policyDocument),
		Description:    aws.String(description),
	})
	if err != nil {
		return "", fmt.Errorf("failed to create policy: %w", err)
	}

	fmt.Printf("Created IAM policy: %s\n", *output.Policy.Arn)
	return *output.Policy.Arn, nil
}

// RoleExists checks if a role exists and returns its ARN
func (m *IAMManager) RoleExists(ctx context.Context, roleName string) (bool, string, error) {
	output, err := m.client.GetRole(ctx, &iam.GetRoleInput{
		RoleName: aws.String(roleName),
	})
	if err != nil {
		var nse *types.NoSuchEntityException
		if errors.As(err, &nse) {
			return false, "", nil
		}
		return false, "", fmt.Errorf("failed to check if role exists: %w", err)
	}

	return true, *output.Role.Arn, nil
}

// PolicyExists checks if a policy exists and returns its ARN
func (m *IAMManager) PolicyExists(ctx context.Context, policyName string) (bool, string, error) {
	// Get account ID
	accountID, err := m.GetAccountID(ctx)
	if err != nil {
		return false, "", err
	}

	policyArn := fmt.Sprintf("arn:aws:iam::%s:policy/%s", accountID, policyName)

	output, err := m.client.GetPolicy(ctx, &iam.GetPolicyInput{
		PolicyArn: aws.String(policyArn),
	})
	if err != nil {
		var nse *types.NoSuchEntityException
		if errors.As(err, &nse) {
			return false, "", nil
		}
		return false, "", fmt.Errorf("failed to check if policy exists: %w", err)
	}

	return true, *output.Policy.Arn, nil
}

// GetAccountID retrieves the AWS account ID
func (m *IAMManager) GetAccountID(ctx context.Context) (string, error) {
	output, err := m.stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("failed to get caller identity: %w", err)
	}

	return *output.Account, nil
}

// DeleteRole deletes an IAM role
func (m *IAMManager) DeleteRole(ctx context.Context, roleName string) error {
	// List attached policies — treat 404 (role not found) as already deleted
	policies, err := m.client.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{
		RoleName: aws.String(roleName),
	})
	if err != nil {
		var nse *types.NoSuchEntityException
		if errors.As(err, &nse) {
			fmt.Printf("      Role %s not found, skipping\n", roleName)
			return nil
		}
		return fmt.Errorf("failed to list attached policies: %w", err)
	}

	// Detach all attached managed policies
	for _, policy := range policies.AttachedPolicies {
		fmt.Printf("      Detaching policy: %s\n", *policy.PolicyArn)
		_, err := m.client.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{
			RoleName:  aws.String(roleName),
			PolicyArn: policy.PolicyArn,
		})
		if err != nil {
			var nse *types.NoSuchEntityException
			if errors.As(err, &nse) {
				fmt.Printf("      Policy %s already detached, skipping\n", *policy.PolicyArn)
				continue
			}
			return fmt.Errorf("failed to detach policy: %w", err)
		}
	}

	// Delete role — treat 404 as already deleted
	_, err = m.client.DeleteRole(ctx, &iam.DeleteRoleInput{
		RoleName: aws.String(roleName),
	})
	if err != nil {
		var nse *types.NoSuchEntityException
		if errors.As(err, &nse) {
			fmt.Printf("      Role %s already deleted, skipping\n", roleName)
			return nil
		}
		return fmt.Errorf("failed to delete role: %w", err)
	}

	return nil
}

// DeletePolicy deletes an IAM policy
func (m *IAMManager) DeletePolicy(ctx context.Context, policyARN string) error {
	// Check if policy exists
	_, err := m.client.GetPolicy(ctx, &iam.GetPolicyInput{
		PolicyArn: aws.String(policyARN),
	})
	if err != nil {
		// Policy doesn't exist, skip
		return nil
	}

	// Delete all policy versions except the default
	versionsOutput, err := m.client.ListPolicyVersions(ctx, &iam.ListPolicyVersionsInput{
		PolicyArn: aws.String(policyARN),
	})
	if err != nil {
		return fmt.Errorf("failed to list policy versions: %w", err)
	}

	nonDefaultVersions := 0
	for _, version := range versionsOutput.Versions {
		if !version.IsDefaultVersion {
			nonDefaultVersions++
			_, err := m.client.DeletePolicyVersion(ctx, &iam.DeletePolicyVersionInput{
				PolicyArn: aws.String(policyARN),
				VersionId: version.VersionId,
			})
			if err != nil {
				return fmt.Errorf("failed to delete policy version: %w", err)
			}
		}
	}
	if nonDefaultVersions > 0 {
		fmt.Printf("      Deleted %d non-default policy version(s)\n", nonDefaultVersions)
	}

	// Delete the policy
	_, err = m.client.DeletePolicy(ctx, &iam.DeletePolicyInput{
		PolicyArn: aws.String(policyARN),
	})
	if err != nil {
		return fmt.Errorf("failed to delete policy: %w", err)
	}

	return nil
}

// CreateIAMUser creates an IAM user
func (m *IAMManager) CreateIAMUser(ctx context.Context, userName string) error {
	// Check if user exists
	_, err := m.client.GetUser(ctx, &iam.GetUserInput{
		UserName: aws.String(userName),
	})
	if err == nil {
		fmt.Printf("IAM user '%s' already exists\n", userName)
		return nil
	}

	// Create user
	_, err = m.client.CreateUser(ctx, &iam.CreateUserInput{
		UserName: aws.String(userName),
	})
	if err != nil {
		return fmt.Errorf("failed to create IAM user: %w", err)
	}

	fmt.Printf("Created IAM user: %s\n", userName)
	return nil
}

// CreateAccessKey creates an access key for an IAM user
func (m *IAMManager) CreateAccessKey(ctx context.Context, userName string) (string, string, error) {
	output, err := m.client.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{
		UserName: aws.String(userName),
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to create access key: %w", err)
	}

	return *output.AccessKey.AccessKeyId, *output.AccessKey.SecretAccessKey, nil
}

// PutUserPolicy adds an inline policy to an IAM user
func (m *IAMManager) PutUserPolicy(ctx context.Context, userName, policyName, policyDocument string) error {
	_, err := m.client.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:       aws.String(userName),
		PolicyName:     aws.String(policyName),
		PolicyDocument: aws.String(policyDocument),
	})
	if err != nil {
		return fmt.Errorf("failed to put user policy: %w", err)
	}

	fmt.Printf("Added inline policy '%s' to user %s\n", policyName, userName)
	return nil
}

// DeleteIAMUser deletes an IAM user and all associated resources
func (m *IAMManager) DeleteIAMUser(ctx context.Context, userName string) error {
	// List and delete access keys
	keysOutput, err := m.client.ListAccessKeys(ctx, &iam.ListAccessKeysInput{
		UserName: aws.String(userName),
	})
	if err == nil {
		for _, key := range keysOutput.AccessKeyMetadata {
			_, err := m.client.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{
				UserName:    aws.String(userName),
				AccessKeyId: key.AccessKeyId,
			})
			if err != nil {
				fmt.Printf("Warning: Failed to delete access key: %v\n", err)
			}
		}
	}

	// List and delete inline policies
	policiesOutput, err := m.client.ListUserPolicies(ctx, &iam.ListUserPoliciesInput{
		UserName: aws.String(userName),
	})
	if err == nil {
		for _, policyName := range policiesOutput.PolicyNames {
			_, err := m.client.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{
				UserName:   aws.String(userName),
				PolicyName: aws.String(policyName),
			})
			if err != nil {
				fmt.Printf("Warning: Failed to delete user policy: %v\n", err)
			}
		}
	}

	// Delete user
	_, err = m.client.DeleteUser(ctx, &iam.DeleteUserInput{
		UserName: aws.String(userName),
	})
	if err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}

	fmt.Printf("Deleted IAM user: %s\n", userName)
	return nil
}
