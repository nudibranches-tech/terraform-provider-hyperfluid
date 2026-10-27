// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// alert_threshold_resource.go — the organization's override of a capacity
// signal's platform thresholds. Destroy resets the signal to the platform
// defaults; the defaults themselves are never written as an override.

var (
	_ resource.Resource                   = &alertThresholdResource{}
	_ resource.ResourceWithConfigure      = &alertThresholdResource{}
	_ resource.ResourceWithImportState    = &alertThresholdResource{}
	_ resource.ResourceWithValidateConfig = &alertThresholdResource{}
)

// alertThresholdSignals are the signals whose thresholds can be overridden.
var alertThresholdSignals = []string{"pvc_disk", "bucket_quota"}

func NewAlertThresholdResource() resource.Resource {
	return &alertThresholdResource{}
}

type alertThresholdResource struct {
	p *providerData
}

type alertThresholdModel struct {
	ID          types.String `tfsdk:"id"`
	Signal      types.String `tfsdk:"signal"`
	WarningPct  types.Int64  `tfsdk:"warning_pct"`
	CriticalPct types.Int64  `tfsdk:"critical_pct"`
}

func (r *alertThresholdResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_threshold"
}

func (r *alertThresholdResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	pct := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{
			Required:            true,
			MarkdownDescription: desc,
			Validators:          []validator.Int64{int64validator.Between(1, 100)},
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Overrides the platform's warning and critical thresholds of a capacity signal for the " +
			"organization. " + alertingFeatureNote,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"signal": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "`pvc_disk` or `bucket_quota`.",
				Validators:          []validator.String{stringvalidator.OneOf(alertThresholdSignals...)},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"warning_pct":  pct("Usage percentage that raises a warning; below `critical_pct`."),
			"critical_pct": pct("Usage percentage that raises a critical alert."),
		},
	}
}

func (r *alertThresholdResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg alertThresholdModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.WarningPct.IsNull() || cfg.WarningPct.IsUnknown() ||
		cfg.CriticalPct.IsNull() || cfg.CriticalPct.IsUnknown() {
		return
	}
	if cfg.WarningPct.ValueInt64() >= cfg.CriticalPct.ValueInt64() {
		resp.Diagnostics.AddAttributeError(path.Root("warning_pct"), "Warning not below critical",
			fmt.Sprintf("warning_pct (%d) must be below critical_pct (%d).", cfg.WarningPct.ValueInt64(), cfg.CriticalPct.ValueInt64()))
	}
}

func (r *alertThresholdResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "expected *providerData")
		return
	}
	r.p = pd
}

func (r *alertThresholdResource) set(ctx context.Context, plan alertThresholdModel) (*console.SignalThresholdsDto, error) {
	return r.p.API.SetAlertThresholds(ctx, r.p.OrgID, plan.Signal.ValueString(),
		int32(plan.WarningPct.ValueInt64()), int32(plan.CriticalPct.ValueInt64()))
}

func (r *alertThresholdResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan alertThresholdModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := r.set(ctx, plan)
	if err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to set alert thresholds", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, alertThresholdFrom(t))...)
}

// Read drops the resource once neither tier is overridden: the signal was reset
// to the platform defaults outside Terraform.
func (r *alertThresholdResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior alertThresholdModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := r.p.API.GetAlertThresholds(ctx, r.p.OrgID, prior.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && !t.WarningIsOverride && !t.CriticalIsOverride) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to read alert thresholds", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, alertThresholdFrom(t))...)
}

func (r *alertThresholdResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan alertThresholdModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := r.set(ctx, plan)
	if err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to set alert thresholds", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, alertThresholdFrom(t))...)
}

func (r *alertThresholdResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state alertThresholdModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.p.API.ResetAlertThresholds(ctx, r.p.OrgID, state.Signal.ValueString()); err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to reset alert thresholds", err)
	}
}

// ImportState takes the signal name, which is also the id.
func (r *alertThresholdResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func alertThresholdFrom(t *console.SignalThresholdsDto) alertThresholdModel {
	return alertThresholdModel{
		ID:          types.StringValue(t.Signal),
		Signal:      types.StringValue(t.Signal),
		WarningPct:  types.Int64Value(int64(t.WarningPct)),
		CriticalPct: types.Int64Value(int64(t.CriticalPct)),
	}
}
