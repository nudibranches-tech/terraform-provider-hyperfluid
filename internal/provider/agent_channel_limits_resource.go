// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// agent_channel_limits_resource.go — the limits an agent applies to every
// audience outside the console: the web widget and the chat channels. They are
// the agent's override of the runtime's defaults, so destroying the resource
// resets them, like hyperfluid_alert_threshold.

var (
	_ resource.Resource                   = &agentChannelLimitsResource{}
	_ resource.ResourceWithConfigure      = &agentChannelLimitsResource{}
	_ resource.ResourceWithImportState    = &agentChannelLimitsResource{}
	_ resource.ResourceWithValidateConfig = &agentChannelLimitsResource{}
)

func NewAgentChannelLimitsResource() resource.Resource {
	return &agentChannelLimitsResource{}
}

type agentChannelLimitsResource struct{ agentChannelBase }

type agentChannelLimitsModel struct {
	ID                     types.String `tfsdk:"id"`
	AgentID                types.String `tfsdk:"agent_id"`
	QuestionsPerHour       types.Int64  `tfsdk:"questions_per_hour"`
	QuestionsPerDay        types.Int64  `tfsdk:"questions_per_day"`
	PerSenderBurst         types.Int64  `tfsdk:"per_sender_burst"`
	PerSenderRefillSeconds types.Int64  `tfsdk:"per_sender_refill_seconds"`
	MaxQuestionLength      types.Int64  `tfsdk:"max_question_length"`
	MaxConcurrent          types.Int64  `tfsdk:"max_concurrent"`
}

var channelLimitNames = []string{
	"questions_per_hour", "questions_per_day", "per_sender_burst", "per_sender_refill_seconds", "max_question_length", "max_concurrent",
}

func (r *agentChannelLimitsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent_channel_limits"
}

func (r *agentChannelLimitsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	limit := func(desc string, maxV int64) schema.Int64Attribute {
		var v validator.Int64 = int64validator.AtLeast(1)
		if maxV > 0 {
			v = int64validator.Between(1, maxV)
		}
		return schema.Int64Attribute{Optional: true, MarkdownDescription: desc + " Left out, the runtime's default.", Validators: []validator.Int64{v}}
	}
	attrs := agentIDAttributes("set of limits")
	attrs["questions_per_hour"] = limit("Questions the agent answers per hour, per pool (the widget and the chat channels are counted apart).", 0)
	attrs["questions_per_day"] = limit("Questions the agent answers per day, per pool.", 0)
	attrs["per_sender_burst"] = limit("Questions one sender may ask in a burst.", 0)
	attrs["per_sender_refill_seconds"] = limit("Seconds for one sender to earn one more question.", 0)
	attrs["max_question_length"] = limit("Longest question from a chat channel, at most 4000 characters (the widget has its own).", 4000)
	attrs["max_concurrent"] = limit("Questions the agent answers at the same time.", 0)
	resp.Schema = schema.Schema{
		MarkdownDescription: "The limits an agent applies to its audiences outside the console (the web widget, Matrix/Tchap, Slack, Discord): " +
			"hourly and daily ceilings, the per-sender bucket, the question length and the concurrency. Each must be at least 1. " +
			"A limit left out takes the runtime's default; every change sends all of them again. Destroying the resource resets the " +
			"agent to the defaults. Limits reset outside Terraform leave the state. Imported by agent id. " +
			"Needs the organization's `sapience_agent_enabled` feature.",
		Attributes: attrs,
	}
}

func (r *agentChannelLimitsResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg agentChannelLimitsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, v := range []types.Int64{cfg.QuestionsPerHour, cfg.QuestionsPerDay, cfg.PerSenderBurst, cfg.PerSenderRefillSeconds, cfg.MaxQuestionLength, cfg.MaxConcurrent} {
		if !v.IsNull() {
			return
		}
	}
	resp.Diagnostics.AddAttributeError(path.Root("questions_per_hour"), "No limit set",
		"Set at least one of "+joinCode(channelLimitNames)+". To keep the runtime's defaults, remove the resource instead.")
}

func joinCode(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += "`" + n + "`"
	}
	return out
}

func channelLimitsBody(m agentChannelLimitsModel) console.AiAgentChannelLimitsDto {
	return console.AiAgentChannelLimitsDto{
		QuestionsPerHour: int32PtrFromInt64(m.QuestionsPerHour), QuestionsPerDay: int32PtrFromInt64(m.QuestionsPerDay),
		PerSenderBurst: int32PtrFromInt64(m.PerSenderBurst), PerSenderRefillSeconds: int32PtrFromInt64(m.PerSenderRefillSeconds),
		MaxQuestionLength: int32PtrFromInt64(m.MaxQuestionLength), MaxConcurrent: int32PtrFromInt64(m.MaxConcurrent),
	}
}

func channelLimitsState(prior agentChannelLimitsModel, l console.AiAgentChannelLimitsDto) agentChannelLimitsModel {
	prior.QuestionsPerHour = optInt64FromInt32(l.QuestionsPerHour)
	prior.QuestionsPerDay = optInt64FromInt32(l.QuestionsPerDay)
	prior.PerSenderBurst = optInt64FromInt32(l.PerSenderBurst)
	prior.PerSenderRefillSeconds = optInt64FromInt32(l.PerSenderRefillSeconds)
	prior.MaxQuestionLength = optInt64FromInt32(l.MaxQuestionLength)
	prior.MaxConcurrent = optInt64FromInt32(l.MaxConcurrent)
	return prior
}

func (r *agentChannelLimitsResource) apply(ctx context.Context, plan agentChannelLimitsModel, id types.String, state *agentChannelLimitsModel) error {
	got, err := r.p.API.SetAiAgentChannelLimits(ctx, r.p.OrgID, plan.AgentID.ValueString(), channelLimitsBody(plan))
	if err != nil {
		return err
	}
	if got.ChannelLimits == nil {
		return errors.New("the console accepted the limits but does not report them")
	}
	plan.ID = id
	*state = channelLimitsState(plan, *got.ChannelLimits)
	return nil
}

func (r *agentChannelLimitsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, state agentChannelLimitsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, plan, plan.AgentID, &state); err != nil {
		addChannelError(&resp.Diagnostics, "Failed to set the channel limits", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *agentChannelLimitsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior agentChannelLimitsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.p.API.GetAiAgentChannels(ctx, r.p.OrgID, prior.AgentID.ValueString())
	if errors.Is(err, client.ErrNotFound) || (err == nil && got.ChannelLimits == nil) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addChannelError(&resp.Diagnostics, "Failed to read the channel limits", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, channelLimitsState(prior, *got.ChannelLimits))...)
}

func (r *agentChannelLimitsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior, state agentChannelLimitsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, plan, prior.ID, &state); err != nil {
		addChannelError(&resp.Diagnostics, "Failed to set the channel limits", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *agentChannelLimitsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state agentChannelLimitsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.p.API.ResetAiAgentChannelLimits(ctx, r.p.OrgID, state.AgentID.ValueString()); err != nil {
		addChannelError(&resp.Diagnostics, "Failed to reset the channel limits", err)
	}
}
