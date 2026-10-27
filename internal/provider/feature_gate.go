// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

// orgFeature is a per-organization entitlement the platform gates an API on.
type orgFeature struct {
	key     string // as an administrator enables it
	display string // as the platform's 403 names it
}

var (
	featureNextflow     = orgFeature{key: "nextflow_enabled", display: "Nextflow"}
	featureComputeDocks = orgFeature{key: "compute_docks_enabled", display: "Compute Docks"}
	featureAiAgents     = orgFeature{key: "sapience_agent_enabled", display: "AI agents"}
)

// addGatedError reports a failed call, naming the feature when the platform
// refused it because the organization does not have it.
func addGatedError(diags *diag.Diagnostics, summary string, f orgFeature, err error) {
	marker := strings.ToLower("does not have access to the " + f.display + " feature")
	if errors.Is(err, client.ErrForbidden) && strings.Contains(strings.ToLower(err.Error()), marker) {
		diags.AddError(f.display+" is not enabled",
			fmt.Sprintf("This organization does not have the `%s` feature. Ask a platform administrator to enable it.\n\n%s", f.key, err))
		return
	}
	diags.AddError(summary, err.Error())
}

// orDefaultInterval lets a test shorten a resource's poll interval.
func orDefaultInterval(d time.Duration) time.Duration {
	if d == 0 {
		return pollInterval
	}
	return d
}
