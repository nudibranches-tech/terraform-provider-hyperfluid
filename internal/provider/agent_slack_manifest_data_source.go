// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

// The Slack app manifest is what the Slack app is created from, before its
// tokens exist and so before the channel can be set: a data source, read on
// its own, rather than an attribute of hyperfluid_agent_slack_channel.

var (
	_ datasource.DataSource              = &agentSlackManifestDataSource{}
	_ datasource.DataSourceWithConfigure = &agentSlackManifestDataSource{}
)

func NewAgentSlackManifestDataSource() datasource.DataSource {
	return &agentSlackManifestDataSource{}
}

type agentSlackManifestDataSource struct {
	p *providerData
}

type agentSlackManifestModel struct {
	AgentID       types.String `tfsdk:"agent_id"`
	Manifest      types.String `tfsdk:"manifest"`
	AppTokenScope types.String `tfsdk:"app_token_scope"`
	Notes         types.List   `tfsdk:"notes"`
}

func (d *agentSlackManifestDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent_slack_manifest"
}

func (d *agentSlackManifestDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The Slack app manifest for an agent: paste it in Slack's \"Create an app from a manifest\" to create the app " +
			"whose bot and app-level tokens `hyperfluid_agent_slack_channel` references. It holds no secret.",
		Attributes: map[string]schema.Attribute{
			"agent_id": schema.StringAttribute{
				Required: true, MarkdownDescription: "Id of the `hyperfluid_agent`.",
				Validators: []validator.String{isUUID()},
			},
			"manifest":        schema.StringAttribute{Computed: true, MarkdownDescription: "The manifest, YAML, named after the agent."},
			"app_token_scope": schema.StringAttribute{Computed: true, MarkdownDescription: "The scope to give the app-level token (`connections:write`)."},
			"notes": schema.ListAttribute{
				ElementType: types.StringType, Computed: true, MarkdownDescription: "What to know when creating the app.",
			},
		},
	}
}

func (d *agentSlackManifestDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *agentSlackManifestDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg agentSlackManifestModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, err := d.p.API.GetAiAgentSlackManifest(ctx, d.p.OrgID, cfg.AgentID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Agent not found", "no agent with id "+cfg.AgentID.ValueString())
		return
	}
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to read the Slack app manifest", featureAiAgents, err)
		return
	}
	notes, diags := types.ListValueFrom(ctx, types.StringType, nonNilStrings(m.Notes))
	resp.Diagnostics.Append(diags...)
	cfg.Manifest = types.StringValue(m.Manifest)
	cfg.AppTokenScope = types.StringValue(m.AppTokenScope)
	cfg.Notes = notes
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
