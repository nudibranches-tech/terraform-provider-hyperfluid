// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
)

// hyperfluid_group data source — look up an existing group by name.

var (
	_ datasource.DataSource              = &groupDataSource{}
	_ datasource.DataSourceWithConfigure = &groupDataSource{}
)

func NewGroupDataSource() datasource.DataSource {
	return &groupDataSource{}
}

type groupDataSource struct {
	p *providerData
}

func (d *groupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

func (d *groupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an existing group by name. Requires `group:read`.",
		Attributes: map[string]schema.Attribute{
			"name":         schema.StringAttribute{Required: true, MarkdownDescription: "Group name, unique within the organization."},
			"id":           schema.StringAttribute{Computed: true, MarkdownDescription: "Group id."},
			"description":  schema.StringAttribute{Computed: true, MarkdownDescription: "Description."},
			"member_count": schema.Int64Attribute{Computed: true, MarkdownDescription: "Number of users and service accounts in the group."},
		},
	}
}

func (d *groupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *groupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg groupModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := d.p.API.FindGroupByName(ctx, d.p.OrgID, cfg.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up group", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, groupToModel(g))...)
}
