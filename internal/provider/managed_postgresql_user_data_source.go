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

// hyperfluid_managed_postgresql_user data source — look up an existing user by
// username within a cluster. Reuses the resource's model + toModel.

var (
	_ datasource.DataSource              = &managedPostgresqlUserDataSource{}
	_ datasource.DataSourceWithConfigure = &managedPostgresqlUserDataSource{}
)

func NewManagedPostgresqlUserDataSource() datasource.DataSource {
	return &managedPostgresqlUserDataSource{}
}

type managedPostgresqlUserDataSource struct {
	p *providerData
}

func (d *managedPostgresqlUserDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_managed_postgresql_user"
}

func (d *managedPostgresqlUserDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	cs := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an existing managed PostgreSQL user by username within a cluster.",
		Attributes: map[string]schema.Attribute{
			"managed_postgresql": schema.StringAttribute{Required: true, MarkdownDescription: "Cluster id the user belongs to."},
			"username":           schema.StringAttribute{Required: true, MarkdownDescription: "Username to look up."},
			"id":                 cs("User id."),
			"permission_level":   cs("Permission level (e.g. editor)."),
			"description":        cs("Free-form description."),
			"tags":               schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "User-defined tags."},
			"phase":              cs("Current lifecycle phase."),
			"slug":               cs("Derived slug."),
		},
	}
}

func (d *managedPostgresqlUserDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *managedPostgresqlUserDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg managedPostgresqlUserModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cluster := cfg.ManagedPostgres.ValueString()
	username := cfg.Username.ValueString()
	u, err := d.p.API.FindManagedPostgresqlUser(ctx, d.p.OrgID, cluster, username)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Managed PostgreSQL user not found", "no user named "+username+" in cluster "+cluster)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up managed PostgreSQL user", err.Error())
		return
	}

	state, diags := (&managedPostgresqlUserResource{p: d.p}).toModel(ctx, cluster, u)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
