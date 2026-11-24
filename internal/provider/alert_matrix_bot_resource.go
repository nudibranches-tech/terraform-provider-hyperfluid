// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// alert_matrix_bot_resource.go — a Matrix account (Tchap or any homeserver)
// the platform logs in to post alerts with. Its password and recovery key are
// referenced as secret ids, never passed in clear. A `matrix` alert channel
// names the bot by id; the API refuses to delete a bot while a channel still
// names it.

var (
	_ resource.Resource                   = &alertMatrixBotResource{}
	_ resource.ResourceWithConfigure      = &alertMatrixBotResource{}
	_ resource.ResourceWithImportState    = &alertMatrixBotResource{}
	_ resource.ResourceWithValidateConfig = &alertMatrixBotResource{}
)

var alertMatrixBotPresets = []string{string(console.MatrixBotPresetTchap), string(console.MatrixBotPresetGeneric)}

func NewAlertMatrixBotResource() resource.Resource {
	return &alertMatrixBotResource{}
}

type alertMatrixBotResource struct {
	p *providerData
}

type alertMatrixBotModel struct {
	ID                    types.String `tfsdk:"id"`
	Name                  types.String `tfsdk:"name"`
	Preset                types.String `tfsdk:"preset"`
	HomeserverURL         types.String `tfsdk:"homeserver_url"`
	UserID                types.String `tfsdk:"user_id"`
	PasswordSecretID      types.String `tfsdk:"password_secret_id"`
	RecoveryKeySecretID   types.String `tfsdk:"recovery_key_secret_id"`
	AllowUnencryptedRooms types.Bool   `tfsdk:"allow_unencrypted_rooms"`
	Phase                 types.String `tfsdk:"phase"`
	DeviceID              types.String `tfsdk:"device_id"`
	Message               types.String `tfsdk:"message"`
	ReloginRequired       types.Bool   `tfsdk:"relogin_required"`
}

func (r *alertMatrixBotResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_matrix_bot"
}

func (r *alertMatrixBotResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Matrix account the platform logs in to post alerts to Matrix or Tchap rooms, named by " +
			"`hyperfluid_alert_channel` channels of kind `matrix`. The account's password and recovery key are " +
			"referenced as plaintext secrets (`hyperfluid_secret`). A bot cannot be destroyed while a channel still " +
			"names it. When the bot's session ends (`relogin_required`), make it log in again from the console or " +
			"with `hfctl alerting matrix-bots relogin`; the provider has no action for it. " + alertingFeatureNote,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Bot id, named by a channel's `matrix.bot_id`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Bot name, shown in the console.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"preset": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "`tchap` (the French state messenger: the homeserver follows from `user_id`) or " +
					"`generic` (any homeserver, named by `homeserver_url`).",
				Validators: []validator.String{stringvalidator.OneOf(alertMatrixBotPresets...)},
			},
			"homeserver_url": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "The homeserver (`https://…`); required with `generic`, refused with `tchap`.",
			},
			"user_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The bot account's MXID, e.g. `@bot-alerts:agent.dinum.tchap.gouv.fr`.",
			},
			"password_secret_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Id of the plaintext secret holding the bot account's password.",
			},
			"recovery_key_secret_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Id of the plaintext secret holding the recovery key the account showed at setup.",
			},
			"allow_unencrypted_rooms": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Whether the bot may post into a room that is not end-to-end encrypted. Defaults to `false`.",
			},
			"phase": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Where the bot's notifier stands: `pending`, `connecting`, `connected`, `degraded`, " +
					"`needs_login`, `disabled` or `unknown`.",
			},
			"device_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The Matrix device the bot lives on, once its first login completed.",
			},
			"message": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "What the phase is about; names the action when a re-login is needed.",
			},
			"relogin_required": schema.BoolAttribute{
				Computed: true,
				MarkdownDescription: "The bot's session ended; re-login from the console or with " +
					"`hfctl alerting matrix-bots relogin`.",
			},
		},
	}
}

// ValidateConfig checks the preset against homeserver_url and the shape of the
// ids, at plan time.
func (r *alertMatrixBotResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg alertMatrixBotModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !cfg.Preset.IsNull() && !cfg.Preset.IsUnknown() && !cfg.HomeserverURL.IsUnknown() {
		switch console.MatrixBotPreset(cfg.Preset.ValueString()) {
		case console.MatrixBotPresetTchap:
			if !cfg.HomeserverURL.IsNull() {
				resp.Diagnostics.AddAttributeError(path.Root("homeserver_url"), "Homeserver URL with the Tchap preset",
					"A `tchap` bot's homeserver follows from its `user_id`; drop `homeserver_url`.")
			}
		case console.MatrixBotPresetGeneric:
			if cfg.HomeserverURL.IsNull() || !strings.HasPrefix(cfg.HomeserverURL.ValueString(), "https://") {
				resp.Diagnostics.AddAttributeError(path.Root("homeserver_url"), "Missing homeserver URL",
					"A `generic` bot needs an `https://` `homeserver_url`.")
			}
		}
	}
	if !cfg.UserID.IsNull() && !cfg.UserID.IsUnknown() {
		if mxid := cfg.UserID.ValueString(); !strings.HasPrefix(mxid, "@") || !strings.Contains(mxid, ":") {
			resp.Diagnostics.AddAttributeError(path.Root("user_id"), "Invalid MXID",
				"`user_id` must be a Matrix user id, `@name:server`.")
		}
	}
	for _, a := range []struct {
		name string
		v    types.String
	}{{"password_secret_id", cfg.PasswordSecretID}, {"recovery_key_secret_id", cfg.RecoveryKeySecretID}} {
		if a.v.IsNull() || a.v.IsUnknown() {
			continue
		}
		if _, err := uuid.Parse(a.v.ValueString()); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root(a.name), "Invalid secret id",
				"`"+a.name+"` must be the UUID of a plaintext secret (`hyperfluid_secret.<name>.id`).")
		}
	}
}

