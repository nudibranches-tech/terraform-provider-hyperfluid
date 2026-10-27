// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

// service_account_attributes_resource.go — the row-level-security attributes
// of one service account, managed authoritatively: apply replaces the whole
// set in one call, and destroy clears it.

var (
	_ resource.Resource                = &serviceAccountAttributesResource{}
	_ resource.ResourceWithConfigure   = &serviceAccountAttributesResource{}
	_ resource.ResourceWithImportState = &serviceAccountAttributesResource{}
)

var serviceAccountAttributePattern = regexp.MustCompile(`^[^:]+::.+$`)

func NewServiceAccountAttributesResource() resource.Resource {
	return &serviceAccountAttributesResource{}
}

type serviceAccountAttributesResource struct {
	p *providerData
}

type serviceAccountAttributesModel struct {
	ID               types.String `tfsdk:"id"`
	ServiceAccountID types.String `tfsdk:"service_account_id"`
	Attributes       types.Set    `tfsdk:"attributes"`
}

func (r *serviceAccountAttributesResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_account_attributes"
}

func (r *serviceAccountAttributesResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The row-level-security attributes of a service account, e.g. `region::occitanie`, which " +
			"data access policies match against. Managed authoritatively: Terraform makes the account hold exactly " +
			"`attributes`, and destroying the resource clears them.\n\n" +
			"Requires `service_account:read`, `service_account:update` and `user_attribute:assign`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The service account id.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"service_account_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Id of the service account (the `hyperfluid_service_account` data source's `id`, not its " +
					"`client_id`). Service accounts are created in the console or with hfctl.",
				Validators:    []validator.String{isUUID()},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"attributes": schema.SetAttribute{
				ElementType:         types.StringType,
				Required:            true,
				MarkdownDescription: "Attributes in `namespace::value` form.",
				Validators: []validator.Set{setvalidator.ValueStringsAre(
					stringvalidator.RegexMatches(serviceAccountAttributePattern, "must be in `namespace::value` form"))},
			},
		},
	}
}

func (r *serviceAccountAttributesResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *serviceAccountAttributesResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serviceAccountAttributesModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, d := r.apply(ctx, plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, got)...)
}

func (r *serviceAccountAttributesResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan serviceAccountAttributesModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, d := r.apply(ctx, plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, got)...)
}

// apply replaces the account's attributes with the planned set and reads them
// back.
func (r *serviceAccountAttributesResource) apply(ctx context.Context, plan serviceAccountAttributesModel) (serviceAccountAttributesModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	attrs, d := setToStringSlice(ctx, plan.Attributes)
	diags.Append(d...)
	if diags.HasError() {
		return plan, diags
	}
	id := plan.ServiceAccountID.ValueString()
	if err := r.p.API.SetServiceAccountAttributes(ctx, r.p.OrgID, id, attrs); err != nil {
		diags.AddError("Failed to set service account attributes", err.Error())
		return plan, diags
	}
	got, err := r.read(ctx, id)
	if err != nil {
		diags.AddError("Failed to read service account attributes after setting them", err.Error())
	}
	return got, diags
}

func (r *serviceAccountAttributesResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior serviceAccountAttributesModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.read(ctx, prior.ServiceAccountID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read service account attributes", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, got)...)
}

func (r *serviceAccountAttributesResource) read(ctx context.Context, id string) (serviceAccountAttributesModel, error) {
	attrs, err := r.p.API.GetServiceAccountAttributes(ctx, r.p.OrgID, id)
	if err != nil {
		return serviceAccountAttributesModel{}, err
	}
	set, d := stringSliceToSet(ctx, nonNil(attrs))
	if d.HasError() {
		return serviceAccountAttributesModel{}, errors.New("convert attributes to a set")
	}
	return serviceAccountAttributesModel{
		ID:               types.StringValue(id),
		ServiceAccountID: types.StringValue(id),
		Attributes:       set,
	}, nil
}

func (r *serviceAccountAttributesResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serviceAccountAttributesModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.p.API.SetServiceAccountAttributes(ctx, r.p.OrgID, state.ServiceAccountID.ValueString(), nil)
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Failed to clear service account attributes", err.Error())
	}
}

func (r *serviceAccountAttributesResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("service_account_id"), req, resp)
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
