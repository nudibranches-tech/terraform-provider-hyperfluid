// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// hyperfluid_bifrost data source — the cluster's query-layer (Bifrost)
// connection endpoints and feature configuration. Bifrost is a per-deployment
// singleton, so this takes no lookup key. Use it to wire clients to the REST /
// pgwire / GraphQL endpoints and to gate config on which query features are on.

var (
	_ datasource.DataSource              = &bifrostDataSource{}
	_ datasource.DataSourceWithConfigure = &bifrostDataSource{}
)

func NewBifrostDataSource() datasource.DataSource {
	return &bifrostDataSource{}
}

type bifrostDataSource struct {
	p *providerData
}

type bifrostFeaturesModel struct {
	PostgresqlEnabled     types.Bool `tfsdk:"postgresql_enabled"`
	GraphqlEnabled        types.Bool `tfsdk:"graphql_enabled"`
	McpEnabled            types.Bool `tfsdk:"mcp_enabled"`
	OpenapiEnabled        types.Bool `tfsdk:"openapi_enabled"`
	FullTextSearchEnabled types.Bool `tfsdk:"full_text_search_enabled"`
	VectorSearchEnabled   types.Bool `tfsdk:"vector_search_enabled"`
}

type bifrostDataSourceModel struct {
	RestURL            types.String          `tfsdk:"rest_url"`
	PgwireEndpoint     types.String          `tfsdk:"pgwire_endpoint"`
	GraphqlURLTemplate types.String          `tfsdk:"graphql_url_template"`
	Features           *bifrostFeaturesModel `tfsdk:"features"`
}

func (d *bifrostDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bifrost"
}

func (d *bifrostDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The cluster's query-layer (Bifrost) connection endpoints and feature configuration. Bifrost is a per-deployment singleton, so this data source takes no arguments.",
		Attributes: map[string]schema.Attribute{
			"rest_url":             schema.StringAttribute{Computed: true, MarkdownDescription: "REST / OpenAPI base URL (e.g. `https://bifrost.example.com`)."},
			"pgwire_endpoint":      schema.StringAttribute{Computed: true, MarkdownDescription: "PostgreSQL wire-protocol endpoint as `host:port`."},
			"graphql_url_template": schema.StringAttribute{Computed: true, MarkdownDescription: "GraphQL URL template — substitute a Data Dock id for `{data_dock_id}`."},
			"features": schema.SingleNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Which Bifrost query features are enabled on this deployment.",
				Attributes: map[string]schema.Attribute{
					"postgresql_enabled":       schema.BoolAttribute{Computed: true, MarkdownDescription: "PostgreSQL wire-protocol access is enabled."},
					"graphql_enabled":          schema.BoolAttribute{Computed: true, MarkdownDescription: "GraphQL API is enabled."},
					"mcp_enabled":              schema.BoolAttribute{Computed: true, MarkdownDescription: "MCP (Model Context Protocol) endpoint is enabled."},
					"openapi_enabled":          schema.BoolAttribute{Computed: true, MarkdownDescription: "OpenAPI / REST access is enabled."},
					"full_text_search_enabled": schema.BoolAttribute{Computed: true, MarkdownDescription: "Full-text search is enabled."},
					"vector_search_enabled":    schema.BoolAttribute{Computed: true, MarkdownDescription: "Vector search is enabled."},
				},
			},
		},
	}
}

func (d *bifrostDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *bifrostDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	info, err := d.p.API.GetBifrostInfo(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Failed to fetch Bifrost info", err.Error())
		return
	}
	features, err := d.p.API.GetBifrostFeatures(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Failed to fetch Bifrost features", err.Error())
		return
	}

	state := bifrostDataSourceModel{
		RestURL:            types.StringValue(info.RestUrl),
		PgwireEndpoint:     types.StringValue(info.PgwireEndpoint),
		GraphqlURLTemplate: types.StringValue(info.GraphqlUrlTemplate),
		Features: &bifrostFeaturesModel{
			PostgresqlEnabled:     types.BoolValue(features.PostgresqlEnabled),
			GraphqlEnabled:        types.BoolValue(features.GraphqlEnabled),
			McpEnabled:            types.BoolValue(features.McpEnabled),
			OpenapiEnabled:        types.BoolValue(features.OpenapiEnabled),
			FullTextSearchEnabled: types.BoolValue(features.FullTextSearchEnabled),
			VectorSearchEnabled:   types.BoolValue(features.VectorSearchEnabled),
		},
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
