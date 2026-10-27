// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// s3_access_key_resource.go — a long-lived S3 access key of an environment
// (Storage → Access keys in the console) for an existing service account. The
// secret is returned once, at issue, and is kept, sensitive, in state. Keys are
// immutable: every argument forces a new key, and a key revoked outside
// Terraform is dropped from state.

var (
	_ resource.Resource                = &s3AccessKeyResource{}
	_ resource.ResourceWithConfigure   = &s3AccessKeyResource{}
	_ resource.ResourceWithImportState = &s3AccessKeyResource{}
)

func NewS3AccessKeyResource() resource.Resource {
	return &s3AccessKeyResource{}
}

type s3AccessKeyResource struct {
	p *providerData
}

type s3AccessKeyModel struct {
	ID               types.String `tfsdk:"id"`
	Env              types.String `tfsdk:"env"`
	StorageZoneID    types.String `tfsdk:"storage_zone_id"`
	Label            types.String `tfsdk:"label"`
	ServiceAccountID types.String `tfsdk:"service_account_id"`
	AccessKeyID      types.String `tfsdk:"access_key_id"`
	SecretAccessKey  types.String `tfsdk:"secret_access_key"`
	Endpoint         types.String `tfsdk:"endpoint"`
	Region           types.String `tfsdk:"region"`
	Tenant           types.String `tfsdk:"tenant"`
	KeyEpoch         types.Int64  `tfsdk:"key_epoch"`
	Scopes           types.List   `tfsdk:"scopes"`
}

func (r *s3AccessKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_s3_access_key"
}

func (r *s3AccessKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A long-lived S3 access key for a service account, valid at the environment's S3 " +
			"gateway and reaching what the account's grants allow.\n\n" +
			"~> Revoking a key also revokes every older key of the same service account in the same zone, so give " +
			"each consumer its own service account.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Key id (uuid).",
				PlanModifiers:       keep,
			},
			"env": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Environment id. Changing this forces a new key.",
				Validators:          []validator.String{isUUID()},
				PlanModifiers:       replace,
			},
			"storage_zone_id": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Storage zone issuing the key; defaults to the environment's default zone. " +
					"Changing this forces a new key.",
				PlanModifiers: replace,
			},
			"label": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Free-form label, at most 255 characters. Changing this forces a new key.",
				Validators:          []validator.String{stringvalidator.LengthAtMost(255)},
				PlanModifiers:       replace,
			},
			"service_account_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Id of the service account the key authenticates as. Changing this forces a new key.",
				Validators:          []validator.String{isUUID()},
				PlanModifiers:       replace,
			},
			"access_key_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The S3 access key id.",
				PlanModifiers:       keep,
			},
			"secret_access_key": schema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				MarkdownDescription: "The S3 secret access key, null after import." + iamStateNote,
				PlanModifiers:       keep,
			},
			"endpoint": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "S3 gateway endpoint the key is valid at.",
				PlanModifiers:       keep,
			},
			"region": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "SigV4 region to sign requests with.",
				PlanModifiers:       keep,
			},
			"tenant": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Storage tenant the key belongs to.",
				PlanModifiers:       keep,
			},
			"key_epoch": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Generation of the key among the service account's keys.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"scopes": schema.ListAttribute{
				ElementType:         types.StringType,
				Computed:            true,
				MarkdownDescription: "Bucket paths (`<tenant>/<bucket>[/<prefix>]`) the service account holds grants on.",
			},
		},
	}
}

