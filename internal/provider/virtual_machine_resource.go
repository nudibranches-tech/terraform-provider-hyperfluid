// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

var (
	_ resource.Resource                = &virtualMachineResource{}
	_ resource.ResourceWithConfigure   = &virtualMachineResource{}
	_ resource.ResourceWithImportState = &virtualMachineResource{}
	_ resource.ResourceWithModifyPlan  = &virtualMachineResource{}
)

// The first boot imports the root disk from the image source.
const virtualMachineWaitTimeout = 30 * time.Minute

var vcpuPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

func NewVirtualMachineResource() resource.Resource {
	return &virtualMachineResource{}
}

type virtualMachineResource struct {
	p        *providerData
	interval time.Duration
}

type virtualMachineModel struct {
	ID                  types.String `tfsdk:"id"`
	Env                 types.String `tfsdk:"env"`
	Name                types.String `tfsdk:"name"`
	Description         types.String `tfsdk:"description"`
	Tags                types.List   `tfsdk:"tags"`
	CPU                 types.String `tfsdk:"cpu"`
	Memory              types.String `tfsdk:"memory"`
	Disk                types.String `tfsdk:"disk"`
	ImageSourceRef      types.String `tfsdk:"image_source_ref"`
	ImageKey            types.String `tfsdk:"image_key"`
	UserDataSecretName  types.String `tfsdk:"user_data_secret_name"`
	Running             types.Bool   `tfsdk:"running"`
	InstallMediaRemoved types.Bool   `tfsdk:"install_media_removed"`

	Slug         types.String `tfsdk:"slug"`
	Phase        types.String `tfsdk:"phase"`
	IPAddress    types.String `tfsdk:"ip_address"`
	InstallMedia types.String `tfsdk:"install_media"`
}

func (r *virtualMachineResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_virtual_machine"
}

func (r *virtualMachineResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	createOnly := func(desc string, validators ...validator.String) schema.StringAttribute {
		return schema.StringAttribute{
			Required: true, MarkdownDescription: desc, Validators: validators,
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		}
	}
	computedStr := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A virtual machine in an environment, booted from an image of the platform catalog " +
			"(see `hyperfluid_virtual_machine_images`). Needs the organization's `virtual_machines_enabled` feature.\n\n" +
			"~> `user_data_secret_name` is read once, at first boot: changing it replaces the virtual machine.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"env": createOnly("Environment id. Changing this forces a new virtual machine."),
			"name": createOnly("Name, a DNS label. Changing this forces a new virtual machine.",
				stringvalidator.RegexMatches(dnsLabelPattern, "must be a DNS label")),
			"description": schema.StringAttribute{
				Optional: true, MarkdownDescription: "Free-form description.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 500)},
			},
			"tags": schema.ListAttribute{
				ElementType: types.StringType, Optional: true, MarkdownDescription: "User-defined tags.",
				Validators: []validator.List{listvalidator.SizeAtLeast(1)},
			},
			"cpu": createOnly("Whole number of vCPUs, e.g. `\"2\"`. Changing this forces a new virtual machine.",
				stringvalidator.RegexMatches(vcpuPattern, "must be a whole number of vCPUs")),
			"memory": createOnly("Guest memory, e.g. `\"4Gi\"`. Changing this forces a new virtual machine."),
			"disk":   createOnly("Root disk size, e.g. `\"40Gi\"`. Changing this forces a new virtual machine."),
			"image_source_ref": createOnly("Image source name, from the image catalog. " +
				"Changing this forces a new virtual machine."),
			"image_key": createOnly("Image key, from the image catalog. Changing this forces a new virtual machine."),
			"user_data_secret_name": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Name of an organization-wide plaintext `hyperfluid_secret` holding cloud-init " +
					"user-data. Changing this forces a new virtual machine.",
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 255)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"running": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Whether the virtual machine runs; `false` stops it. Omitted, it starts.",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"install_media_removed": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Set to `true` once the guest is installed, to detach the media of an installer " +
					"image at the next restart. It cannot be set back to `false`.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},

			"slug":          computedStr("Derived slug."),
			"phase":         computedStr("Lifecycle phase, e.g. `Running` or `Stopped`."),
			"ip_address":    computedStr("Primary IP address of the guest, once it runs."),
			"install_media": computedStr("`Attached` or `Removed` for an installer image; null for a disk image."),
		},
	}
}

