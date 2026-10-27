// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

var (
	_ resource.Resource                = &agentResource{}
	_ resource.ResourceWithConfigure   = &agentResource{}
	_ resource.ResourceWithImportState = &agentResource{}
)

const agentWaitTimeout = 10 * time.Minute

func NewAgentResource() resource.Resource {
	return &agentResource{}
}

type agentResource struct {
	p        *providerData
	interval time.Duration
}

type agentModel struct {
	ID               types.String `tfsdk:"id"`
	Env              types.String `tfsdk:"env"`
	Name             types.String `tfsdk:"name"`
	Description      types.String `tfsdk:"description"`
	SystemPrompt     types.String `tfsdk:"system_prompt"`
	Mission          types.String `tfsdk:"mission"`
	Context          types.String `tfsdk:"context"`
	Speciality       types.String `tfsdk:"speciality"`
	SubagentOf       types.String `tfsdk:"subagent_of"`
	QuestionSet      types.List   `tfsdk:"question_set"`
	Tools            types.List   `tfsdk:"tools"`
	Inference        types.Object `tfsdk:"inference"`
	Budgets          types.Object `tfsdk:"budgets"`
	DataSources      types.List   `tfsdk:"data_sources"`
	ServiceAccountID types.String `tfsdk:"service_account_id"`
	DesiredState     types.String `tfsdk:"desired_state"`
	Interfaces       types.Set    `tfsdk:"interfaces"`
	Mail             types.Object `tfsdk:"mail"`

	Slug          types.String `tfsdk:"slug"`
	Phase         types.String `tfsdk:"phase"`
	StatusMessage types.String `tfsdk:"status_message"`
	MailAddress   types.String `tfsdk:"mail_address"`
}

type agentInferenceModel struct {
	Provider       types.String `tfsdk:"provider"`
	Model          types.String `tfsdk:"model"`
	APIKeySecretID types.String `tfsdk:"api_key_secret_id"`
	EndpointURL    types.String `tfsdk:"endpoint_url"`
}

type agentBudgetsModel struct {
	MaxTurns            types.Int64 `tfsdk:"max_turns"`
	MaxCompletionTokens types.Int64 `tfsdk:"max_completion_tokens"`
}

type agentDockSourceModel struct {
	DataDockID types.String `tfsdk:"data_dock_id"`
	Slug       types.String `tfsdk:"slug"`
	Tables     types.Set    `tfsdk:"tables"`
}

type agentMailModel struct {
	Mode             types.String `tfsdk:"mode"`
	DisplayName      types.String `tfsdk:"display_name"`
	AllowedSenders   types.Set    `tfsdk:"allowed_senders"`
	FromAddress      types.String `tfsdk:"from_address"`
	Username         types.String `tfsdk:"username"`
	PasswordSecretID types.String `tfsdk:"password_secret_id"`
	ImapHost         types.String `tfsdk:"imap_host"`
	ImapPort         types.Int64  `tfsdk:"imap_port"`
	SMTPHost         types.String `tfsdk:"smtp_host"`
	SMTPPort         types.Int64  `tfsdk:"smtp_port"`
	UseTLS           types.Bool   `tfsdk:"use_tls"`
}

var (
	agentInferenceType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"provider": types.StringType, "model": types.StringType, "api_key_secret_id": types.StringType, "endpoint_url": types.StringType,
	}}
	agentBudgetsType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"max_turns": types.Int64Type, "max_completion_tokens": types.Int64Type,
	}}
	agentDockSourceType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"data_dock_id": types.StringType, "slug": types.StringType, "tables": types.SetType{ElemType: types.StringType},
	}}
	agentMailType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"mode": types.StringType, "display_name": types.StringType, "allowed_senders": types.SetType{ElemType: types.StringType},
		"from_address": types.StringType, "username": types.StringType, "password_secret_id": types.StringType,
		"imap_host": types.StringType, "imap_port": types.Int64Type, "smtp_host": types.StringType, "smtp_port": types.Int64Type,
		"use_tls": types.BoolType,
	}}
)

func (r *agentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent"
}

