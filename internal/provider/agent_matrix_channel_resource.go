// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

var (
	_ resource.Resource                   = &agentMatrixChannelResource{}
	_ resource.ResourceWithConfigure      = &agentMatrixChannelResource{}
	_ resource.ResourceWithImportState    = &agentMatrixChannelResource{}
	_ resource.ResourceWithValidateConfig = &agentMatrixChannelResource{}
)

func NewAgentMatrixChannelResource() resource.Resource {
	return &agentMatrixChannelResource{agentChannelBase{channel: client.ChannelMatrix, display: "Matrix"}}
}

type agentMatrixChannelResource struct{ agentChannelBase }

type agentMatrixChannelModel struct {
	ID                     types.String `tfsdk:"id"`
	AgentID                types.String `tfsdk:"agent_id"`
	Preset                 types.String `tfsdk:"preset"`
	HomeserverURL          types.String `tfsdk:"homeserver_url"`
	UserID                 types.String `tfsdk:"user_id"`
	PasswordSecretID       types.String `tfsdk:"password_secret_id"`
	RecoveryKeySecretID    types.String `tfsdk:"recovery_key_secret_id"`
	ExtraAuthHosts         types.Set    `tfsdk:"extra_auth_hosts"`
	AllowedRooms           types.Set    `tfsdk:"allowed_rooms"`
	AllowedUsers           types.Set    `tfsdk:"allowed_users"`
	AutoJoin               types.String `tfsdk:"auto_join"`
	DirectMessages         types.Bool   `tfsdk:"direct_messages"`
	UnencryptedRooms       types.String `tfsdk:"unencrypted_rooms"`
	AllowExternalInference types.Bool   `tfsdk:"allow_external_inference"`
	ReloginTrigger         types.String `tfsdk:"relogin_trigger"`

	channelStatusModel
	DeviceID        types.String `tfsdk:"device_id"`
	ReloginRequired types.Bool   `tfsdk:"relogin_required"`
	ReloginPending  types.Bool   `tfsdk:"relogin_pending"`
}

func (r *agentMatrixChannelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent_matrix_channel"
}

func (r *agentMatrixChannelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := agentIDAttributes("Matrix channel")
	attrs["preset"] = schema.StringAttribute{
		Optional: true,
		MarkdownDescription: "`tchap` or `generic` (left out). `tchap` derives the homeserver and its authentication hosts from `user_id`, " +
			"which must then be on a server under `tchap.gouv.fr`; it refuses `homeserver_url`, and an agent whose model is not served " +
			"in the cluster unless `allow_external_inference` is set.",
		Validators: []validator.String{stringvalidator.OneOf("tchap", "generic")},
	}
	attrs["homeserver_url"] = schema.StringAttribute{
		Optional: true, MarkdownDescription: "The homeserver's client API URL, `https://…`. Required with the `generic` preset, refused with `tchap`.",
		Validators: []validator.String{stringvalidator.RegexMatches(httpsURLPattern, "must be an https:// URL")},
	}
	attrs["user_id"] = schema.StringAttribute{
		Required: true, MarkdownDescription: "The bot's Matrix user id, `@name:server`. The bot logs in with it, never with an email address.",
		Validators: []validator.String{stringvalidator.RegexMatches(matrixUserIDPattern, "must be a Matrix user id, @name:server")},
	}
	attrs["password_secret_id"] = secretIDAttribute("The bot account's password, used only to create its device.")
	attrs["recovery_key_secret_id"] = secretIDAttribute("The account's recovery key (shown when its encryption was set up), to verify the bot's device.")
	attrs["extra_auth_hosts"] = allowlistAttribute("More hosts the login may need to reach, such as an authentication service on its own host.")
	attrs["allowed_rooms"] = allowlistAttribute("Room ids (`!…:server`) the bot answers in.")
	attrs["allowed_users"] = allowlistAttribute("User ids the bot answers, globs allowed (`@*:server`). Required for `direct_messages`.")
	attrs["auto_join"] = schema.StringAttribute{
		Optional: true, MarkdownDescription: "Which invitations the bot accepts: `allowlisted` (the platform's default), `invited` or `never`.",
		Validators: []validator.String{stringvalidator.OneOf("allowlisted", "invited", "never")},
	}
	attrs["direct_messages"] = schema.BoolAttribute{Optional: true, MarkdownDescription: "Whether the bot answers direct messages from `allowed_users`."}
	attrs["unencrypted_rooms"] = schema.StringAttribute{
		Optional: true, MarkdownDescription: "Whether the bot answers in unencrypted rooms: `never`, `allowlisted` (the platform's default) or `any`.",
		Validators: []validator.String{stringvalidator.OneOf("never", "allowlisted", "any")},
	}
	attrs["allow_external_inference"] = schema.BoolAttribute{
		Optional:            true,
		MarkdownDescription: "With the `tchap` preset, accept a model that is not served in the cluster: decrypted messages then leave it. Flagged in the console.",
	}
	attrs["relogin_trigger"] = schema.StringAttribute{
		Optional: true,
		MarkdownDescription: "Any value; changing it to a new value asks for one more login of the bot (the operator mounts the password and recovery key " +
			"for 30 minutes, or until the bot is back). Only sent while `relogin_required` is true and no re-login is pending; otherwise the " +
			"new value is recorded with a warning and nothing is sent. Removing it, or setting it at creation, does not log in again. " +
			"Needs `secret:access` on both Matrix secrets. Terraform-only: the platform does not store it.",
		Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
	}
	channelStatusAttributes(attrs)
	attrs["device_id"] = schema.StringAttribute{Computed: true, MarkdownDescription: "The bot's Matrix device, once it has logged in."}
	attrs["relogin_required"] = schema.BoolAttribute{
		Computed: true, MarkdownDescription: "The bot's session ended after its first device: change `relogin_trigger` to log it in again.",
	}
	attrs["relogin_pending"] = schema.BoolAttribute{Computed: true, MarkdownDescription: "A re-login was requested and the bot has not reconnected since."}
	resp.Schema = schema.Schema{
		MarkdownDescription: "The Matrix channel of an agent, Tchap included (`preset = \"tchap\"`): the bot is an ordinary account that logs in once, " +
			"keeps its device and reads encrypted rooms. " +
			"Creating it sets the channel and lists `matrix` in the agent's interfaces; destroying it removes both, and the agent stays. " +
			"The generic preset needs `allowed_rooms` or `allowed_users`. " + channelCommonNote,
		Attributes: attrs,
	}
}

