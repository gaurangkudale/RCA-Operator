/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package telemetry

// min returns the minimum of two integers
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// contains checks if s contains any of the substrings
func containsSubstring(s string, subs ...string) bool {
	for _, sub := range subs {
		if substringIndexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

// substringIndexOf returns the index of substring in s, or -1 if not found
func substringIndexOf(s, substring string) int {
	for i := 0; i <= len(s)-len(substring); i++ {
		match := true
		for j := 0; j < len(substring); j++ {
			if s[i+j] != substring[j] {
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

// truncateResponse truncates a response body for logging (max 300 chars)
func truncateResponseBody(body []byte) string {
	s := string(body)
	maxLen := 300
	if len(s) > maxLen {
		return s[:maxLen] + "... (truncated)"
	}
	return s
}
