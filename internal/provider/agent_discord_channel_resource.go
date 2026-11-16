// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

var (
	_ resource.Resource                = &agentDiscordChannelResource{}
	_ resource.ResourceWithConfigure   = &agentDiscordChannelResource{}
	_ resource.ResourceWithImportState = &agentDiscordChannelResource{}
)

func NewAgentDiscordChannelResource() resource.Resource {
	return &agentDiscordChannelResource{agentChannelBase{channel: client.ChannelDiscord, display: "Discord"}}
}

type agentDiscordChannelResource struct{ agentChannelBase }

type agentDiscordChannelModel struct {
	ID               types.String `tfsdk:"id"`
	AgentID          types.String `tfsdk:"agent_id"`
	BotTokenSecretID types.String `tfsdk:"bot_token_secret_id"`
	AllowedGuilds    types.Set    `tfsdk:"allowed_guilds"`
	AllowedChannels  types.Set    `tfsdk:"allowed_channels"`
	AllowedUsers     types.Set    `tfsdk:"allowed_users"`
	DirectMessages   types.Bool   `tfsdk:"direct_messages"`
	channelStatusModel
}

func (r *agentDiscordChannelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent_discord_channel"
}

func (r *agentDiscordChannelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := agentIDAttributes("Discord channel")
	attrs["bot_token_secret_id"] = secretIDAttribute("The Discord application's bot token.")
	attrs["allowed_guilds"] = schema.SetAttribute{
		ElementType: types.StringType, Required: true,
		MarkdownDescription: "Ids of the Discord servers (guilds) the bot serves, checked on every message; it leaves any other server it is added to.",
		Validators: []validator.Set{
			setvalidator.SizeAtLeast(1),
			setvalidator.ValueStringsAre(stringvalidator.RegexMatches(discordIDPattern, "must be a Discord id (digits)")),
		},
	}
	attrs["allowed_channels"] = allowlistAttribute("Channel ids the bot answers in. Left out, every channel of the allowed servers.")
	attrs["allowed_users"] = allowlistAttribute("User ids the bot answers. Left out, every member of the allowed servers.")
	attrs["direct_messages"] = schema.BoolAttribute{Optional: true, MarkdownDescription: "Whether the bot answers direct messages. Left out, the platform's default (off)."}
	channelStatusAttributes(attrs)
	resp.Schema = schema.Schema{
		MarkdownDescription: "The Discord channel of an agent: a bot on the Discord gateway, so no public endpoint is needed. It answers a mention or a " +
			"reply that pings it (it does not request the privileged message content intent). " +
			"Creating it sets the channel and lists `discord` in the agent's interfaces; destroying it removes both, and the agent stays. " +
			channelCommonNote,
		Attributes: attrs,
	}
}

func (r *agentDiscordChannelResource) body(ctx context.Context, m agentDiscordChannelModel, diags *diag.Diagnostics) console.AiAgentDiscordChannelConfig {
	body := console.AiAgentDiscordChannelConfig{
		BotTokenSecretId: uuidPtr(m.BotTokenSecretID, diags),
		AllowedGuilds:    sortedSetPtr(ctx, m.AllowedGuilds, diags),
		AllowedChannels:  sortedSetPtr(ctx, m.AllowedChannels, diags),
		AllowedUsers:     sortedSetPtr(ctx, m.AllowedUsers, diags),
		DirectMessages:   boolPtr(m.DirectMessages),
	}
	return body
}

func (r *agentDiscordChannelResource) toState(ctx context.Context, prior agentDiscordChannelModel, v console.AiAgentDiscordChannelView, diags *diag.Diagnostics) agentDiscordChannelModel {
	out := prior
	c := v.Config
	out.BotTokenSecretID = keepSecretID(prior.BotTokenSecretID, c.BotTokenSecretId)
	out.AllowedGuilds = keepEquivalentSet(ctx, prior.AllowedGuilds, derefStrings(c.AllowedGuilds), strings.TrimSpace, diags)
	out.AllowedChannels = keepEquivalentSet(ctx, prior.AllowedChannels, derefStrings(c.AllowedChannels), strings.TrimSpace, diags)
	out.AllowedUsers = keepEquivalentSet(ctx, prior.AllowedUsers, derefStrings(c.AllowedUsers), strings.TrimSpace, diags)
	out.DirectMessages = types.BoolPointerValue(c.DirectMessages)
	out.channelStatusModel = channelStatus(v.State)
	return out
}

func (r *agentDiscordChannelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan agentDiscordChannelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	body := r.body(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.p.API.SetAiAgentDiscordChannel(ctx, r.p.OrgID, plan.AgentID.ValueString(), body)
	if err != nil {
		addChannelError(&resp.Diagnostics, "Failed to set the Discord channel", err)
		return
	}
	if got.Discord.Config == nil {
		resp.Diagnostics.AddError("Discord channel missing after set", "The console accepted the Discord channel but does not report it.")
		return
	}
	plan.ID = plan.AgentID
	state := r.toState(ctx, plan, got.Discord, &resp.Diagnostics)
	addChannelWarnings(&resp.Diagnostics, r.display, got.Discord.State)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *agentDiscordChannelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior agentDiscordChannelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.p.API.GetAiAgentChannels(ctx, r.p.OrgID, prior.AgentID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && got.Discord.Config == nil) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addChannelError(&resp.Diagnostics, "Failed to read the Discord channel", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, r.toState(ctx, prior, got.Discord, &resp.Diagnostics))...)
}

func (r *agentDiscordChannelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior agentDiscordChannelModel
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
	if reflect.DeepEqual(body, before) {
		got, err = r.p.API.GetAiAgentChannels(ctx, r.p.OrgID, agentID)
	} else {
		got, err = r.p.API.SetAiAgentDiscordChannel(ctx, r.p.OrgID, agentID, body)
	}
	if err != nil {
		addChannelError(&resp.Diagnostics, "Failed to set the Discord channel", err)
		return
	}
	if got.Discord.Config == nil {
		resp.Diagnostics.AddError("Discord channel missing", "The console does not report the Discord channel of this agent any more.")
		return
	}
	plan.ID = prior.ID
	state := r.toState(ctx, plan, got.Discord, &resp.Diagnostics)
	addChannelWarnings(&resp.Diagnostics, r.display, got.Discord.State)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *agentDiscordChannelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state agentDiscordChannelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.remove(ctx, state.AgentID.ValueString(), &resp.Diagnostics)
}

var discordIDPattern = regexp.MustCompile(`^\s*[0-9]+\s*$`)
