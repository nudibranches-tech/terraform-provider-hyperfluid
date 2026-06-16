// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"time"

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
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// managed_postgresql_user_resource.go — a Postgres role within a cluster.
// Import id is "<cluster_id>/<user_id>".

var (
	_ resource.Resource                = &managedPostgresqlUserResource{}
	_ resource.ResourceWithConfigure   = &managedPostgresqlUserResource{}
	_ resource.ResourceWithImportState = &managedPostgresqlUserResource{}
)

const managedPostgresqlUserWaitTimeout = 5 * time.Minute

func NewManagedPostgresqlUserResource() resource.Resource {
	return &managedPostgresqlUserResource{}
}

type managedPostgresqlUserResource struct {
	p *providerData
}

type managedPostgresqlUserModel struct {
	ID              types.String `tfsdk:"id"`
	ManagedPostgres types.String `tfsdk:"managed_postgresql"`
	Username        types.String `tfsdk:"username"`
	PermissionLevel types.String `tfsdk:"permission_level"`
	Description     types.String `tfsdk:"description"`
	Tags            types.List   `tfsdk:"tags"`

	// computed
	Phase types.String `tfsdk:"phase"`
	Slug  types.String `tfsdk:"slug"`
}

func (r *managedPostgresqlUserResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_managed_postgresql_user"
}

func (r *managedPostgresqlUserResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A PostgreSQL role within a managed cluster. Import id is \"<cluster_id>/<user_id>\".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"managed_postgresql": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Cluster id this user belongs to. Changing this forces a new user.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"username": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Postgres role name. Changing this forces a new user.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"permission_level": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Permission level: editor or viewer.",
				Validators:          []validator.String{stringvalidator.OneOf("editor", "viewer")},
			},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Free-form description."},
			"tags": schema.ListAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				MarkdownDescription: "User-defined tags.",
			},
			"phase": schema.StringAttribute{Computed: true, MarkdownDescription: "Current lifecycle phase."},
			"slug":  schema.StringAttribute{Computed: true, MarkdownDescription: "Derived slug."},
		},
	}
}

func (r *managedPostgresqlUserResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *managedPostgresqlUserResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan managedPostgresqlUserModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tags, d := listToStringSlice(ctx, plan.Tags)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := console.CreateManagedPostgresqlUserCrdRequestBody{
		Username:    plan.Username.ValueString(),
		Description: stringPtr(plan.Description),
	}
	if tags != nil {
		body.Tags = &tags
	}
	if !plan.PermissionLevel.IsNull() && !plan.PermissionLevel.IsUnknown() {
		pl := console.PermissionLevel(plan.PermissionLevel.ValueString())
		body.PermissionLevel = &pl
	}

	cluster := plan.ManagedPostgres.ValueString()
	created, err := r.p.API.CreateManagedPostgresqlUser(ctx, r.p.OrgID, cluster, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create managed postgresql user", err.Error())
		return
	}

	state, d := r.toModel(ctx, cluster, created)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *managedPostgresqlUserResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior managedPostgresqlUserModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	cluster := prior.ManagedPostgres.ValueString()
	u, err := r.p.API.GetManagedPostgresqlUser(ctx, r.p.OrgID, cluster, prior.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read managed postgresql user", err.Error())
		return
	}
	state, d := r.toModel(ctx, cluster, u)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *managedPostgresqlUserResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state managedPostgresqlUserModel
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

	body := console.PatchManagedPostgresqlUserCrdRequestBody{
		Description: stringPtr(plan.Description),
	}
	if tags != nil {
		body.Tags = &tags
	}
	if !plan.PermissionLevel.IsNull() && !plan.PermissionLevel.IsUnknown() {
		pl := console.PermissionLevel(plan.PermissionLevel.ValueString())
		body.PermissionLevel = &pl
	}

	cluster := state.ManagedPostgres.ValueString()
	if err := r.p.API.PatchManagedPostgresqlUser(ctx, r.p.OrgID, cluster, state.ID.ValueString(), body); err != nil {
		resp.Diagnostics.AddError("Failed to update managed postgresql user", err.Error())
		return
	}
	u, err := r.p.API.GetManagedPostgresqlUser(ctx, r.p.OrgID, cluster, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read user after update", err.Error())
		return
	}
	newState, d := r.toModel(ctx, cluster, u)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *managedPostgresqlUserResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state managedPostgresqlUserModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	cluster := state.ManagedPostgres.ValueString()
	userID := state.ID.ValueString()
	if err := r.p.API.DeleteManagedPostgresqlUser(ctx, r.p.OrgID, cluster, userID); err != nil {
		resp.Diagnostics.AddError("Failed to delete managed postgresql user", err.Error())
		return
	}
	if err := pollGoneOn404(ctx, managedPostgresqlUserWaitTimeout, func() error {
		_, err := r.p.API.GetManagedPostgresqlUser(ctx, r.p.OrgID, cluster, userID)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("User still present after delete", err.Error())
	}
}

// ImportState parses "<cluster_id>/<user_id>".
func (r *managedPostgresqlUserResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	cluster, userID, err := splitID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("managed_postgresql"), cluster)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), userID)...)
}

func (r *managedPostgresqlUserResource) toModel(ctx context.Context, cluster string, u *console.ManagedPostgresqlUserResponse) (managedPostgresqlUserModel, diag.Diagnostics) {
	tags, d := stringSliceToList(ctx, u.Tags)
	return managedPostgresqlUserModel{
		ID:              types.StringValue(u.Id.String()),
		ManagedPostgres: types.StringValue(cluster),
		Username:        types.StringValue(u.Username),
		PermissionLevel: types.StringValue(u.PermissionLevel),
		Description:     optString(u.Description),
		Tags:            tags,
		Phase:           optString(u.Phase),
		Slug:            types.StringValue(u.Slug),
	}, d
}
