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
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

var (
	_ resource.Resource                   = &virtualMachineUserResource{}
	_ resource.ResourceWithConfigure      = &virtualMachineUserResource{}
	_ resource.ResourceWithImportState    = &virtualMachineUserResource{}
	_ resource.ResourceWithValidateConfig = &virtualMachineUserResource{}
)

// A guest user needs a running guest whose agent has connected, which a fresh
// virtual machine reaches some time after it reports Running.
const (
	virtualMachineGuestWaitTimeout = 15 * time.Minute
	virtualMachineUserWaitTimeout  = 10 * time.Minute
)

var guestUsernamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func NewVirtualMachineUserResource() resource.Resource {
	return &virtualMachineUserResource{}
}

type virtualMachineUserResource struct {
	p        *providerData
	interval time.Duration
}

type virtualMachineUserModel struct {
	ID                     types.String `tfsdk:"id"`
	VirtualMachine         types.String `tfsdk:"virtual_machine"`
	Username               types.String `tfsdk:"username"`
	GeneratePassword       types.Bool   `tfsdk:"generate_password"`
	SSHPublicKeySecretName types.String `tfsdk:"ssh_public_key_secret_name"`
	Tags                   types.List   `tfsdk:"tags"`

	Slug  types.String `tfsdk:"slug"`
	Phase types.String `tfsdk:"phase"`
}

func (r *virtualMachineUserResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_virtual_machine_user"
}

func (r *virtualMachineUserResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A guest OS account of a `hyperfluid_virtual_machine`, applied by its guest agent. " +
			"Every change replaces the account. Import it as `<vm_id>/<user_id>`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"virtual_machine": schema.StringAttribute{
				Required: true, MarkdownDescription: "Virtual machine id.", PlanModifiers: replace,
			},
			"username": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Account name: a lowercase letter, then lowercase letters, digits, `_` or `-`.",
				Validators:          []validator.String{stringvalidator.RegexMatches(guestUsernamePattern, "must be a portable username of at most 32 characters")},
				PlanModifiers:       replace,
			},
			"generate_password": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Generate a password, kept in the secret manager and never in state.",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown(), boolplanmodifier.RequiresReplace()},
			},
			"ssh_public_key_secret_name": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Name of an organization-wide plaintext `hyperfluid_secret` holding the SSH public key to authorize.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers:       replace,
			},
			"tags": schema.ListAttribute{
				ElementType: types.StringType, Optional: true, MarkdownDescription: "User-defined tags.",
				Validators:    []validator.List{listvalidator.SizeAtLeast(1)},
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
			},
			"slug":  schema.StringAttribute{Computed: true, MarkdownDescription: "Derived slug."},
			"phase": schema.StringAttribute{Computed: true, MarkdownDescription: "Lifecycle phase, `Ready` once the guest applied the credentials."},
		},
	}
}

// ValidateConfig refuses an account without a credential at plan time.
func (r *virtualMachineUserResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg virtualMachineUserModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.GeneratePassword.IsUnknown() || cfg.SSHPublicKeySecretName.IsUnknown() {
		return
	}
	if !cfg.GeneratePassword.ValueBool() && cfg.SSHPublicKeySecretName.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("generate_password"), "Missing credential",
			"Set `generate_password = true`, `ssh_public_key_secret_name`, or both.")
	}
}

func (r *virtualMachineUserResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *virtualMachineUserResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan virtualMachineUserModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := console.CreateVirtualMachineUserCrdRequestBody{
		Username:               plan.Username.ValueString(),
		GeneratePassword:       boolPtr(plan.GeneratePassword),
		SshPublicKeySecretName: stringPtr(plan.SSHPublicKeySecretName),
		Tags:                   strListPtr(ctx, plan.Tags, &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	vmID := plan.VirtualMachine.ValueString()
	// The virtual machine refuses guest users until it runs with its guest agent
	// connected; both come on their own after a boot.
	created, err := waitForReadyEvery(ctx, virtualMachineGuestWaitTimeout, orDefaultInterval(r.interval), func() (*console.VirtualMachineUserResponse, bool, error) {
		u, err := r.p.API.CreateVirtualMachineUser(ctx, r.p.OrgID, vmID, body)
		if errors.Is(err, client.ErrGuestNotReady) {
			return nil, false, nil
		}
		return u, err == nil, err
	})
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to create virtual machine user", featureVMs, err)
		return
	}
	id := created.Id.String()
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("virtual_machine"), vmID)...)
	u, err := r.waitReady(ctx, vmID, id)
	if err != nil {
		resp.Diagnostics.AddError("Virtual machine user did not become ready", err.Error())
		return
	}
	state, d := virtualMachineUserFrom(ctx, u)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *virtualMachineUserResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior virtualMachineUserModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	u, err := r.p.API.GetVirtualMachineUser(ctx, r.p.OrgID, prior.VirtualMachine.ValueString(), prior.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to read virtual machine user", featureVMs, err)
		return
	}
	state, d := virtualMachineUserFrom(ctx, u)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Update is never called: every attribute forces a new account.
func (r *virtualMachineUserResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Virtual machine users cannot be updated", "Every change to a virtual machine user replaces it.")
}

func (r *virtualMachineUserResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state virtualMachineUserModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vmID, id := state.VirtualMachine.ValueString(), state.ID.ValueString()
	if err := r.p.API.DeleteVirtualMachineUser(ctx, r.p.OrgID, vmID, id); err != nil {
		addGatedError(&resp.Diagnostics, "Failed to delete virtual machine user", featureVMs, err)
		return
	}
	if err := pollGoneOn404Every(ctx, virtualMachineUserWaitTimeout, orDefaultInterval(r.interval), func() error {
		_, err := r.p.API.GetVirtualMachineUser(ctx, r.p.OrgID, vmID, id)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Virtual machine user still present after delete", err.Error())
	}
}

// ImportState parses "<vm_id>/<user_id>".
func (r *virtualMachineUserResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	vmID, userID, err := splitID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("virtual_machine"), vmID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), userID)...)
}

// waitReady waits for the guest agent to confirm the credentials.
func (r *virtualMachineUserResource) waitReady(ctx context.Context, vmID, id string) (*console.VirtualMachineUserResponse, error) {
	return waitForReadyEvery(ctx, virtualMachineUserWaitTimeout, orDefaultInterval(r.interval), func() (*console.VirtualMachineUserResponse, bool, error) {
		u, err := r.p.API.GetVirtualMachineUser(ctx, r.p.OrgID, vmID, id)
		if err != nil {
			return nil, false, err
		}
		phase := ""
		if u.Phase != nil {
			phase = *u.Phase
		}
		if phase == "Failed" {
			if msg := falseConditionMessage(u.Conditions); msg != "" {
				return nil, false, fmt.Errorf("the virtual machine user failed: %s", msg)
			}
			return nil, false, errors.New("the virtual machine user entered the Failed phase")
		}
		return u, phase == "Ready", nil
	})
}

func virtualMachineUserFrom(ctx context.Context, u *console.VirtualMachineUserResponse) (virtualMachineUserModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	return virtualMachineUserModel{
		ID:                     types.StringValue(u.Id.String()),
		VirtualMachine:         types.StringValue(u.VirtualMachineId.String()),
		Username:               types.StringValue(u.Username),
		GeneratePassword:       types.BoolValue(u.HasPassword),
		SSHPublicKeySecretName: optNonEmpty(u.SshKeySecretName),
		Tags:                   nullIfEmptyList(ctx, u.Tags, &diags),
		Slug:                   types.StringValue(u.Slug),
		Phase:                  optString(u.Phase),
	}, diags
}
