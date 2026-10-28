// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

var (
	_ resource.Resource                = &sqlEngineClusterResource{}
	_ resource.ResourceWithConfigure   = &sqlEngineClusterResource{}
	_ resource.ResourceWithImportState = &sqlEngineClusterResource{}
)

const sqlEngineClusterWaitTimeout = 15 * time.Minute

// trinoMemory is the size format Trino reads (`2GB`), which the API enforces
// instead of the Kubernetes one (`2Gi`).
var trinoMemory = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(MB|GB|TB|PB)$`)

func NewSqlEngineClusterResource() resource.Resource {
	return &sqlEngineClusterResource{}
}

type sqlEngineClusterResource struct {
	p *providerData
}

type sqlEngineClusterModel struct {
	ID                    types.String `tfsdk:"id"`
	Env                   types.String `tfsdk:"env"`
	Name                  types.String `tfsdk:"name"`
	Description           types.String `tfsdk:"description"`
	RefreshInterval       types.Int64  `tfsdk:"refresh_interval"`
	ExposeToInternet      types.Bool   `tfsdk:"expose_to_internet"`
	WorkerReplicas        types.Int64  `tfsdk:"worker_replicas"`
	CoordinatorCPU        types.String `tfsdk:"coordinator_cpu"`
	CoordinatorMemory     types.String `tfsdk:"coordinator_memory"`
	WorkerCPU             types.String `tfsdk:"worker_cpu"`
	WorkerMemory          types.String `tfsdk:"worker_memory"`
	MaxMemoryPerNode      types.String `tfsdk:"max_memory_per_node"`
	QueryMaxMemory        types.String `tfsdk:"query_max_memory"`
	QueryMaxExecutionTime types.String `tfsdk:"query_max_execution_time"`
	TaskConcurrency       types.Int64  `tfsdk:"task_concurrency"`
	HashPartitionCount    types.Int64  `tfsdk:"hash_partition_count"`
	WorkerAutoScaling     types.Object `tfsdk:"worker_auto_scaling"`

	Host   types.String `tfsdk:"host"`
	Port   types.Int64  `tfsdk:"port"`
	Status types.String `tfsdk:"status"`
}

type workerAutoScalingModel struct {
	Enabled                 types.Bool  `tfsdk:"enabled"`
	MinReplicas             types.Int64 `tfsdk:"min_replicas"`
	MaxReplicas             types.Int64 `tfsdk:"max_replicas"`
	TargetCPUUtilization    types.Int64 `tfsdk:"target_cpu_utilization"`
	TargetMemoryUtilization types.Int64 `tfsdk:"target_memory_utilization"`
}

var workerAutoScalingType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"enabled":                   types.BoolType,
	"min_replicas":              types.Int64Type,
	"max_replicas":              types.Int64Type,
	"target_cpu_utilization":    types.Int64Type,
	"target_memory_utilization": types.Int64Type,
}}

func (r *sqlEngineClusterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_sql_engine_cluster"
}

func (r *sqlEngineClusterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	memory := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{
			Optional: true, MarkdownDescription: desc,
			Validators: []validator.String{stringvalidator.RegexMatches(trinoMemory, "must be a Trino size such as `2GB` or `512MB`")},
		}
	}
	optStr := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A SQL engine cluster (Trino) in an environment. Its catalogs are `hyperfluid_sql_engine_catalog` resources.\n\n" +
			"~> Changing `env` or `name` deletes the cluster and the catalogs it serves.",
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
				MarkdownDescription: "Cluster name, unique in the environment. Changing this forces a new cluster.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Optional: true, MarkdownDescription: "Free-form description.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"refresh_interval": schema.Int64Attribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Seconds between two refreshes of the catalog metadata the console shows.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
				Validators:          []validator.Int64{int64validator.AtLeast(0)},
			},
			"expose_to_internet": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Whether native clients reach the cluster from outside. Left out, it is not managed (the platform exposes clusters by default).",
			},
			"worker_replicas": schema.Int64Attribute{
				Required:            true,
				MarkdownDescription: "Number of Trino workers.",
				Validators:          []validator.Int64{int64validator.AtLeast(0)},
			},
			"coordinator_cpu":          optStr("Coordinator CPU, as a Kubernetes quantity such as `500m`."),
			"coordinator_memory":       memory("Coordinator memory, such as `2GB`."),
			"worker_cpu":               optStr("Worker CPU, as a Kubernetes quantity such as `500m`."),
			"worker_memory":            memory("Worker memory, such as `2GB`."),
			"max_memory_per_node":      memory("Memory one query may use on a node, such as `3GB`."),
			"query_max_memory":         memory("Memory one query may use across the cluster, such as `2GB`."),
			"query_max_execution_time": optStr("Longest a query may run, as a Trino duration such as `1h`."),
			"task_concurrency": schema.Int64Attribute{
				Optional: true, MarkdownDescription: "Trino `task.concurrency`.",
				Validators: []validator.Int64{int64validator.AtLeast(1)},
			},
			"hash_partition_count": schema.Int64Attribute{
				Optional: true, MarkdownDescription: "Trino `query.hash-partition-count`.",
				Validators: []validator.Int64{int64validator.AtLeast(1)},
			},
			"worker_auto_scaling": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Scale the workers between two bounds on their load.",
				Attributes: map[string]schema.Attribute{
					"enabled":      schema.BoolAttribute{Required: true, MarkdownDescription: "Whether autoscaling is on."},
					"min_replicas": schema.Int64Attribute{Required: true, MarkdownDescription: "Fewest workers."},
					"max_replicas": schema.Int64Attribute{Required: true, MarkdownDescription: "Most workers."},
					"target_cpu_utilization": schema.Int64Attribute{
						Optional: true, Computed: true,
						MarkdownDescription: "Average worker CPU utilization to aim for, in percent.",
						PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
						Validators:          []validator.Int64{int64validator.Between(1, 100)},
					},
					"target_memory_utilization": schema.Int64Attribute{
						Optional: true, MarkdownDescription: "Average worker memory utilization to aim for, in percent.",
						Validators: []validator.Int64{int64validator.Between(1, 100)},
					},
				},
			},

			"host":   schema.StringAttribute{Computed: true, MarkdownDescription: "External host, empty when not exposed."},
			"port":   schema.Int64Attribute{Computed: true, MarkdownDescription: "External port."},
			"status": schema.StringAttribute{Computed: true, MarkdownDescription: "Current status, such as `Online` or `Sleeping`."},
		},
	}
}

func (r *sqlEngineClusterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *sqlEngineClusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan sqlEngineClusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	env, err := uuid.Parse(plan.Env.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("env"), "Invalid env", "must be a UUID: "+err.Error())
		return
	}
	cfg, d := trinoConfigFrom(ctx, plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	var kind console.DataDockKindRequest
	if err := kind.FromDataDockKindRequest0(console.DataDockKindRequest0{
		Type: console.DataDockKindRequest0TypeTrinoInternal, Content: cfg,
	}); err != nil {
		resp.Diagnostics.AddError("Failed to encode the cluster configuration", err.Error())
		return
	}
	name := plan.Name.ValueString()
	created, err := r.p.API.CreateDataDock(ctx, console.CreateDataDockRequestBody{
		HarborId: env, Name: name, Slug: name, ConnectionKind: kind,
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to create SQL engine cluster", err.Error())
		return
	}
	id := created.Id.String()
	// Saved right away so a failure below leaves a tainted resource, not an orphan.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)

	// Create takes neither the description nor the refresh interval.
	patch := console.UpdateDataDockRequest{}
	if !plan.Description.IsNull() {
		patch.Description = stringPtr(plan.Description)
	}
	if !plan.RefreshInterval.IsNull() && !plan.RefreshInterval.IsUnknown() {
		patch.RefreshInterval = int32PtrFromInt64(plan.RefreshInterval)
	}
	if patch.Description != nil || patch.RefreshInterval != nil {
		if err := r.p.API.UpdateDataDock(ctx, id, patch); err != nil {
			resp.Diagnostics.AddError("Failed to configure SQL engine cluster", err.Error())
			return
		}
	}
	if !plan.ExposeToInternet.IsNull() {
		if err := r.p.API.SetDataDockExposure(ctx, id, plan.ExposeToInternet.ValueBool()); err != nil {
			resp.Diagnostics.AddError("Failed to set SQL engine cluster exposure", err.Error())
			return
		}
	}
	if err := r.waitReady(ctx, id); err != nil {
		resp.Diagnostics.AddError("SQL engine cluster did not become ready", err.Error())
		return
	}
	state, err := r.readInto(ctx, id, plan.ExposeToInternet)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read SQL engine cluster after create", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *sqlEngineClusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior sqlEngineClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.readInto(ctx, prior.ID.ValueString(), prior.ExposeToInternet)
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read SQL engine cluster", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *sqlEngineClusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state sqlEngineClusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()

	patch := console.UpdateDataDockRequest{}
	if !plan.Description.Equal(state.Description) {
		desc := plan.Description.ValueString()
		patch.Description = &desc
	}
	if !plan.RefreshInterval.IsUnknown() && !plan.RefreshInterval.Equal(state.RefreshInterval) {
		patch.RefreshInterval = int32PtrFromInt64(plan.RefreshInterval)
	}
	// The configuration is replaced as a whole and restarts Trino, so it is only
	// sent when part of it changed.
	configChanged := trinoConfigChanged(plan, state)
	if configChanged {
		cfg, d := trinoConfigFrom(ctx, plan)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		var kind console.UpdateDataDockRequestKind
		if err := kind.FromUpdateDataDockRequestKind0(console.UpdateDataDockRequestKind0{
			Type: console.UpdateDataDockRequestKind0TypeTrinoInternal, Content: cfg,
		}); err != nil {
			resp.Diagnostics.AddError("Failed to encode the cluster configuration", err.Error())
			return
		}
		patch.Kind = &kind
	}
	if patch.Description != nil || patch.RefreshInterval != nil || patch.Kind != nil {
		if err := r.p.API.UpdateDataDock(ctx, id, patch); err != nil {
			resp.Diagnostics.AddError("Failed to update SQL engine cluster", err.Error())
			return
		}
	}
	if !plan.ExposeToInternet.IsNull() && !plan.ExposeToInternet.Equal(state.ExposeToInternet) {
		if err := r.p.API.SetDataDockExposure(ctx, id, plan.ExposeToInternet.ValueBool()); err != nil {
			resp.Diagnostics.AddError("Failed to set SQL engine cluster exposure", err.Error())
			return
		}
	}
	if configChanged {
		if err := r.waitReady(ctx, id); err != nil {
			resp.Diagnostics.AddError("SQL engine cluster did not become ready after update", err.Error())
			return
		}
	}
	newState, err := r.readInto(ctx, id, plan.ExposeToInternet)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read SQL engine cluster after update", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *sqlEngineClusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state sqlEngineClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.p.API.DeleteDataDock(ctx, id); err != nil {
		resp.Diagnostics.AddError("Failed to delete SQL engine cluster", err.Error())
		return
	}
	if err := pollGoneOn404(ctx, sqlEngineClusterWaitTimeout, func() error {
		_, err := r.p.API.GetDataDock(ctx, r.p.OrgID, id)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("SQL engine cluster still present after delete", err.Error())
	}
}

func (r *sqlEngineClusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *sqlEngineClusterResource) waitReady(ctx context.Context, id string) error {
	_, err := waitForReady(ctx, sqlEngineClusterWaitTimeout, func() (*console.DataDock, bool, error) {
		d, err := r.p.API.GetDataDock(ctx, r.p.OrgID, id)
		if err != nil {
			return nil, false, err
		}
		switch d.Status {
		case console.DataDockStatusOnline, console.DataDockStatusSleeping:
			return d, true, nil
		case console.DataDockStatusOffline, console.DataDockStatusDeleting:
			return nil, false, fmt.Errorf("the cluster entered the %s status", d.Status)
		}
		return d, false, nil
	})
	return err
}

// readInto maps the cluster onto the model. The exposure is only read back
// while managed (exposure non-null), since the platform resolves an unset one.
func (r *sqlEngineClusterResource) readInto(ctx context.Context, id string, exposure types.Bool) (sqlEngineClusterModel, error) {
	d, err := r.p.API.GetDataDock(ctx, r.p.OrgID, id)
	if err != nil {
		return sqlEngineClusterModel{}, err
	}
	m, err := sqlEngineClusterFrom(ctx, d)
	if err != nil {
		return sqlEngineClusterModel{}, err
	}
	m.ExposeToInternet = types.BoolNull()
	if !exposure.IsNull() {
		exposed, err := r.p.API.GetDataDockExposure(ctx, id)
		if err != nil {
			return sqlEngineClusterModel{}, err
		}
		m.ExposeToInternet = types.BoolPointerValue(exposed)
	}
	return m, nil
}

// sqlEngineClusterFrom maps a cluster onto the model, every attribute but the
// exposure (resource and data source share it).
func sqlEngineClusterFrom(ctx context.Context, d *console.DataDock) (sqlEngineClusterModel, error) {
	kind, err := d.Kind.AsDataDockKind0()
	if err != nil {
		return sqlEngineClusterModel{}, fmt.Errorf("decode the cluster configuration: %w", err)
	}
	cfg := kind.Content
	scaling := types.ObjectNull(workerAutoScalingType.AttrTypes)
	if a := cfg.WorkerAutoScaling; a != nil {
		var diags diag.Diagnostics
		scaling, diags = types.ObjectValueFrom(ctx, workerAutoScalingType.AttrTypes, workerAutoScalingModel{
			Enabled:                 types.BoolValue(a.Enabled != nil && *a.Enabled),
			MinReplicas:             optInt64FromInt32(a.MinReplicas),
			MaxReplicas:             optInt64FromInt32(a.MaxReplicas),
			TargetCPUUtilization:    optInt64FromInt32(a.TargetCpuUtilization),
			TargetMemoryUtilization: optInt64FromInt32(a.TargetMemoryUtilization),
		})
		if diags.HasError() {
			return sqlEngineClusterModel{}, errors.New("failed to convert worker_auto_scaling")
		}
	}
	desc := types.StringNull()
	if d.Description != "" {
		desc = types.StringValue(d.Description)
	}
	return sqlEngineClusterModel{
		ID:                    types.StringValue(d.Id.String()),
		Env:                   types.StringValue(d.HarborId.String()),
		Name:                  types.StringValue(d.Slug),
		Description:           desc,
		RefreshInterval:       types.Int64Value(int64(d.RefreshInterval)),
		ExposeToInternet:      types.BoolNull(),
		WorkerReplicas:        types.Int64Value(int64(cfg.WorkerReplicas)),
		CoordinatorCPU:        optString(cfg.CoordinatorCpu),
		CoordinatorMemory:     optString(cfg.CoordinatorMemory),
		WorkerCPU:             optString(cfg.WorkerCpu),
		WorkerMemory:          optString(cfg.WorkerMemory),
		MaxMemoryPerNode:      optString(cfg.MaxMemoryPerNode),
		QueryMaxMemory:        optString(cfg.QueryMaxMemory),
		QueryMaxExecutionTime: optString(cfg.QueryMaxExecutionTime),
		TaskConcurrency:       optInt64FromInt32(cfg.TaskConcurrency),
		HashPartitionCount:    optInt64FromInt32(cfg.HashPartitionCount),
		WorkerAutoScaling:     scaling,
		Host:                  types.StringValue(d.Host),
		Port:                  types.Int64Value(int64(d.Port)),
		Status:                types.StringValue(string(d.Status)),
	}, nil
}

// trinoConfigFrom builds the engine configuration from the plan. An attribute
// left out stays absent, so the platform resolves its default at reconcile.
func trinoConfigFrom(ctx context.Context, m sqlEngineClusterModel) (console.TrinoInternalConfig, diag.Diagnostics) {
	var diags diag.Diagnostics
	cfg := console.TrinoInternalConfig{
		WorkerReplicas:        int32(m.WorkerReplicas.ValueInt64()),
		CoordinatorCpu:        stringPtr(m.CoordinatorCPU),
		CoordinatorMemory:     stringPtr(m.CoordinatorMemory),
		WorkerCpu:             stringPtr(m.WorkerCPU),
		WorkerMemory:          stringPtr(m.WorkerMemory),
		MaxMemoryPerNode:      stringPtr(m.MaxMemoryPerNode),
		QueryMaxMemory:        stringPtr(m.QueryMaxMemory),
		QueryMaxExecutionTime: stringPtr(m.QueryMaxExecutionTime),
		TaskConcurrency:       int32PtrFromInt64(m.TaskConcurrency),
		HashPartitionCount:    int32PtrFromInt64(m.HashPartitionCount),
	}
	if !m.WorkerAutoScaling.IsNull() && !m.WorkerAutoScaling.IsUnknown() {
		var a workerAutoScalingModel
		diags.Append(m.WorkerAutoScaling.As(ctx, &a, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
		if diags.HasError() {
			return cfg, diags
		}
		cfg.WorkerAutoScaling = &console.WorkerAutoScaling{
			Enabled:                 boolPtr(a.Enabled),
			MinReplicas:             int32PtrFromInt64(a.MinReplicas),
			MaxReplicas:             int32PtrFromInt64(a.MaxReplicas),
			TargetCpuUtilization:    int32PtrFromInt64(a.TargetCPUUtilization),
			TargetMemoryUtilization: int32PtrFromInt64(a.TargetMemoryUtilization),
		}
	}
	return cfg, diags
}

func trinoConfigChanged(plan, state sqlEngineClusterModel) bool {
	return !plan.WorkerReplicas.Equal(state.WorkerReplicas) ||
		!plan.CoordinatorCPU.Equal(state.CoordinatorCPU) ||
		!plan.CoordinatorMemory.Equal(state.CoordinatorMemory) ||
		!plan.WorkerCPU.Equal(state.WorkerCPU) ||
		!plan.WorkerMemory.Equal(state.WorkerMemory) ||
		!plan.MaxMemoryPerNode.Equal(state.MaxMemoryPerNode) ||
		!plan.QueryMaxMemory.Equal(state.QueryMaxMemory) ||
		!plan.QueryMaxExecutionTime.Equal(state.QueryMaxExecutionTime) ||
		!plan.TaskConcurrency.Equal(state.TaskConcurrency) ||
		!plan.HashPartitionCount.Equal(state.HashPartitionCount) ||
		!plan.WorkerAutoScaling.Equal(state.WorkerAutoScaling)
}