func (r *agentMatrixChannelResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg agentMatrixChannelModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Preset.IsUnknown() {
		return
	}
	tchap := cfg.Preset.ValueString() == "tchap"
	if tchap {
		if !cfg.HomeserverURL.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("homeserver_url"), "homeserver_url with the tchap preset",
				"The `tchap` preset derives the homeserver from `user_id`; leave `homeserver_url` out.")
		}
		if !cfg.UserID.IsUnknown() && !cfg.UserID.IsNull() && !isTchapUserID(cfg.UserID.ValueString()) {
			resp.Diagnostics.AddAttributeError(path.Root("user_id"), "Not a Tchap account",
				"With the `tchap` preset, `user_id` must be on a server under `tchap.gouv.fr` (`@name:<server>.tchap.gouv.fr`).")
		}
	}
	if !tchap {
		if cfg.HomeserverURL.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("homeserver_url"), "Missing homeserver_url",
				"The generic preset needs the homeserver's URL; only `preset = \"tchap\"` derives it.")
		}
		if cfg.AllowedRooms.IsNull() && cfg.AllowedUsers.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("allowed_rooms"), "No allowlist",
				"A generic Matrix channel is open to the whole federation: set `allowed_rooms` or `allowed_users`.")
		}
		if !cfg.AllowExternalInference.IsNull() && !cfg.AllowExternalInference.IsUnknown() && cfg.AllowExternalInference.ValueBool() {
			resp.Diagnostics.AddAttributeError(path.Root("allow_external_inference"), "Tchap only",
				"`allow_external_inference` only overrides the `tchap` preset's rule.")
		}
	}
	if !cfg.DirectMessages.IsUnknown() && cfg.DirectMessages.ValueBool() && cfg.AllowedUsers.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("direct_messages"), "Direct messages need allowed_users",
			"Name who may write to the bot directly in `allowed_users`.")
	}
}

func (r *agentMatrixChannelResource) body(ctx context.Context, m agentMatrixChannelModel, diags *diag.Diagnostics) console.AiAgentMatrixChannelConfig {
	return console.AiAgentMatrixChannelConfig{
		Preset:                 enumPtr[console.MatrixPresetDto](m.Preset),
		HomeserverUrl:          stringPtr(m.HomeserverURL),
		UserId:                 stringPtr(m.UserID),
		PasswordSecretId:       uuidPtr(m.PasswordSecretID, diags),
		RecoveryKeySecretId:    uuidPtr(m.RecoveryKeySecretID, diags),
		ExtraAuthHosts:         sortedSetPtr(ctx, m.ExtraAuthHosts, diags),
		AllowedRooms:           sortedSetPtr(ctx, m.AllowedRooms, diags),
		AllowedUsers:           sortedSetPtr(ctx, m.AllowedUsers, diags),
		AutoJoin:               enumPtr[console.MatrixAutoJoinDto](m.AutoJoin),
		DirectMessages:         boolPtr(m.DirectMessages),
		UnencryptedRooms:       enumPtr[console.MatrixUnencryptedRoomsDto](m.UnencryptedRooms),
		AllowExternalInference: boolPtr(m.AllowExternalInference),
	}
}

