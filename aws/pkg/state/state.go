// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package state

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// StateStore manages automation state
type StateStore struct {
	filePath string
}

// State represents the current state of the automation
type State struct {
	LastUpdated   time.Time         `json:"last_updated"`
	ClusterType   string            `json:"cluster_type"`
	ClusterName   string            `json:"cluster_name"`
	Operations    []Operation       `json:"operations"`
	Resources     map[string]string `json:"resources"`
	Configuration map[string]string `json:"configuration"`
}

// Operation represents a completed operation
type Operation struct {
	Type        string    `json:"type"`
	Status      string    `json:"status"`
	Timestamp   time.Time `json:"timestamp"`
	Description string    `json:"description"`
	Error       string    `json:"error,omitempty"`
}

// NewStateStore creates a new state store
func NewStateStore(filePath string) *StateStore {
	return &StateStore{
		filePath: filePath,
	}
}

// Load loads state from file
func (s *StateStore) Load() (*State, error) {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Return new state if file doesn't exist
			return s.newState(), nil
		}
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal state: %w", err)
	}

	return &state, nil
}

// Save saves state to file
func (s *StateStore) Save(state *State) error {
	state.LastUpdated = time.Now()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	if err := os.WriteFile(s.filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	return nil
}

// AddOperation adds an operation to the state
func (s *StateStore) AddOperation(state *State, opType, status, description string, err error) {
	op := Operation{
		Type:        opType,
		Status:      status,
		Timestamp:   time.Now(),
		Description: description,
	}

	if err != nil {
		op.Error = err.Error()
	}

	state.Operations = append(state.Operations, op)
}

// SetResource sets a resource in the state
func (s *StateStore) SetResource(state *State, key, value string) {
	if state.Resources == nil {
		state.Resources = make(map[string]string)
	}
	state.Resources[key] = value
}

// GetResource gets a resource from the state
func (s *StateStore) GetResource(state *State, key string) (string, bool) {
	if state.Resources == nil {
		return "", false
	}
	value, exists := state.Resources[key]
	return value, exists
}

// newState creates a new empty state
func (s *StateStore) newState() *State {
	return &State{
		LastUpdated:   time.Now(),
		Operations:    []Operation{},
		Resources:     make(map[string]string),
		Configuration: make(map[string]string),
	}
}

// Clear clears the state file
func (s *StateStore) Clear() error {
	if err := os.Remove(s.filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to clear state file: %w", err)
	}
	return nil
}