func (r *virtualMachineResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ModifyPlan refuses at plan time what the platform cannot do: create a virtual
// machine without its installation media, or re-attach removed media.
func (r *virtualMachineResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan virtualMachineModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.InstallMediaRemoved.IsNull() || plan.InstallMediaRemoved.IsUnknown() {
		return
	}
	var state virtualMachineModel
	if !req.State.Raw.IsNull() {
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	}
	creating := req.State.Raw.IsNull() || virtualMachineReplaced(plan, state)
	switch {
	case creating && plan.InstallMediaRemoved.ValueBool():
		resp.Diagnostics.AddAttributeError(path.Root("install_media_removed"), "Installation media cannot be removed at creation",
			"A new virtual machine boots its installer from the media. Leave `install_media_removed` unset or `false` "+
				"until the guest is installed, then set it to `true`.")
	case !creating && !plan.InstallMediaRemoved.ValueBool() && state.InstallMediaRemoved.ValueBool():
		resp.Diagnostics.AddAttributeError(path.Root("install_media_removed"), "Installation media cannot be re-attached",
			"The installation media of this virtual machine has been removed; `install_media_removed` cannot go back to `false`.")
	}
}

// virtualMachineReplaced reports a change to an attribute the API cannot patch.
func virtualMachineReplaced(plan, state virtualMachineModel) bool {
	for _, pair := range [][2]types.String{
		{plan.Env, state.Env}, {plan.Name, state.Name}, {plan.CPU, state.CPU}, {plan.Memory, state.Memory},
		{plan.Disk, state.Disk}, {plan.ImageSourceRef, state.ImageSourceRef}, {plan.ImageKey, state.ImageKey},
		{plan.UserDataSecretName, state.UserDataSecretName},
	} {
		if !pair[0].Equal(pair[1]) {
			return true
		}
	}
	return false
}

func (r *virtualMachineResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan virtualMachineModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := console.CreateVirtualMachineCrdRequestBody{
		Name:               plan.Name.ValueString(),
		Description:        stringPtr(plan.Description),
		Tags:               strListPtr(ctx, plan.Tags, &resp.Diagnostics),
		Cpu:                plan.CPU.ValueString(),
		Memory:             plan.Memory.ValueString(),
		Disk:               plan.Disk.ValueString(),
		ImageSourceRef:     plan.ImageSourceRef.ValueString(),
		ImageKey:           plan.ImageKey.ValueString(),
		UserDataSecretName: stringPtr(plan.UserDataSecretName),
		Running:            boolPtr(plan.Running),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	created, err := r.p.API.CreateVirtualMachine(ctx, r.p.OrgID, plan.Env.ValueString(), body)
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to create virtual machine", featureVMs, err)
		return
	}
	id := created.Id.String()
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	vm, err := r.waitReady(ctx, id, created.Running)
	if err != nil {
		resp.Diagnostics.AddError("Virtual machine did not become ready", err.Error())
		return
	}
	state, d := virtualMachineFrom(ctx, vm)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *virtualMachineResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior virtualMachineModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vm, err := r.p.API.GetVirtualMachine(ctx, r.p.OrgID, prior.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || r.deniedBecauseGone(ctx, prior, err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to read virtual machine", featureVMs, err)
		return
	}
	state, d := virtualMachineFrom(ctx, vm)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *virtualMachineResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state virtualMachineModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, diags := virtualMachinePatch(ctx, plan, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.p.API.PatchVirtualMachine(ctx, r.p.OrgID, id, body); err != nil {
		addGatedError(&resp.Diagnostics, "Failed to update virtual machine", featureVMs, err)
		return
	}
	running := state.Running.ValueBool()
	if !plan.Running.IsUnknown() && !plan.Running.IsNull() {
		running = plan.Running.ValueBool()
	}
	vm, err := r.waitReady(ctx, id, running)
	if err != nil {
		resp.Diagnostics.AddError("Virtual machine did not become ready after update", err.Error())
		return
	}
	newState, d := virtualMachineFrom(ctx, vm)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *virtualMachineResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state virtualMachineModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.p.API.DeleteVirtualMachine(ctx, r.p.OrgID, id); err != nil {
		addGatedError(&resp.Diagnostics, "Failed to delete virtual machine", featureVMs, err)
		return
	}
	if err := pollGoneOn404Every(ctx, virtualMachineWaitTimeout, orDefaultInterval(r.interval), func() error {
		_, err := r.p.API.GetVirtualMachine(ctx, r.p.OrgID, id)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Virtual machine still present after delete", err.Error())
	}
}

func (r *virtualMachineResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// deniedBecauseGone tells a deleted virtual machine from a refused read: the
// console denies, rather than 404s, a read of an id it can no longer resolve,
// so a 403 is checked against the environment's list.
func (r *virtualMachineResource) deniedBecauseGone(ctx context.Context, prior virtualMachineModel, err error) bool {
	if !errors.Is(err, client.ErrForbidden) || prior.Env.IsNull() || prior.Name.IsNull() {
		return false
	}
	vm, findErr := r.p.API.FindVirtualMachine(ctx, r.p.OrgID, prior.Env.ValueString(), prior.Name.ValueString())
	if errors.Is(findErr, client.ErrNotFound) {
		return true
	}
	return findErr == nil && vm.Id.String() != prior.ID.ValueString()
}

// waitReady waits for `Running`, or `Stopped` when the machine is meant to be off.
func (r *virtualMachineResource) waitReady(ctx context.Context, id string, running bool) (*console.VirtualMachineResponse, error) {
	want := "Stopped"
	if running {
		want = "Running"
	}
	return waitForReadyEvery(ctx, virtualMachineWaitTimeout, orDefaultInterval(r.interval), func() (*console.VirtualMachineResponse, bool, error) {
		vm, err := r.p.API.GetVirtualMachine(ctx, r.p.OrgID, id)
		if err != nil {
			return nil, false, err
		}
		phase := ""
		if vm.Phase != nil {
			phase = *vm.Phase
		}
		switch phase {
		case "Failed":
			if msg := falseConditionMessage(vm.Conditions); msg != "" {
				return nil, false, fmt.Errorf("the virtual machine failed: %s", msg)
			}
			return nil, false, errors.New("the virtual machine entered the Failed phase")
		case "Deleting":
			return nil, false, errors.New("the virtual machine is being deleted")
		}
		return vm, phase == want, nil
	})
}

func virtualMachineFrom(ctx context.Context, vm *console.VirtualMachineResponse) (virtualMachineModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	installMedia := types.StringNull()
	if vm.InstallMedia != nil {
		installMedia = types.StringValue(string(*vm.InstallMedia))
	}
	return virtualMachineModel{
		ID:                  types.StringValue(vm.Id.String()),
		Env:                 types.StringValue(vm.HarborId.String()),
		Name:                types.StringValue(vm.Name),
		Description:         optNonEmpty(vm.Description),
		Tags:                nullIfEmptyList(ctx, vm.Tags, &diags),
		CPU:                 types.StringValue(vm.Cpu),
		Memory:              types.StringValue(vm.Memory),
		Disk:                types.StringValue(vm.Disk),
		ImageSourceRef:      types.StringValue(vm.ImageSourceRef),
		ImageKey:            types.StringValue(vm.ImageKey),
		UserDataSecretName:  optNonEmpty(vm.UserDataSecretName),
		Running:             types.BoolValue(vm.Running),
		InstallMediaRemoved: types.BoolValue(vm.InstallMedia != nil && *vm.InstallMedia == console.InstallMediaRemoved),
		Slug:                types.StringValue(vm.Slug),
		Phase:               optString(vm.Phase),
		IPAddress:           optNonEmpty(vm.IpAddress),
		InstallMedia:        installMedia,
	}, diags
}

// virtualMachinePatch sends what changed. A removed description or tag list is
// cleared; a removed `running` keeps the current state.
func virtualMachinePatch(ctx context.Context, plan, state virtualMachineModel) (console.PatchVirtualMachineCrdRequestBody, diag.Diagnostics) {
	var (
		diags diag.Diagnostics
		body  console.PatchVirtualMachineCrdRequestBody
	)
	changed := func(p, s attr.Value) bool { return !p.IsUnknown() && !p.Equal(s) }
	empty := ""
	if changed(plan.Description, state.Description) {
		body.Description = orElse(stringPtr(plan.Description), &empty)
	}
	if changed(plan.Tags, state.Tags) {
		body.Tags = orElse(strListPtr(ctx, plan.Tags, &diags), &[]string{})
	}
	if changed(plan.Running, state.Running) && !plan.Running.IsNull() {
		body.Running = boolPtr(plan.Running)
	}
	if plan.InstallMediaRemoved.ValueBool() && !state.InstallMediaRemoved.ValueBool() {
		remove := true
		body.RemoveInstallMedia = &remove
	}
	return body, diags
}
