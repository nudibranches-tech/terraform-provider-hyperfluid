// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = &alertChannelDataSource{}
	_ datasource.DataSourceWithConfigure = &alertChannelDataSource{}
)

func NewAlertChannelDataSource() datasource.DataSource {
	return &alertChannelDataSource{}
}

type alertChannelDataSource struct {
	p *providerData
}

type alertChannelDataSourceModel struct {
	Name       types.String `tfsdk:"name"`
	ID         types.String `tfsdk:"id"`
	Kind       types.String `tfsdk:"kind"`
	Recipients types.Set    `tfsdk:"recipients"`
	URLMasked  types.String `tfsdk:"url_masked"`
	Enabled    types.Bool   `tfsdk:"enabled"`
}

func (d *alertChannelDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_channel"
}

func (d *alertChannelDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an alert channel by name. The webhook URL is only returned masked. " +
			alertingFeatureNote,
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Channel name to look up."},
			"id":   schema.StringAttribute{Computed: true, MarkdownDescription: "Channel id."},
			"kind": schema.StringAttribute{Computed: true, MarkdownDescription: "`email`, `slack_webhook` or `discord_webhook`."},
			"recipients": schema.SetAttribute{
				ElementType: types.StringType, Computed: true, MarkdownDescription: "Email addresses of an email channel.",
			},
			"url_masked": schema.StringAttribute{Computed: true, MarkdownDescription: "The webhook URL, truncated by the platform."},
			"enabled":    schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether alerts are delivered."},
		},
	}
}

func (d *alertChannelDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *alertChannelDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg alertChannelDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ch, err := d.p.API.FindAlertChannelByName(ctx, d.p.OrgID, cfg.Name.ValueString())
	if err != nil {
		addAlertingError(&resp.Diagnostics, "Failed to look up alert channel", err)
		return
	}
	m, diags := alertChannelFrom(ctx, ch)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, alertChannelDataSourceModel{
		Name: m.Name, ID: m.ID, Kind: m.Kind, Recipients: m.Recipients, URLMasked: m.URLMasked, Enabled: m.Enabled,
	})...)
}
