// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// hyperfluid_role data source — look up a role by name, system roles included,
// with the permissions it holds.

var (
	_ datasource.DataSource              = &roleDataSource{}
	_ datasource.DataSourceWithConfigure = &roleDataSource{}
)

func NewRoleDataSource() datasource.DataSource {
	return &roleDataSource{}
}

type roleDataSource struct {
	p *providerData
}

type roleDataSourceModel struct {
	Name        types.String `tfsdk:"name"`
	ID          types.String `tfsdk:"id"`
	Description types.String `tfsdk:"description"`
	Color       types.String `tfsdk:"color"`
	IsSystem    types.Bool   `tfsdk:"is_system"`
	Permissions types.Set    `tfsdk:"permissions"`
}

func (d *roleDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role"
}

func (d *roleDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up a role by name, including the system roles `owner`, `editor` and `viewer`, " +
			"to bind it with `hyperfluid_user_role`, `hyperfluid_group_role` or `hyperfluid_service_account_role`. " +
			"Requires `role:read`.",
		Attributes: map[string]schema.Attribute{
			"name":        schema.StringAttribute{Required: true, MarkdownDescription: "Role name, unique within the organization."},
			"id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Role id."},
			"description": schema.StringAttribute{Computed: true, MarkdownDescription: "Description."},
			"color":       schema.StringAttribute{Computed: true, MarkdownDescription: "Color the console shows the role with."},
			"is_system":   schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the platform manages the role; a system role cannot be changed."},
			"permissions": schema.SetAttribute{
				ElementType:         types.StringType,
				Computed:            true,
				MarkdownDescription: "Permission keys the role holds, e.g. `secret:read`.",
			},
		},
	}
}

func (d *roleDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *roleDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg roleDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	role, err := d.p.API.FindRoleByName(ctx, d.p.OrgID, cfg.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up role", err.Error())
		return
	}
	entries, err := d.p.API.ListRolePermissions(ctx, role.Id.String())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read the role's permissions", err.Error())
		return
	}
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, e.Key)
	}
	perms, diags := stringSliceToSet(ctx, keys)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, roleDataSourceModel{
		Name:        types.StringValue(role.Name),
		ID:          types.StringValue(role.Id.String()),
		Description: optString(role.Description),
		Color:       optString(role.Color),
		IsSystem:    types.BoolValue(role.IsSystem),
		Permissions: perms,
	})...)
}