func (r *agentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	optStr := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, MarkdownDescription: desc, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}}
	}
	uuidStr := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, MarkdownDescription: desc, Validators: []validator.String{isUUID()}}
	}
	port := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Optional: true, MarkdownDescription: desc, Validators: []validator.Int64{int64validator.Between(1, 65535)}}
	}
	computedStr := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Sapience AI agent in an environment; a capability is one with a `speciality` and a `subagent_of`. " +
			"Needs the organization's `sapience_agent_enabled` feature.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"env": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Environment id. Changing this forces a new agent.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Name, unique in the organization; a capability may omit it to be named after its agent. Changing this forces a new agent.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"description": optStr("Free-form description."),
			"system_prompt": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "System prompt; omitted, a capability the platform offers gets its own.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"mission":     optStr("A capability's own instructions, added to its system prompt."),
			"context":     optStr("Extra context appended to the system prompt."),
			"speciality":  optStr("What the agent is for: `routing`, `platform-ops`, `data-sql`, `documents` or your own value."),
			"subagent_of": optStr("Slug of the routing agent this capability answers for."),
			"question_set": schema.ListAttribute{
				ElementType: types.StringType, Optional: true, MarkdownDescription: "Questions the agent asks to understand a request.",
				Validators: []validator.List{listvalidator.SizeAtLeast(1)},
			},
			"tools": schema.ListAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				MarkdownDescription: "Tools: `ask_sender`, `hfctl` or `hfctl:<command>`; `[]` allows all, and omitted takes the speciality's own set.",
				PlanModifiers:       []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"inference": schema.SingleNestedAttribute{
				Required:            true,
				MarkdownDescription: "Model the agent runs on.",
				Attributes: map[string]schema.Attribute{
					"provider": schema.StringAttribute{
						Required: true, MarkdownDescription: "`sapience` or `openrouter`.",
						Validators: []validator.String{stringvalidator.OneOf("sapience", "openrouter", "custom")},
					},
					"model":             optStr("Model: `sapience:shared/<name>`, `sapience:org/<name>` or an OpenRouter model id."),
					"api_key_secret_id": uuidStr("Id of the secret holding the OpenRouter API key."),
					"endpoint_url":      optStr("Base URL of a `custom` provider, for tests."),
				},
			},
			"budgets": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Spending limits per question; omitted, the runtime derives them from the model.",
				Attributes: map[string]schema.Attribute{
					"max_turns": schema.Int64Attribute{
						Optional: true, MarkdownDescription: "Tool-call round trips per question.",
						Validators: []validator.Int64{
							int64validator.AtLeast(1),
							int64validator.AtLeastOneOf(path.MatchRelative().AtParent().AtName("max_completion_tokens")),
						},
					},
					"max_completion_tokens": schema.Int64Attribute{
						Optional: true, MarkdownDescription: "Completion tokens per turn.",
						Validators: []validator.Int64{int64validator.AtLeast(1)},
					},
				},
			},
			"data_sources": schema.ListNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Data docks the agent may read.",
				Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"data_dock_id": schema.StringAttribute{Required: true, MarkdownDescription: "Data dock id.", Validators: []validator.String{isUUID()}},
					"slug":         schema.StringAttribute{Required: true, MarkdownDescription: "Data dock slug."},
					"tables": schema.SetAttribute{
						ElementType: types.StringType, Optional: true,
						MarkdownDescription: "Tables (`catalog.schema.table`) the agent may read; omitted, the whole dock.",
						Validators:          []validator.Set{setvalidator.SizeAtLeast(1)},
					},
				}},
			},
			"service_account_id": uuidStr("Id of the service account the agent authenticates as; it must be viewer-scoped."),
			"desired_state": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "`Running` (the default for a new agent) or `Stopped`.",
				Validators:          []validator.String{stringvalidator.OneOf("Running", "Stopped")},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"interfaces": schema.SetAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				MarkdownDescription: "Where the agent is reachable: `web` (when omitted), `mail`, `slack` or `discord`.",
				Validators: []validator.Set{
					setvalidator.SizeAtLeast(1),
					setvalidator.ValueStringsAre(stringvalidator.OneOf("web", "mail", "slack", "discord")),
				},
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
			},
			"mail": schema.SingleNestedAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Mailbox of the `mail` interface; removing it keeps the current one.",
				PlanModifiers:       []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
				Attributes: map[string]schema.Attribute{
					"mode": schema.StringAttribute{
						Optional: true, Computed: true,
						MarkdownDescription: "`external` (your own mailbox) or `managed` (provisioned by the platform).",
						Validators:          []validator.String{stringvalidator.OneOf("external", "managed")},
						PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
					},
					"display_name": optStr("Name recipients see."),
					"allowed_senders": schema.SetAttribute{
						ElementType: types.StringType, Optional: true,
						MarkdownDescription: "Senders the agent answers, as an address or `@domain`. Omitted, everyone.",
						Validators:          []validator.Set{setvalidator.SizeAtLeast(1)},
					},
					"from_address":       optStr("From address of replies. External mode only."),
					"username":           optStr("IMAP and SMTP login. External mode only."),
					"password_secret_id": uuidStr("Id of the secret holding the mailbox password. External mode only."),
					"imap_host":          optStr("IMAP host. External mode only."),
					"imap_port":          port("IMAP port. External mode only."),
					"smtp_host":          optStr("SMTP host. External mode only."),
					"smtp_port":          port("SMTP port. External mode only."),
					"use_tls": schema.BoolAttribute{
						Optional: true, Computed: true, MarkdownDescription: "TLS on the mail connections. External mode only.",
						PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
					},
				},
			},

			"slug":           computedStr("Derived slug; capabilities name their agent by it in `subagent_of`."),
			"phase":          computedStr("Lifecycle phase, e.g. `Running`, `Stopped` or `AwaitingServiceAccount`."),
			"status_message": computedStr("Detail on the current phase."),
			"mail_address":   computedStr("Address the agent is reachable at by mail."),
		},
	}
}

