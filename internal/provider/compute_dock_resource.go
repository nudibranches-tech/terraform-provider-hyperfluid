// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

var (
	_ resource.Resource                = &computeDockResource{}
	_ resource.ResourceWithConfigure   = &computeDockResource{}
	_ resource.ResourceWithImportState = &computeDockResource{}
)

const computeDockWaitTimeout = 20 * time.Minute

var dnsLabelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func NewComputeDockResource() resource.Resource {
	return &computeDockResource{}
}

type computeDockResource struct {
	p        *providerData
	interval time.Duration
}

type computeDockModel struct {
	ID                     types.String `tfsdk:"id"`
	Env                    types.String `tfsdk:"env"`
	Name                   types.String `tfsdk:"name"`
	Description            types.String `tfsdk:"description"`
	Tags                   types.List   `tfsdk:"tags"`
	Engine                 types.String `tfsdk:"engine"`
	Enabled                types.Bool   `tfsdk:"enabled"`
	Version                types.String `tfsdk:"version"`
	Partitions             types.List   `tfsdk:"partitions"`
	HomeSize               types.String `tfsdk:"home_size"`
	HomeStorageClass       types.String `tfsdk:"home_storage_class"`
	RestAPIEnabled         types.Bool   `tfsdk:"rest_api_enabled"`
	RestAPIReplicas        types.Int64  `tfsdk:"rest_api_replicas"`
	LoginEnabled           types.Bool   `tfsdk:"login_enabled"`
	LoginReplicas          types.Int64  `tfsdk:"login_replicas"`
	AccountingEnabled      types.Bool   `tfsdk:"accounting_enabled"`
	OciJobs                types.Bool   `tfsdk:"oci_jobs"`
	AutoSuspendIdleMinutes types.Int64  `tfsdk:"auto_suspend_idle_minutes"`

	Slug         types.String `tfsdk:"slug"`
	Phase        types.String `tfsdk:"phase"`
	SSHEndpoint  types.String `tfsdk:"ssh_endpoint"`
	RestEndpoint types.String `tfsdk:"rest_endpoint"`
}

type computeDockPartitionModel struct {
	Name        types.String `tfsdk:"name"`
	CPU         types.String `tfsdk:"cpu"`
	Memory      types.String `tfsdk:"memory"`
	GPU         types.Int64  `tfsdk:"gpu"`
	NodeMin     types.Int64  `tfsdk:"node_min"`
	NodeMax     types.Int64  `tfsdk:"node_max"`
	WalltimeMax types.String `tfsdk:"walltime_max"`
	Default     types.Bool   `tfsdk:"default"`
}

var computeDockPartitionType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"name": types.StringType, "cpu": types.StringType, "memory": types.StringType, "gpu": types.Int64Type,
	"node_min": types.Int64Type, "node_max": types.Int64Type, "walltime_max": types.StringType, "default": types.BoolType,
}}

func (r *computeDockResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compute_dock"
}

