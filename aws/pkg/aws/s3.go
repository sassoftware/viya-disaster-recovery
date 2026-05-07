// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Manager handles S3 operations
type S3Manager struct {
	client *s3.Client
	region string
}

// NewS3Manager creates a new S3 manager
func NewS3Manager(ctx context.Context, region string) (*S3Manager, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &S3Manager{
		client: s3.NewFromConfig(cfg),
		region: region,
	}, nil
}

// CreateBucket creates an S3 bucket for Velero backups
func (m *S3Manager) CreateBucket(ctx context.Context, bucketName, region string) error {
	// Ensure we're using the correct region-specific client
	if region != m.region {
		cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
		if err != nil {
			return fmt.Errorf("failed to load AWS config for region %s: %w", region, err)
		}
		m.client = s3.NewFromConfig(cfg)
		m.region = region
	}

	// Check if bucket exists
	exists, err := m.BucketExists(ctx, bucketName, region)
	if err != nil {
		return err
	}
	if exists {
		fmt.Printf("S3 bucket '%s' already exists\n", bucketName)
		return nil
	}

	// Create bucket configuration
	input := &s3.CreateBucketInput{
		Bucket: aws.String(bucketName),
	}

	// For regions other than us-east-1, we need to specify LocationConstraint
	if region != "us-east-1" {
		input.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(region),
		}
	}

	_, err = m.client.CreateBucket(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to create S3 bucket: %w", err)
	}

	fmt.Printf("Created S3 bucket: %s in region %s\n", bucketName, region)

	// Enable versioning
	if err := m.EnableVersioning(ctx, bucketName); err != nil {
		return err
	}

	// Enable encryption
	if err := m.EnableEncryption(ctx, bucketName); err != nil {
		return err
	}

	return nil
}

// BucketExists checks if a bucket exists in the specified region
func (m *S3Manager) BucketExists(ctx context.Context, bucketName, region string) (bool, error) {
	// Ensure we're using the correct region-specific client
	if region != m.region {
		cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
		if err != nil {
			return false, fmt.Errorf("failed to load AWS config for region %s: %w", region, err)
		}
		m.client = s3.NewFromConfig(cfg)
		m.region = region
	}

	_, err := m.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucketName),
	})
	if err != nil {
		// Check if error is NotFound (404) or MovedPermanently (301)
		// 301 means bucket exists in a different region or is owned by another account
		if isNotFoundError(err) || isMovedPermanentlyError(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check bucket existence: %w", err)
	}

	return true, nil
}

// EnableVersioning enables versioning on a bucket
func (m *S3Manager) EnableVersioning(ctx context.Context, bucketName string) error {
	_, err := m.client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket: aws.String(bucketName),
		VersioningConfiguration: &types.VersioningConfiguration{
			Status: types.BucketVersioningStatusEnabled,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to enable versioning: %w", err)
	}

	fmt.Printf("Enabled versioning on bucket: %s\n", bucketName)
	return nil
}

// EnableEncryption enables default encryption on a bucket
func (m *S3Manager) EnableEncryption(ctx context.Context, bucketName string) error {
	_, err := m.client.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
		Bucket: aws.String(bucketName),
		ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{
			Rules: []types.ServerSideEncryptionRule{
				{
					ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{
						SSEAlgorithm: types.ServerSideEncryptionAes256,
					},
					BucketKeyEnabled: aws.Bool(true),
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to enable encryption: %w", err)
	}

	fmt.Printf("Enabled encryption on bucket: %s\n", bucketName)
	return nil
}

// DeleteBucket deletes an S3 bucket (must be empty)
func (m *S3Manager) DeleteBucket(ctx context.Context, bucketName string) error {
	_, err := m.client.DeleteBucket(ctx, &s3.DeleteBucketInput{
		Bucket: aws.String(bucketName),
	})
	if err != nil {
		return fmt.Errorf("failed to delete bucket: %w", err)
	}

	fmt.Printf("Deleted S3 bucket: %s\n", bucketName)
	return nil
}

// EmptyBucket deletes all objects in a bucket
func (m *S3Manager) EmptyBucket(ctx context.Context, bucketName string) error {
	fmt.Printf("      Listing objects in bucket %s...\n", bucketName)
	// List all objects
	paginator := s3.NewListObjectVersionsPaginator(m.client, &s3.ListObjectVersionsInput{
		Bucket: aws.String(bucketName),
	})

	totalVersions := 0
	totalMarkers := 0
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("failed to list objects: %w", err)
		}

		// Delete object versions
		if len(page.Versions) > 0 {
			totalVersions += len(page.Versions)
			objects := make([]types.ObjectIdentifier, len(page.Versions))
			for i, v := range page.Versions {
				objects[i] = types.ObjectIdentifier{
					Key:       v.Key,
					VersionId: v.VersionId,
				}
			}

			_, err = m.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(bucketName),
				Delete: &types.Delete{
					Objects: objects,
					Quiet:   aws.Bool(true),
				},
			})
			if err != nil {
				return fmt.Errorf("failed to delete objects: %w", err)
			}
			fmt.Printf("      Deleted %d object version(s)\n", len(page.Versions))
		}

		// Delete delete markers
		if len(page.DeleteMarkers) > 0 {
			totalMarkers += len(page.DeleteMarkers)
			objects := make([]types.ObjectIdentifier, len(page.DeleteMarkers))
			for i, m := range page.DeleteMarkers {
				objects[i] = types.ObjectIdentifier{
					Key:       m.Key,
					VersionId: m.VersionId,
				}
			}

			_, err = m.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(bucketName),
				Delete: &types.Delete{
					Objects: objects,
					Quiet:   aws.Bool(true),
				},
			})
			if err != nil {
				return fmt.Errorf("failed to delete markers: %w", err)
			}
			fmt.Printf("      Deleted %d delete marker(s)\n", len(page.DeleteMarkers))
		}
	}

	if totalVersions > 0 || totalMarkers > 0 {
		fmt.Printf("      Total deleted: %d versions, %d markers\n", totalVersions, totalMarkers)
	} else {
		fmt.Printf("      Bucket was already empty\n")
	}
	return nil
}

// Helper function to check if error is NotFound
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	// Check for 404 NotFound error
	errStr := err.Error()
	return contains(errStr, "NotFound") || contains(errStr, "404")
}

// Helper function to check if error is MovedPermanently (301)
func isMovedPermanentlyError(err error) bool {
	if err == nil {
		return false
	}
	// Check for 301 MovedPermanently error
	// This occurs when bucket exists in a different region or is owned by another account
	errStr := err.Error()
	return contains(errStr, "MovedPermanently") || contains(errStr, "301")
}

// Helper function for case-insensitive string contains
func contains(str, substr string) bool {
	return len(str) >= len(substr) && (str == substr ||
		(len(str) > len(substr) &&
			(startsWithIgnoreCase(str, substr) ||
				endsWithIgnoreCase(str, substr) ||
				indexIgnoreCase(str, substr) >= 0)))
}

func startsWithIgnoreCase(str, prefix string) bool {
	if len(str) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if toLower(str[i]) != toLower(prefix[i]) {
			return false
		}
	}
	return true
}

func endsWithIgnoreCase(str, suffix string) bool {
	if len(str) < len(suffix) {
		return false
	}
	offset := len(str) - len(suffix)
	for i := 0; i < len(suffix); i++ {
		if toLower(str[offset+i]) != toLower(suffix[i]) {
			return false
		}
	}
	return true
}

func indexIgnoreCase(str, substr string) int {
	for i := 0; i <= len(str)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			if toLower(str[i+j]) != toLower(substr[j]) {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func toLower(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}
