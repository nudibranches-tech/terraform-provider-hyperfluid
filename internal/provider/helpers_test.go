// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import "testing"

func TestConditionMessage(t *testing.T) {
	// Conditions as the generated client models them: an untyped JSON array of
	// objects (decoded to []interface{} of map[string]interface{}).
	conditions := []interface{}{
		map[string]interface{}{"type": "SecretApplied", "status": "True", "message": "ok"},
		map[string]interface{}{
			"type":    "S3Reachable",
			"status":  "False",
			"reason":  "BucketUnreachable",
			"message": "HeadBucket on 'my-backups' failed: access denied",
		},
	}

	tests := []struct {
		name       string
		conditions interface{}
		condType   string
		want       string
	}{
		{"found", conditions, "S3Reachable", "HeadBucket on 'my-backups' failed: access denied"},
		{"other condition", conditions, "SecretApplied", "ok"},
		{"missing type", conditions, "ObjectStoreApplied", ""},
		{"nil", nil, "S3Reachable", ""},
		{"wrong shape", "not-an-array", "S3Reachable", ""},
		{"empty array", []interface{}{}, "S3Reachable", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := conditionMessage(tt.conditions, tt.condType); got != tt.want {
				t.Errorf("conditionMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}