func (r *alertMatrixBotResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func alertMatrixBotBody(plan alertMatrixBotModel) (console.MatrixBotBody, diag.Diagnostics) {
	var diags diag.Diagnostics
	password, err := uuid.Parse(plan.PasswordSecretID.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("password_secret_id"), "Invalid secret id", err.Error())
	}
	recovery, err := uuid.Parse(plan.RecoveryKeySecretID.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("recovery_key_secret_id"), "Invalid secret id", err.Error())
	}
	return console.MatrixBotBody{
		Name:                  plan.Name.ValueString(),
		Preset:                console.MatrixBotPreset(plan.Preset.ValueString()),
		HomeserverUrl:         stringPtr(plan.HomeserverURL),
		UserId:                plan.UserID.ValueString(),
		PasswordSecretId:      password,
		RecoveryKeySecretId:   recovery,
		AllowUnencryptedRooms: boolPtr(plan.AllowUnencryptedRooms),
	}, diags
}

func (r *alertMatrixBotResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan alertMatrixBotModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, d := alertMatrixBotBody(plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	created, err := r.p.API.CreateAlertMatrixBot(ctx, r.p.OrgID, body)
	if err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to create Matrix bot", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, alertMatrixBotFrom(created, &plan))...)
}

func (r *alertMatrixBotResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior alertMatrixBotModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	bot, err := r.p.API.GetAlertMatrixBot(ctx, r.p.OrgID, prior.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to read Matrix bot", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, alertMatrixBotFrom(bot, &prior))...)
}

func (r *alertMatrixBotResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state alertMatrixBotModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, d := alertMatrixBotBody(plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	updated, err := r.p.API.UpdateAlertMatrixBot(ctx, r.p.OrgID, state.ID.ValueString(), body)
	if err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to update Matrix bot", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, alertMatrixBotFrom(updated, &plan))...)
}

func (r *alertMatrixBotResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state alertMatrixBotModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.p.API.DeleteAlertMatrixBot(ctx, r.p.OrgID, state.ID.ValueString())
	if errors.Is(err, client.ErrMatrixBotInUse) {
		resp.Diagnostics.AddError("Matrix bot still in use",
			"A `matrix` alert channel still names this bot. Destroy or repoint those channels first; a channel "+
				"that references the bot's id in the same configuration is destroyed before it.\n\n"+err.Error())
		return
	}
	if err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to delete Matrix bot", err)
	}
}

func (r *alertMatrixBotResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// alertMatrixBotFrom maps a bot into state. prior (the plan or the previous
// state, nil on import) keeps the spelling of the ids the configuration used
// when they name the same UUID.
func alertMatrixBotFrom(b *console.MatrixBotDto, prior *alertMatrixBotModel) alertMatrixBotModel {
	sameUUID := func(priorValue types.String, apiValue string) types.String {
		if priorValue.IsNull() || priorValue.IsUnknown() || !strings.EqualFold(priorValue.ValueString(), apiValue) {
			return types.StringValue(apiValue)
		}
		return priorValue
	}
	var priorPassword, priorRecovery types.String
	if prior != nil {
		priorPassword, priorRecovery = prior.PasswordSecretID, prior.RecoveryKeySecretID
	}
	return alertMatrixBotModel{
		ID:                    types.StringValue(b.Id.String()),
		Name:                  types.StringValue(b.Name),
		Preset:                types.StringValue(string(b.Preset)),
		HomeserverURL:         optString(b.HomeserverUrl),
		UserID:                types.StringValue(b.UserId),
		PasswordSecretID:      sameUUID(priorPassword, b.PasswordSecretId.String()),
		RecoveryKeySecretID:   sameUUID(priorRecovery, b.RecoveryKeySecretId.String()),
		AllowUnencryptedRooms: types.BoolValue(b.AllowUnencryptedRooms),
		Phase:                 types.StringValue(string(b.Status.Phase)),
		DeviceID:              optString(b.Status.DeviceId),
		Message:               optString(b.Status.Message),
		ReloginRequired:       types.BoolValue(b.Status.ReloginRequired),
	}
}