func (r *agentMatrixChannelResource) toState(ctx context.Context, prior agentMatrixChannelModel, v console.AiAgentMatrixChannelView, diags *diag.Diagnostics) agentMatrixChannelModel {
	out := prior
	c := v.Config
	out.Preset = enumString(c.Preset)
	out.HomeserverURL = keepEquivalent(prior.HomeserverURL, c.HomeserverUrl, strings.TrimSpace)
	out.UserID = keepEquivalent(prior.UserID, c.UserId, strings.TrimSpace)
	out.PasswordSecretID = keepSecretID(prior.PasswordSecretID, c.PasswordSecretId)
	out.RecoveryKeySecretID = keepSecretID(prior.RecoveryKeySecretID, c.RecoveryKeySecretId)
	out.ExtraAuthHosts = keepEquivalentSet(ctx, prior.ExtraAuthHosts, derefStrings(c.ExtraAuthHosts), strings.TrimSpace, diags)
	out.AllowedRooms = keepEquivalentSet(ctx, prior.AllowedRooms, derefStrings(c.AllowedRooms), strings.TrimSpace, diags)
	out.AllowedUsers = keepEquivalentSet(ctx, prior.AllowedUsers, derefStrings(c.AllowedUsers), strings.TrimSpace, diags)
	out.AutoJoin = enumString(c.AutoJoin)
	out.DirectMessages = types.BoolPointerValue(c.DirectMessages)
	out.UnencryptedRooms = enumString(c.UnencryptedRooms)
	out.AllowExternalInference = keepFalseAsAbsent(prior.AllowExternalInference, c.AllowExternalInference)
	out.channelStatusModel = channelStatus(v.State)
	out.DeviceID = optNonEmpty(v.State.DeviceId)
	out.ReloginRequired = types.BoolValue(v.ReloginRequired)
	out.ReloginPending = types.BoolValue(v.ReloginPending)
	return out
}

func matrixState(v *console.AiAgentChannelsResponse) console.AiAgentChannelStateDto {
	return v.Matrix.State
}

func addMatrixWarnings(diags *diag.Diagnostics, v console.AiAgentMatrixChannelView) {
	addChannelWarnings(diags, "Matrix", v.State)
	if v.ReloginRequired && !v.ReloginPending {
		diags.AddWarning("The Matrix bot must log in again",
			"Its session ended after its first device. Change `relogin_trigger` to a new value and apply to request one more login.")
	}
}

func (r *agentMatrixChannelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan agentMatrixChannelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	body := r.body(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	// The first login needs no request: the operator logs the bot in until it
	// has a device. A trigger set at creation is only recorded.
	got, err := r.p.API.SetAiAgentMatrixChannel(ctx, r.p.OrgID, plan.AgentID.ValueString(), body)
	if err != nil {
		addChannelError(&resp.Diagnostics, "Failed to set the Matrix channel", err)
		return
	}
	got = r.settle(ctx, plan.AgentID.ValueString(), got, matrixState, &resp.Diagnostics)
	if got.Matrix.Config == nil {
		resp.Diagnostics.AddError("Matrix channel missing after set", "The console accepted the Matrix channel but does not report it.")
		return
	}
	plan.ID = plan.AgentID
	state := r.toState(ctx, plan, got.Matrix, &resp.Diagnostics)
	addMatrixWarnings(&resp.Diagnostics, got.Matrix)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *agentMatrixChannelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior agentMatrixChannelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.p.API.GetAiAgentChannels(ctx, r.p.OrgID, prior.AgentID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && got.Matrix.Config == nil) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addChannelError(&resp.Diagnostics, "Failed to read the Matrix channel", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, r.toState(ctx, prior, got.Matrix, &resp.Diagnostics))...)
}

