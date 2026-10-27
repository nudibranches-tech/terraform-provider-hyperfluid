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

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// environment_resource.go — an environment (a Harbor in the API). There is no
// update endpoint, so name and slug force a new environment; only the
// provider-side deletion_protection changes in place.

var (
	_ resource.Resource                = &environmentResource{}
	_ resource.ResourceWithConfigure   = &environmentResource{}
	_ resource.ResourceWithImportState = &environmentResource{}
	_ resource.ResourceWithModifyPlan  = &environmentResource{}
)

// environmentDeleteTimeout bounds the wait for the namespace and every
// workload in it to terminate.
const environmentDeleteTimeout = 15 * time.Minute

var environmentSlugRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func NewEnvironmentResource() resource.Resource {
	return &environmentResource{}
}

type environmentResource struct {
	p *providerData
}

type environmentModel struct {
	ID                 types.String `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	Slug               types.String `tfsdk:"slug"`
	Namespace          types.String `tfsdk:"namespace"`
	OwnerID            types.String `tfsdk:"owner_id"`
	DeletionProtection types.Bool   `tfsdk:"deletion_protection"`
}

func (r *environmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_environment"
}

func (r *environmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An environment: the space other resources are created in, through its `id`.\n\n" +
			"~> Destroying an environment deletes every resource in it. While `deletion_protection` is true, " +
			"both destroying it and any change that replaces it (`name`, `slug`) fail.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Environment id, the value other resources take as `env`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Display name, 3 to 63 characters. Changing this forces a new environment.",
				Validators:          []validator.String{stringvalidator.LengthBetween(3, 63)},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"slug": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Slug: lowercase letters, digits and hyphens. It is the import id. " +
					"Changing this forces a new environment.",
				Validators: []validator.String{stringvalidator.RegexMatches(environmentSlugRE,
					"must be lowercase letters, digits and hyphens, starting and ending with a letter or digit")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"namespace": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Kubernetes namespace the environment's workloads run in.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"owner_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Id of the user who owns the environment, or null.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"deletion_protection": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Refuse to destroy or replace the environment. Defaults to true; Terraform-only, never sent to the platform.",
			},
		},
	}
}

func (r *environmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ModifyPlan refuses a replacement of a protected environment at plan time, so
// nothing is created before the destroy half of the replacement fails.
func (r *environmentResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var state, plan environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.Name.Equal(state.Name) && plan.Slug.Equal(state.Slug) {
		return
	}
	if state.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Environment is protected from replacement",
			fmt.Sprintf("Changing `name` or `slug` replaces environment %q, which deletes every resource in it. "+
				"Set deletion_protection = false and apply first if that is intended.", state.Slug.ValueString()))
	}
}

func (r *environmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan environmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	h, err := r.p.API.CreateEnvironment(ctx, r.p.OrgID, plan.Name.ValueString(), plan.Slug.ValueString())
	if err != nil {
		summary, detail := environmentCreateError(err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, environmentFrom(h, plan.DeletionProtection))...)
}

func (r *environmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	h, err := r.p.API.GetEnvironmentBySlug(ctx, r.p.OrgID, prior.Slug.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read environment", err.Error())
		return
	}
	// The slug was freed and taken again outside Terraform: this is another environment.
	if !prior.ID.IsNull() && !prior.ID.IsUnknown() && prior.ID.ValueString() != h.Id.String() {
		resp.State.RemoveResource(ctx)
		return
	}
	protection := prior.DeletionProtection
	if protection.IsNull() || protection.IsUnknown() {
		protection = types.BoolValue(true)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, environmentFrom(h, protection))...)
}

// Update only ever changes deletion_protection, which lives in state alone.
func (r *environmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan environmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *environmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	slug := state.Slug.ValueString()
	if state.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Environment is protected from deletion",
			fmt.Sprintf("Destroying environment %q deletes every resource in it. "+
				"Set deletion_protection = false and apply first if that is intended.", slug))
		return
	}
	err := r.p.API.DeleteEnvironment(ctx, r.p.OrgID, slug)
	if errors.Is(err, client.ErrNotFound) {
		return
	}
	if errors.Is(err, client.ErrForbidden) {
		resp.Diagnostics.AddError("Missing permission to delete environments",
			err.Error()+"\n\nDeleting an environment needs the `harbor:delete` permission on it.")
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to delete environment", err.Error())
		return
	}
	id := state.ID.ValueString()
	if err := pollGoneOn404(ctx, environmentDeleteTimeout, func() error {
		h, err := r.p.API.GetEnvironmentBySlug(ctx, r.p.OrgID, slug)
		if err == nil && h.Id.String() != id {
			return client.ErrNotFound
		}
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Environment still present after delete", err.Error())
	}
}

// ImportState takes the environment's slug.
func (r *environmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("slug"), req, resp)
}

func environmentFrom(h *console.Harbor, protection types.Bool) environmentModel {
	m := environmentModel{
		ID:                 types.StringValue(h.Id.String()),
		Name:               types.StringValue(h.Name),
		Slug:               types.StringValue(h.Slug),
		Namespace:          types.StringValue(h.Namespace),
		OwnerID:            types.StringNull(),
		DeletionProtection: protection,
	}
	if h.OwnerId != nil {
		m.OwnerID = types.StringValue(h.OwnerId.String())
	}
	return m
}

// environmentCreateError names what refused a create: the plan's environment
// quota or a missing permission. Any other refusal keeps the server's message.
func environmentCreateError(err error) (summary, detail string) {
	var quota *client.QuotaExceededError
	switch {
	case errors.As(err, &quota):
		return "Environment quota reached", fmt.Sprintf("The organization already has %d of the %d environments its plan allows. "+
			"Delete an unused environment, or ask a platform administrator to raise the organization's environment quota.\n\n%s",
			quota.Current, quota.Limit, quota.Message)
	case errors.Is(err, client.ErrForbidden) && strings.Contains(err.Error(), "missing permission"):
		return "Missing permission to create environments", err.Error() +
			"\n\nCreating an environment needs the `harbor:create` permission on the organization."
	default:
		return "Failed to create environment", err.Error()
	}
}
