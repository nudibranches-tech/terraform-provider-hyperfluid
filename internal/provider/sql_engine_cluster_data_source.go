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

var (
	_ datasource.DataSource              = &sqlEngineClusterDataSource{}
	_ datasource.DataSourceWithConfigure = &sqlEngineClusterDataSource{}
)

func NewSqlEngineClusterDataSource() datasource.DataSource {
	return &sqlEngineClusterDataSource{}
}

type sqlEngineClusterDataSource struct {
	p *providerData
}

type sqlEngineClusterDataModel struct {
	ID             types.String `tfsdk:"id"`
	Env            types.String `tfsdk:"env"`
	Name           types.String `tfsdk:"name"`
	Description    types.String `tfsdk:"description"`
	WorkerReplicas types.Int64  `tfsdk:"worker_replicas"`
	Host           types.String `tfsdk:"host"`
	Port           types.Int64  `tfsdk:"port"`
	Status         types.String `tfsdk:"status"`
}

func (d *sqlEngineClusterDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_sql_engine_cluster"
}

func (d *sqlEngineClusterDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	cs := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an existing SQL engine cluster by name within an environment.",
		Attributes: map[string]schema.Attribute{
			"env":             schema.StringAttribute{Required: true, MarkdownDescription: "Environment id the cluster runs in."},
			"name":            schema.StringAttribute{Required: true, MarkdownDescription: "Cluster name."},
			"id":              cs("Cluster id."),
			"description":     cs("Free-form description."),
			"worker_replicas": schema.Int64Attribute{Computed: true, MarkdownDescription: "Number of Trino workers."},
			"host":            cs("External host, empty when not exposed."),
			"port":            schema.Int64Attribute{Computed: true, MarkdownDescription: "External port."},
			"status":          cs("Current status, such as `Online` or `Sleeping`."),
		},
	}
}

func (d *sqlEngineClusterDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *sqlEngineClusterDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg sqlEngineClusterDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := cfg.Name.ValueString()
	dock, err := d.p.API.FindDataDock(ctx, d.p.OrgID, cfg.Env.ValueString(), name)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("SQL engine cluster not found", "no SQL engine cluster named "+name+" in environment "+cfg.Env.ValueString())
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up SQL engine cluster", err.Error())
		return
	}
	m, err := sqlEngineClusterFrom(ctx, dock)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read SQL engine cluster", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, sqlEngineClusterDataModel{
		ID: m.ID, Env: m.Env, Name: m.Name, Description: m.Description,
		WorkerReplicas: m.WorkerReplicas, Host: m.Host, Port: m.Port, Status: m.Status,
	})...)
}
