// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// STSManager handles STS operations
type STSManager struct {
	client *sts.Client
}

// NewSTSManager creates a new STS manager
func NewSTSManager(ctx context.Context, region string) (*STSManager, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &STSManager{
		client: sts.NewFromConfig(cfg),
	}, nil
}

// GetAccountID retrieves the AWS account ID
func (m *STSManager) GetAccountID(ctx context.Context) (string, error) {
	output, err := m.client.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("failed to get caller identity: %w", err)
	}

	return *output.Account, nil
}

// GetCallerIdentity retrieves information about the AWS caller
func (m *STSManager) GetCallerIdentity(ctx context.Context) (*CallerIdentity, error) {
	output, err := m.client.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to get caller identity: %w", err)
	}

	return &CallerIdentity{
		Account: *output.Account,
		Arn:     *output.Arn,
		UserId:  *output.UserId,
	}, nil
}

// CallerIdentity contains AWS caller identity information
type CallerIdentity struct {
	Account string
	Arn     string
	UserId  string
}
