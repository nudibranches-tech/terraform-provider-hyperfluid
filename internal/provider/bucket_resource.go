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
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// bucket_resource.go is the REFERENCE resource — M1 clones this file per
// resource. It demonstrates the shared patterns: ForceNew identity,
// wait-for-ready, delete-poll-to-404, and "env/name" import.
//
// quota_gb / freeze_writes are deliberately NOT exposed: they are only settable
// via the console bucket-PATCH endpoint, which currently 500s for any body
// (backend bug nudibranches-tech/hyperfluid#2596). Both fields remain available
// in the console UI (Settings → bucket) and can be re-added here once that bug
// is fixed — see issue #16.

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
	ID            types.String `tfsdk:"id"`              // "env/name"
	Env           types.String `tfsdk:"env"`             // ForceNew
	Name          types.String `tfsdk:"name"`            // ForceNew
	StorageZoneID types.String `tfsdk:"storage_zone_id"` // ForceNew, computed when omitted
	Ready         types.Bool   `tfsdk:"ready"`           // computed (status)
}

func (r *bucketResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket"
}

func (r *bucketResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An object-storage bucket within an environment.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Composite identifier `env/name`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"env": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Environment id the bucket lives in. Changing this forces a new bucket.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Bucket name. Changing this forces a new bucket.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"storage_zone_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Storage zone the bucket is placed in. Omit for the org's " +
					"primary zone (`default`). Placement is fixed at creation, so changing " +
					"this forces a new bucket. A non-default value must be a zone the org has enabled.",
				PlanModifiers: []planmodifier.String{
					// Hold the resolved zone (the API fills "default" when omitted) so an
					// unconfigured bucket keeps its stored value instead of planning unknown.
					stringplanmodifier.UseStateForUnknown(),
					// Only force replacement when the zone is explicitly configured and
					// changes — an omitted zone resolves to the stored value (see above),
					// so it must never look like a change that recreates the bucket.
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
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

	env := plan.Env.ValueString()
	name := plan.Name.ValueString()
	if err := r.p.API.CreateBucket(ctx, env, name, plan.StorageZoneID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Failed to create bucket", err.Error())
		return
	}

	b, err := waitForReady(ctx, bucketWaitTimeout, func() (*console.HFBucketDetail, bool, error) {
		b, err := r.p.API.GetBucket(ctx, env, name)
		if err != nil {
			return nil, false, err
		}
		return b, b.Ready, nil
	})
	if err != nil {
		resp.Diagnostics.AddError("Bucket did not become ready", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, r.toModel(env, b))...)
}

func (r *bucketResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state bucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	env := state.Env.ValueString()
	b, err := r.p.API.GetBucket(ctx, env, state.Name.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx) // drifted away → let TF plan a recreate
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read bucket", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, r.toModel(env, b))...)
}

// Update has no in-place-updatable fields — env and name both force replacement
// and there are no other mutable attributes — so it is effectively unreachable.
// It re-reads the live bucket to keep state consistent if it is ever called.
func (r *bucketResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan bucketModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	env := plan.Env.ValueString()
	b, err := r.p.API.GetBucket(ctx, env, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read bucket after update", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, r.toModel(env, b))...)
}

func (r *bucketResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state bucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	env := state.Env.ValueString()
	name := state.Name.ValueString()
	if err := r.p.API.DeleteBucket(ctx, env, name); err != nil && !errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Failed to delete bucket", err.Error())
		return
	}

	// M3: a 204 does not guarantee the resource is gone — poll until 404.
	if err := pollGoneOn404(ctx, bucketWaitTimeout, func() error {
		_, err := r.p.API.GetBucket(ctx, env, name)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Bucket still present after delete", err.Error())
	}
}

// ImportState parses the "env/name" import id into the identity attributes;
// the subsequent Read populates the rest of the state.
func (r *bucketResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	env, name, err := splitEnvName(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("env"), env)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}

func (r *bucketResource) toModel(env string, b *console.HFBucketDetail) bucketModel {
	return bucketModel{
		ID:            types.StringValue(env + "/" + b.Name),
		Env:           types.StringValue(env),
		Name:          types.StringValue(b.Name),
		StorageZoneID: types.StringValue(b.ZoneId),
		Ready:         types.BoolValue(b.Ready),
	}
}