func (r *computeDockResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	// The platform resolves these when omitted and reports what it chose.
	resolvedStr := func(desc string, replace bool) schema.StringAttribute {
		mods := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
		if replace {
			mods = append(mods, stringplanmodifier.RequiresReplace())
		}
		return schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: desc, PlanModifiers: mods}
	}
	resolvedBool := func(desc string, replace bool) schema.BoolAttribute {
		mods := []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}
		if replace {
			mods = append(mods, boolplanmodifier.RequiresReplace())
		}
		return schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: desc, PlanModifiers: mods}
	}
	replicas := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{
			Optional: true, Computed: true, MarkdownDescription: desc,
			Validators:    []validator.Int64{int64validator.AtLeast(0)},
			PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown(), int64planmodifier.RequiresReplace()},
		}
	}
	computedStr := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Compute Dock: a Slurm cluster in an environment. Needs the organization's `compute_docks_enabled` feature.\n\n" +
			"~> Removing a partition leaves its nodes running until the platform prunes them.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"env": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Environment id. Changing this forces a new dock.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Name: lowercase letters, digits and hyphens. Changing this forces a new dock.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.RegexMatches(slugPattern, "must be lowercase letters, digits and hyphens")},
			},
			"description": schema.StringAttribute{
				Optional: true, MarkdownDescription: "Free-form description.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"tags": schema.ListAttribute{
				ElementType: types.StringType, Optional: true, MarkdownDescription: "User-defined tags.",
				Validators: []validator.List{listvalidator.SizeAtLeast(1)},
			},
			"engine": func() schema.StringAttribute {
				a := resolvedStr("Compute engine: `slurm`. Changing this forces a new dock.", true)
				a.Validators = []validator.String{stringvalidator.OneOf(string(console.ComputeEngineInputSlurm))}
				return a
			}(),
			"enabled": resolvedBool("`false` suspends the cluster; its compute scales to zero.", false),
			"version": resolvedStr("Slurm image tag; omitted, the platform default is used, and removing it keeps the current tag.", false),
			"partitions": schema.ListNestedAttribute{
				Required:            true,
				MarkdownDescription: "Compute partitions, at least one, replaced as a whole.",
				Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{
						Required: true, MarkdownDescription: "Partition name, unique within the dock.",
						Validators: []validator.String{stringvalidator.RegexMatches(dnsLabelPattern, "must be a DNS label")},
					},
					"cpu":    schema.StringAttribute{Required: true, MarkdownDescription: "CPU per node, e.g. `\"8\"`."},
					"memory": schema.StringAttribute{Required: true, MarkdownDescription: "Memory per node, e.g. `\"32Gi\"`."},
					"gpu": schema.Int64Attribute{
						Optional: true, MarkdownDescription: "GPUs per node.",
						Validators: []validator.Int64{int64validator.AtLeast(1)},
					},
					"node_min": schema.Int64Attribute{
						Optional: true, Computed: true, MarkdownDescription: "Nodes kept running; `0` scales to zero.",
						Validators:    []validator.Int64{int64validator.AtLeast(0)},
						PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
					},
					"node_max": schema.Int64Attribute{
						Required: true, MarkdownDescription: "Maximum number of nodes.",
						Validators: []validator.Int64{int64validator.AtLeast(1)},
					},
					"walltime_max": schema.StringAttribute{Optional: true, MarkdownDescription: "Maximum job duration, e.g. `\"04:00:00\"`."},
					"default": schema.BoolAttribute{
						Optional: true, Computed: true, MarkdownDescription: "Whether jobs land here when they name no partition.",
						PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
					},
				}},
			},
			"home_size":          resolvedStr("Size of the shared `/home` volume, e.g. `\"100Gi\"`.", false),
			"home_storage_class": resolvedStr("`ReadWriteMany` storage class of `/home`. Changing this forces a new dock.", true),
			"rest_api_enabled":   resolvedBool("Whether `slurmrestd` runs. Changing this forces a new dock.", true),
			"rest_api_replicas":  replicas("Number of `slurmrestd` replicas. Changing this forces a new dock."),
			"login_enabled":      resolvedBool("Whether an SSH login node runs. Changing this forces a new dock.", true),
			"login_replicas":     replicas("Number of login replicas. Changing this forces a new dock."),
			"accounting_enabled": resolvedBool("Whether `sacct` job accounting runs. Changing this forces a new dock.", true),
			"oci_jobs": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Let jobs run in their own OCI image; not recovered by import. Changing this forces a new dock.",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"auto_suspend_idle_minutes": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Idle minutes after which the cluster suspends; omitted, it never suspends on its own.",
				Validators:          []validator.Int64{int64validator.AtLeast(1)},
			},

			"slug":          computedStr("Derived slug."),
			"phase":         computedStr("Lifecycle phase, e.g. `Running` or `Suspended`."),
			"ssh_endpoint":  computedStr("SSH endpoint of the login node."),
			"rest_endpoint": computedStr("`slurmrestd` endpoint."),
		},
	}
}

