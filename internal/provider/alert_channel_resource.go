// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// alert_channel_resource.go — a notification channel alerts are delivered
// through. A webhook `url` is a credential the API only returns masked, so it
// is write-only like a secret's value and rotated with `url_wo_version`.

var (
	_ resource.Resource                   = &alertChannelResource{}
	_ resource.ResourceWithConfigure      = &alertChannelResource{}
	_ resource.ResourceWithImportState    = &alertChannelResource{}
	_ resource.ResourceWithValidateConfig = &alertChannelResource{}
)

var alertChannelKinds = []string{
	string(console.ChannelKindEmail), string(console.ChannelKindSlackWebhook), string(console.ChannelKindDiscordWebhook),
}

func NewAlertChannelResource() resource.Resource {
	return &alertChannelResource{}
}

type alertChannelResource struct {
	p *providerData
}

type alertChannelModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	Kind         types.String `tfsdk:"kind"`
	Recipients   types.Set    `tfsdk:"recipients"`
	URL          types.String `tfsdk:"url"` // write-only — never in state
	URLWoVersion types.String `tfsdk:"url_wo_version"`
	URLMasked    types.String `tfsdk:"url_masked"`
	Enabled      types.Bool   `tfsdk:"enabled"`
}

func (r *alertChannelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_channel"
}

func (r *alertChannelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A notification channel alerts are delivered through. A signal routed to no channel " +
			"reaches every enabled one, so a new channel is live at once. " + alertingFeatureNote,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Channel name, unique in the organization.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 128)},
			},
			"kind": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "`email`, `slack_webhook` or `discord_webhook`.",
				Validators:          []validator.String{stringvalidator.OneOf(alertChannelKinds...)},
			},
			"recipients": schema.SetAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "Email addresses; required for `email`, refused for webhooks.",
			},
			"url": schema.StringAttribute{
				Optional:  true,
				WriteOnly: true,
				Sensitive: true,
				MarkdownDescription: "Webhook URL, required on create for a webhook kind; write-only (Terraform 1.11 " +
					"or later). Left out on update, the stored URL is kept.",
			},
			"url_wo_version": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Any value; change it together with `url` to rotate the webhook URL.",
			},
			"url_masked": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The webhook URL, truncated by the platform.",
			},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Whether alerts are delivered; a disabled channel keeps its routing.",
			},
		},
	}
}

// ValidateConfig checks which of recipients and url the kind takes, at plan
// time. Nothing it reports quotes the URL.
func (r *alertChannelResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg alertChannelModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Kind.IsNull() || cfg.Kind.IsUnknown() {
		return
	}
	if cfg.Kind.ValueString() == string(console.ChannelKindEmail) {
		if !cfg.URL.IsNull() && !cfg.URL.IsUnknown() {
			resp.Diagnostics.AddAttributeError(path.Root("url"), "URL on an email channel",
				"An email channel takes `recipients`, not a `url`.")
		}
		if cfg.Recipients.IsNull() || (!cfg.Recipients.IsUnknown() && len(cfg.Recipients.Elements()) == 0) {
			resp.Diagnostics.AddAttributeError(path.Root("recipients"), "Missing recipients",
				"An email channel needs at least one recipient address.")
		}
		return
	}
	if !cfg.Recipients.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("recipients"), "Recipients on a webhook channel",
			"A webhook channel takes a `url`, not `recipients`.")
	}
}

func (r *alertChannelResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// body builds the write request; url comes from the config, where the
// write-only value lives, and is left out when unset.
func (r *alertChannelResource) body(ctx context.Context, plan alertChannelModel, config tfsdk.Config) (console.ChannelBody, diag.Diagnostics) {
	var diags diag.Diagnostics
	var url types.String
	diags.Append(config.GetAttribute(ctx, path.Root("url"), &url)...)
	recipients, d := setToStringSlice(ctx, plan.Recipients)
	diags.Append(d...)
	body := console.ChannelBody{
		Name:    plan.Name.ValueString(),
		Kind:    console.ChannelKind(plan.Kind.ValueString()),
		Enabled: boolPtr(plan.Enabled),
		Url:     stringPtr(url),
	}
	if recipients != nil {
		body.Recipients = &recipients
	}
	return body, diags
}

func (r *alertChannelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan alertChannelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, d := r.body(ctx, plan, req.Config)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if body.Kind != console.ChannelKindEmail && body.Url == nil {
		resp.Diagnostics.AddAttributeError(path.Root("url"), "Missing url", "`url` is required when creating a webhook channel.")
		return
	}
	created, err := r.p.API.CreateAlertChannel(ctx, r.p.OrgID, body)
	if err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to create alert channel", err)
		return
	}
	state, d := alertChannelFrom(ctx, created)
	resp.Diagnostics.Append(d...)
	state.URLWoVersion = plan.URLWoVersion
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *alertChannelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior alertChannelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ch, err := r.p.API.GetAlertChannel(ctx, r.p.OrgID, prior.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to read alert channel", err)
		return
	}
	state, d := alertChannelFrom(ctx, ch)
	resp.Diagnostics.Append(d...)
	state.URLWoVersion = prior.URLWoVersion // not echoed by the API
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *alertChannelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state alertChannelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, d := r.body(ctx, plan, req.Config)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.URLWoVersion.Equal(state.URLWoVersion) && body.Kind != console.ChannelKindEmail && body.Url == nil {
		resp.Diagnostics.AddAttributeError(path.Root("url"), "Missing url",
			"`url` is required when `url_wo_version` changes (rotation).")
		return
	}
	updated, err := r.p.API.UpdateAlertChannel(ctx, r.p.OrgID, state.ID.ValueString(), body)
	if err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to update alert channel", err)
		return
	}
	newState, d := alertChannelFrom(ctx, updated)
	resp.Diagnostics.Append(d...)
	newState.URLWoVersion = plan.URLWoVersion
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *alertChannelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state alertChannelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.p.API.DeleteAlertChannel(ctx, r.p.OrgID, state.ID.ValueString()); err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to delete alert channel", err)
	}
}

func (r *alertChannelResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// alertChannelFrom maps a channel into state. url is always null (write-only)
// and the caller sets url_wo_version; a webhook's empty recipients read as null.
func alertChannelFrom(ctx context.Context, ch *console.NotificationChannelDto) (alertChannelModel, diag.Diagnostics) {
	recipients := types.SetNull(types.StringType)
	var d diag.Diagnostics
	if len(ch.Recipients) > 0 {
		recipients, d = stringSliceToSet(ctx, ch.Recipients)
	}
	return alertChannelModel{
		ID:         types.StringValue(ch.Id.String()),
		Name:       types.StringValue(ch.Name),
		Kind:       types.StringValue(string(ch.Kind)),
		Recipients: recipients,
		URL:        types.StringNull(),
		URLMasked:  optString(ch.UrlMasked),
		Enabled:    types.BoolValue(ch.Enabled),
	}, d
}
