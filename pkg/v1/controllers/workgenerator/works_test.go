/*
Copyright 2026 The KubeFleet Authors.

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

package workgenerator

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

// TestWorkOwnerLabelValue tests the workOwnerLabelValue function.
//
// The hashes below are the first 12 hex characters of the SHA-256 digests of the original (dotted) owner names,
// computed independently of the implementation.
func TestWorkOwnerLabelValue(t *testing.T) {
	dottedOwnerName := dottedName(100, 3)
	// The dot-free form of the name, truncated to 48 characters (61 - 12 - 1).
	wantDottedOwnerNamePrefix := strings.ReplaceAll(dottedOwnerName, ".", "")[:48]

	testCases := []struct {
		name      string
		ownerName string
		want      string
	}{
		{
			name:      "short name (unchanged)",
			ownerName: "binding-1",
			want:      "binding-1",
		},
		{
			name:      "name exactly at the length limit (unchanged)",
			ownerName: strings.Repeat("a", 61),
			want:      strings.Repeat("a", 61),
		},
		{
			name:      "name one character over the length limit (truncated, with hash)",
			ownerName: strings.Repeat("a", 62),
			want:      strings.Repeat("a", 48) + "-f506898cc7c2",
		},
		{
			name:      "short name with dots (dots removed, with hash)",
			ownerName: "my.binding",
			want:      "mybinding-d602fbfb398e",
		},
		{
			name:      "long name with dots (dots removed, truncated, with hash)",
			ownerName: dottedOwnerName,
			want:      wantDottedOwnerNamePrefix + "-f996857ab27a",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := workOwnerLabelValue(tc.ownerName)
			if got != tc.want {
				t.Errorf("workOwnerLabelValue(%q) = %q, want %q", tc.ownerName, got, tc.want)
			}
			if errs := validation.IsValidLabelValue(got); len(errs) > 0 {
				t.Errorf("workOwnerLabelValue(%q) = %q, which is not a valid label value: %v", tc.ownerName, got, errs)
			}
		})
	}
}

// TestWorkOwnerLabelValueDistinct verifies that owner names that reduce to the same prefix still map to different
// label values, as the hash is computed over the original name.
func TestWorkOwnerLabelValueDistinct(t *testing.T) {
	ownerNames := []string{
		"a.bc",
		"ab.c",
		"abc.",
		strings.Repeat("a", 62),
		strings.Repeat("a", 63),
		strings.Repeat("a", 48) + "." + strings.Repeat("a", 20),
	}

	seen := make(map[string]string, len(ownerNames))
	for _, ownerName := range ownerNames {
		got := workOwnerLabelValue(ownerName)
		if got != workOwnerLabelValue(ownerName) {
			t.Errorf("workOwnerLabelValue(%q) is not deterministic", ownerName)
		}
		if other, found := seen[got]; found {
			t.Errorf("workOwnerLabelValue(%q) = %q, which collides with the value for %q", ownerName, got, other)
		}
		seen[got] = ownerName
	}
}
