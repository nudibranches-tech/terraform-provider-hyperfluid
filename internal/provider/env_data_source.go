// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// env is ambient context (created out-of-band), so it is a data source, not
// a resource. It resolves a env name/slug to the `id` that nearly every
// resource needs as a path scope.

var (
	_ datasource.DataSource              = &envDataSource{}
	_ datasource.DataSourceWithConfigure = &envDataSource{}
)

func NewEnvDataSource() datasource.DataSource {
	return &envDataSource{}
}

type envDataSource struct {
	p *providerData
}

type envModel struct {
	Name types.String `tfsdk:"name"` // lookup key (slug)
	ID   types.String `tfsdk:"id"`   // resolved
}

func (d *envDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_env"
}

func (d *envDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an existing environment by name. Envs are created out-of-band; this data source resolves the id used to scope resources.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Env name (slug) to look up.",
			},
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Resolved env id.",
			},
		},
	}
}

func (d *envDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *envDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg envModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	envs, err := d.p.API.ListHarbors(ctx, d.p.OrgID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list envs", err.Error())
		return
	}

	want := cfg.Name.ValueString()
	for _, h := range envs {
		if h.Slug == want || h.Name == want {
			cfg.ID = types.StringValue(h.Id.String())
			resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
			return
		}
	}
	resp.Diagnostics.AddError("Env not found", "no env named "+want+" in this organization")
}
