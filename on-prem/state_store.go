package main

import (
	"encoding/json"
	"os"
	"time"
)

type State struct {
	LastUpdated string            `json:"lastUpdated"`
	Steps       map[string]string `json:"steps"`
	BackupName  string            `json:"backupName,omitempty"`
	RestoreName string            `json:"restoreName,omitempty"`
}

func LoadState() *State {
	b, err := os.ReadFile("state.json")
	if err != nil {
		return &State{Steps: map[string]string{}}
	}
	var s State
	if json.Unmarshal(b, &s) != nil || s.Steps == nil {
		return &State{Steps: map[string]string{}}
	}
	return &s
}

func (s *State) Mark(step, status string) error {
	s.LastUpdated = time.Now().Format(time.RFC3339)
	s.Steps[step] = status
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile("state.json", b, 0600)
}