func (r *agentMatrixChannelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior agentMatrixChannelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	body := r.body(ctx, plan, &resp.Diagnostics)
	before := r.body(ctx, prior, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	agentID := prior.AgentID.ValueString()
	var (
		got *console.AiAgentChannelsResponse
		err error
	)
	if !reflect.DeepEqual(body, before) {
		if got, err = r.p.API.SetAiAgentMatrixChannel(ctx, r.p.OrgID, agentID, body); err != nil {
			addChannelError(&resp.Diagnostics, "Failed to set the Matrix channel", err)
			return
		}
		got = r.settle(ctx, agentID, got, matrixState, &resp.Diagnostics)
	}
	plan.ID = prior.ID
	if triggerRequested(plan.ReloginTrigger, prior.ReloginTrigger) {
		if got == nil {
			if got, err = r.p.API.GetAiAgentChannels(ctx, r.p.OrgID, agentID); err != nil {
				addChannelError(&resp.Diagnostics, "Failed to read the Matrix channel", err)
				return
			}
		}
		// The platform takes a re-login only when the bot needs one and none is
		// pending. Otherwise there is nothing to ask for: the trigger is recorded
		// and the apply says why nothing was sent.
		switch v := got.Matrix; {
		case v.ReloginPending:
			resp.Diagnostics.AddWarning("A Matrix re-login is already pending",
				"No new re-login was requested: the bot has not reconnected since the last one. If it does not within 30 minutes, "+
					"the request lapses and `relogin_required` turns true again; change `relogin_trigger` then.")
		case !v.ReloginRequired:
			resp.Diagnostics.AddWarning("The Matrix bot does not need a re-login",
				"No re-login was requested: `relogin_required` is false (the bot is logged in, or still making its first login, "+
					"which needs no request). The new `relogin_trigger` is recorded.")
		default:
			relogged, err := r.p.API.ReloginAiAgentMatrixChannel(ctx, r.p.OrgID, agentID)
			if err != nil {
				// The configuration may already be applied: record it, keep the old
				// trigger so the next apply asks again, and say why it was refused.
				plan.ReloginTrigger = prior.ReloginTrigger
				r.saveAfterFailedRelogin(ctx, plan, got, agentID, resp)
				var refused *client.RefusedError
				if errors.As(err, &refused) && !client.IsConflict(err) {
					resp.Diagnostics.AddAttributeError(path.Root("relogin_trigger"), "The Matrix re-login was refused",
						refused.Message+"\n\nThe trigger was not recorded: the next apply asks again. A re-login is possible "+
							"while `relogin_required` is true and none is pending.")
					return
				}
				addChannelError(&resp.Diagnostics, "Failed to request a Matrix re-login", err)
				return
			}
			got = relogged
		}
	}
	if got == nil {
		if got, err = r.p.API.GetAiAgentChannels(ctx, r.p.OrgID, agentID); err != nil {
			addChannelError(&resp.Diagnostics, "Failed to read the Matrix channel", err)
			return
		}
	}
	if got.Matrix.Config == nil {
		resp.Diagnostics.AddError("Matrix channel missing", "The console does not report the Matrix channel of this agent any more.")
		return
	}
	state := r.toState(ctx, plan, got.Matrix, &resp.Diagnostics)
	addMatrixWarnings(&resp.Diagnostics, got.Matrix)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// saveAfterFailedRelogin records what the apply did change before the
// re-login was refused.
func (r *agentMatrixChannelResource) saveAfterFailedRelogin(ctx context.Context, plan agentMatrixChannelModel, got *console.AiAgentChannelsResponse, agentID string, resp *resource.UpdateResponse) {
	if got == nil {
		var err error
		if got, err = r.p.API.GetAiAgentChannels(ctx, r.p.OrgID, agentID); err != nil || got.Matrix.Config == nil {
			return
		}
	}
	var diags diag.Diagnostics
	state := r.toState(ctx, plan, got.Matrix, &diags)
	if !diags.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	}
}

func (r *agentMatrixChannelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state agentMatrixChannelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.remove(ctx, state.AgentID.ValueString(), &resp.Diagnostics)
}

// isTchapUserID mirrors the platform's rule: the MXID's server name, without a
// port, is a host under tchap.gouv.fr (not tchap.gouv.fr itself).
func isTchapUserID(mxid string) bool {
	_, server, ok := strings.Cut(strings.TrimSpace(mxid), ":")
	if !ok {
		return false
	}
	if host, port, found := strings.Cut(server, ":"); found && port != "" && strings.Trim(port, "0123456789") == "" {
		server = host
	}
	server = strings.ToLower(strings.TrimSuffix(server, "."))
	rest, found := strings.CutSuffix(server, "tchap.gouv.fr")
	return found && len(rest) > 1 && strings.HasSuffix(rest, ".")
}
