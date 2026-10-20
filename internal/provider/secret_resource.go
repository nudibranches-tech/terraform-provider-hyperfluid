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

// secret_resource.go — managed secret. `value` is a write-only argument: it is
// sent to the API but NEVER persisted to Terraform state. Because state holds no
// value, Terraform can't detect value changes — rotate by bumping
// `value_wo_version`. Read reconciles metadata only (never calls /value).
// Supported secret types: plaintext, json and scm_credential (the Git
// credential a container app's image_source reads its repository with).

var (
	_ resource.Resource                   = &secretResource{}
	_ resource.ResourceWithConfigure      = &secretResource{}
	_ resource.ResourceWithImportState    = &secretResource{}
	_ resource.ResourceWithValidateConfig = &secretResource{}
)

const secretWaitTimeout = 2 * time.Minute

func NewSecretResource() resource.Resource {
	return &secretResource{}
}

type secretResource struct {
	p *providerData
}

type secretModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	SecretType     types.String `tfsdk:"secret_type"`
	Value          types.String `tfsdk:"value"`            // write-only — never in state
	ValueWoVersion types.String `tfsdk:"value_wo_version"` // bump to push a new value
	Description    types.String `tfsdk:"description"`
	Tags           types.List   `tfsdk:"tags"`
	SecretPath     types.String `tfsdk:"secret_path"`
}

func (r *secretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret"
}

func (r *secretResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A managed secret. `value` is write-only — it is never stored in Terraform state. " +
			"Requires Terraform >= 1.11. Rotate the value by changing `value` together with `value_wo_version`.\n\n" +
			"A secret of type `scm_credential` is a Git credential: reference it by `name` from a " +
			"`hyperfluid_container_app`'s `image_source.git.credential`. It is created organization-wide, " +
			"which is what a Git source requires.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Secret name. Changing this forces a new secret.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"secret_type": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Secret type: `plaintext`, `json` or `scm_credential` (a Git credential). " +
					"Changing this forces a new secret.",
				Validators:    []validator.String{stringvalidator.OneOf("plaintext", "json", "scm_credential")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"value": schema.StringAttribute{
				Optional:  true,
				WriteOnly: true,
				Sensitive: true,
				MarkdownDescription: "Secret material (write-only — never persisted to state). For `json`, a JSON-encoded string. " +
					"For `scm_credential`, a JSON object `{ \"provider\", \"username\"?, \"base_url\"?, \"token\" }`: " +
					"`provider` is `github`, `gitlab` or `forgejo`, `base_url` names a self-hosted instance (leave it out " +
					"for the public one), and `token` is the access token — read-only is enough for a Git source. " +
					"Required on create.",
			},
			"value_wo_version": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Arbitrary token; change it together with `value` to push a new value (rotation). Needed because the write-only `value` is not tracked in state.",
			},
			"description": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Free-form description.",
			},
			"tags": schema.ListAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				MarkdownDescription: "User-defined tags.",
			},
			"secret_path": schema.StringAttribute{Computed: true, MarkdownDescription: "Platform path of the secret."},
		},
	}
}

// ValidateConfig checks the shape of a Git credential at plan time, where the
// write-only value is visible in the config, instead of at apply time on the
// API. Nothing it reports quotes the value.
func (r *secretResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var secretType, value types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("secret_type"), &secretType)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("value"), &value)...)
	if resp.Diagnostics.HasError() || secretType.ValueString() != "scm_credential" ||
		value.IsNull() || value.IsUnknown() {
		return
	}
	if _, err := client.BuildSecretValue("scm_credential", value.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("value"), "Invalid Git credential", err.Error())
	}
}

func (r *secretResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *secretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan secretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Write-only value lives in config, not plan/state.
	var valueCfg types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("value"), &valueCfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if valueCfg.IsNull() {
		resp.Diagnostics.AddError("Missing value", "`value` is required when creating a secret.")
		return
	}

	tags, d := listToStringSlice(ctx, plan.Tags)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	val, err := client.BuildSecretValue(plan.SecretType.ValueString(), valueCfg.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid secret value", err.Error())
		return
	}

	body := console.CreateSecretRequestBody{
		Name:        plan.Name.ValueString(),
		SecretType:  console.SecretType(plan.SecretType.ValueString()),
		Value:       val,
		Description: stringPtr(plan.Description),
	}
	if tags != nil {
		body.Tags = &tags
	}

	created, err := r.p.API.CreateSecret(ctx, r.p.OrgID, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create secret", err.Error())
		return
	}

	state, d := r.toModel(ctx, created)
	resp.Diagnostics.Append(d...)
	state.ValueWoVersion = plan.ValueWoVersion
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *secretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior secretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	meta, err := r.p.API.GetSecret(ctx, r.p.OrgID, prior.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read secret", err.Error())
		return
	}
	state, d := r.toModel(ctx, meta)
	resp.Diagnostics.Append(d...)
	state.ValueWoVersion = prior.ValueWoVersion // not echoed by the API
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *secretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state secretModel
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

	body := console.UpdateSecretRequestBody{
		Description: stringPtr(plan.Description),
	}
	if tags != nil {
		body.Tags = &tags
	}

	// Rotate the value only when value_wo_version changed.
	if !plan.ValueWoVersion.Equal(state.ValueWoVersion) {
		var valueCfg types.String
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("value"), &valueCfg)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if valueCfg.IsNull() {
			resp.Diagnostics.AddError("Missing value", "`value` is required when `value_wo_version` changes (rotation).")
			return
		}
		val, err := client.BuildSecretValue(plan.SecretType.ValueString(), valueCfg.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Invalid secret value", err.Error())
			return
		}
		body.Value = val
	}

	if err := r.p.API.UpdateSecret(ctx, r.p.OrgID, state.ID.ValueString(), body); err != nil {
		resp.Diagnostics.AddError("Failed to update secret", err.Error())
		return
	}

	meta, err := r.p.API.GetSecret(ctx, r.p.OrgID, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read secret after update", err.Error())
		return
	}
	newState, d := r.toModel(ctx, meta)
	resp.Diagnostics.Append(d...)
	newState.ValueWoVersion = plan.ValueWoVersion
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *secretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state secretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.p.API.DeleteSecret(ctx, r.p.OrgID, id); err != nil {
		resp.Diagnostics.AddError("Failed to delete secret", err.Error())
		return
	}
	if err := pollGoneOn404(ctx, secretWaitTimeout, func() error {
		_, err := r.p.API.GetSecret(ctx, r.p.OrgID, id)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Secret still present after delete", err.Error())
	}
}

func (r *secretResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// toModel maps metadata into state. value is always null (write-only); the
// caller sets value_wo_version (the API does not echo it).
func (r *secretResource) toModel(ctx context.Context, meta *console.SecretMetadataResponse) (secretModel, diag.Diagnostics) {
	tags, d := stringSliceToList(ctx, meta.Tags)
	return secretModel{
		ID:          types.StringValue(meta.Id.String()),
		Name:        types.StringValue(meta.Name),
		SecretType:  types.StringValue(string(meta.SecretType)),
		Value:       types.StringNull(),
		Description: optString(meta.Description),
		Tags:        tags,
		SecretPath:  types.StringValue(meta.SecretPath),
	}, d
}