func (r *computeDockResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *computeDockResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan computeDockModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var diags diag.Diagnostics
	partitions := computeDockPartitionsRequest(ctx, plan.Partitions, &diags)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := console.CreateComputeDockCrdRequestBody{
		Name:              plan.Name.ValueString(),
		Description:       stringPtr(plan.Description),
		Tags:              strListPtr(ctx, plan.Tags, &resp.Diagnostics),
		Enabled:           boolPtr(plan.Enabled),
		Version:           stringPtr(plan.Version),
		Partitions:        partitions,
		HomeSize:          stringPtr(plan.HomeSize),
		HomeStorageClass:  stringPtr(plan.HomeStorageClass),
		RestApiEnabled:    boolPtr(plan.RestAPIEnabled),
		RestApiReplicas:   int32PtrFromInt64(plan.RestAPIReplicas),
		LoginEnabled:      boolPtr(plan.LoginEnabled),
		LoginReplicas:     int32PtrFromInt64(plan.LoginReplicas),
		AccountingEnabled: boolPtr(plan.AccountingEnabled),
		OciJobs:           boolPtr(plan.OciJobs),
	}
	if !plan.Engine.IsNull() && !plan.Engine.IsUnknown() {
		e := console.ComputeEngineInput(plan.Engine.ValueString())
		body.Engine = &e
	}
	if !plan.AutoSuspendIdleMinutes.IsNull() && !plan.AutoSuspendIdleMinutes.IsUnknown() {
		body.AutoSuspend = autoSuspendRequest(plan.AutoSuspendIdleMinutes)
	}

	created, err := r.p.API.CreateComputeDock(ctx, r.p.OrgID, plan.Env.ValueString(), body)
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to create compute dock", featureComputeDocks, err)
		return
	}
	id := created.Id.String()
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if err := r.waitReady(ctx, id); err != nil {
		resp.Diagnostics.AddError("Compute dock did not become ready", err.Error())
		return
	}
	state, err := r.readInto(ctx, id, plan.OciJobs)
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to read compute dock after create", featureComputeDocks, err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *computeDockResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior computeDockModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.readInto(ctx, prior.ID.ValueString(), prior.OciJobs)
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to read compute dock", featureComputeDocks, err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *computeDockResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state computeDockModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, diags := computeDockPatch(ctx, plan, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	orphaned, err := r.p.API.PatchComputeDock(ctx, r.p.OrgID, id, body)
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to update compute dock", featureComputeDocks, err)
		return
	}
	if len(orphaned) > 0 {
		resp.Diagnostics.AddWarning("Removed partitions keep running",
			fmt.Sprintf("The nodes of %s keep running until the platform prunes them.", strings.Join(orphaned, ", ")))
	}
	if err := r.waitReady(ctx, id); err != nil {
		resp.Diagnostics.AddError("Compute dock did not become ready after update", err.Error())
		return
	}
	newState, err := r.readInto(ctx, id, plan.OciJobs)
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to read compute dock after update", featureComputeDocks, err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *computeDockResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state computeDockModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.p.API.DeleteComputeDock(ctx, r.p.OrgID, id); err != nil {
		addGatedError(&resp.Diagnostics, "Failed to delete compute dock", featureComputeDocks, err)
		return
	}
	if err := pollGoneOn404Every(ctx, computeDockWaitTimeout, orDefaultInterval(r.interval), func() error {
		_, err := r.p.API.GetComputeDock(ctx, r.p.OrgID, id)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Compute dock still present after delete", err.Error())
	}
}

