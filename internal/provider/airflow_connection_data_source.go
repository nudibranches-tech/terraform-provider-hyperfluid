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
		MarkdownDescription: "Look up a managed Airflow connection by the `conn_id` a DAG asks for — a " +
			"connection declared through the console, `hfctl`, or another Terraform configuration.\n\n" +
			"This is a read of the declaration and its status, which is what makes it useful for asserting " +
			"that a connection a DAG depends on is actually in force: `phase`, `source_applied` and " +
			"`collision` say whether the credential reached the target, and `resolved_permission_level` " +
			"says what access it granted — where the target is one the platform grants access on at all, " +
			"which `permission_level_applies` reports. No credential is exposed: the platform mints a " +
			"bucket connection's object-store session fresh, in-cluster and short-lived and it is part of " +
			"no read, and neither is the client secret a Trino connection's row carries.",
		Attributes: map[string]schema.Attribute{
			"airflow": schema.StringAttribute{Required: true, MarkdownDescription: "Id of the `hyperfluid_airflow` environment the connection belongs to."},
			"conn_id": schema.StringAttribute{Required: true, MarkdownDescription: "The connection id a DAG asks Airflow for."},

			"id": cs("Composite identifier `<airflow_id>/<conn_id>`, the same string the " +
				"`hyperfluid_airflow_connection` resource exports as `id` and `terraform import` takes."),
			"name":                   cs("The connection object's own name, which is how the API addresses it. Not the same string as `conn_id`."),
			"managed_postgresql_ref": cs("Name of the PostgreSQL cluster the connection targets, for a database connection."),
			"bucket_ref":             cs("Name of the bucket the connection targets, for a bucket connection."),
			"trino_ref":              cs("Name of the Trino Data Dock the connection targets, for a Trino connection."),
			"catalog": cs("The Trino catalog the connection opens against. Set for a Trino connection, where " +
				"it is required, and null for every other type."),
			"permission_level": cs("The level pinned on the connection, or null when it follows the platform default."),
			"connection_type": cs("The Airflow `conn_type` of the row the platform wrote, derived from which " +
				"target it names: `postgres`, `aws` for a bucket, or `hyperfluid_trino`."),
			"resolved_permission_level": cs("The level actually in force: the pinned value, or the platform's " +
				"own default when nothing is pinned. Read `permission_level_applies` first — where the knob " +
				"does not apply, this field describes nothing the platform granted."),
			"permission_level_applies": cb("Whether `permission_level` means anything for this connection's " +
				"type. False for a Trino connection, which carries the environment's own service account and " +
				"is therefore granted no authority of its own for a level to scale."),
			"phase": cs("Lifecycle phase: `Pending`, `WaitingForDependency`, `Collision`, `TakingOver`, " +
				"`Applying`, `Ready`, `Parked`, `Deleting` or `Failed`. `Parked` means the environment is asleep."),
			"spec_observed":  cb("Whether the platform has looked at the current declaration yet."),
			"source_applied": cb("Whether the credential has actually been applied to the target."),
			"collision":      cb("Whether an Airflow connection row the platform does not manage — a hand-written one, say — already owns this `conn_id`."),
			"collision_existing_connection_type": cs("Type of the connection already holding this `conn_id`, " +
				"when there is a collision."),
			"conditions": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The platform's status conditions for this connection — the detail behind `phase`.",
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
