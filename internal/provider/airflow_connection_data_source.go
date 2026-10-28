// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

// hyperfluid_airflow_connection data source — look up a managed connection by
// its conn_id within an environment. Reuses the resource's model + mapper.

var (
	_ datasource.DataSource              = &airflowConnectionDataSource{}
	_ datasource.DataSourceWithConfigure = &airflowConnectionDataSource{}
)

func NewAirflowConnectionDataSource() datasource.DataSource {
	return &airflowConnectionDataSource{}
}

type airflowConnectionDataSource struct {
	p *providerData
}

func (d *airflowConnectionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_airflow_connection"
}

func (d *airflowConnectionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	cs := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	cb := func(desc string) schema.BoolAttribute {
		return schema.BoolAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up a managed Airflow connection by its `conn_id`. No credential is exposed.",
		Attributes: map[string]schema.Attribute{
			"airflow": schema.StringAttribute{Required: true, MarkdownDescription: "Id of the `hyperfluid_airflow` environment the connection belongs to."},
			"conn_id": schema.StringAttribute{Required: true, MarkdownDescription: "The connection id a DAG asks Airflow for."},

			"id":                        cs("Identifier `<airflow_id>/<conn_id>`."),
			"name":                      cs("Name the platform gave the connection object, different from `conn_id`."),
			"managed_postgresql_ref":    cs("PostgreSQL cluster the connection targets, for a database connection."),
			"bucket_ref":                cs("Bucket the connection targets, for a bucket connection."),
			"trino_ref":                 cs("Trino Data Dock the connection targets, for a Trino connection."),
			"catalog":                   cs("Default Trino catalog of a Trino connection, null otherwise."),
			"permission_level":          cs("Pinned access level, or null when the platform default applies."),
			"connection_type":           cs("Airflow `conn_type`: `postgres`, `aws` for a bucket, or `hyperfluid_trino`."),
			"resolved_permission_level": cs("Access level in force. Meaningless when `permission_level_applies` is false."),
			"permission_level_applies":  cb("Whether `permission_level` applies to this connection's type; false for Trino."),
			"phase": cs("Lifecycle phase: `Pending`, `WaitingForDependency`, `Collision`, `TakingOver`, " +
				"`Applying`, `Ready`, `Parked` (environment asleep), `Deleting` or `Failed`."),
			"spec_observed":                      cb("Whether the platform has processed the current configuration."),
			"source_applied":                     cb("Whether the credential has been applied to the target."),
			"collision":                          cb("Whether a connection the platform does not manage already holds `conn_id`."),
			"collision_existing_connection_type": cs("Type of the connection already holding `conn_id`, on a collision."),
			"conditions": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Status conditions behind `phase`.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"type":    cs("Condition type."),
						"status":  cs("`True`, `False` or `Unknown`."),
						"reason":  cs("Machine-readable reason."),
						"message": cs("Human-readable explanation."),
					},
				},
			},
		},
	}
}

func (d *airflowConnectionDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *airflowConnectionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg airflowConnectionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	airflowID := cfg.Airflow.ValueString()
	connID := cfg.ConnID.ValueString()
	conn, err := d.p.API.FindAirflowConnection(ctx, d.p.OrgID, airflowID, connID)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Airflow connection not found", "no connection with conn_id "+connID+" on Airflow environment "+airflowID)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up Airflow connection", err.Error())
		return
	}

	// Reuse the resource's mapper so the model mapping lives in one place.
	state, d2 := airflowConnectionToModel(ctx, airflowID, conn)
	resp.Diagnostics.Append(d2...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
