// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// ai_api_key_resource.go — an LLM-gateway API key. The secret `key` is returned
// only once (by create) and is stored — sensitive — in state, since it cannot be
// re-fetched (the list/read endpoint exposes only key_prefix). There is no PATCH:
// name, scopes and expires_in_days are all immutable, so any change replaces the
// key (a fresh secret). On import the secret is unrecoverable and stays null.

var (
	_ resource.Resource                = &aiApiKeyResource{}
	_ resource.ResourceWithConfigure   = &aiApiKeyResource{}
	_ resource.ResourceWithImportState = &aiApiKeyResource{}
)

const aiApiKeyWaitTimeout = 2 * time.Minute

func NewAiApiKeyResource() resource.Resource {
	return &aiApiKeyResource{}
}

type aiApiKeyResource struct {
	p *providerData
}

type aiApiKeyModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`            // ForceNew
	Scopes        types.Set    `tfsdk:"scopes"`          // ForceNew, opt+computed (order-insensitive)
	ExpiresInDays types.Int64  `tfsdk:"expires_in_days"` // ForceNew, not echoed by the API
	Key           types.String `tfsdk:"key"`             // write-once secret, kept sensitive

	// computed
	KeyPrefix  types.String `tfsdk:"key_prefix"`
	CreatedAt  types.String `tfsdk:"created_at"`
	ExpiresAt  types.String `tfsdk:"expires_at"`
	LastUsedAt types.String `tfsdk:"last_used_at"`
}

func (r *aiApiKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_api_key"
}

func (r *aiApiKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An LLM-gateway API key. The secret `key` is shown only at creation and stored " +
			"(sensitive) in state — it cannot be re-read. The key is immutable: changing any attribute " +
			"replaces it with a new secret.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "API key id (uuid).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Key name. Changing this forces a new key.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"scopes": schema.SetAttribute{
				ElementType: types.StringType,
				Optional:    true, Computed: true,
				MarkdownDescription: "Scopes granted to the key, e.g. `model:*` (all models) or `model:<model_id>`. " +
					"Changing this forces a new key. Modelled as a set, so ordering never causes a diff.",
				PlanModifiers: []planmodifier.Set{
					// Hold the resolved scopes (the API may default them) so an omitted
					// set keeps its stored value instead of re-planning every apply.
					setplanmodifier.UseStateForUnknown(),
					setplanmodifier.RequiresReplace(),
				},
			},
			"expires_in_days": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Lifetime in days. Omit for a non-expiring key. Changing this forces a new key. Not echoed by the API, so it cannot be recovered on import.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"key": schema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				MarkdownDescription: "The secret API key. Returned only at creation and stored in state; null after import.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},

			"key_prefix":   schema.StringAttribute{Computed: true, MarkdownDescription: "Non-secret key prefix (identifies the key in listings)."},
			"created_at":   schema.StringAttribute{Computed: true, MarkdownDescription: "Creation timestamp (RFC3339)."},
			"expires_at":   schema.StringAttribute{Computed: true, MarkdownDescription: "Expiry timestamp (RFC3339), if the key expires."},
			"last_used_at": schema.StringAttribute{Computed: true, MarkdownDescription: "Last-used timestamp (RFC3339), if ever used."},
		},
	}
}

func (r *aiApiKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *aiApiKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan aiApiKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	scopes, d := setToStringSlice(ctx, plan.Scopes)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := console.CreateApiKeyRequest{
		Name:          plan.Name.ValueString(),
		ExpiresInDays: int32PtrFromInt64(plan.ExpiresInDays),
	}
	if scopes != nil {
		body.Scopes = &scopes
	}

	created, err := r.p.API.CreateApiKey(ctx, r.p.OrgID, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create API key", err.Error())
		return
	}

	scopeSet, d := stringSliceToSet(ctx, created.Scopes)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	state := aiApiKeyModel{
		ID:            types.StringValue(created.Id.String()),
		Name:          types.StringValue(created.Name),
		Scopes:        scopeSet,
		ExpiresInDays: plan.ExpiresInDays, // not echoed by the API
		Key:           types.StringValue(created.Key),
		KeyPrefix:     types.StringValue(created.KeyPrefix),
		CreatedAt:     timeString(created.CreatedAt),
		ExpiresAt:     optTimeString(created.ExpiresAt),
		LastUsedAt:    types.StringNull(),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *aiApiKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior aiApiKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}

	k, err := r.p.API.GetApiKey(ctx, r.p.OrgID, prior.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read API key", err.Error())
		return
	}

	state, d := r.toModel(ctx, k)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Carry through the values the API never returns.
	state.Key = prior.Key                     // write-once secret
	state.ExpiresInDays = prior.ExpiresInDays // only expires_at is echoed
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Update is unreachable — every attribute forces replacement — but re-reads to
// keep state consistent if it is ever called.
func (r *aiApiKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior aiApiKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}

	k, err := r.p.API.GetApiKey(ctx, r.p.OrgID, prior.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read API key after update", err.Error())
		return
	}
	state, d := r.toModel(ctx, k)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Key = prior.Key
	state.ExpiresInDays = plan.ExpiresInDays
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *aiApiKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state aiApiKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.p.API.RevokeApiKey(ctx, r.p.OrgID, id); err != nil && !errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Failed to revoke API key", err.Error())
		return
	}
	if err := pollGoneOn404(ctx, aiApiKeyWaitTimeout, func() error {
		_, err := r.p.API.GetApiKey(ctx, r.p.OrgID, id)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("API key still present after revoke", err.Error())
	}
}

func (r *aiApiKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// toModel maps the (secret-free) list/read view; the caller restores key and
// expires_in_days, which the API never echoes.
func (r *aiApiKeyResource) toModel(ctx context.Context, k *console.ApiKeyResponse) (aiApiKeyModel, diag.Diagnostics) {
	scopes, d := stringSliceToSet(ctx, k.Scopes)
	return aiApiKeyModel{
		ID:         types.StringValue(k.Id.String()),
		Name:       types.StringValue(k.Name),
		Scopes:     scopes,
		KeyPrefix:  types.StringValue(k.KeyPrefix),
		CreatedAt:  timeString(k.CreatedAt),
		ExpiresAt:  optTimeString(k.ExpiresAt),
		LastUsedAt: optTimeString(k.LastUsedAt),
	}, d
}
