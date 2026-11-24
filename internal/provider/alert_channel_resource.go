// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
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
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// alert_channel_resource.go — a notification channel alerts are delivered
// through. A webhook `url` is a credential the API only returns masked, so it
// is write-only like a secret's value and rotated with `url_wo_version`. A
// Matrix channel names a bot (hyperfluid_alert_matrix_bot) and a room instead.
//
// Matrix room, alias or id: the API resolves an alias (`#ops:server`) to the
// room id (`!abc:server`) and stores both, the alias as `room_label`. `room`
// keeps the value the configuration named, so an alias never turns into a
// perpetual diff against the id the API reports. On read, the prior `room` is
// kept while the bot is unchanged and it still names the stored room — it equals
// either the stored `room_label` or the stored `room_id`; otherwise (import, or
// the channel was repointed outside Terraform) `room` becomes the `room_label`
// when there is one, else the `room_id`. The resolved id is always in
// `matrix.room_id`.

var (
	_ resource.Resource                   = &alertChannelResource{}
	_ resource.ResourceWithConfigure      = &alertChannelResource{}
	_ resource.ResourceWithImportState    = &alertChannelResource{}
	_ resource.ResourceWithValidateConfig = &alertChannelResource{}
)

var alertChannelKinds = []string{
	string(console.ChannelKindEmail), string(console.ChannelKindSlackWebhook), string(console.ChannelKindDiscordWebhook),
	string(console.ChannelKindTeamsWebhook), string(console.ChannelKindMatrix),
}

// isWebhookChannelKind reports the kinds delivered to a write-only `url`.
func isWebhookChannelKind(kind console.ChannelKind) bool {
	switch kind {
	case console.ChannelKindSlackWebhook, console.ChannelKindDiscordWebhook, console.ChannelKindTeamsWebhook:
		return true
	}
	return false
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
	Matrix       types.Object `tfsdk:"matrix"`
}

type alertChannelMatrixModel struct {
	BotID  types.String `tfsdk:"bot_id"`
	Room   types.String `tfsdk:"room"`
	RoomID types.String `tfsdk:"room_id"`
}

func alertChannelMatrixAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{"bot_id": types.StringType, "room": types.StringType, "room_id": types.StringType}
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
				Required: true,
				MarkdownDescription: "`email`, `slack_webhook`, `discord_webhook`, `teams_webhook` (a Microsoft Teams " +
					"Workflows webhook) or `matrix` (a Matrix or Tchap room, posted to by a `hyperfluid_alert_matrix_bot`).",
				Validators: []validator.String{stringvalidator.OneOf(alertChannelKinds...)},
			},
			"recipients": schema.SetAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "Email addresses; required for `email`, refused for the other kinds.",
			},
			"url": schema.StringAttribute{
				Optional:  true,
				WriteOnly: true,
				Sensitive: true,
				MarkdownDescription: "Webhook URL, required on create for a webhook kind and refused for `email` and " +
					"`matrix`; write-only (Terraform 1.11 or later). Left out on update, the stored URL is kept. A Teams " +
					"URL must be on `*.logic.azure.com` or `*.environment.api.powerplatform.com`.",
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
			"matrix": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "The bot and room of a `matrix` channel; required for that kind and refused for " +
					"the others.",
				Attributes: map[string]schema.Attribute{
					"bot_id": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "Id of the `hyperfluid_alert_matrix_bot` that posts the alerts.",
					},
					"room": schema.StringAttribute{
						Required: true,
						MarkdownDescription: "Room id (`!abc:server`) or alias (`#ops:server`). The platform resolves " +
							"an alias through the bot and stores the id; the configured value is kept here, so an alias " +
							"does not show a diff against the resolved id.",
					},
					"room_id": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "The room id the alerts are posted to, resolved from `room`.",
						PlanModifiers:       []planmodifier.String{matrixRoomIDUnlessRoomChanged{}},
					},
				},
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
	kind := console.ChannelKind(cfg.Kind.ValueString())
	if kind != console.ChannelKindMatrix && !cfg.Matrix.IsNull() && !cfg.Matrix.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("matrix"), "Matrix settings on a non-Matrix channel",
			"`matrix` is only taken by a channel of kind `matrix`.")
	}
	if kind == console.ChannelKindMatrix {
		if !cfg.URL.IsNull() && !cfg.URL.IsUnknown() {
			resp.Diagnostics.AddAttributeError(path.Root("url"), "URL on a Matrix channel",
				"A Matrix channel takes `matrix`, not a `url`.")
		}
		if !cfg.Recipients.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("recipients"), "Recipients on a Matrix channel",
				"A Matrix channel takes `matrix`, not `recipients`.")
		}
		if cfg.Matrix.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("matrix"), "Missing matrix settings",
				"A Matrix channel needs `matrix = { bot_id, room }`.")
			return
		}
		if cfg.Matrix.IsUnknown() {
			return
		}
		var m alertChannelMatrixModel
		resp.Diagnostics.Append(cfg.Matrix.As(ctx, &m, basetypes.ObjectAsOptions{})...)
		if !m.BotID.IsNull() && !m.BotID.IsUnknown() {
			if _, err := uuid.Parse(m.BotID.ValueString()); err != nil {
				resp.Diagnostics.AddAttributeError(path.Root("matrix").AtName("bot_id"), "Invalid bot id",
					"`bot_id` must be the UUID of a `hyperfluid_alert_matrix_bot`.")
			}
		}
		if !m.Room.IsNull() && !m.Room.IsUnknown() {
			if room := m.Room.ValueString(); !strings.HasPrefix(room, "!") && !strings.HasPrefix(room, "#") {
				resp.Diagnostics.AddAttributeError(path.Root("matrix").AtName("room"), "Invalid Matrix room",
					fmt.Sprintf("`room` must be a room id (`!…`) or an alias (`#…:server`), got %q.", room))
			}
		}
		return
	}
	if kind == console.ChannelKindEmail {
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
	if !plan.Matrix.IsNull() && !plan.Matrix.IsUnknown() {
		var m alertChannelMatrixModel
		diags.Append(plan.Matrix.As(ctx, &m, basetypes.ObjectAsOptions{})...)
		botID, err := uuid.Parse(m.BotID.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root("matrix").AtName("bot_id"), "Invalid bot id",
				"`bot_id` must be the UUID of a `hyperfluid_alert_matrix_bot`.")
			return body, diags
		}
		body.Matrix = &console.MatrixChannelBody{BotId: botID, Room: m.Room.ValueString()}
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
	if isWebhookChannelKind(body.Kind) && body.Url == nil {
		resp.Diagnostics.AddAttributeError(path.Root("url"), "Missing url", "`url` is required when creating a webhook channel.")
		return
	}
	if body.Kind == console.ChannelKindMatrix && body.Matrix == nil {
		resp.Diagnostics.AddAttributeError(path.Root("matrix"), "Missing matrix settings",
			"A Matrix channel needs `matrix = { bot_id, room }`.")
		return
	}
	created, err := r.p.API.CreateAlertChannel(ctx, r.p.OrgID, body)
	if err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to create alert channel", err)
		return
	}
	state, d := alertChannelFrom(ctx, created, plan.Matrix, true)
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
	state, d := alertChannelFrom(ctx, ch, prior.Matrix, false)
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
	if !plan.URLWoVersion.Equal(state.URLWoVersion) && isWebhookChannelKind(body.Kind) && body.Url == nil {
		resp.Diagnostics.AddAttributeError(path.Root("url"), "Missing url",
			"`url` is required when `url_wo_version` changes (rotation).")
		return
	}
	keepResolvedRoom(ctx, &body, plan.Matrix, state.Matrix, &resp.Diagnostics)
	updated, err := r.p.API.UpdateAlertChannel(ctx, r.p.OrgID, state.ID.ValueString(), body)
	if err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to update alert channel", err)
		return
	}
	newState, d := alertChannelFrom(ctx, updated, plan.Matrix, true)
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
// prior is the matrix object the plan or the previous state named. After a
// write (written true) the API has just resolved the planned room, so the
// planned value is kept as long as the bot is the planned one: state must match
// the plan. On a read, the prior room is kept only while it still names the
// stored room (see the file comment).
func alertChannelFrom(ctx context.Context, ch *console.NotificationChannelDto, prior types.Object, written bool) (alertChannelModel, diag.Diagnostics) {
	recipients := types.SetNull(types.StringType)
	var diags diag.Diagnostics
	if len(ch.Recipients) > 0 {
		var d diag.Diagnostics
		recipients, d = stringSliceToSet(ctx, ch.Recipients)
		diags.Append(d...)
	}
	matrix := types.ObjectNull(alertChannelMatrixAttrTypes())
	if ch.Matrix != nil {
		var priorRoom, priorBot *string
		if !prior.IsNull() && !prior.IsUnknown() {
			var pm alertChannelMatrixModel
			diags.Append(prior.As(ctx, &pm, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
			if !pm.Room.IsNull() && !pm.Room.IsUnknown() {
				priorRoom = pm.Room.ValueStringPointer()
			}
			if !pm.BotID.IsNull() && !pm.BotID.IsUnknown() {
				priorBot = pm.BotID.ValueStringPointer()
			}
		}
		room := matrixRoomForState(ch.Matrix, priorBot, priorRoom, written)
		botID := ch.Matrix.BotId.String()
		if priorBot != nil && strings.EqualFold(*priorBot, botID) {
			botID = *priorBot // the same UUID, spelled as configured
		}
		var d diag.Diagnostics
		matrix, d = types.ObjectValueFrom(ctx, alertChannelMatrixAttrTypes(), alertChannelMatrixModel{
			BotID:  types.StringValue(botID),
			Room:   types.StringValue(room),
			RoomID: types.StringValue(ch.Matrix.RoomId),
		})
		diags.Append(d...)
	}
	return alertChannelModel{
		ID:         types.StringValue(ch.Id.String()),
		Name:       types.StringValue(ch.Name),
		Kind:       types.StringValue(string(ch.Kind)),
		Recipients: recipients,
		URL:        types.StringNull(),
		URLMasked:  optString(ch.UrlMasked),
		Enabled:    types.BoolValue(ch.Enabled),
		Matrix:     matrix,
	}, diags
}

// keepResolvedRoom sends the room id already resolved, not the configured
// alias, when an update leaves the bot and the room unchanged. Re-sending the
// alias would make the platform resolve it again through the bot: a rename
// would then fail while the bot is not connected, and an alias repointed since
// would move the channel to another room while the plan kept the old room_id
// (an inconsistent result).
func keepResolvedRoom(ctx context.Context, body *console.ChannelBody, plan, state types.Object, diags *diag.Diagnostics) {
	if body.Matrix == nil || plan.IsNull() || plan.IsUnknown() || state.IsNull() || state.IsUnknown() {
		return
	}
	var planned, prior alertChannelMatrixModel
	diags.Append(plan.As(ctx, &planned, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
	diags.Append(state.As(ctx, &prior, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
	if prior.RoomID.IsNull() || prior.RoomID.IsUnknown() || prior.RoomID.ValueString() == "" {
		return
	}
	if strings.EqualFold(planned.BotID.ValueString(), prior.BotID.ValueString()) &&
		planned.Room.ValueString() == prior.Room.ValueString() {
		body.Matrix.Room = prior.RoomID.ValueString()
	}
}

// matrixRoomForState picks the `room` written to state. The prior value is kept
// while the bot is unchanged and it still names the stored room — as its alias
// (`room_label`) or its id, or unconditionally right after the write that
// resolved it; otherwise the alias the API stored wins, then the id.
func matrixRoomForState(m *console.MatrixChannelDto, priorBot, priorRoom *string, written bool) string {
	if priorRoom != nil && priorBot != nil && strings.EqualFold(*priorBot, m.BotId.String()) {
		if written || *priorRoom == m.RoomId || (m.RoomLabel != nil && *priorRoom == *m.RoomLabel) {
			return *priorRoom
		}
	}
	if m.RoomLabel != nil && *m.RoomLabel != "" {
		return *m.RoomLabel
	}
	return m.RoomId
}

// matrixRoomIDUnlessRoomChanged keeps the known room_id in the plan while the
// bot and room are the ones the state already resolved, so an unrelated change
// (a rename, enabling the channel) does not show room_id as unknown. A changed
// bot or room leaves it unknown: the API resolves the room again.
type matrixRoomIDUnlessRoomChanged struct{}

func (matrixRoomIDUnlessRoomChanged) Description(context.Context) string {
	return "Keeps the resolved room id while the bot and room are unchanged."
}

func (m matrixRoomIDUnlessRoomChanged) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (matrixRoomIDUnlessRoomChanged) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() || !req.PlanValue.IsUnknown() || req.Plan.Raw.IsNull() {
		return
	}
	parent := req.Path.ParentPath()
	for _, name := range []string{"bot_id", "room"} {
		var planned, prior types.String
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, parent.AtName(name), &planned)...)
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, parent.AtName(name), &prior)...)
		if resp.Diagnostics.HasError() || planned.IsUnknown() || !planned.Equal(prior) {
			return
		}
	}
	resp.PlanValue = req.StateValue
}
