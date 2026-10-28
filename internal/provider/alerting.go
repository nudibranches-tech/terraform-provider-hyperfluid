// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/diag"
)

var featureAlerting = orgFeature{key: "alerting_enabled", display: "alerting"}

const alertingFeatureNote = "Needs the organization's `alerting_enabled` feature."

func addAlertingError(diags *diag.Diagnostics, summary string, err error) {
	addGatedError(diags, summary, featureAlerting, err)
}
