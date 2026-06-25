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

// hyperfluid_storage_zone data source — look up one of the org's storage zones
// by its zone id (the stable `cephZones` catalog key, e.g. "default"). Lets a
// config validate a zone exists / is enabled and read its endpoint before
// placing a bucket in it.

var (
	_ datasource.DataSource              = &storageZoneDataSource{}
	_ datasource.DataSourceWithConfigure = &storageZoneDataSource{}
)

func NewStorageZoneDataSource() datasource.DataSource {
	return &storageZoneDataSource{}
}

type storageZoneDataSource struct {
	p *providerData
}

type storageZoneModel struct {
	ZoneID           types.String `tfsdk:"zone_id"` // lookup key
	Name             types.String `tfsdk:"name"`
	Description      types.String `tfsdk:"description"`
	Primary          types.Bool   `tfsdk:"primary"`
	Enabled          types.Bool   `tfsdk:"enabled"`
	Ready            types.Bool   `tfsdk:"ready"`
	ExternalEndpoint types.String `tfsdk:"external_endpoint"`
}

func (d *storageZoneDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_storage_zone"
}

func (d *storageZoneDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up one of the organization's object-storage zones by id. Use the resolved `zone_id` to place a bucket (`hyperfluid_bucket.storage_zone_id`).",
		Attributes: map[string]schema.Attribute{
			"zone_id":           schema.StringAttribute{Required: true, MarkdownDescription: "Zone id to look up (the `cephZones` catalog key, e.g. `default`)."},
			"name":              schema.StringAttribute{Computed: true, MarkdownDescription: "Human-readable zone name."},
			"description":       schema.StringAttribute{Computed: true, MarkdownDescription: "Free-form description (null if unset)."},
			"primary":           schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether this is the org's primary zone (always enabled, never removable)."},
			"enabled":           schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the org has enabled storage in this zone."},
			"ready":             schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the zone's storage is provisioned and ready (only meaningful when enabled)."},
			"external_endpoint": schema.StringAttribute{Computed: true, MarkdownDescription: "External S3 endpoint of the zone, once provisioned (null otherwise)."},
		},
	}
}

func (d *storageZoneDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *storageZoneDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg storageZoneModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	zoneID := cfg.ZoneID.ValueString()
	z, err := d.p.API.FindStorageZone(ctx, d.p.OrgID, zoneID)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Storage zone not found", "no storage zone with id "+zoneID+" in this organization")
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up storage zone", err.Error())
		return
	}

	cfg.Name = types.StringValue(z.Name)
	cfg.Primary = types.BoolValue(z.Primary)
	cfg.Enabled = types.BoolValue(z.Enabled)
	cfg.Ready = types.BoolValue(z.Ready)
	cfg.Description = types.StringPointerValue(z.Description)
	cfg.ExternalEndpoint = types.StringPointerValue(z.ExternalEndpoint)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