func (r *agentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *agentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan agentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	harbor, err := uuid.Parse(plan.Env.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("env"), "Invalid env", "must be a UUID: "+err.Error())
		return
	}
	var diags diag.Diagnostics
	inference := agentInferenceRequest(ctx, plan.Inference, &diags)
	body := console.CreateAiAgentRequest{
		HarborId:         harbor,
		Name:             stringPtr(plan.Name),
		Description:      stringPtr(plan.Description),
		SystemPrompt:     stringPtr(plan.SystemPrompt),
		Mission:          stringPtr(plan.Mission),
		Context:          stringPtr(plan.Context),
		Speciality:       stringPtr(plan.Speciality),
		SubagentOf:       stringPtr(plan.SubagentOf),
		QuestionSet:      strListPtr(ctx, plan.QuestionSet, &diags),
		Tools:            strListPtr(ctx, plan.Tools, &diags),
		Budgets:          agentBudgetsRequest(ctx, plan.Budgets, &diags),
		DataSources:      agentDataSourcesRequest(ctx, plan.DataSources, &diags),
		ServiceAccountId: uuidPtr(plan.ServiceAccountID, &diags),
		Interfaces:       strSetPtr(ctx, plan.Interfaces, &diags),
		Mail:             agentMailRequest(ctx, plan.Mail, &diags),
	}
	if inference != nil {
		body.Inference = *inference
	}
	if !plan.DesiredState.IsNull() && !plan.DesiredState.IsUnknown() {
		ds := console.AiAgentDesiredStateDto(plan.DesiredState.ValueString())
		body.DesiredState = &ds
	}
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.p.API.CreateAiAgent(ctx, r.p.OrgID, body)
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to create agent", featureAiAgents, err)
		return
	}
	id := created.Id.String()
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	r.settle(ctx, id, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.readInto(ctx, id)
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to read agent after create", featureAiAgents, err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *agentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior agentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.readInto(ctx, prior.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to read agent", featureAiAgents, err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *agentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state agentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, diags := agentPatch(ctx, plan, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if _, err := r.p.API.UpdateAiAgent(ctx, r.p.OrgID, id, body); err != nil {
		addGatedError(&resp.Diagnostics, "Failed to update agent", featureAiAgents, err)
		return
	}
	r.settle(ctx, id, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	newState, err := r.readInto(ctx, id)
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to read agent after update", featureAiAgents, err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *agentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state agentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.p.API.DeleteAiAgent(ctx, r.p.OrgID, id); err != nil {
		addGatedError(&resp.Diagnostics, "Failed to delete agent", featureAiAgents, err)
		return
	}
	if err := pollGoneOn404Every(ctx, agentWaitTimeout, orDefaultInterval(r.interval), func() error {
		_, err := r.p.API.GetAiAgent(ctx, r.p.OrgID, id)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Agent still present after delete", err.Error())
	}
}

func (r *agentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// settle waits for a phase the agent will not leave on its own. An agent
// waiting for a service account settles there, with a warning.
func (r *agentResource) settle(ctx context.Context, id string, diags *diag.Diagnostics) {
	a, err := waitForReadyEvery(ctx, agentWaitTimeout, orDefaultInterval(r.interval), func() (*console.AiAgentResponse, bool, error) {
		a, err := r.p.API.GetAiAgent(ctx, r.p.OrgID, id)
		if err != nil {
			return nil, false, err
		}
		switch a.Status.Phase {
		case "Failed":
			msg := "the agent failed to start"
			if a.Status.Message != nil && *a.Status.Message != "" {
				msg += ": " + *a.Status.Message
			}
			return nil, false, errors.New(msg)
		case "Running", "Stopped", "AwaitingServiceAccount":
			return a, true, nil
		}
		return a, false, nil
	})
	if err != nil {
		diags.AddError("Agent did not settle", err.Error())
		return
	}
	if a.Status.Phase == "AwaitingServiceAccount" {
		diags.AddWarning("Agent is waiting for a service account",
			"The agent uses a tool that needs a service account; set service_account_id for it to start.")
	}
}

func (r *agentResource) readInto(ctx context.Context, id string) (agentModel, error) {
	a, err := r.p.API.GetAiAgent(ctx, r.p.OrgID, id)
	if err != nil {
		return agentModel{}, err
	}
	var diags diag.Diagnostics
	tools, d := types.ListValueFrom(ctx, types.StringType, a.Tools)
	diags.Append(d...)
	interfaces, d := types.SetValueFrom(ctx, types.StringType, a.Interfaces)
	diags.Append(d...)

	sa := types.StringNull()
	if a.ServiceAccountId != nil {
		sa = types.StringValue(a.ServiceAccountId.String())
	}
	m := agentModel{
		ID:               types.StringValue(id),
		Env:              types.StringValue(a.HarborId.String()),
		Name:             types.StringValue(a.Name),
		Description:      optNonEmpty(a.Description),
		SystemPrompt:     types.StringValue(a.SystemPrompt),
		Mission:          optNonEmpty(a.Mission),
		Context:          optNonEmpty(a.Context),
		Speciality:       optNonEmpty(a.Speciality),
		SubagentOf:       optNonEmpty(a.SubagentOf),
		QuestionSet:      nullIfEmptyList(ctx, a.QuestionSet, &diags),
		Tools:            tools,
		Inference:        agentInferenceObject(ctx, a.Inference, &diags),
		Budgets:          agentBudgetsObject(ctx, a.Budgets, &diags),
		DataSources:      agentDataSourcesList(ctx, a.DataSources, &diags),
		ServiceAccountID: sa,
		DesiredState:     types.StringValue(string(a.DesiredState)),
		Interfaces:       interfaces,
		Mail:             agentMailObject(ctx, a.Mail, &diags),
		Slug:             types.StringValue(a.Slug),
		Phase:            types.StringValue(a.Status.Phase),
		StatusMessage:    optString(a.Status.Message),
		MailAddress:      types.StringNull(),
	}
	if a.Status.Mail != nil {
		m.MailAddress = optString(a.Status.Mail.Address)
	}
	if diags.HasError() {
		return agentModel{}, fmt.Errorf("mapping agent %s: %v", id, diags.Errors())
	}
	return m, nil
}

// agentPatch sends what changed. A removed text or list is cleared with an
// empty value, and a removed service account is detached.
func agentPatch(ctx context.Context, plan, state agentModel) (console.UpdateAiAgentRequest, diag.Diagnostics) {
	var (
		diags diag.Diagnostics
		body  console.UpdateAiAgentRequest
	)
	changed := func(p, s attr.Value) bool { return !p.IsUnknown() && !p.Equal(s) }
	set := func(p, s attr.Value) bool { return changed(p, s) && !p.IsNull() }
	empty := ""
	text := func(p, s types.String) *string {
		if !changed(p, s) {
			return nil
		}
		return orElse(stringPtr(p), &empty)
	}
	body.Description = text(plan.Description, state.Description)
	body.Mission = text(plan.Mission, state.Mission)
	body.Context = text(plan.Context, state.Context)
	body.Speciality = text(plan.Speciality, state.Speciality)
	body.SubagentOf = text(plan.SubagentOf, state.SubagentOf)
	if set(plan.SystemPrompt, state.SystemPrompt) {
		body.SystemPrompt = stringPtr(plan.SystemPrompt)
	}
	if changed(plan.QuestionSet, state.QuestionSet) {
		body.QuestionSet = orElse(strListPtr(ctx, plan.QuestionSet, &diags), &[]string{})
	}
	if set(plan.Tools, state.Tools) {
		body.Tools = strListPtr(ctx, plan.Tools, &diags)
	}
	if changed(plan.Inference, state.Inference) {
		body.Inference = agentInferenceRequest(ctx, plan.Inference, &diags)
	}
	if changed(plan.Budgets, state.Budgets) {
		body.Budgets = orElse(agentBudgetsRequest(ctx, plan.Budgets, &diags), &console.AiAgentBudgetsDto{})
	}
	if changed(plan.DataSources, state.DataSources) {
		body.DataSources = orElse(agentDataSourcesRequest(ctx, plan.DataSources, &diags), &[]console.AiAgentDataSourceDto{})
	}
	if changed(plan.ServiceAccountID, state.ServiceAccountID) {
		if plan.ServiceAccountID.IsNull() {
			detach := true
			body.DetachServiceAccount = &detach
		} else {
			body.ServiceAccountId = uuidPtr(plan.ServiceAccountID, &diags)
		}
	}
	if set(plan.DesiredState, state.DesiredState) {
		ds := console.AiAgentDesiredStateDto(plan.DesiredState.ValueString())
		body.DesiredState = &ds
	}
	if set(plan.Interfaces, state.Interfaces) {
		body.Interfaces = strSetPtr(ctx, plan.Interfaces, &diags)
	}
	if set(plan.Mail, state.Mail) {
		body.Mail = agentMailRequest(ctx, plan.Mail, &diags)
	}
	return body, diags
}

// ── request builders ───────────────────────────────────────────────────────

func uuidPtr(v types.String, diags *diag.Diagnostics) *uuid.UUID {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	u, err := uuid.Parse(v.ValueString())
	if err != nil {
		diags.AddError("Invalid UUID", fmt.Sprintf("%q: %s", v.ValueString(), err))
		return nil
	}
	return &u
}

func agentInferenceRequest(ctx context.Context, obj types.Object, diags *diag.Diagnostics) *console.AiAgentInferenceDto {
	m, ok := objectAsModel[agentInferenceModel](ctx, obj, diags)
	if !ok {
		return nil
	}
	return &console.AiAgentInferenceDto{
		Provider: m.Provider.ValueString(), Model: stringPtr(m.Model),
		ApiKeySecretId: uuidPtr(m.APIKeySecretID, diags), EndpointUrl: stringPtr(m.EndpointURL),
	}
}

func agentBudgetsRequest(ctx context.Context, obj types.Object, diags *diag.Diagnostics) *console.AiAgentBudgetsDto {
	m, ok := objectAsModel[agentBudgetsModel](ctx, obj, diags)
	if !ok {
		return nil
	}
	return &console.AiAgentBudgetsDto{MaxTurns: int32PtrFromInt64(m.MaxTurns), MaxCompletionTokens: int32PtrFromInt64(m.MaxCompletionTokens)}
}

func agentDataSourcesRequest(ctx context.Context, l types.List, diags *diag.Diagnostics) *[]console.AiAgentDataSourceDto {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	var models []agentDockSourceModel
	diags.Append(l.ElementsAs(ctx, &models, false)...)
	out := make([]console.AiAgentDataSourceDto, 0, len(models))
	for _, m := range models {
		id := uuidPtr(m.DataDockID, diags)
		if id == nil {
			continue
		}
		out = append(out, console.AiAgentDataSourceDto{DataDockId: *id, Slug: m.Slug.ValueString(), Tables: strSetPtr(ctx, m.Tables, diags)})
	}
	return &out
}

func agentMailRequest(ctx context.Context, obj types.Object, diags *diag.Diagnostics) *console.AiAgentMailDto {
	m, ok := objectAsModel[agentMailModel](ctx, obj, diags)
	if !ok {
		return nil
	}
	out := &console.AiAgentMailDto{
		DisplayName: stringPtr(m.DisplayName), AllowedSenders: strSetPtr(ctx, m.AllowedSenders, diags),
		FromAddress: stringPtr(m.FromAddress), Username: stringPtr(m.Username),
		PasswordSecretId: uuidPtr(m.PasswordSecretID, diags),
		ImapHost:         stringPtr(m.ImapHost), ImapPort: int32PtrFromInt64(m.ImapPort),
		SmtpHost: stringPtr(m.SMTPHost), SmtpPort: int32PtrFromInt64(m.SMTPPort),
		UseTls: boolPtr(m.UseTLS),
	}
	if !m.Mode.IsNull() && !m.Mode.IsUnknown() {
		mode := console.AiAgentMailModeDto(m.Mode.ValueString())
		out.Mode = &mode
	}
	return out
}

// ── read mappers ───────────────────────────────────────────────────────────

func agentInferenceObject(ctx context.Context, in console.AiAgentInferenceDto, diags *diag.Diagnostics) types.Object {
	key := types.StringNull()
	if in.ApiKeySecretId != nil {
		key = types.StringValue(in.ApiKeySecretId.String())
	}
	return objectFromModel(ctx, agentInferenceType, agentInferenceModel{
		Provider: types.StringValue(in.Provider), Model: optNonEmpty(in.Model),
		APIKeySecretID: key, EndpointURL: optNonEmpty(in.EndpointUrl),
	}, diags)
}

func agentBudgetsObject(ctx context.Context, b console.AiAgentBudgetsDto, diags *diag.Diagnostics) types.Object {
	if b.MaxTurns == nil && b.MaxCompletionTokens == nil {
		return types.ObjectNull(agentBudgetsType.AttrTypes)
	}
	return objectFromModel(ctx, agentBudgetsType, agentBudgetsModel{
		MaxTurns: optInt64FromInt32(b.MaxTurns), MaxCompletionTokens: optInt64FromInt32(b.MaxCompletionTokens),
	}, diags)
}

func agentDataSourcesList(ctx context.Context, sources []console.AiAgentDataSourceDto, diags *diag.Diagnostics) types.List {
	if len(sources) == 0 {
		return types.ListNull(agentDockSourceType)
	}
	models := make([]agentDockSourceModel, 0, len(sources))
	for _, s := range sources {
		models = append(models, agentDockSourceModel{
			DataDockID: types.StringValue(s.DataDockId.String()), Slug: types.StringValue(s.Slug),
			Tables: nullIfEmptySet(ctx, derefStrings(s.Tables), diags),
		})
	}
	l, d := types.ListValueFrom(ctx, agentDockSourceType, models)
	diags.Append(d...)
	return l
}

func agentMailObject(ctx context.Context, m *console.AiAgentMailDto, diags *diag.Diagnostics) types.Object {
	if m == nil {
		return types.ObjectNull(agentMailType.AttrTypes)
	}
	mode := types.StringNull()
	if m.Mode != nil {
		mode = types.StringValue(string(*m.Mode))
	}
	secret := types.StringNull()
	if m.PasswordSecretId != nil {
		secret = types.StringValue(m.PasswordSecretId.String())
	}
	return objectFromModel(ctx, agentMailType, agentMailModel{
		Mode: mode, DisplayName: optNonEmpty(m.DisplayName),
		AllowedSenders: nullIfEmptySet(ctx, derefStrings(m.AllowedSenders), diags),
		FromAddress:    optNonEmpty(m.FromAddress), Username: optNonEmpty(m.Username), PasswordSecretID: secret,
		ImapHost: optNonEmpty(m.ImapHost), ImapPort: optInt64FromInt32(m.ImapPort),
		SMTPHost: optNonEmpty(m.SmtpHost), SMTPPort: optInt64FromInt32(m.SmtpPort),
		UseTLS: types.BoolPointerValue(m.UseTls),
	}, diags)
}
