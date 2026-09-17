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

// hyperfluid_airflow data source — look up an existing Airflow environment by
// name within a harbor. Reuses the resource's model + readInto, so the two
// surfaces can never disagree about how the API maps into attributes.

var (
	_ datasource.DataSource              = &airflowDataSource{}
	_ datasource.DataSourceWithConfigure = &airflowDataSource{}
)

func NewAirflowDataSource() datasource.DataSource {
	return &airflowDataSource{}
}

type airflowDataSource struct {
	p *providerData
}

func (d *airflowDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_airflow"
}

func (d *airflowDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	cs := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	cb := func(desc string) schema.BoolAttribute {
		return schema.BoolAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an existing Airflow environment by name within a harbor — an " +
			"environment created through the console, `hfctl`, or another Terraform configuration.\n\n" +
			"Its `id` is what a `hyperfluid_airflow_connection`'s `airflow` takes, and `dag_bucket` names " +
			"the bucket DAGs are delivered through, so this data source is the usual way to attach " +
			"connections or a DAG-upload pipeline to an environment somebody else owns.",
		Attributes: map[string]schema.Attribute{
			"env":  schema.StringAttribute{Required: true, MarkdownDescription: "Environment (harbor) id the Airflow environment runs in."},
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Airflow environment name."},

			"id":             cs("Environment id. This is the value a `hyperfluid_airflow_connection`'s `airflow` takes."),
			"postgres_ref":   cs("Name of the referenced metadata PostgreSQL cluster, or null when the platform provisioned one."),
			"dag_bucket_ref": cs("Name of the referenced DAG bucket, or null when the platform provisioned one."),
			"node_tier":      cs("Size of the four Airflow components: `micro`, `small`, `medium` or `large`."),
			"triggerer_enabled": cb("Whether the triggerer runs, which is what lets deferrable operators " +
				"give their worker slot back while they wait."),
			"sleep_mode":          cb("Whether every component is scaled to zero."),
			"runtime_image":       cs("Pinned Airflow runtime image, or null when the environment tracks the platform's own."),
			"task_quota_max_pods": schema.Int64Attribute{Computed: true, MarkdownDescription: "Ceiling on concurrent task pods, or null when the platform default is in charge."},
			"description":         cs("Free-form description."),
			"tags":                schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "User-defined tags."},

			"egress": schema.SingleNestedAttribute{
				Computed: true,
				MarkdownDescription: "Egress the environment's task pods are granted on top of the platform " +
					"baseline. Null when the environment runs on the baseline alone.",
				Attributes: map[string]schema.Attribute{
					"fqdns": schema.SetAttribute{
						ElementType: types.StringType, Computed: true,
						MarkdownDescription: "Public hostnames task pods may reach on 443.",
					},
					"in_cluster": schema.SetNestedAttribute{
						Computed:            true,
						MarkdownDescription: "Same-harbor services task pods may reach.",
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"kind": schema.StringAttribute{Computed: true, MarkdownDescription: "Kind of service."},
								"name": schema.StringAttribute{Computed: true, MarkdownDescription: "The target's slug in the same harbor."},
							},
						},
					},
					"allowlists": schema.SetAttribute{
						ElementType: types.StringType, Computed: true,
						MarkdownDescription: "Names of the harbor's shared egress allow-lists attached to the environment.",
					},
				},
			},
			"config": schema.MapAttribute{
				ElementType: types.StringType, Computed: true,
				MarkdownDescription: "`airflow.cfg` overrides, keyed `section.key`. Null when none are set.",
			},

			"slug":                    cs("Derived slug. This is the name an `egress.in_cluster` entry would target."),
			"phase":                   cs("Live lifecycle phase: `Pending`, `Provisioning`, `Running`, `Sleeping`, `Error` or `Unknown`."),
			"web_url":                 cs("Public HTTPS URL of the Airflow UI, once the route is serving."),
			"public_host":             cs("Public hostname the UI is published under."),
			"network_mode":            cs("How the UI is exposed: `ingress` or `httproute`."),
			"task_namespace":          cs("Dedicated Kubernetes namespace the environment's task pods run in."),
			"dag_bucket":              cs("Bucket DAGs are delivered through. Upload DAGs under its `dags/` prefix."),
			"service_account":         cs("Data-plane service account every task pod runs as."),
			"managed_postgresql_name": cs("Name of the metadata PostgreSQL cluster, whether referenced or provisioned."),
			"unresolved_allowlists": schema.ListAttribute{
				ElementType: types.StringType, Computed: true,
				MarkdownDescription: "Names from `egress.allowlists` the last reconcile could not resolve to an allow-list of this harbor.",
			},
			"cpu_request":    cs("CPU request per component, resolved from the tier."),
			"cpu_limit":      cs("CPU limit per component, resolved from the tier."),
			"memory_request": cs("Memory request per component, resolved from the tier."),
			"memory_limit":   cs("Memory limit per component, resolved from the tier."),
		},
	}
}

func (d *airflowDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *airflowDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg airflowModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	env := cfg.Env.ValueString()
	name := cfg.Name.ValueString()
	id, err := d.p.API.FindAirflow(ctx, d.p.OrgID, env, name)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Airflow environment not found", "no Airflow environment named "+name+" in environment "+env)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up Airflow environment", err.Error())
		return
	}

	// Reuse the resource's mapper (it only needs the API client) so the model
	// mapping lives in one place. There is no prior tier to fall back on here,
	// so the tier is whatever the resolved cpu/memory identify, and no settled
	// CRD to reuse either — a data source reads, it never waits for one.
	state, err := (&airflowResource{p: d.p}).readInto(ctx, id, types.StringNull(), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Airflow environment", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
