// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// alert_rule_resource.go — an alert on one Monitoring panel of one resource,
// with a warning and/or a critical tier. Every attribute is updated in place.

var (
	_ resource.Resource                     = &alertRuleResource{}
	_ resource.ResourceWithConfigure        = &alertRuleResource{}
	_ resource.ResourceWithImportState      = &alertRuleResource{}
	_ resource.ResourceWithConfigValidators = &alertRuleResource{}
	_ resource.ResourceWithValidateConfig   = &alertRuleResource{}
)

var alertRuleResourceKinds = []string{
	string(console.ResourceKindPostgres), string(console.ResourceKindModel), string(console.ResourceKindContainerApp),
	string(console.ResourceKindTrino), string(console.ResourceKindKafka), string(console.ResourceKindHfKeyValueCache),
	string(console.ResourceKindDevWorkstation), string(console.ResourceKindGithubRunnerPool),
	string(console.ResourceKindScheduledQuery),
}

func NewAlertRuleResource() resource.Resource {
	return &alertRuleResource{}
}

type alertRuleResource struct {
	p *providerData
}

type alertRuleModel struct {
	ID            types.String  `tfsdk:"id"`
	ResourceKind  types.String  `tfsdk:"resource_kind"`
	ResourceRef   types.String  `tfsdk:"resource_ref"`
	ResourceName  types.String  `tfsdk:"resource_name"`
	PanelID       types.String  `tfsdk:"panel_id"`
	Comparator    types.String  `tfsdk:"comparator"`
	WarningValue  types.Float64 `tfsdk:"warning_value"`
	CriticalValue types.Float64 `tfsdk:"critical_value"`
	ForSeconds    types.Int64   `tfsdk:"for_seconds"`
	Enabled       types.Bool    `tfsdk:"enabled"`
}

func (r *alertRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_rule"
}

func (r *alertRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An alert on one Monitoring panel of one resource, delivered through the alert channels. " +
			alertingFeatureNote,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"resource_kind": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Kind of the watched resource, e.g. `postgres`, `container_app` or `trino`.",
				Validators:          []validator.String{stringvalidator.OneOf(alertRuleResourceKinds...)},
			},
			"resource_ref": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The watched resource: its id, a model's name, or `<environment_id>/<pool_name>` " +
					"for a `github_runner_pool`.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"resource_name": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Name shown in alerts; defaults to `resource_ref`. Set by the platform for a " +
					"`scheduled_query`.",
			},
			"panel_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Panel to watch, e.g. `trino_queued_queries` (`hfctl alerting panels` lists them).",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"comparator": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:             stringdefault.StaticString(string(console.ComparatorGt)),
				MarkdownDescription: "`gt` fires above the thresholds, `lt` below them.",
				Validators:          []validator.String{stringvalidator.OneOf(string(console.ComparatorGt), string(console.ComparatorLt))},
			},
			"warning_value": schema.Float64Attribute{
				Optional:            true,
				MarkdownDescription: "Warning threshold, crossed before `critical_value`.",
			},
			"critical_value": schema.Float64Attribute{
				Optional:            true,
				MarkdownDescription: "Critical threshold.",
			},
			"for_seconds": schema.Int64Attribute{
				Optional: true, Computed: true,
				Default:             int64default.StaticInt64(300),
				MarkdownDescription: "How long the condition must hold before the alert fires, up to 86400.",
				Validators:          []validator.Int64{int64validator.Between(0, 86400)},
			},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Whether the rule is evaluated.",
			},
		},
	}
}

func (r *alertRuleResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.AtLeastOneOf(path.MatchRoot("warning_value"), path.MatchRoot("critical_value")),
	}
}

// ValidateConfig refuses a resource_name the platform would overwrite, which
// would otherwise end the apply in an inconsistent result.
func (r *alertRuleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var kind, name types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("resource_kind"), &kind)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("resource_name"), &name)...)
	if kind.ValueString() == string(console.ResourceKindScheduledQuery) && !name.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("resource_name"), "resource_name on a scheduled query",
			"The platform names a scheduled query's rule itself; remove `resource_name`.")
	}
}

func (r *alertRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func alertRuleBody(plan alertRuleModel) console.AlertRuleBody {
	name := plan.ResourceRef.ValueString()
	if n := stringPtr(plan.ResourceName); n != nil {
		name = *n
	}
	forSeconds := int32(plan.ForSeconds.ValueInt64())
	return console.AlertRuleBody{
		ResourceKind:  console.ResourceKind(plan.ResourceKind.ValueString()),
		ResourceRef:   plan.ResourceRef.ValueString(),
		ResourceName:  name,
		PanelId:       plan.PanelID.ValueString(),
		Comparator:    console.Comparator(plan.Comparator.ValueString()),
		WarningValue:  plan.WarningValue.ValueFloat64Pointer(),
		CriticalValue: plan.CriticalValue.ValueFloat64Pointer(),
		ForSeconds:    &forSeconds,
		Enabled:       boolPtr(plan.Enabled),
	}
}

func (r *alertRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan alertRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	created, err := r.p.API.CreateAlertRule(ctx, r.p.OrgID, alertRuleBody(plan))
	if err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to create alert rule", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, alertRuleFrom(created))...)
}

func (r *alertRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior alertRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rule, err := r.p.API.GetAlertRule(ctx, r.p.OrgID, prior.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to read alert rule", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, alertRuleFrom(rule))...)
}

func (r *alertRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state alertRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	updated, err := r.p.API.UpdateAlertRule(ctx, r.p.OrgID, state.ID.ValueString(), alertRuleBody(plan))
	if err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to update alert rule", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, alertRuleFrom(updated))...)
}

func (r *alertRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state alertRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.p.API.DeleteAlertRule(ctx, r.p.OrgID, state.ID.ValueString()); err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to delete alert rule", err)
	}
}

func (r *alertRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func alertRuleFrom(rule *console.AlertRuleDto) alertRuleModel {
	return alertRuleModel{
		ID:            types.StringValue(rule.Id.String()),
		ResourceKind:  types.StringValue(string(rule.ResourceKind)),
		ResourceRef:   types.StringValue(rule.ResourceRef),
		ResourceName:  types.StringValue(rule.ResourceName),
		PanelID:       types.StringValue(rule.PanelId),
		Comparator:    types.StringValue(string(rule.Comparator)),
		WarningValue:  types.Float64PointerValue(rule.WarningValue),
		CriticalValue: types.Float64PointerValue(rule.CriticalValue),
		ForSeconds:    types.Int64Value(int64(rule.ForSeconds)),
		Enabled:       types.BoolValue(rule.Enabled),
	}
}
