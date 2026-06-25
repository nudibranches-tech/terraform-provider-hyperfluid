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

// hyperfluid_bucket data source — look up an existing object-storage bucket by
// name within an environment (created by the console, CLI, or another config).
// Read-only, so it can surface quota_gb / freeze_writes even though the resource
// can't manage them yet (see #16).

var (
	_ datasource.DataSource              = &bucketDataSource{}
	_ datasource.DataSourceWithConfigure = &bucketDataSource{}
)

func NewBucketDataSource() datasource.DataSource {
	return &bucketDataSource{}
}

type bucketDataSource struct {
	p *providerData
}

type bucketDataSourceModel struct {
	Env           types.String `tfsdk:"env"`
	Name          types.String `tfsdk:"name"`
	ID            types.String `tfsdk:"id"`
	StorageZoneID types.String `tfsdk:"storage_zone_id"`
	QuotaGB       types.Int64  `tfsdk:"quota_gb"`
	FreezeWrites  types.Bool   `tfsdk:"freeze_writes"`
	Ready         types.Bool   `tfsdk:"ready"`
}

func (d *bucketDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket"
}

func (d *bucketDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an existing object-storage bucket by name within an environment.",
		Attributes: map[string]schema.Attribute{
			"env":             schema.StringAttribute{Required: true, MarkdownDescription: "Environment id the bucket lives in."},
			"name":            schema.StringAttribute{Required: true, MarkdownDescription: "Bucket name."},
			"id":              schema.StringAttribute{Computed: true, MarkdownDescription: "Composite identifier `env/name`."},
			"storage_zone_id": schema.StringAttribute{Computed: true, MarkdownDescription: "Storage zone the bucket lives in (`default` for the primary)."},
			"quota_gb":        schema.Int64Attribute{Computed: true, MarkdownDescription: "Storage quota in GB (null if unset)."},
			"freeze_writes":   schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the bucket rejects writes."},
			"ready":           schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the bucket is provisioned and ready."},
		},
	}
}

func (d *bucketDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *bucketDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg bucketDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	env := cfg.Env.ValueString()
	name := cfg.Name.ValueString()
	b, err := d.p.API.GetBucket(ctx, env, name)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Bucket not found", "no bucket named "+name+" in environment "+env)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up bucket", err.Error())
		return
	}

	cfg.ID = types.StringValue(env + "/" + b.Name)
	cfg.StorageZoneID = types.StringValue(b.ZoneId)
	cfg.FreezeWrites = types.BoolValue(b.FreezeWrites)
	cfg.Ready = types.BoolValue(b.Ready)
	if b.QuotaGb != nil {
		cfg.QuotaGB = types.Int64Value(int64(*b.QuotaGb))
	} else {
		cfg.QuotaGB = types.Int64Null()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
