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

// hyperfluid_managed_postgresql data source — look up an existing cluster by name
// within an environment. Reuses the resource's model + readInto.

var (
	_ datasource.DataSource              = &managedPostgresqlDataSource{}
	_ datasource.DataSourceWithConfigure = &managedPostgresqlDataSource{}
)

func NewManagedPostgresqlDataSource() datasource.DataSource {
	return &managedPostgresqlDataSource{}
}

type managedPostgresqlDataSource struct {
	p *providerData
}

func (d *managedPostgresqlDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_managed_postgresql"
}

func (d *managedPostgresqlDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	cs := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	ci := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an existing managed PostgreSQL cluster by name within an environment.",
		Attributes: map[string]schema.Attribute{
			"env":                schema.StringAttribute{Required: true, MarkdownDescription: "Environment id the cluster runs in."},
			"name":               schema.StringAttribute{Required: true, MarkdownDescription: "Cluster name."},
			"id":                 cs("Cluster id."),
			"database_name":      cs("Application database name."),
			"engine":             cs("Database engine."),
			"version":            cs("Engine version."),
			"node_tier":          cs("Node tier."),
			"storage_capacity":   ci("Storage capacity in GB."),
			"backup_policy":      cs("Backup policy."),
			"backup_target_id":   cs("Backup target id, if any."),
			"configuration":      cs("Cluster configuration (e.g. standalone)."),
			"expose_to_internet": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the cluster is reachable from the internet via an external NodePort Service."},
			"description":        cs("Free-form description."),
			"tags":               schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "User-defined tags."},
			"phase":              cs("Current lifecycle phase."),
			"instances":          ci("Configured instance count."),
			"ready_instances":    ci("Ready instance count."),
			"write_endpoint":     cs("Primary (read-write) endpoint."),
			"read_endpoint":      cs("Read endpoint."),
			"external_endpoint":  cs("External endpoint, if exposed."),
		},
	}
}

func (d *managedPostgresqlDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *managedPostgresqlDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg managedPostgresqlModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	env := cfg.Env.ValueString()
	name := cfg.Name.ValueString()
	id, err := d.p.API.FindManagedPostgresql(ctx, d.p.OrgID, env, name)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Managed PostgreSQL not found", "no cluster named "+name+" in environment "+env)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up managed PostgreSQL", err.Error())
		return
	}

	// Reuse the resource's mapper (it only needs the API client) so the model
	// mapping lives in one place.
	state, err := (&managedPostgresqlResource{p: d.p}).readInto(ctx, env, id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read managed PostgreSQL", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
