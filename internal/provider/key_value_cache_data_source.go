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

// hyperfluid_key_value_cache data source — look up an existing cache by name
// within an environment. Reuses the resource's model + readInto.

var (
	_ datasource.DataSource              = &keyValueCacheDataSource{}
	_ datasource.DataSourceWithConfigure = &keyValueCacheDataSource{}
)

func NewKeyValueCacheDataSource() datasource.DataSource {
	return &keyValueCacheDataSource{}
}

type keyValueCacheDataSource struct {
	p *providerData
}

func (d *keyValueCacheDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_key_value_cache"
}

func (d *keyValueCacheDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	cs := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an existing key-value cache by name within an environment.",
		Attributes: map[string]schema.Attribute{
			"env":                     schema.StringAttribute{Required: true, MarkdownDescription: "Environment id the cache runs in."},
			"name":                    schema.StringAttribute{Required: true, MarkdownDescription: "Cache name."},
			"id":                      cs("Cache id."),
			"image":                   cs("Cache image."),
			"maxmemory":               cs("Max memory (e.g. 256mb)."),
			"maxmemory_policy":        cs("Eviction policy."),
			"description":             cs("Free-form description."),
			"tags":                    schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "User-defined tags."},
			"host":                    cs("Connection host."),
			"port":                    schema.Int64Attribute{Computed: true, MarkdownDescription: "Connection port."},
			"credentials_secret_name": cs("Secret holding the connection credentials."),
			"phase":                   cs("Current lifecycle phase."),
			"slug":                    cs("Derived slug."),
		},
	}
}

func (d *keyValueCacheDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *keyValueCacheDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg keyValueCacheModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	env := cfg.Env.ValueString()
	name := cfg.Name.ValueString()
	id, err := d.p.API.FindKeyValueCache(ctx, d.p.OrgID, env, name)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Key-value cache not found", "no cache named "+name+" in environment "+env)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up key-value cache", err.Error())
		return
	}

	// Reuse the resource's mapper (it only needs the API client) so the model
	// mapping lives in one place.
	state, err := (&keyValueCacheResource{p: d.p}).readInto(ctx, env, id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read key-value cache", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
