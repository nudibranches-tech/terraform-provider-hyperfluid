// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-validators/boolvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// managed_postgresql_resource.go — DBaaS cluster. Unlike container_app, the
// status view echoes node_tier/engine/version/backup_policy/tags, so reads map
// cleanly with no resource_tier-style preserve; only storage_capacity is mapped
// back from the "NGi" storage_size string.

var (
	_ resource.Resource                = &managedPostgresqlResource{}
	_ resource.ResourceWithConfigure   = &managedPostgresqlResource{}
	_ resource.ResourceWithImportState = &managedPostgresqlResource{}
)

const managedPostgresqlWaitTimeout = 15 * time.Minute

func NewManagedPostgresqlResource() resource.Resource {
	return &managedPostgresqlResource{}
}

type managedPostgresqlResource struct {
	p *providerData
}

type managedPostgresqlModel struct {
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
	BackupPlacement  types.String `tfsdk:"backup_placement"`
	Configuration    types.String `tfsdk:"configuration"`
	ExposeToInternet types.Bool   `tfsdk:"expose_to_internet"`
	Description      types.String `tfsdk:"description"`
	Tags             types.List   `tfsdk:"tags"`

	// Framework types, not Go structs: the nested objects carry null and, for
	// `restore`, the framework's write-only nullification.
	Pitr             types.Object `tfsdk:"pitr"`
	Restore          types.Object `tfsdk:"restore"`
	RestoreWoVersion types.String `tfsdk:"restore_wo_version"`

	// computed
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

// pitrModel is the configured point-in-time recovery policy.
type pitrModel struct {
	Enabled                types.Bool  `tfsdk:"enabled"`
	ArchiveIntervalSeconds types.Int64 `tfsdk:"archive_interval_seconds"`
}

func pitrType() basetypes.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"enabled":                  types.BoolType,
		"archive_interval_seconds": types.Int64Type,
	}}
}

// restoreModel is the create-only bootstrap descriptor.
type restoreModel struct {
	BackupID         types.String `tfsdk:"backup_id"`
	SourceInstanceID types.String `tfsdk:"source_instance_id"`
	TargetTime       types.String `tfsdk:"target_time"`
	Exclusive        types.Bool   `tfsdk:"exclusive"`
}

func restoreType() basetypes.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"backup_id":          types.StringType,
		"source_instance_id": types.StringType,
		"target_time":        types.StringType,
		"exclusive":          types.BoolType,
	}}
}

// managedPostgresqlKeep carries the attributes no read echoes back, so a
// refresh keeps what the configuration asked for instead of nulling it: the
// status view reports neither the backup target or placement a database ships
// to nor its recovery policy, and the CRD spec view exposes none of them either.
type managedPostgresqlKeep struct {
	Pitr             types.Object
	BackupTargetID   types.String
	BackupPlacement  types.String
	RestoreWoVersion types.String
}

func (r *managedPostgresqlResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_managed_postgresql"
}

