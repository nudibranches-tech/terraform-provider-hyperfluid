// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// harbor is ambient context (created out-of-band), so it is a data source, not
// a resource. It resolves a harbor name/slug to the `id` that nearly every
// resource needs as a path scope.

var (
	_ datasource.DataSource              = &harborDataSource{}
	_ datasource.DataSourceWithConfigure = &harborDataSource{}
)

func NewHarborDataSource() datasource.DataSource {
	return &harborDataSource{}
}

type harborDataSource struct {
	p *providerData
}

type harborModel struct {
	Name types.String `tfsdk:"name"` // lookup key (slug)
	ID   types.String `tfsdk:"id"`   // resolved
}

func (d *harborDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_harbor"
}

func (d *harborDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an existing harbor by name. Harbors are created out-of-band; this data source resolves the id used to scope resources.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Harbor name (slug) to look up.",
			},
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Resolved harbor id.",
			},
		},
	}
}

func (d *harborDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *harborDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg harborModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	harbors, err := d.p.API.ListHarbors(ctx, d.p.OrgID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list harbors", err.Error())
		return
	}

	want := cfg.Name.ValueString()
	for _, h := range harbors {
		if h.Slug == want || h.Name == want {
			cfg.ID = types.StringValue(h.ID)
			resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
			return
		}
	}
	resp.Diagnostics.AddError("Harbor not found", "no harbor named "+want+" in this organization")
}
