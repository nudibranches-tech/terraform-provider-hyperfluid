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
	_ datasource.DataSource              = &computeDockDataSource{}
	_ datasource.DataSourceWithConfigure = &computeDockDataSource{}
)

func NewComputeDockDataSource() datasource.DataSource {
	return &computeDockDataSource{}
}

type computeDockDataSource struct {
	p *providerData
}

type computeDockDataSourceModel struct {
	ID           types.String `tfsdk:"id"`
	Env          types.String `tfsdk:"env"`
	Name         types.String `tfsdk:"name"`
	Description  types.String `tfsdk:"description"`
	Tags         types.List   `tfsdk:"tags"`
	Enabled      types.Bool   `tfsdk:"enabled"`
	Version      types.String `tfsdk:"version"`
	Slug         types.String `tfsdk:"slug"`
	Phase        types.String `tfsdk:"phase"`
	SSHEndpoint  types.String `tfsdk:"ssh_endpoint"`
	RestEndpoint types.String `tfsdk:"rest_endpoint"`
}

func (d *computeDockDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compute_dock"
}

func (d *computeDockDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	cs := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up a Compute Dock by name within an environment.",
		Attributes: map[string]schema.Attribute{
			"env":           schema.StringAttribute{Required: true, MarkdownDescription: "Environment id."},
			"name":          schema.StringAttribute{Required: true, MarkdownDescription: "Dock name."},
			"id":            cs("Dock id."),
			"description":   cs("Free-form description."),
			"tags":          schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "User-defined tags."},
			"enabled":       schema.BoolAttribute{Computed: true, MarkdownDescription: "`false` when the cluster is suspended."},
			"version":       cs("Pinned Slurm image tag, if any."),
			"slug":          cs("Derived slug."),
			"phase":         cs("Lifecycle phase."),
			"ssh_endpoint":  cs("SSH endpoint of the login node."),
			"rest_endpoint": cs("`slurmrestd` endpoint."),
		},
	}
}

func (d *computeDockDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *computeDockDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg computeDockDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	env, name := cfg.Env.ValueString(), cfg.Name.ValueString()
	id, err := d.p.API.FindComputeDock(ctx, d.p.OrgID, env, name)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Compute dock not found", "no compute dock named "+name+" in environment "+env)
		return
	}
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to look up compute dock", featureComputeDocks, err)
		return
	}
	m, err := (&computeDockResource{p: d.p}).readInto(ctx, id, types.BoolNull())
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to read compute dock", featureComputeDocks, err)
		return
	}
	out := computeDockDataSourceModel{
		ID: m.ID, Env: m.Env, Name: m.Name, Description: m.Description, Tags: m.Tags, Enabled: m.Enabled,
		Version: m.Version, Slug: m.Slug, Phase: m.Phase, SSHEndpoint: m.SSHEndpoint, RestEndpoint: m.RestEndpoint,
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}
