// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// hyperfluid_permissions data source — the platform's permission catalog, the
// keys roles and grants take.

var (
	_ datasource.DataSource              = &permissionsDataSource{}
	_ datasource.DataSourceWithConfigure = &permissionsDataSource{}
)

func NewPermissionsDataSource() datasource.DataSource {
	return &permissionsDataSource{}
}

type permissionsDataSource struct {
	p *providerData
}

type permissionsDataSourceModel struct {
	ResourceType types.String `tfsdk:"resource_type"`
	Keys         types.List   `tfsdk:"keys"`
	IDs          types.Map    `tfsdk:"ids"`
	Permissions  types.List   `tfsdk:"permissions"`
}

func permissionEntryType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"id":            types.StringType,
		"key":           types.StringType,
		"resource_type": types.StringType,
		"action":        types.StringType,
		"description":   types.StringType,
	}}
}

func (d *permissionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_permissions"
}

func (d *permissionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The permission keys (`<resource_type>:<action>`) that roles and grants take.",
		Attributes: map[string]schema.Attribute{
			"resource_type": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Only list the permissions of this resource type, e.g. `secret`.",
			},
			"keys": schema.ListAttribute{
				ElementType:         types.StringType,
				Computed:            true,
				MarkdownDescription: "Permission keys, sorted.",
			},
			"ids": schema.MapAttribute{
				ElementType:         types.StringType,
				Computed:            true,
				MarkdownDescription: "Permission id by key; ids differ between installations, keys do not.",
			},
			"permissions": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The catalog entries, sorted by key.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":            schema.StringAttribute{Computed: true, MarkdownDescription: "Permission id."},
						"key":           schema.StringAttribute{Computed: true, MarkdownDescription: "`<resource_type>:<action>`."},
						"resource_type": schema.StringAttribute{Computed: true, MarkdownDescription: "Resource type."},
						"action":        schema.StringAttribute{Computed: true, MarkdownDescription: "Action."},
						"description":   schema.StringAttribute{Computed: true, MarkdownDescription: "What the permission allows."},
					},
				},
			},
		},
	}
}

func (d *permissionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *permissionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg permissionsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	entries, err := d.p.API.ListPermissions(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list permissions", err.Error())
		return
	}
	keys := []string{}
	ids := map[string]attr.Value{}
	objs := []attr.Value{}
	// The API returns the catalog ordered by key.
	for _, e := range entries {
		if !cfg.ResourceType.IsNull() && e.ResourceType != cfg.ResourceType.ValueString() {
			continue
		}
		keys = append(keys, e.Key)
		ids[e.Key] = types.StringValue(e.Id.String())
		obj, diags := types.ObjectValue(permissionEntryType().AttrTypes, map[string]attr.Value{
			"id":            types.StringValue(e.Id.String()),
			"key":           types.StringValue(e.Key),
			"resource_type": types.StringValue(e.ResourceType),
			"action":        types.StringValue(e.Action),
			"description":   optString(e.Description),
		})
		resp.Diagnostics.Append(diags...)
		objs = append(objs, obj)
	}
	keyList, diags := stringSliceToList(ctx, keys)
	resp.Diagnostics.Append(diags...)
	idMap, diags := types.MapValue(types.StringType, ids)
	resp.Diagnostics.Append(diags...)
	list, diags := types.ListValue(permissionEntryType(), objs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	cfg.Keys, cfg.IDs, cfg.Permissions = keyList, idMap, list
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
