// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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

// agent_channel_common.go — what the chat channel resources share. Each
// channel (Matrix/Tchap, Slack, Discord) is its own resource on an agent, like
// the widget: the console gives each its own PUT (replace the block, list the
// interface) and DELETE (null the block, drop the interface), and every write
// answers the whole channels view. Secrets are referenced by the id of a
// plaintext hyperfluid_secret of the organization; no secret value is ever
// sent or stored. The status is the operator's and moves on its own, so it is
// computed with no plan modifier: a refresh records it and plans nothing.

// channelStatusModel is the status every channel resource reports.
type channelStatusModel struct {
	Configured  types.Bool   `tfsdk:"configured"`
	Phase       types.String `tfsdk:"phase"`
	Identity    types.String `tfsdk:"identity"`
	Message     types.String `tfsdk:"message"`
	LastEventAt types.String `tfsdk:"last_event_at"`
}

// agentChannelBase is the part of a channel resource that does not depend on
// the channel's own configuration.
type agentChannelBase struct {
	p       *providerData
	channel client.AgentChannel
	display string // "Matrix", "Slack", "Discord"
}

func (b *agentChannelBase) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "expected *providerData")
		return
	}
	b.p = pd
}

// ImportState takes the agent id, like the widget.
func (b *agentChannelBase) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importByAgentID(ctx, req, resp)
}

func importByAgentID(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !uuidPattern.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import id", fmt.Sprintf("expected the agent id (a UUID), got %q", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("agent_id"), req.ID)...)
}

// remove is the shared Delete: the channel's block and interface go, the agent
// stays.
func (b *agentChannelBase) remove(ctx context.Context, agentID string, diags *diag.Diagnostics) {
	if err := b.p.API.RemoveAiAgentChannel(ctx, b.p.OrgID, agentID, b.channel); err != nil {
		addChannelError(diags, "Failed to remove the "+b.display+" channel", err)
	}
}

// agentIDAttributes are the id and agent_id every per-agent resource of this
// family carries.
func agentIDAttributes(what string) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed: true, MarkdownDescription: "The agent id.",
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"agent_id": schema.StringAttribute{
			Required: true, MarkdownDescription: "Id of the `hyperfluid_agent`. Changing this forces a new " + what + ".",
			Validators:    []validator.String{isUUID()},
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		},
	}
}

// channelStatusAttributes are computed on purpose without plan modifiers: they
// are the operator's view, which changes between applies, so an update plans
// them unknown and a refresh simply records them.
func channelStatusAttributes(attrs map[string]schema.Attribute) {
	computed := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	attrs["configured"] = schema.BoolAttribute{
		Computed: true, MarkdownDescription: "Whether the channel is active: its configuration is present and its interface listed on the agent.",
	}
	attrs["phase"] = computed("The gateway's phase for this channel: `Connecting`, `Connected`, `NeedsLogin`, `Degraded` or `Disabled`. " +
		"Empty until the operator has reported it. Refreshed on read, it never plans a change on its own.")
	attrs["identity"] = computed("Who the bot is on the network, as the gateway reports it.")
	attrs["message"] = computed("The gateway's detail on the current phase.")
	attrs["last_event_at"] = computed("When the gateway last saw an event, RFC 3339.")
}

// secretIDAttribute is a required reference to a plaintext secret: the id
// only, never the value.
func secretIDAttribute(desc string) schema.StringAttribute {
	return schema.StringAttribute{
		Required:            true,
		MarkdownDescription: desc + " The id of a plaintext `hyperfluid_secret` of the organization: the value is never sent or stored by Terraform.",
		Validators:          []validator.String{isUUID()},
	}
}

// allowlistAttribute is an optional, unordered allowlist.
func allowlistAttribute(desc string) schema.SetAttribute {
	return schema.SetAttribute{
		ElementType: types.StringType, Optional: true, MarkdownDescription: desc,
		Validators: []validator.Set{
			setvalidator.SizeAtLeast(1),
			setvalidator.ValueStringsAre(stringvalidator.RegexMatches(notBlankPattern, "must not be blank")),
		},
	}
}

func channelStatus(st console.AiAgentChannelStateDto) channelStatusModel {
	phase := types.StringNull()
	if st.Phase != nil {
		phase = types.StringValue(string(*st.Phase))
	}
	last := types.StringNull()
	if st.LastEventAt != nil {
		last = types.StringValue(st.LastEventAt.UTC().Format(time.RFC3339))
	}
	return channelStatusModel{
		Configured: types.BoolValue(st.Configured), Phase: phase,
		Identity: optNonEmpty(st.Identity), Message: optNonEmpty(st.Message), LastEventAt: last,
	}
}

// addChannelWarnings says, on the apply that sets the channel, what the
// gateway reports as wrong; a channel is never waited for, since connecting
// depends on the network on the other side.
func addChannelWarnings(diags *diag.Diagnostics, display string, st console.AiAgentChannelStateDto) {
	if st.Phase == nil {
		return
	}
	switch *st.Phase {
	case console.AiAgentChannelPhaseDtoNeedsLogin, console.AiAgentChannelPhaseDtoDegraded:
		detail := "The gateway reports " + string(*st.Phase) + "."
		if st.Message != nil && *st.Message != "" {
			detail += " " + *st.Message
		}
		diags.AddWarning(fmt.Sprintf("The %s channel is not connected", display), detail)
	}
}

// addChannelError reports a failed channel call; a refusal is the platform's
// verdict, shown in its own words.
func addChannelError(diags *diag.Diagnostics, summary string, err error) {
	var refused *client.RefusedError
	if errors.As(err, &refused) {
		diags.AddError("The platform refused: "+strings.TrimPrefix(summary, "Failed to "),
			refused.Message+"\n\n"+
				"A chat channel is refused on a capability, on an agent whose tools include general `hfctl` unless "+
				"`allow_platform_tools` is set on the agent, and for a secret that is not a plaintext secret of this organization.")
		return
	}
	addGatedError(diags, summary, featureAiAgents, err)
}

// ── value mapping ──────────────────────────────────────────────────────────

func uuidString(u *uuid.UUID) *string {
	if u == nil {
		return nil
	}
	s := u.String()
	return &s
}

// keepSecretID keeps the configured spelling of a secret id the console
// returns lowercased.
func keepSecretID(prior types.String, api *uuid.UUID) types.String {
	return keepEquivalent(prior, uuidString(api), strings.ToLower)
}

// keepFalseAsAbsent maps a flag the console stores as absent when false: a
// configured false stays false.
func keepFalseAsAbsent(prior types.Bool, api *bool) types.Bool {
	if (api == nil || !*api) && !prior.IsNull() && !prior.IsUnknown() && !prior.ValueBool() {
		return prior
	}
	if api == nil {
		return types.BoolNull()
	}
	return types.BoolValue(*api)
}

func enumPtr[T ~string](v types.String) *T {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	e := T(v.ValueString())
	return &e
}

func enumString[T ~string](v *T) types.String {
	if v == nil {
		return types.StringNull()
	}
	return types.StringValue(string(*v))
}

var matrixUserIDPattern = regexp.MustCompile(`^\s*@[^:\s]+:\S+\s*$`)
