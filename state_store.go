// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type State struct {
	Steps map[string]string `json:"steps"`
}

const stateFile = "state.json"

func LoadState() State {
	state := State{Steps: make(map[string]string)}
	file, err := os.ReadFile(stateFile)
	if err != nil {
		return state
	}
	json.Unmarshal(file, &state)
	return state
}

func SaveState(state State) {
	data, _ := json.MarshalIndent(state, "", "  ")
	os.WriteFile(stateFile, data, 0644)
}

func UpdateStepState(state *State, step, status string) {
	state.Steps[step] = status
	SaveState(*state)
	fmt.Printf("[%s] %s\n", status, step)
}
