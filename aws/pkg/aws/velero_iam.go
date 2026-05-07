// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

// VeleroIAMManager handles Velero IAM user and policy setup
type VeleroIAMManager struct {
	iamClient *iam.Client
}

// NewVeleroIAMManager creates a new Velero IAM manager
func NewVeleroIAMManager(cfg aws.Config) *VeleroIAMManager {
	return &VeleroIAMManager{
		iamClient: iam.NewFromConfig(cfg),
	}
}

// CreateVeleroPolicy creates the IAM policy JSON for Velero
func (v *VeleroIAMManager) CreateVeleroPolicy(bucketName string) (string, error) {
	policy := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{
				"Effect": "Allow",
				"Action": []string{
					"ec2:DescribeVolumes",
					"ec2:DescribeSnapshots",
					"ec2:CreateTags",
					"ec2:CreateVolume",
					"ec2:CreateSnapshot",
					"ec2:DeleteSnapshot",
				},
				"Resource": "*",
			},
			{
				"Effect": "Allow",
				"Action": []string{
					"s3:GetObject",
					"s3:DeleteObject",
					"s3:PutObject",
					"s3:PutObjectTagging",
					"s3:AbortMultipartUpload",
					"s3:ListMultipartUploadParts",
				},
				"Resource": []string{
					fmt.Sprintf("arn:aws:s3:::%s/*", bucketName),
				},
			},
			{
				"Effect": "Allow",
				"Action": []string{
					"s3:ListBucket",
				},
				"Resource": []string{
					fmt.Sprintf("arn:aws:s3:::%s", bucketName),
				},
			},
		},
	}

	policyJSON, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal Velero policy: %w", err)
	}

	return string(policyJSON), nil
}

// CreateVeleroUser creates an IAM user for Velero
func (v *VeleroIAMManager) CreateVeleroUser(ctx context.Context, userName string) error {
	// Check if user exists
	getUserInput := &iam.GetUserInput{
		UserName: aws.String(userName),
	}

	_, err := v.iamClient.GetUser(ctx, getUserInput)
	if err == nil {
		fmt.Printf("IAM user %s already exists\n", userName)
		return nil
	}

	// Create user
	fmt.Printf("Creating IAM user: %s\n", userName)
	createInput := &iam.CreateUserInput{
		UserName: aws.String(userName),
	}

	_, err = v.iamClient.CreateUser(ctx, createInput)
	if err != nil {
		return fmt.Errorf("failed to create IAM user: %w", err)
	}

	fmt.Printf("✓ Successfully created IAM user: %s\n", userName)
	return nil
}

// AttachVeleroPolicy attaches inline policy to Velero IAM user
func (v *VeleroIAMManager) AttachVeleroPolicy(ctx context.Context, userName, bucketName string) error {
	policyDocument, err := v.CreateVeleroPolicy(bucketName)
	if err != nil {
		return err
	}

	input := &iam.PutUserPolicyInput{
		UserName:       aws.String(userName),
		PolicyName:     aws.String("velero-primary-policy"),
		PolicyDocument: aws.String(policyDocument),
	}

	_, err = v.iamClient.PutUserPolicy(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to attach Velero policy: %w", err)
	}

	fmt.Println("✓ Successfully attached Velero policy to user")
	return nil
}

// CreateAccessKey creates access keys for Velero IAM user
func (v *VeleroIAMManager) CreateAccessKey(ctx context.Context, userName string) (*AccessKeyPair, error) {
	// List existing access keys
	listInput := &iam.ListAccessKeysInput{
		UserName: aws.String(userName),
	}

	listOutput, err := v.iamClient.ListAccessKeys(ctx, listInput)
	if err != nil {
		return nil, fmt.Errorf("failed to list access keys: %w", err)
	}

	// If user already has 2 keys (AWS limit), we need to delete one first
	if len(listOutput.AccessKeyMetadata) >= 2 {
		fmt.Printf("Warning: User %s already has %d access keys. Consider deleting old keys.\n", userName, len(listOutput.AccessKeyMetadata))
		return nil, fmt.Errorf("user already has maximum number of access keys (2)")
	}

	// Create new access key
	createInput := &iam.CreateAccessKeyInput{
		UserName: aws.String(userName),
	}

	createOutput, err := v.iamClient.CreateAccessKey(ctx, createInput)
	if err != nil {
		return nil, fmt.Errorf("failed to create access key: %w", err)
	}

	accessKey := &AccessKeyPair{
		AccessKeyID:     aws.ToString(createOutput.AccessKey.AccessKeyId),
		SecretAccessKey: aws.ToString(createOutput.AccessKey.SecretAccessKey),
	}

	fmt.Println("✓ Successfully created access key for Velero")
	return accessKey, nil
}

// DeleteAccessKey deletes an access key for a user
func (v *VeleroIAMManager) DeleteAccessKey(ctx context.Context, userName, accessKeyID string) error {
	input := &iam.DeleteAccessKeyInput{
		UserName:    aws.String(userName),
		AccessKeyId: aws.String(accessKeyID),
	}

	_, err := v.iamClient.DeleteAccessKey(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to delete access key: %w", err)
	}

	fmt.Printf("✓ Successfully deleted access key: %s\n", accessKeyID)
	return nil
}

// AccessKeyPair holds AWS access key credentials
type AccessKeyPair struct {
	AccessKeyID     string
	SecretAccessKey string
}

// SaveToFile saves the access key credentials to a file in AWS credentials format
func (a *AccessKeyPair) SaveToFile(filepath string) error {
	content := fmt.Sprintf("[default]\naws_access_key_id=%s\naws_secret_access_key=%s\n", a.AccessKeyID, a.SecretAccessKey)

	// Write to file
	if err := os.WriteFile(filepath, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write credentials file: %w", err)
	}

	fmt.Printf("\n✓ Velero credentials saved to: %s\n", filepath)
	fmt.Println("IMPORTANT: Keep this file secure. It contains AWS access keys.")

	return nil
}
