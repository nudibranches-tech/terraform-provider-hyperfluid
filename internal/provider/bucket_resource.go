// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

// bucket_resource.go is the REFERENCE resource — M1 clones this file per
// resource. It demonstrates every shared pattern: ForceNew identity,
// patch-only fields, wait-for-ready, delete-poll-to-404, and "harbor/name"
// import.

var (
	_ resource.Resource                = &bucketResource{}
	_ resource.ResourceWithConfigure   = &bucketResource{}
	_ resource.ResourceWithImportState = &bucketResource{}
)

const bucketWaitTimeout = 5 * time.Minute

func NewBucketResource() resource.Resource {
	return &bucketResource{}
}

type bucketResource struct {
	p *providerData
}

type bucketModel struct {
	ID           types.String `tfsdk:"id"`            // "harbor/name"
	Harbor       types.String `tfsdk:"harbor"`        // ForceNew
	Name         types.String `tfsdk:"name"`          // ForceNew
	QuotaGB      types.Int64  `tfsdk:"quota_gb"`      // patch-only → Optional+Computed
	FreezeWrites types.Bool   `tfsdk:"freeze_writes"` // patch-only → Optional+Computed
	Ready        types.Bool   `tfsdk:"ready"`         // computed (status)
}

func (r *bucketResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket"
}

func (r *bucketResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An object-storage bucket within a harbor.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Composite identifier `harbor/name`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"harbor": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Harbor id the bucket lives in. Changing this forces a new bucket.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Bucket name. Changing this forces a new bucket.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"quota_gb": schema.Int64Attribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Storage quota in GB. Applied via update (not accepted at create).",
			},
			"freeze_writes": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "When true, the bucket rejects writes.",
			},
			"ready": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the bucket is provisioned and ready.",
			},
		},
	}
}

func (r *bucketResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *bucketResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan bucketModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	harbor := plan.Harbor.ValueString()
	if _, err := r.p.API.CreateBucket(ctx, harbor, client.CreateBucketBody{Name: plan.Name.ValueString()}); err != nil {
		resp.Diagnostics.AddError("Failed to create bucket", err.Error())
		return
	}

	// quota/freeze are not accepted at create → apply via PATCH if set.
	if !plan.QuotaGB.IsNull() || !plan.FreezeWrites.IsNull() {
		if err := r.patch(ctx, plan); err != nil {
			resp.Diagnostics.AddError("Failed to set bucket quota/freeze", err.Error())
			return
		}
	}

	b, err := waitForReady(ctx, bucketWaitTimeout, func() (*client.Bucket, bool, error) {
		b, err := r.p.API.GetBucket(ctx, harbor, plan.Name.ValueString())
		if err != nil {
			return nil, false, err
		}
		return b, b.Ready, nil
	})
	if err != nil {
		resp.Diagnostics.AddError("Bucket did not become ready", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, r.toModel(harbor, b))...)
}

func (r *bucketResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state bucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	harbor := state.Harbor.ValueString()
	b, err := r.p.API.GetBucket(ctx, harbor, state.Name.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx) // drifted away → let TF plan a recreate
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read bucket", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, r.toModel(harbor, b))...)
}

func (r *bucketResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan bucketModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.patch(ctx, plan); err != nil {
		resp.Diagnostics.AddError("Failed to update bucket", err.Error())
		return
	}

	harbor := plan.Harbor.ValueString()
	b, err := r.p.API.GetBucket(ctx, harbor, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read bucket after update", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, r.toModel(harbor, b))...)
}

func (r *bucketResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state bucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	harbor := state.Harbor.ValueString()
	name := state.Name.ValueString()
	if err := r.p.API.DeleteBucket(ctx, harbor, name); err != nil && !errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Failed to delete bucket", err.Error())
		return
	}

	// M3: a 204 does not guarantee the resource is gone — poll until 404.
	if err := pollGoneOn404(ctx, bucketWaitTimeout, func() error {
		_, err := r.p.API.GetBucket(ctx, harbor, name)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Bucket still present after delete", err.Error())
	}
}

// ImportState parses the "harbor/name" import id into the identity attributes;
// the subsequent Read populates the rest of the state.
func (r *bucketResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	harbor, name, err := splitHarborName(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("harbor"), harbor)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}

// patch sends only the in-place-updatable fields.
func (r *bucketResource) patch(ctx context.Context, m bucketModel) error {
	var body client.PatchBucketBody
	if !m.QuotaGB.IsNull() && !m.QuotaGB.IsUnknown() {
		v := m.QuotaGB.ValueInt64()
		body.QuotaGB = &v
	}
	if !m.FreezeWrites.IsNull() && !m.FreezeWrites.IsUnknown() {
		v := m.FreezeWrites.ValueBool()
		body.FreezeWrites = &v
	}
	_, err := r.p.API.PatchBucket(ctx, m.Harbor.ValueString(), m.Name.ValueString(), body)
	return err
}

func (r *bucketResource) toModel(harbor string, b *client.Bucket) bucketModel {
	m := bucketModel{
		ID:     types.StringValue(harbor + "/" + b.Name),
		Harbor: types.StringValue(harbor),
		Name:   types.StringValue(b.Name),
		Ready:  types.BoolValue(b.Ready),
	}
	if b.QuotaGB != nil {
		m.QuotaGB = types.Int64Value(*b.QuotaGB)
	} else {
		m.QuotaGB = types.Int64Null()
	}
	if b.FreezeWrites != nil {
		m.FreezeWrites = types.BoolValue(*b.FreezeWrites)
	} else {
		m.FreezeWrites = types.BoolNull()
	}
	return m
}
