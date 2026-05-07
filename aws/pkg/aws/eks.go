// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
)

// EKSManager handles EKS operations
type EKSManager struct {
	client *eks.Client
}

// NewEKSManager creates a new EKS manager
func NewEKSManager(ctx context.Context, region string) (*EKSManager, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &EKSManager{
		client: eks.NewFromConfig(cfg),
	}, nil
}

// GetOIDCProvider retrieves the OIDC provider ID for an EKS cluster
func (m *EKSManager) GetOIDCProvider(ctx context.Context, clusterName string) (string, error) {
	output, err := m.client.DescribeCluster(ctx, &eks.DescribeClusterInput{
		Name: &clusterName,
	})
	if err != nil {
		return "", fmt.Errorf("failed to describe cluster: %w", err)
	}

	if output.Cluster.Identity == nil || output.Cluster.Identity.Oidc == nil {
		return "", fmt.Errorf("cluster does not have OIDC provider configured")
	}

	issuer := *output.Cluster.Identity.Oidc.Issuer
	// Extract ID from URL: https://oidc.eks.region.amazonaws.com/id/XXXXXX
	parts := strings.Split(issuer, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid OIDC issuer URL: %s", issuer)
	}

	oidcID := parts[len(parts)-1]
	fmt.Printf("Retrieved OIDC ID for cluster '%s': %s\n", clusterName, oidcID)
	return oidcID, nil
}

// GetClusterInfo retrieves cluster information
func (m *EKSManager) GetClusterInfo(ctx context.Context, clusterName string) (*ClusterInfo, error) {
	output, err := m.client.DescribeCluster(ctx, &eks.DescribeClusterInput{
		Name: &clusterName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe cluster: %w", err)
	}

	cluster := output.Cluster
	info := &ClusterInfo{
		Name:    *cluster.Name,
		Version: *cluster.Version,
		Status:  string(cluster.Status),
		Region:  getRegionFromARN(*cluster.Arn),
	}

	if cluster.Identity != nil && cluster.Identity.Oidc != nil {
		info.OIDCIssuer = *cluster.Identity.Oidc.Issuer
	}

	if cluster.ResourcesVpcConfig != nil {
		info.VpcID = *cluster.ResourcesVpcConfig.VpcId
	}

	return info, nil
}

// ClusterExists checks if a cluster exists
func (m *EKSManager) ClusterExists(ctx context.Context, clusterName string) (bool, error) {
	_, err := m.client.DescribeCluster(ctx, &eks.DescribeClusterInput{
		Name: &clusterName,
	})
	if err != nil {
		if strings.Contains(err.Error(), "ResourceNotFoundException") {
			return false, nil
		}
		return false, fmt.Errorf("failed to check cluster existence: %w", err)
	}

	return true, nil
}

// ClusterInfo contains information about an EKS cluster
type ClusterInfo struct {
	Name       string
	Version    string
	Status     string
	Region     string
	OIDCIssuer string
	VpcID      string
}

// Helper function to extract region from ARN
func getRegionFromARN(arn string) string {
	// ARN format: arn:aws:eks:region:account-id:cluster/cluster-name
	parts := strings.Split(arn, ":")
	if len(parts) >= 4 {
		return parts[3]
	}
	return ""
}