func (r *managedPostgresqlResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	forceNewStr := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{
			Optional: true, Computed: true, MarkdownDescription: desc,
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
		}
	}
	computedStr := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A managed PostgreSQL cluster in an environment.\n\n" +
			"~> Changing an attribute that forces a new cluster deletes the database and its data.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"env": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Environment id the cluster runs in. Changing this forces a new cluster.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Cluster name (slug). Changing this forces a new cluster.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"database_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Application database name. Changing this forces a new cluster.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"engine": func() schema.StringAttribute {
				a := forceNewStr("Engine: `postgresql`, `postgis` or `timescaledb`. Changing this forces a new cluster.")
				a.Validators = []validator.String{stringvalidator.OneOf("postgresql", "postgis", "timescaledb")}
				return a
			}(),
			"version": forceNewStr("PostgreSQL major version, e.g. `17`. Changing this forces a new cluster."),
			"node_tier": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Size: `nano`, `micro`, `small`, `medium`, `large` or `xlarge`.",
				Validators:          []validator.String{stringvalidator.OneOf("nano", "micro", "small", "medium", "large", "xlarge")},
			},
			"storage_capacity": schema.Int64Attribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Storage capacity in GB, 1 to 30. It can grow in place but not shrink.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"backup_policy": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Backup policy: `automated` or `manual` (default). Changing this forces a new cluster.",
				Validators:          []validator.String{stringvalidator.OneOf("automated", "manual")},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
			},
			"backup_target_id": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Id of the backup target that receives backups. Conflicts with `backup_placement`; " +
					"`automated` backups and `pitr` need one of the two. Not recovered by import. Changing this forces a new cluster.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.ConflictsWith(path.MatchRoot("backup_placement"))},
			},
			"backup_placement": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Set to `platform` to let the platform choose the backup target. Conflicts with " +
					"`backup_target_id`; removing it keeps the current placement, and import does not recover it.",
				Validators: []validator.String{stringvalidator.OneOf(string(console.BackupPlacementPlatform))},
			},
			"configuration": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Topology: `standalone` or `high-availability`.",
				Validators:          []validator.String{stringvalidator.OneOf("standalone", "high-availability")},
			},
			"expose_to_internet": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Whether the cluster gets an external endpoint reachable from the internet. Defaults to false.",
				Default:             booldefault.StaticBool(false),
			},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Free-form description."},
			"tags": schema.ListAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				MarkdownDescription: "User-defined tags.",
				PlanModifiers:       []planmodifier.List{},
			},
			"pitr": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Point-in-time recovery policy; needs `backup_target_id` or `backup_placement`. " +
					"Removing it does not turn recovery off (set `enabled = false`), and import does not recover it.",
				Attributes: map[string]schema.Attribute{
					"enabled": schema.BoolAttribute{
						Required: true,
						MarkdownDescription: "Whether a bounded recovery point objective is kept. `false` keeps archiving " +
							"but without forced switches, so recent changes on an idle database may not be recoverable.",
					},
					"archive_interval_seconds": schema.Int64Attribute{
						Optional:   true,
						Validators: []validator.Int64{int64validator.Between(60, 86400)},
						MarkdownDescription: "Seconds between forced WAL switches, 60 to 86400: the worst-case recovery " +
							"point on an idle database. Defaults to the platform's interval; once set, removing it keeps the last value.",
					},
				},
			},
			"restore": schema.SingleNestedAttribute{
				Optional: true, WriteOnly: true,
				MarkdownDescription: "Create the database from a backup or another database's archive. Write-only " +
					"(Terraform 1.11 or later) and read at create only: change `restore_wo_version` with it. `engine`, " +
					"`version` and enough `storage_capacity` must match the source.",
				Attributes: map[string]schema.Attribute{
					"backup_id": schema.StringAttribute{
						Optional: true, WriteOnly: true,
						MarkdownDescription: "Id of a completed backup in the same environment. Conflicts with `source_instance_id`.",
						Validators: []validator.String{
							stringvalidator.ExactlyOneOf(path.MatchRelative().AtParent().AtName("source_instance_id")),
						},
					},
					"source_instance_id": schema.StringAttribute{
						Optional: true, WriteOnly: true,
						MarkdownDescription: "Id of a database in the same environment whose archive to restore from; it must " +
							"have completed at least one base backup.",
					},
					"target_time": schema.StringAttribute{
						Optional: true, WriteOnly: true,
						MarkdownDescription: "RFC 3339 time to recover to, between the source's `first_recoverability_point` " +
							"and now. Requires `source_instance_id`; defaults to the latest archived point.",
						Validators: []validator.String{
							stringvalidator.AlsoRequires(path.MatchRelative().AtParent().AtName("source_instance_id")),
						},
					},
					"exclusive": schema.BoolAttribute{
						Optional: true, WriteOnly: true,
						MarkdownDescription: "Stop just before `target_time` instead of at it.",
						Validators: []validator.Bool{
							boolvalidator.AlsoRequires(path.MatchRelative().AtParent().AtName("target_time")),
						},
					},
				},
			},
			"restore_wo_version": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Any value; change it together with `restore` to restore again. Changing this forces a new cluster.",
				Validators:          []validator.String{stringvalidator.AlsoRequires(path.MatchRoot("restore"))},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},

			"phase":             computedStr("Current lifecycle phase."),
			"instances":         schema.Int64Attribute{Computed: true, MarkdownDescription: "Desired instance count."},
			"ready_instances":   schema.Int64Attribute{Computed: true, MarkdownDescription: "Ready instance count."},
			"write_endpoint":    computedStr("Primary (read-write) endpoint."),
			"read_endpoint":     computedStr("Read-only endpoint."),
			"external_endpoint": computedStr("External endpoint, if exposed."),
			"slug":              computedStr("Derived slug. This is the name a `hyperfluid_service_link` endpoint takes."),
			"archive_interval_seconds": schema.Int64Attribute{
				Computed: true,
				MarkdownDescription: "Seconds between forced WAL switches on the running cluster. `0` means none; " +
					"null means the database is not archived or not reported yet.",
			},
			"first_recoverability_point": computedStr("Earliest time a restore can target, or null when no base " +
				"backup has completed."),
			"last_successful_backup_time": computedStr("When the last base backup completed."),
			"last_failed_backup_time":     computedStr("When the last backup failed."),
		},
	}
}

