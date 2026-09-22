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
// status view reports neither the backup target a database ships to nor its
// recovery policy, and the CRD spec view exposes neither either.
type managedPostgresqlKeep struct {
	Pitr             types.Object
	BackupTargetID   types.String
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
		MarkdownDescription: "A managed PostgreSQL cluster (DBaaS).",
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
				a := forceNewStr("Engine: postgresql, postgis, timescaledb.")
				a.Validators = []validator.String{stringvalidator.OneOf("postgresql", "postgis", "timescaledb")}
				return a
			}(),
			"version": forceNewStr("PostgreSQL major version, e.g. \"17\"."),
			"node_tier": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Resource tier: nano, micro, small, medium, large, xlarge.",
				Validators:          []validator.String{stringvalidator.OneOf("nano", "micro", "small", "medium", "large", "xlarge")},
			},
			"storage_capacity": schema.Int64Attribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Storage capacity in GB (1-30). Growth is applied in place but is " +
					"eventually consistent — `plan` may show the increase as pending until the " +
					"underlying volume finishes expanding.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"backup_policy": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Backup policy: automated or manual (defaults to manual). Changing this forces a new cluster.",
				Validators:          []validator.String{stringvalidator.OneOf("automated", "manual")},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
			},
			"backup_target_id": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Backup target id — where base backups and, with `pitr`, the " +
					"write-ahead log ship. Required when `backup_policy` is `automated` and whenever " +
					"`pitr` is set. Changing this forces a new cluster.\n\n" +
					"~> No API view reports it, so Terraform carries it from prior state. " +
					"`terraform import` cannot recover it, and a target attached elsewhere is invisible " +
					"to `terraform plan`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"configuration": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Topology: standalone or high-availability.",
				Validators:          []validator.String{stringvalidator.OneOf("standalone", "high-availability")},
			},
			"expose_to_internet": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Whether the cluster is reachable from the internet via an external NodePort Service. Defaults to false (reachable only in-cluster), matching the platform's private-by-default posture. Set true to publish an external endpoint.",
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
				MarkdownDescription: "Point-in-time recovery policy. Needs a `backup_target_id`, because the " +
					"write-ahead log has to ship somewhere; the platform refuses the policy otherwise, " +
					"and refuses it at apply time rather than at plan time.\n\n" +
					"~> No API view echoes this policy back, so Terraform carries it from prior state. " +
					"`terraform import` therefore cannot recover it, and a policy changed in the console " +
					"or through `hfctl` is invisible to `terraform plan`.\n\n" +
					"~> Removing `pitr` from the configuration does **not** turn recovery off: the " +
					"platform reads an absent policy as \"leave it as it is\", so `enabled = false` is " +
					"the only off switch.",
				Attributes: map[string]schema.Attribute{
					"enabled": schema.BoolAttribute{
						Required: true,
						MarkdownDescription: "Whether a bounded recovery point objective is kept. `false` does not " +
							"stop archiving — a database with a backup target attached always ships its WAL, " +
							"because a base backup is not consistent without it. It drops the forced segment " +
							"switch, so WAL ships only as segments fill and an idle hour may not be recoverable.",
					},
					"archive_interval_seconds": schema.Int64Attribute{
						Optional:   true,
						Validators: []validator.Int64{int64validator.Between(60, 86400)},
						MarkdownDescription: "Seconds between forced WAL segment switches (60-86400) — the " +
							"worst-case recovery point objective while the database is idle. Left out, the " +
							"platform resolves its own interval at every reconcile, so a platform change " +
							"reaches the database.\n\n" +
							"~> There is no way back to that resolved interval: a pin can be changed to " +
							"another number, and dropping it from the config keeps the pin the platform " +
							"already stored. It also survives `enabled = false`, so turning recovery back " +
							"on restores the same objective.\n\n" +
							"This is what the database asks for. The top-level `archive_interval_seconds` " +
							"is what the running cluster carries.",
					},
				},
			},
			"restore": schema.SingleNestedAttribute{
				Optional: true, WriteOnly: true,
				MarkdownDescription: "Bootstrap the database from an existing archive instead of an empty " +
					"`initdb`. Write-only: it is sent on create and never written to state, so it requires " +
					"Terraform >= 1.11.\n\n" +
					"~> Read at create only. The platform cannot restore a database in place and " +
					"echoes nothing back, so Terraform cannot see this block change on its own: " +
					"editing it produces no plan unless `restore_wo_version` changes with it.\n\n" +
					"`engine` and `version` must match the source exactly, and an omitted `engine` or " +
					"`version` is compared as `postgresql` / `17` rather than inherited — state both on " +
					"any other source. Leaving `backup_target_id` out inherits the source's backup target " +
					"and its schedule, which can read back as `backup_policy = \"automated\"`. Nothing " +
					"checks `storage_capacity` against the source's volume: a restore into a smaller one " +
					"is accepted here and fails inside the cluster.",
				Attributes: map[string]schema.Attribute{
					"backup_id": schema.StringAttribute{
						Optional: true, WriteOnly: true,
						MarkdownDescription: "Id of a completed backup to restore exactly as it was taken. " +
							"Must live in the same environment as the new database.",
						Validators: []validator.String{
							stringvalidator.ExactlyOneOf(path.MatchRelative().AtParent().AtName("source_instance_id")),
						},
					},
					"source_instance_id": schema.StringAttribute{
						Optional: true, WriteOnly: true,
						MarkdownDescription: "Id of the database whose continuous WAL archive to restore from. " +
							"Must live in the same environment as the new database, and is the only route that " +
							"accepts a `target_time`. The source must have completed at least one base backup.",
					},
					"target_time": schema.StringAttribute{
						Optional: true, WriteOnly: true,
						MarkdownDescription: "RFC 3339 point to recover to. Must fall between the source's " +
							"`first_recoverability_point` and now, which the platform checks when the create " +
							"call runs — the window is not known at plan time. Left out, recovery replays to " +
							"the latest archived WAL.",
						Validators: []validator.String{
							stringvalidator.AlsoRequires(path.MatchRelative().AtParent().AtName("source_instance_id")),
						},
					},
					"exclusive": schema.BoolAttribute{
						Optional: true, WriteOnly: true,
						MarkdownDescription: "Stop immediately before `target_time` rather than at it.",
						Validators: []validator.Bool{
							boolvalidator.AlsoRequires(path.MatchRelative().AtParent().AtName("target_time")),
						},
					},
				},
			},
			"restore_wo_version": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Arbitrary token that makes a changed `restore` visible to Terraform. " +
					"A write-only attribute never reaches state, so nothing else can tell one restore " +
					"descriptor from the next; change this alongside `restore` to ask for the restore " +
					"again. Changing it forces a new cluster, because a restore only happens when a " +
					"database is created.",
				Validators:    []validator.String{stringvalidator.AlsoRequires(path.MatchRoot("restore"))},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
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
				MarkdownDescription: "Seconds between forced WAL segment switches on the running cluster — " +
					"the interval the platform resolved, not the one `pitr` asks for, so it lags a change " +
					"until the cluster picks it up. `0` means no forced switch, so nothing bounds the lag " +
					"on an idle database; null means the database archives nowhere, or the platform has " +
					"not reported on it yet.",
			},
			"first_recoverability_point": computedStr("Earliest point a restore can target, as the backup " +
				"catalog reports it. Null on a database that archives nowhere, and on one that ships WAL " +
				"but has never completed a base backup — which is not restorable however healthy its " +
				"archiver looks."),
			"last_successful_backup_time": computedStr("When the last base backup completed. A " +
				"continuous-archive restore needs one, because recovery replays forward from a base backup."),
			"last_failed_backup_time": computedStr("When the last backup failed."),
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
