// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// K8sClient wraps Kubernetes client
type K8sClient struct {
	clientset *kubernetes.Clientset
	config    *rest.Config
}

// NewK8sClient creates a new Kubernetes client
func NewK8sClient(kubeconfigPath string) (*K8sClient, error) {
	// Expand path if it contains ~
	if kubeconfigPath[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		kubeconfigPath = filepath.Join(home, kubeconfigPath[1:])
	}

	// Load kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	// Create clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	return &K8sClient{
		clientset: clientset,
		config:    config,
	}, nil
}

// TestConnection tests the connection to the Kubernetes cluster
func (c *K8sClient) TestConnection(ctx context.Context) error {
	_, err := c.clientset.Discovery().ServerVersion()
	if err != nil {
		return fmt.Errorf("failed to connect to Kubernetes cluster: %w", err)
	}
	return nil
}

// GetServerVersion returns the Kubernetes server version
func (c *K8sClient) GetServerVersion(ctx context.Context) (string, error) {
	version, err := c.clientset.Discovery().ServerVersion()
	if err != nil {
		return "", fmt.Errorf("failed to get server version: %w", err)
	}
	return version.String(), nil
}

// GetClientset returns the Kubernetes clientset
func (c *K8sClient) GetClientset() *kubernetes.Clientset {
	return c.clientset
}

// GetConfig returns the Kubernetes config
func (c *K8sClient) GetConfig() *rest.Config {
	return c.config
}