func (r *managedPostgresqlResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "expected *providerData")
		return
	}
	r.p = pd
}

func (r *managedPostgresqlResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan managedPostgresqlModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tags, d := listToStringSlice(ctx, plan.Tags)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := console.CreateManagedPostgresqlCrdRequestBody{
		Name:             plan.Name.ValueString(),
		DatabaseName:     plan.DatabaseName.ValueString(),
		Description:      stringPtr(plan.Description),
		StorageCapacity:  int32PtrFromInt64(plan.StorageCapacity),
		ExposeToInternet: boolPtr(plan.ExposeToInternet),
	}
	if tags != nil {
		body.Tags = &tags
	}
	if !plan.Engine.IsNull() && !plan.Engine.IsUnknown() {
		e := console.Engine(plan.Engine.ValueString())
		body.Engine = &e
	}
	if !plan.Version.IsNull() && !plan.Version.IsUnknown() {
		body.Version = stringPtr(plan.Version)
	}
	if !plan.NodeTier.IsNull() && !plan.NodeTier.IsUnknown() {
		nt := console.NodeTier(plan.NodeTier.ValueString())
		body.NodeTier = &nt
	}
	if !plan.Configuration.IsNull() && !plan.Configuration.IsUnknown() {
		cfg := console.Configuration(plan.Configuration.ValueString())
		body.Configuration = &cfg
	}
	if !plan.BackupPolicy.IsNull() && !plan.BackupPolicy.IsUnknown() {
		bp := console.BackupPolicy(plan.BackupPolicy.ValueString())
		body.BackupPolicy = &bp
	}
	if !plan.BackupTargetID.IsNull() && !plan.BackupTargetID.IsUnknown() {
		bt, err := uuid.Parse(plan.BackupTargetID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Invalid backup_target_id", "must be a UUID: "+err.Error())
			return
		}
		body.BackupTargetId = &bt
	}
	if !plan.BackupPlacement.IsNull() && !plan.BackupPlacement.IsUnknown() {
		bp := console.BackupPlacement(plan.BackupPlacement.ValueString())
		body.BackupPlacement = &bp
	}

	pitr, d := pitrRequestFrom(ctx, plan.Pitr)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	body.Pitr = pitr

	// The config, not the plan: `restore` is write-only, so the framework has
	// already nullified it everywhere but there.
	var restoreCfg types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("restore"), &restoreCfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	restore, d := restoreFrom(ctx, restoreCfg)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	body.Restore = restore

	created, err := r.p.API.CreateManagedPostgresql(ctx, r.p.OrgID, plan.Env.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create managed postgresql", err.Error())
		return
	}
	id := created.Id.String()

	if err := r.waitReady(ctx, id); err != nil {
		resp.Diagnostics.AddError("Cluster did not become ready", err.Error())
		return
	}
	state, err := r.readInto(ctx, id, managedPostgresqlKeep{
		Pitr:             plan.Pitr,
		BackupTargetID:   plan.BackupTargetID,
		BackupPlacement:  plan.BackupPlacement,
		RestoreWoVersion: plan.RestoreWoVersion,
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to read cluster after create", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *managedPostgresqlResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior managedPostgresqlModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.readInto(ctx, prior.ID.ValueString(), managedPostgresqlKeep{
		Pitr:             prior.Pitr,
		BackupTargetID:   prior.BackupTargetID,
		BackupPlacement:  prior.BackupPlacement,
		RestoreWoVersion: prior.RestoreWoVersion,
	})
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read managed postgresql", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *managedPostgresqlResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state managedPostgresqlModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tags, d := listToStringSlice(ctx, plan.Tags)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := console.PatchManagedPostgresqlCrdRequestBody{
		Description:      stringPtr(plan.Description),
		StorageCapacity:  int32PtrFromInt64(plan.StorageCapacity),
		ExposeToInternet: boolPtr(plan.ExposeToInternet),
	}
	if tags != nil {
		body.Tags = &tags
	}
	if !plan.NodeTier.IsNull() && !plan.NodeTier.IsUnknown() {
		nt := console.NodeTier(plan.NodeTier.ValueString())
		body.NodeTier = &nt
	}
	if !plan.Configuration.IsNull() && !plan.Configuration.IsUnknown() {
		cfg := console.Configuration(plan.Configuration.ValueString())
		body.Configuration = &cfg
	}
	pitr, d := pitrRequestFrom(ctx, plan.Pitr)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	body.Pitr = pitr
	// Sent only when it changes: a placement cannot be unset through the API, so
	// one removed from the configuration is left as it is.
	if !plan.BackupPlacement.IsNull() && !plan.BackupPlacement.IsUnknown() && !plan.BackupPlacement.Equal(state.BackupPlacement) {
		bp := console.BackupPlacement(plan.BackupPlacement.ValueString())
		body.BackupPlacement = &bp
	}

	id := state.ID.ValueString()
	if err := r.p.API.PatchManagedPostgresql(ctx, r.p.OrgID, id, body); err != nil {
		resp.Diagnostics.AddError("Failed to update managed postgresql", err.Error())
		return
	}
	if err := r.waitReady(ctx, id); err != nil {
		resp.Diagnostics.AddError("Cluster did not become ready after update", err.Error())
		return
	}
	newState, err := r.readInto(ctx, id, managedPostgresqlKeep{
		Pitr:             plan.Pitr,
		BackupTargetID:   plan.BackupTargetID,
		BackupPlacement:  plan.BackupPlacement,
		RestoreWoVersion: plan.RestoreWoVersion,
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to read cluster after update", err.Error())
		return
	}
	// Configured in-place fields reflect the plan (desired state the operator
	// converges to): a PVC resize / tier change is eventually consistent, so the
	// status can still report the old storage_size/node_tier right after PATCH.
	// Only the computed status fields are taken from the read.
	if !plan.StorageCapacity.IsUnknown() {
		newState.StorageCapacity = plan.StorageCapacity
	}
	if !plan.NodeTier.IsUnknown() {
		newState.NodeTier = plan.NodeTier
	}
	if !plan.Configuration.IsUnknown() {
		newState.Configuration = plan.Configuration
	}
	if !plan.Tags.IsUnknown() {
		newState.Tags = plan.Tags
	}
	newState.Description = plan.Description
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *managedPostgresqlResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state managedPostgresqlModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.p.API.DeleteManagedPostgresql(ctx, r.p.OrgID, id); err != nil {
		resp.Diagnostics.AddError("Failed to delete managed postgresql", err.Error())
		return
	}
	if err := pollGoneOn404(ctx, managedPostgresqlWaitTimeout, func() error {
		_, err := r.p.API.GetManagedPostgresql(ctx, r.p.OrgID, id)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Cluster still present after delete", err.Error())
	}
}

func (r *managedPostgresqlResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *managedPostgresqlResource) waitReady(ctx context.Context, id string) error {
	_, err := waitForReady(ctx, managedPostgresqlWaitTimeout, func() (*console.ManagedPostgresqlResponse, bool, error) {
		c, err := r.p.API.GetManagedPostgresql(ctx, r.p.OrgID, id)
		if err != nil {
			return nil, false, err
		}
		// Fail fast: the operator parks a cluster it refuses to build at
		// phase=Failed and names the reason on the ClusterReady condition (an
		// archive prefix another database already occupies, a backup secret it
		// cannot resolve, a CNPG cluster it cannot recover). Nothing is created in
		// that state, so polling to the timeout only hides why.
		if c.Phase != nil && *c.Phase == "Failed" {
			msg := conditionMessage(c.Conditions, "ClusterReady")
			if msg == "" {
				msg = "database entered the Failed phase"
			}
			return nil, false, errors.New(msg)
		}
		ready := c.Instances > 0 && c.ReadyInstances == c.Instances
		return c, ready, nil
	})
	return err
}

// readInto builds the model from both views, carrying keep through for the
// attributes neither view reports.
func (r *managedPostgresqlResource) readInto(ctx context.Context, id string, keep managedPostgresqlKeep) (managedPostgresqlModel, error) {
	c, err := r.p.API.GetManagedPostgresql(ctx, r.p.OrgID, id)
	if err != nil {
		return managedPostgresqlModel{}, err
	}
	// expose_to_internet lives on the CRD spec, not the status view, so it needs a
	// separate /crd GET (mirrors container_app's spec/status split).
	spec, err := r.p.API.GetManagedPostgresqlSpec(ctx, r.p.OrgID, id)
	if err != nil {
		return managedPostgresqlModel{}, err
	}
	tags, d := stringSliceToList(ctx, c.Tags)
	if d.HasError() {
		return managedPostgresqlModel{}, errors.New("failed to convert tags")
	}

	m := managedPostgresqlModel{
		ID:               types.StringValue(id),
		Env:              types.StringValue(c.HarborId.String()),
		Name:             types.StringValue(c.Name),
		DatabaseName:     types.StringValue(c.DatabaseName),
		Engine:           types.StringValue(c.Engine),
		Version:          types.StringValue(c.Version),
		NodeTier:         types.StringValue(c.NodeTier),
		StorageCapacity:  types.Int64Value(parseStorageGB(c.StorageSize)),
		BackupPolicy:     optString(&c.BackupPolicy),
		Configuration:    types.StringValue(c.Configuration),
		ExposeToInternet: types.BoolValue(spec.ExposeToInternet),
		Description:      optString(c.Description),
		Tags:             tags,
		Phase:            optString(c.Phase),
		Instances:        types.Int64Value(int64(c.Instances)),
		ReadyInstances:   types.Int64Value(int64(c.ReadyInstances)),
		WriteEndpoint:    optString(c.WriteEndpoint),
		ReadEndpoint:     optString(c.ReadEndpoint),
		ExternalEndpoint: optString(c.ExternalEndpoint),
		Slug:             types.StringValue(c.Slug),

		BackupTargetID:           keep.BackupTargetID,
		BackupPlacement:          keep.BackupPlacement,
		Pitr:                     keep.Pitr,
		Restore:                  types.ObjectNull(restoreType().AttrTypes),
		RestoreWoVersion:         keep.RestoreWoVersion,
		ArchiveIntervalSeconds:   optInt64FromInt32(c.ArchiveIntervalSeconds),
		FirstRecoverabilityPoint: optTimeString(c.FirstRecoverabilityPoint),
		LastSuccessfulBackupTime: optTimeString(c.LastSuccessfulBackupTime),
		LastFailedBackupTime:     optTimeString(c.LastFailedBackupTime),
	}
	return m, nil
}

// pitrRequestFrom converts a configured `pitr` into the API input. Returns nil
// when the config leaves it out, which the caller sends as an absent field —
// and which the platform reads as "leave the policy as it is".
func pitrRequestFrom(ctx context.Context, obj types.Object) (*console.PitrRequest, diag.Diagnostics) {
	var d diag.Diagnostics
	if obj.IsNull() || obj.IsUnknown() {
		return nil, d
	}
	var m pitrModel
	d.Append(obj.As(ctx, &m, basetypes.ObjectAsOptions{})...)
	if d.HasError() {
		return nil, d
	}
	return &console.PitrRequest{
		Enabled:                m.Enabled.ValueBool(),
		ArchiveIntervalSeconds: int32PtrFromInt64(m.ArchiveIntervalSeconds),
	}, d
}

// restoreFrom converts a configured `restore` into the API input, parsing the
// ids and the timestamp into the shapes the API takes. Returns nil when the
// config leaves it out, which bootstraps an empty database.
func restoreFrom(ctx context.Context, obj types.Object) (*console.RestoreFromBackup, diag.Diagnostics) {
	var d diag.Diagnostics
	if obj.IsNull() || obj.IsUnknown() {
		return nil, d
	}
	var m restoreModel
	d.Append(obj.As(ctx, &m, basetypes.ObjectAsOptions{})...)
	if d.HasError() {
		return nil, d
	}

	out := &console.RestoreFromBackup{}
	root := path.Root("restore")
	if !m.BackupID.IsNull() && !m.BackupID.IsUnknown() {
		id, err := uuid.Parse(m.BackupID.ValueString())
		if err != nil {
			d.AddAttributeError(root.AtName("backup_id"), "Invalid backup_id", "must be a UUID: "+err.Error())
			return nil, d
		}
		out.BackupId = &id
	}
	if !m.SourceInstanceID.IsNull() && !m.SourceInstanceID.IsUnknown() {
		id, err := uuid.Parse(m.SourceInstanceID.ValueString())
		if err != nil {
			d.AddAttributeError(root.AtName("source_instance_id"), "Invalid source_instance_id", "must be a UUID: "+err.Error())
			return nil, d
		}
		out.SourceInstanceId = &id
	}
	if !m.TargetTime.IsNull() && !m.TargetTime.IsUnknown() {
		t, err := time.Parse(time.RFC3339, m.TargetTime.ValueString())
		if err != nil {
			d.AddAttributeError(root.AtName("target_time"), "Invalid target_time", "must be an RFC 3339 timestamp: "+err.Error())
			return nil, d
		}
		out.TargetTime = &t
	}
	if !m.Exclusive.IsNull() && !m.Exclusive.IsUnknown() {
		out.Exclusive = m.Exclusive.ValueBoolPointer()
	}
	return out, d
}
