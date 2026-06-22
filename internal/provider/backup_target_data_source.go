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

// hyperfluid_backup_target data source — look up an existing backup target by
// name within an environment. Reuses the resource's model + readInto.

var (
	_ datasource.DataSource              = &backupTargetDataSource{}
	_ datasource.DataSourceWithConfigure = &backupTargetDataSource{}
)

func NewBackupTargetDataSource() datasource.DataSource {
	return &backupTargetDataSource{}
}

type backupTargetDataSource struct {
	p *providerData
}

func (d *backupTargetDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_backup_target"
}

func (d *backupTargetDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	cs := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an existing backup target by name within an environment.",
		Attributes: map[string]schema.Attribute{
			"env":                           schema.StringAttribute{Required: true, MarkdownDescription: "Environment id the target belongs to."},
			"name":                          schema.StringAttribute{Required: true, MarkdownDescription: "Backup target name."},
			"id":                            cs("Backup target id."),
			"endpoint_url":                  cs("S3-compatible endpoint URL."),
			"destination_path":              cs("Bucket + prefix backups are stored under."),
			"access_key_secret_name":        cs("Secret holding the S3 access key id."),
			"secret_access_key_secret_name": cs("Secret holding the S3 secret access key."),
			"insecure":                      schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether TLS verification is skipped."},
			"description":                   cs("Free-form description."),
			"tags":                          schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "User-defined tags."},
			"phase":                         cs("Current lifecycle phase."),
			"slug":                          cs("Derived slug."),
		},
	}
}

func (d *backupTargetDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *backupTargetDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg backupTargetModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	env := cfg.Env.ValueString()
	name := cfg.Name.ValueString()
	id, err := d.p.API.FindBackupTarget(ctx, d.p.OrgID, env, name)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Backup target not found", "no backup target named "+name+" in environment "+env)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up backup target", err.Error())
		return
	}

	// Reuse the resource's mapper (it only needs the API client) so the model
	// mapping lives in one place.
	state, err := (&backupTargetResource{p: d.p}).readInto(ctx, env, id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read backup target", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