func (r *s3AccessKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *s3AccessKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan s3AccessKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	env, zone := plan.Env.ValueString(), plan.StorageZoneID.ValueString()
	said, err := uuidFromString(plan.ServiceAccountID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("service_account_id"), "Invalid service_account_id", err.Error())
		return
	}
	// Rotate mints a key at a new epoch, so this resource owns a credential of
	// its own instead of colliding with one the account already holds.
	rotate := true
	issued, err := r.p.API.IssueS3Key(ctx, env, console.IssueS3KeyRequest{
		ServiceAccountId: said,
		Label:            stringPtr(plan.Label),
		ZoneId:           stringPtr(plan.StorageZoneID),
		Rotate:           &rotate,
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to issue S3 access key", err.Error())
		return
	}
	state := plan
	state.ID = types.StringValue(issued.Id.String())
	state.AccessKeyID = types.StringValue(issued.AccessKeyId)
	state.SecretAccessKey = types.StringValue(issued.SecretAccessKey)
	state.Endpoint = types.StringValue(issued.Endpoint)
	state.Region = types.StringValue(issued.Region)
	state.Tenant = types.StringValue(issued.Tenant)
	state.KeyEpoch = types.Int64Value(int64(issued.KeyEpoch))

	entry, err := r.p.API.GetS3Key(ctx, env, zone, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read S3 access key after issuing it", err.Error())
		// The key exists: keep it in state so it is revoked on destroy.
		state.Scopes = types.ListNull(types.StringType)
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		return
	}
	resp.Diagnostics.Append(applyS3KeyEntry(ctx, &state, entry)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// applyS3KeyEntry copies what the key ledger reports into state.
func applyS3KeyEntry(ctx context.Context, state *s3AccessKeyModel, entry *console.S3KeyListEntry) diag.Diagnostics {
	scopes, diags := stringSliceToList(ctx, nonNil(entry.Scopes))
	state.Scopes = scopes
	state.AccessKeyID = types.StringValue(entry.AccessKeyId)
	state.Tenant = types.StringValue(entry.Tenant)
	state.KeyEpoch = types.Int64Value(int64(entry.KeyEpoch))
	state.Label = optString(entry.Label)
	// A key can outlive its account; the account it was issued for stays
	// recorded.
	if entry.ServiceAccountId != nil {
		state.ServiceAccountID = types.StringValue(entry.ServiceAccountId.String())
	}
	return diags
}

func (r *s3AccessKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state s3AccessKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	entry, err := r.p.API.GetS3Key(ctx, state.Env.ValueString(), state.StorageZoneID.ValueString(), state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read S3 access key", err.Error())
		return
	}
	// A revoked key stays in the ledger, but it no longer authenticates.
	if !entry.Active {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(applyS3KeyEntry(ctx, &state, entry)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Update is unreachable — every argument forces replacement.
func (r *s3AccessKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state s3AccessKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *s3AccessKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state s3AccessKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	revoked, err := r.p.API.RevokeS3Key(ctx, state.Env.ValueString(), state.StorageZoneID.ValueString(), state.ID.ValueString())
	switch {
	case errors.Is(err, client.ErrNotFound):
		// Unknown or already revoked: either way it no longer authenticates.
	case err != nil:
		resp.Diagnostics.AddError("Failed to revoke S3 access key", err.Error())
		return
	case len(revoked.AlsoRevoked) > 0:
		resp.Diagnostics.AddWarning("Older S3 keys revoked too",
			"Revoking this key also revoked the older keys of the same service account: "+
				strings.Join(revoked.AlsoRevoked, ", ")+".")
	}
}

// ImportState takes `<env>/<key_id>`, or `<env>/<storage_zone_id>/<key_id>` for
// a key of another zone. The secret cannot be imported.
func (r *s3AccessKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	var env, zone, id string
	switch {
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		env, id = parts[0], parts[1]
	case len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != "":
		env, zone, id = parts[0], parts[1], parts[2]
	default:
		resp.Diagnostics.AddError("Invalid import id",
			"expected import id in the form \"<env>/<key_id>\" or \"<env>/<storage_zone_id>/<key_id>\", got \""+req.ID+"\"")
		return
	}
	entry, err := r.p.API.GetS3Key(ctx, env, zone, id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to find the S3 access key to import", err.Error())
		return
	}
	if !entry.Active {
		resp.Diagnostics.AddError("S3 access key revoked", "key "+id+" is revoked and cannot be imported")
		return
	}
	state := s3AccessKeyModel{
		ID:               types.StringValue(id),
		Env:              types.StringValue(env),
		StorageZoneID:    types.StringNull(),
		ServiceAccountID: types.StringNull(),
		SecretAccessKey:  types.StringNull(),
		Endpoint:         types.StringNull(),
		Region:           types.StringNull(),
	}
	if zone != "" {
		state.StorageZoneID = types.StringValue(zone)
	}
	resp.Diagnostics.Append(applyS3KeyEntry(ctx, &state, entry)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}
