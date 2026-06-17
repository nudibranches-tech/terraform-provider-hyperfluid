// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// hyperfluid_secret data source — look up an existing secret's metadata by name.
// Returns no value (the secret material is never read into state); use it to
// reference secrets by name/path in other resources.

var (
	_ datasource.DataSource              = &secretDataSource{}
	_ datasource.DataSourceWithConfigure = &secretDataSource{}
)

func NewSecretDataSource() datasource.DataSource {
	return &secretDataSource{}
}

type secretDataSource struct {
	p *providerData
}

type secretDataSourceModel struct {
	Name        types.String `tfsdk:"name"`
	ID          types.String `tfsdk:"id"`
	SecretType  types.String `tfsdk:"secret_type"`
	SecretPath  types.String `tfsdk:"secret_path"`
	Description types.String `tfsdk:"description"`
	Tags        types.List   `tfsdk:"tags"`
}

func (d *secretDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret"
}

func (d *secretDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an existing secret's metadata by name. The secret value is never returned.",
		Attributes: map[string]schema.Attribute{
			"name":        schema.StringAttribute{Required: true, MarkdownDescription: "Secret name to look up."},
			"id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Secret id."},
			"secret_type": schema.StringAttribute{Computed: true, MarkdownDescription: "Secret type."},
			"secret_path": schema.StringAttribute{Computed: true, MarkdownDescription: "Platform path of the secret."},
			"description": schema.StringAttribute{Computed: true, MarkdownDescription: "Description."},
			"tags": schema.ListAttribute{
				ElementType: types.StringType, Computed: true, MarkdownDescription: "User-defined tags.",
			},
		},
	}
}

func (d *secretDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *secretDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg secretDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	meta, err := d.p.API.FindSecretByName(ctx, d.p.OrgID, cfg.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up secret", err.Error())
		return
	}
	tags, diags := stringSliceToList(ctx, meta.Tags)
	resp.Diagnostics.Append(diags...)
	cfg.ID = types.StringValue(meta.Id.String())
	cfg.SecretType = types.StringValue(string(meta.SecretType))
	cfg.SecretPath = types.StringValue(meta.SecretPath)
	cfg.Description = optString(meta.Description)
	cfg.Tags = tags
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
