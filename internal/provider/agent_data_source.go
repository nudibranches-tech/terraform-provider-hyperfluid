// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

var (
	_ datasource.DataSource              = &agentDataSource{}
	_ datasource.DataSourceWithConfigure = &agentDataSource{}
)

func NewAgentDataSource() datasource.DataSource {
	return &agentDataSource{}
}

type agentDataSource struct {
	p *providerData
}

type agentDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	Env         types.String `tfsdk:"env"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Speciality  types.String `tfsdk:"speciality"`
	SubagentOf  types.String `tfsdk:"subagent_of"`
	Interfaces  types.Set    `tfsdk:"interfaces"`
	Slug        types.String `tfsdk:"slug"`
	Phase       types.String `tfsdk:"phase"`
	MailAddress types.String `tfsdk:"mail_address"`
}

func (d *agentDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent"
}

func (d *agentDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	cs := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up a Sapience AI agent by name within an environment, e.g. the routing agent a capability answers for.",
		Attributes: map[string]schema.Attribute{
			"env":          schema.StringAttribute{Required: true, MarkdownDescription: "Environment id."},
			"name":         schema.StringAttribute{Required: true, MarkdownDescription: "Agent name."},
			"id":           cs("Agent id."),
			"description":  cs("Free-form description."),
			"speciality":   cs("What the agent is for."),
			"subagent_of":  cs("Slug of the routing agent it answers for, if it is a capability."),
			"interfaces":   schema.SetAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Where the agent is reachable."},
			"slug":         cs("Derived slug, which a capability's `subagent_of` names."),
			"phase":        cs("Lifecycle phase."),
			"mail_address": cs("Address the agent is reachable at by mail."),
		},
	}
}

func (d *agentDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "expected *providerData")
		return
	}
	d.p = pd
}

func (d *agentDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg agentDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	env, name := cfg.Env.ValueString(), cfg.Name.ValueString()
	a, err := d.p.API.FindAiAgent(ctx, d.p.OrgID, env, name)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Agent not found", "no agent named "+name+" in environment "+env)
		return
	}
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to look up agent", featureAiAgents, err)
		return
	}
	m, err := (&agentResource{p: d.p}).readInto(ctx, a.Id.String())
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to read agent", featureAiAgents, err)
		return
	}
	out := agentDataSourceModel{
		ID: m.ID, Env: m.Env, Name: m.Name, Description: m.Description, Speciality: m.Speciality,
		SubagentOf: m.SubagentOf, Interfaces: m.Interfaces, Slug: m.Slug, Phase: m.Phase, MailAddress: m.MailAddress,
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}
