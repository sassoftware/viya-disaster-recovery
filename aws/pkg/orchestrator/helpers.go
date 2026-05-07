// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package orchestrator

// splitLines splits a string into lines (handles \r\n and \n)
func splitLines(s string) []string {
	lines := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// splitFields splits a line into fields by whitespace
func splitFields(s string) []string {
	fields := []string{}
	field := ""
	inField := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if inField {
				fields = append(fields, field)
				field = ""
				inField = false
			}
		} else {
			field += string(r)
			inField = true
		}
	}
	if inField {
		fields = append(fields, field)
	}
	return fields
}

// startsWith checks if a string starts with a prefix
func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
