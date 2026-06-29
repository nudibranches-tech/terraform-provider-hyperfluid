// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

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

	// computed
	Phase            types.String `tfsdk:"phase"`
	Instances        types.Int64  `tfsdk:"instances"`
	ReadyInstances   types.Int64  `tfsdk:"ready_instances"`
	WriteEndpoint    types.String `tfsdk:"write_endpoint"`
	ReadEndpoint     types.String `tfsdk:"read_endpoint"`
	ExternalEndpoint types.String `tfsdk:"external_endpoint"`
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
				Optional:            true,
				MarkdownDescription: "Backup target id (required when backup_policy is automated). Changing this forces a new cluster.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
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

			"phase":             computedStr("Current lifecycle phase."),
			"instances":         schema.Int64Attribute{Computed: true, MarkdownDescription: "Desired instance count."},
			"ready_instances":   schema.Int64Attribute{Computed: true, MarkdownDescription: "Ready instance count."},
			"write_endpoint":    computedStr("Primary (read-write) endpoint."),
			"read_endpoint":     computedStr("Read-only endpoint."),
			"external_endpoint": computedStr("External endpoint, if exposed."),
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
	state, err := r.readInto(ctx, plan.Env.ValueString(), id)
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
	state, err := r.readInto(ctx, prior.Env.ValueString(), prior.ID.ValueString())
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

	id := state.ID.ValueString()
	if err := r.p.API.PatchManagedPostgresql(ctx, r.p.OrgID, id, body); err != nil {
		resp.Diagnostics.AddError("Failed to update managed postgresql", err.Error())
		return
	}
	if err := r.waitReady(ctx, id); err != nil {
		resp.Diagnostics.AddError("Cluster did not become ready after update", err.Error())
		return
	}
	newState, err := r.readInto(ctx, plan.Env.ValueString(), id)
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
		ready := c.Instances > 0 && c.ReadyInstances == c.Instances
		return c, ready, nil
	})
	return err
}

func (r *managedPostgresqlResource) readInto(ctx context.Context, env, id string) (managedPostgresqlModel, error) {
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
		Env:              types.StringValue(env),
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
	}
	// backup_target_id and backup_policy=="" handling: the status view reports
	// backup_policy but not the target id; keep target id null (it's ForceNew,
	// set only at create and not echoed).
	m.BackupTargetID = types.StringNull()
	return m, nil
}