func (r *computeDockResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// waitReady waits for `Running`, or `Suspended` for a disabled dock.
func (r *computeDockResource) waitReady(ctx context.Context, id string) error {
	_, err := waitForReadyEvery(ctx, computeDockWaitTimeout, orDefaultInterval(r.interval), func() (*console.ComputeDockResponse, bool, error) {
		d, err := r.p.API.GetComputeDock(ctx, r.p.OrgID, id)
		if err != nil {
			return nil, false, err
		}
		phase := ""
		if d.Phase != nil {
			phase = *d.Phase
		}
		switch phase {
		case "Failed", "Terminating":
			return nil, false, fmt.Errorf("the compute dock entered the %s phase", phase)
		case "Running":
			return d, true, nil
		case "Suspended":
			return d, !d.Enabled, nil
		}
		return d, false, nil
	})
	return err
}

// readInto builds the model from both views. oci_jobs is in neither, so it
// is carried from ociJobs.
func (r *computeDockResource) readInto(ctx context.Context, id string, ociJobs types.Bool) (computeDockModel, error) {
	d, err := r.p.API.GetComputeDock(ctx, r.p.OrgID, id)
	if err != nil {
		return computeDockModel{}, err
	}
	spec, err := r.p.API.GetComputeDockSpec(ctx, r.p.OrgID, id)
	if err != nil {
		return computeDockModel{}, err
	}
	var diags diag.Diagnostics
	partitions := make([]computeDockPartitionModel, 0, len(spec.Partitions))
	for _, p := range spec.Partitions {
		partitions = append(partitions, computeDockPartitionModel{
			Name: types.StringValue(p.Name), CPU: types.StringValue(p.Cpu), Memory: types.StringValue(p.Memory),
			GPU: optInt64FromInt32(p.Gpu), NodeMin: types.Int64Value(int64(p.NodeMin)), NodeMax: types.Int64Value(int64(p.NodeMax)),
			WalltimeMax: optString(p.WalltimeMax), Default: types.BoolValue(p.Default),
		})
	}
	partitionList, dd := types.ListValueFrom(ctx, computeDockPartitionType, partitions)
	diags.Append(dd...)
	idle := types.Int64Null()
	if spec.AutoSuspendEnabled {
		idle = optInt64FromInt32(spec.AutoSuspendIdleMinutes)
	}
	m := computeDockModel{
		ID:                     types.StringValue(id),
		Env:                    types.StringValue(d.HarborId.String()),
		Name:                   types.StringValue(d.Name),
		Description:            optNonEmpty(d.Description),
		Tags:                   nullIfEmptyList(ctx, d.Tags, &diags),
		Engine:                 types.StringValue(spec.Engine),
		Enabled:                types.BoolValue(spec.Enabled),
		Version:                optString(spec.Version),
		Partitions:             partitionList,
		HomeSize:               types.StringValue(spec.HomeSize),
		HomeStorageClass:       optString(spec.HomeStorageClass),
		RestAPIEnabled:         types.BoolValue(spec.RestApiEnabled),
		RestAPIReplicas:        types.Int64Value(int64(spec.RestApiReplicas)),
		LoginEnabled:           types.BoolValue(spec.LoginEnabled),
		LoginReplicas:          types.Int64Value(int64(spec.LoginReplicas)),
		AccountingEnabled:      types.BoolValue(spec.AccountingEnabled),
		OciJobs:                ociJobs,
		AutoSuspendIdleMinutes: idle,
		Slug:                   types.StringValue(d.Slug),
		Phase:                  optString(d.Phase),
		SSHEndpoint:            optString(d.SshEndpoint),
		RestEndpoint:           optString(d.RestEndpoint),
	}
	if diags.HasError() {
		return computeDockModel{}, fmt.Errorf("mapping compute dock %s: %v", id, diags.Errors())
	}
	return m, nil
}

// computeDockPatch sends what changed. A removed description or tag list is
// cleared; a removed version, size or flag keeps its current value.
func computeDockPatch(ctx context.Context, plan, state computeDockModel) (console.PatchComputeDockCrdRequestBody, diag.Diagnostics) {
	var (
		diags diag.Diagnostics
		body  console.PatchComputeDockCrdRequestBody
	)
	changed := func(p, s attr.Value) bool { return !p.IsUnknown() && !p.Equal(s) }
	set := func(p, s attr.Value) bool { return changed(p, s) && !p.IsNull() }
	empty := ""
	if changed(plan.Description, state.Description) {
		body.Description = orElse(stringPtr(plan.Description), &empty)
	}
	if changed(plan.Tags, state.Tags) {
		body.Tags = orElse(strListPtr(ctx, plan.Tags, &diags), &[]string{})
	}
	if set(plan.Enabled, state.Enabled) {
		body.Enabled = boolPtr(plan.Enabled)
	}
	if set(plan.Version, state.Version) {
		body.Version = stringPtr(plan.Version)
	}
	if set(plan.HomeSize, state.HomeSize) {
		body.HomeSize = stringPtr(plan.HomeSize)
	}
	if changed(plan.Partitions, state.Partitions) {
		p := computeDockPartitionsRequest(ctx, plan.Partitions, &diags)
		body.Partitions = &p
	}
	if changed(plan.AutoSuspendIdleMinutes, state.AutoSuspendIdleMinutes) {
		body.AutoSuspend = autoSuspendRequest(plan.AutoSuspendIdleMinutes)
	}
	return body, diags
}

// autoSuspendRequest turns auto-suspend on after idle minutes, or off for null.
func autoSuspendRequest(idle types.Int64) *console.AutoSuspendInput {
	if idle.IsNull() {
		off := false
		return &console.AutoSuspendInput{Enabled: &off}
	}
	on := true
	return &console.AutoSuspendInput{Enabled: &on, IdleMinutes: int32PtrFromInt64(idle)}
}

func computeDockPartitionsRequest(ctx context.Context, l types.List, diags *diag.Diagnostics) []console.PartitionInput {
	var models []computeDockPartitionModel
	diags.Append(l.ElementsAs(ctx, &models, false)...)
	out := make([]console.PartitionInput, 0, len(models))
	for _, m := range models {
		out = append(out, console.PartitionInput{
			Name: m.Name.ValueString(), Cpu: stringPtr(m.CPU), Memory: stringPtr(m.Memory), Gpu: int32PtrFromInt64(m.GPU),
			NodeMin: int32PtrFromInt64(m.NodeMin), NodeMax: int32PtrFromInt64(m.NodeMax),
			WalltimeMax: stringPtr(m.WalltimeMax), Default: boolPtr(m.Default),
		})
	}
	return out
}
