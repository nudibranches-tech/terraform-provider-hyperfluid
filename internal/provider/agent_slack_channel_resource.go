// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"

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
	_ resource.Resource                = &agentSlackChannelResource{}
	_ resource.ResourceWithConfigure   = &agentSlackChannelResource{}
	_ resource.ResourceWithImportState = &agentSlackChannelResource{}
)

func NewAgentSlackChannelResource() resource.Resource {
	return &agentSlackChannelResource{agentChannelBase{channel: client.ChannelSlack, display: "Slack"}}
}

type agentSlackChannelResource struct{ agentChannelBase }

type agentSlackChannelModel struct {
	ID               types.String `tfsdk:"id"`
	AgentID          types.String `tfsdk:"agent_id"`
	BotTokenSecretID types.String `tfsdk:"bot_token_secret_id"`
	AppTokenSecretID types.String `tfsdk:"app_token_secret_id"`
	TeamID           types.String `tfsdk:"team_id"`
	AllowedChannels  types.Set    `tfsdk:"allowed_channels"`
	AllowedUsers     types.Set    `tfsdk:"allowed_users"`
	DirectMessages   types.Bool   `tfsdk:"direct_messages"`
	channelStatusModel
}

func (r *agentSlackChannelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent_slack_channel"
}

func (r *agentSlackChannelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := agentIDAttributes("Slack channel")
	attrs["bot_token_secret_id"] = secretIDAttribute("The Slack app's bot token (`xoxb-…`).")
	attrs["app_token_secret_id"] = secretIDAttribute("The Slack app's app-level token (`xapp-…`, scope `connections:write`), for Socket Mode.")
	attrs["team_id"] = schema.StringAttribute{
		Required: true, MarkdownDescription: "The Slack workspace id (`T…`). Events from any other workspace, Slack Connect included, are dropped.",
		Validators: []validator.String{stringvalidator.RegexMatches(notBlankPattern, "must not be blank")},
	}
	attrs["allowed_channels"] = allowlistAttribute("Channel ids the bot answers in. Left out, every channel it is invited to.")
	attrs["allowed_users"] = allowlistAttribute("User ids the bot answers. Left out, everyone in the workspace.")
	attrs["direct_messages"] = schema.BoolAttribute{Optional: true, MarkdownDescription: "Whether the bot answers direct messages. Left out, the platform's default (off)."}
	channelStatusAttributes(attrs)
	resp.Schema = schema.Schema{
		MarkdownDescription: "The Slack channel of an agent: a Slack app in Socket Mode, so no public endpoint is needed. " +
			"Creating it sets the channel and lists `slack` in the agent's interfaces; destroying it removes both, and the agent stays. " +
			"Create the Slack app from the `hyperfluid_agent_slack_manifest` data source, and store its two tokens as plaintext secrets. " +
			channelCommonNote,
		Attributes: attrs,
	}
}

// channelCommonNote is said on every channel resource page.
const channelCommonNote = "Every change sends the whole channel configuration again: an argument removed from the configuration returns to the " +
	"platform's default. A channel removed outside Terraform leaves the state, and the next apply sets it again. " +
	"The status attributes (`phase`, `identity`, `message`, `last_event_at`) are the gateway's and are refreshed on read without planning changes. " +
	"The platform refuses a channel on a capability, and on an agent whose tools include general `hfctl` unless the agent sets `allow_platform_tools`. " +
	"Imported by agent id. Needs the organization's `sapience_agent_enabled` feature."

func (r *agentSlackChannelResource) body(ctx context.Context, m agentSlackChannelModel, diags *diag.Diagnostics) console.AiAgentSlackChannelConfig {
	body := console.AiAgentSlackChannelConfig{
		BotTokenSecretId: uuidPtr(m.BotTokenSecretID, diags),
		AppTokenSecretId: uuidPtr(m.AppTokenSecretID, diags),
		TeamId:           stringPtr(m.TeamID),
		AllowedChannels:  sortedSetPtr(ctx, m.AllowedChannels, diags),
		AllowedUsers:     sortedSetPtr(ctx, m.AllowedUsers, diags),
		DirectMessages:   boolPtr(m.DirectMessages),
	}
	return body
}

func (r *agentSlackChannelResource) toState(ctx context.Context, prior agentSlackChannelModel, v console.AiAgentSlackChannelView, diags *diag.Diagnostics) agentSlackChannelModel {
	out := prior
	c := v.Config
	out.BotTokenSecretID = keepSecretID(prior.BotTokenSecretID, c.BotTokenSecretId)
	out.AppTokenSecretID = keepSecretID(prior.AppTokenSecretID, c.AppTokenSecretId)
	out.TeamID = keepEquivalent(prior.TeamID, c.TeamId, strings.TrimSpace)
	out.AllowedChannels = keepEquivalentSet(ctx, prior.AllowedChannels, derefStrings(c.AllowedChannels), strings.TrimSpace, diags)
	out.AllowedUsers = keepEquivalentSet(ctx, prior.AllowedUsers, derefStrings(c.AllowedUsers), strings.TrimSpace, diags)
	out.DirectMessages = types.BoolPointerValue(c.DirectMessages)
	out.channelStatusModel = channelStatus(v.State)
	return out
}

func (r *agentSlackChannelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan agentSlackChannelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	body := r.body(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.p.API.SetAiAgentSlackChannel(ctx, r.p.OrgID, plan.AgentID.ValueString(), body)
	if err != nil {
		addChannelError(&resp.Diagnostics, "Failed to set the Slack channel", err)
		return
	}
	if got.Slack.Config == nil {
		resp.Diagnostics.AddError("Slack channel missing after set", "The console accepted the Slack channel but does not report it.")
		return
	}
	plan.ID = plan.AgentID
	state := r.toState(ctx, plan, got.Slack, &resp.Diagnostics)
	addChannelWarnings(&resp.Diagnostics, r.display, got.Slack.State)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *agentSlackChannelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior agentSlackChannelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.p.API.GetAiAgentChannels(ctx, r.p.OrgID, prior.AgentID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && got.Slack.Config == nil) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addChannelError(&resp.Diagnostics, "Failed to read the Slack channel", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, r.toState(ctx, prior, got.Slack, &resp.Diagnostics))...)
}

func (r *agentSlackChannelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior agentSlackChannelModel
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
		got, err = r.p.API.SetAiAgentSlackChannel(ctx, r.p.OrgID, agentID, body)
	}
	if err != nil {
		addChannelError(&resp.Diagnostics, "Failed to set the Slack channel", err)
		return
	}
	if got.Slack.Config == nil {
		resp.Diagnostics.AddError("Slack channel missing", "The console does not report the Slack channel of this agent any more.")
		return
	}
	plan.ID = prior.ID
	state := r.toState(ctx, plan, got.Slack, &resp.Diagnostics)
	addChannelWarnings(&resp.Diagnostics, r.display, got.Slack.State)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *agentSlackChannelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state agentSlackChannelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.remove(ctx, state.AgentID.ValueString(), &resp.Diagnostics)
}

// sortedSetPtr is a set as a request list, in a stable order so two plans of
// the same set compare equal.
func sortedSetPtr(ctx context.Context, s types.Set, diags *diag.Diagnostics) *[]string {
	out := strSetPtr(ctx, s, diags)
	if out != nil {
		slices.Sort(*out)
	}
	return out
}
