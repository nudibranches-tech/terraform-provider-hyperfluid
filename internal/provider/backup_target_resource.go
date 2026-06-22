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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// backup_target_resource.go — external (BYO-S3) backup target. The create API
// takes a discriminated `source` union, but patch + the read view are flat, so
// the schema is flat too (the client builds the union at create). The internal
// (org Ceph bucket) source variant is a follow-up.

var (
	_ resource.Resource                = &backupTargetResource{}
	_ resource.ResourceWithConfigure   = &backupTargetResource{}
	_ resource.ResourceWithImportState = &backupTargetResource{}
)

const backupTargetWaitTimeout = 5 * time.Minute

func NewBackupTargetResource() resource.Resource {
	return &backupTargetResource{}
}

type backupTargetResource struct {
	p *providerData
}

type backupTargetModel struct {
	ID                        types.String `tfsdk:"id"`
	Env                       types.String `tfsdk:"env"`
	Name                      types.String `tfsdk:"name"`
	EndpointURL               types.String `tfsdk:"endpoint_url"`
	DestinationPath           types.String `tfsdk:"destination_path"`
	AccessKeySecretName       types.String `tfsdk:"access_key_secret_name"`
	SecretAccessKeySecretName types.String `tfsdk:"secret_access_key_secret_name"`
	Insecure                  types.Bool   `tfsdk:"insecure"`
	Description               types.String `tfsdk:"description"`
	Tags                      types.List   `tfsdk:"tags"`

	// computed
	Phase types.String `tfsdk:"phase"`
	Slug  types.String `tfsdk:"slug"`
}

func (r *backupTargetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_backup_target"
}

func (r *backupTargetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An external (S3-compatible) backup target. Credentials are referenced as secret names, never inline.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"env": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Environment id the target belongs to. Changing this forces a new target.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Backup target name (slug). Changing this forces a new target.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"endpoint_url": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "S3-compatible endpoint URL, e.g. https://s3.eu-west-3.amazonaws.com.",
			},
			"destination_path": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Bucket + prefix, e.g. s3://backups/.",
			},
			"access_key_secret_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Name of the secret holding the S3 access key id.",
			},
			"secret_access_key_secret_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Name of the secret holding the S3 secret access key.",
			},
			"insecure": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Skip TLS verification when probing the endpoint (dev only). Changing this forces a new target.",
				Default:             booldefault.StaticBool(false),
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
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

func (r *backupTargetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *backupTargetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan backupTargetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tags, d := listToStringSlice(ctx, plan.Tags)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.p.API.CreateExternalBackupTarget(ctx, r.p.OrgID, plan.Env.ValueString(), client.ExternalBackupTargetInput{
		Name:                      plan.Name.ValueString(),
		EndpointURL:               plan.EndpointURL.ValueString(),
		DestinationPath:           plan.DestinationPath.ValueString(),
		AccessKeySecretName:       plan.AccessKeySecretName.ValueString(),
		SecretAccessKeySecretName: plan.SecretAccessKeySecretName.ValueString(),
		Insecure:                  boolPtr(plan.Insecure),
		Description:               stringPtr(plan.Description),
		Tags:                      tags,
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to create backup target", err.Error())
		return
	}
	id := created.Id.String()

	if err := r.waitReady(ctx, id); err != nil {
		resp.Diagnostics.AddError("Backup target did not become ready", err.Error())
		return
	}
	state, err := r.readInto(ctx, plan.Env.ValueString(), id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read backup target after create", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *backupTargetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior backupTargetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.readInto(ctx, prior.Env.ValueString(), prior.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read backup target", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *backupTargetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state backupTargetModel
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

	body := console.PatchBackupTargetCrdRequestBody{
		EndpointUrl:               stringPtr(plan.EndpointURL),
		DestinationPath:           stringPtr(plan.DestinationPath),
		AccessKeySecretName:       stringPtr(plan.AccessKeySecretName),
		SecretAccessKeySecretName: stringPtr(plan.SecretAccessKeySecretName),
		Description:               stringPtr(plan.Description),
	}
	if tags != nil {
		body.Tags = &tags
	}

	id := state.ID.ValueString()
	if err := r.p.API.PatchBackupTarget(ctx, r.p.OrgID, id, body); err != nil {
		resp.Diagnostics.AddError("Failed to update backup target", err.Error())
		return
	}
	newState, err := r.readInto(ctx, plan.Env.ValueString(), id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read backup target after update", err.Error())
		return
	}
	// In-place fields reflect the plan (operator converges; the read can lag).
	if !plan.EndpointURL.IsUnknown() {
		newState.EndpointURL = plan.EndpointURL
	}
	if !plan.DestinationPath.IsUnknown() {
		newState.DestinationPath = plan.DestinationPath
	}
	if !plan.AccessKeySecretName.IsUnknown() {
		newState.AccessKeySecretName = plan.AccessKeySecretName
	}
	if !plan.SecretAccessKeySecretName.IsUnknown() {
		newState.SecretAccessKeySecretName = plan.SecretAccessKeySecretName
	}
	if !plan.Tags.IsUnknown() {
		newState.Tags = plan.Tags
	}
	newState.Description = plan.Description
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *backupTargetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state backupTargetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.p.API.DeleteBackupTarget(ctx, r.p.OrgID, id); err != nil {
		resp.Diagnostics.AddError("Failed to delete backup target", err.Error())
		return
	}
	if err := pollGoneOn404(ctx, backupTargetWaitTimeout, func() error {
		_, err := r.p.API.GetBackupTarget(ctx, r.p.OrgID, id)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Backup target still present after delete", err.Error())
	}
}

func (r *backupTargetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *backupTargetResource) waitReady(ctx context.Context, id string) error {
	_, err := waitForReady(ctx, backupTargetWaitTimeout, func() (*console.BackupTargetResponse, bool, error) {
		bt, err := r.p.API.GetBackupTarget(ctx, r.p.OrgID, id)
		if err != nil {
			return nil, false, err
		}
		// Fail fast: the operator sets phase=Failed (with an S3Reachable condition
		// explaining why — missing secret, unreachable bucket, denied permission)
		// when the credentials/endpoint probe fails. Surfacing that beats polling
		// the full timeout only to report a generic "did not become ready".
		if bt.Phase == "Failed" {
			msg := conditionMessage(bt.Conditions, "S3Reachable")
			if msg == "" {
				msg = "backup target entered the Failed phase"
			}
			return nil, false, errors.New(msg)
		}
		return bt, bt.Phase == "Ready", nil
	})
	return err
}

func (r *backupTargetResource) readInto(ctx context.Context, env, id string) (backupTargetModel, error) {
	bt, err := r.p.API.GetBackupTarget(ctx, r.p.OrgID, id)
	if err != nil {
		return backupTargetModel{}, err
	}
	tags, d := stringSliceToList(ctx, bt.Tags)
	if d.HasError() {
		return backupTargetModel{}, errors.New("failed to convert tags")
	}
	return backupTargetModel{
		ID:                        types.StringValue(id),
		Env:                       types.StringValue(env),
		Name:                      types.StringValue(bt.Name),
		EndpointURL:               types.StringValue(bt.EndpointUrl),
		DestinationPath:           types.StringValue(bt.DestinationPath),
		AccessKeySecretName:       optString(bt.AccessKeySecretName),
		SecretAccessKeySecretName: optString(bt.SecretAccessKeySecretName),
		Insecure:                  types.BoolValue(bt.Insecure),
		Description:               optString(bt.Description),
		Tags:                      tags,
		Phase:                     types.StringValue(bt.Phase),
		Slug:                      types.StringValue(bt.Slug),
	}, nil
}
