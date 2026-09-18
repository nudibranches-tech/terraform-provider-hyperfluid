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
// within an environment. Reuses the resource's readInto and projects its model
// onto the readable attributes: the resource's `pitr`, `restore` and
// `restore_wo_version` have no data-source counterpart (nothing reads them
// back), and the framework requires the model and the schema to line up field
// for field.

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

type managedPostgresqlDataSourceModel struct {
	ID               types.String `tfsdk:"id"`
	Env              types.String `tfsdk:"env"`
	Name             types.String `tfsdk:"name"`
	DatabaseName     types.String `tfsdk:"database_name"`
	Engine           types.String `tfsdk:"engine"`
	Version          types.String `tfsdk:"version"`
	NodeTier         types.String `tfsdk:"node_tier"`
	StorageCapacity  types.Int64  `tfsdk:"storage_capacity"`
	BackupPolicy     types.String `tfsdk:"backup_policy"`
	BackupTargetID   types.String `tfsdk:"backup_target_id"`
	Configuration    types.String `tfsdk:"configuration"`
	ExposeToInternet types.Bool   `tfsdk:"expose_to_internet"`
	Description      types.String `tfsdk:"description"`
	Tags             types.List   `tfsdk:"tags"`
	Phase            types.String `tfsdk:"phase"`
	Instances        types.Int64  `tfsdk:"instances"`
	ReadyInstances   types.Int64  `tfsdk:"ready_instances"`
	WriteEndpoint    types.String `tfsdk:"write_endpoint"`
	ReadEndpoint     types.String `tfsdk:"read_endpoint"`
	ExternalEndpoint types.String `tfsdk:"external_endpoint"`
	Slug             types.String `tfsdk:"slug"`

	ArchiveIntervalSeconds   types.Int64  `tfsdk:"archive_interval_seconds"`
	FirstRecoverabilityPoint types.String `tfsdk:"first_recoverability_point"`
	LastSuccessfulBackupTime types.String `tfsdk:"last_successful_backup_time"`
	LastFailedBackupTime     types.String `tfsdk:"last_failed_backup_time"`
}

// dataSource projects the resource model onto the readable attributes.
func (m managedPostgresqlModel) dataSource() managedPostgresqlDataSourceModel {
	return managedPostgresqlDataSourceModel{
		ID:                       m.ID,
		Env:                      m.Env,
		Name:                     m.Name,
		DatabaseName:             m.DatabaseName,
		Engine:                   m.Engine,
		Version:                  m.Version,
		NodeTier:                 m.NodeTier,
		StorageCapacity:          m.StorageCapacity,
		BackupPolicy:             m.BackupPolicy,
		BackupTargetID:           m.BackupTargetID,
		Configuration:            m.Configuration,
		ExposeToInternet:         m.ExposeToInternet,
		Description:              m.Description,
		Tags:                     m.Tags,
		Phase:                    m.Phase,
		Instances:                m.Instances,
		ReadyInstances:           m.ReadyInstances,
		WriteEndpoint:            m.WriteEndpoint,
		ReadEndpoint:             m.ReadEndpoint,
		ExternalEndpoint:         m.ExternalEndpoint,
		Slug:                     m.Slug,
		ArchiveIntervalSeconds:   m.ArchiveIntervalSeconds,
		FirstRecoverabilityPoint: m.FirstRecoverabilityPoint,
		LastSuccessfulBackupTime: m.LastSuccessfulBackupTime,
		LastFailedBackupTime:     m.LastFailedBackupTime,
	}
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
			"backup_target_id":   cs("Backup target id. Always null: no API view reports which target a database ships to."),
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
			"slug":               cs("Derived slug. This is the name a `hyperfluid_service_link` endpoint takes."),
			"archive_interval_seconds": ci("Seconds between forced WAL segment switches on the running " +
				"cluster. `0` means no forced switch, so nothing bounds the lag on an idle database; null " +
				"means the database archives nowhere, or the platform has not reported on it yet."),
			"first_recoverability_point": cs("Earliest point a restore can target, as the backup catalog " +
				"reports it. Null on a database that archives nowhere, and on one that ships WAL but has " +
				"never completed a base backup."),
			"last_successful_backup_time": cs("When the last base backup completed. A continuous-archive " +
				"restore needs one, because recovery replays forward from a base backup."),
			"last_failed_backup_time": cs("When the last backup failed."),
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
	var cfg managedPostgresqlDataSourceModel
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
	state, err := (&managedPostgresqlResource{p: d.p}).readInto(ctx, id, managedPostgresqlKeep{
		Pitr:             types.ObjectNull(pitrType().AttrTypes),
		BackupTargetID:   types.StringNull(),
		RestoreWoVersion: types.StringNull(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to read managed PostgreSQL", err.Error())
		return
	}
	out := state.dataSource()
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}
