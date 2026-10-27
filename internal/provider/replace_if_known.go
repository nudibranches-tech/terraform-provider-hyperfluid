// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
)

// requiresReplaceIfKnown replaces the resource when a create-only attribute
// changes, except from a null state: no read reports the attribute, so after an
// import the first apply adopts the configured value instead of replacing.
func requiresReplaceIfKnown() planmodifier.String {
	return stringplanmodifier.RequiresReplaceIf(
		func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
			resp.RequiresReplace = !req.StateValue.IsNull()
		},
		"Changing this forces a new resource, unless the value was never recorded (after an import).",
		"Changing this forces a new resource, unless the value was never recorded (after an import).",
	)
}
