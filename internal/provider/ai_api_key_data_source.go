// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
)

// hyperfluid_ai_api_key data source — look up an existing API key's metadata by
// name. The secret is never returned by the API, so this surfaces only the
// non-secret fields (use the resource if you need the key material).

var (
	_ datasource.DataSource              = &aiApiKeyDataSource{}
	_ datasource.DataSourceWithConfigure = &aiApiKeyDataSource{}
)

func NewAiApiKeyDataSource() datasource.DataSource {
	return &aiApiKeyDataSource{}
}

type aiApiKeyDataSource struct {
	p *providerData
}

func (d *aiApiKeyDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_api_key"
}

func (d *aiApiKeyDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	cs := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	ci := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an API key's metadata by name; the secret key is never returned.",
		Attributes: map[string]schema.Attribute{
			"name":                  schema.StringAttribute{Required: true, MarkdownDescription: "API key name. Must match exactly one key."},
			"id":                    cs("API key id (uuid)."),
			"key_prefix":            cs("Non-secret key prefix."),
			"scopes":                schema.SetAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Scopes granted to the key."},
			"created_at":            cs("Creation timestamp (RFC3339)."),
			"expires_at":            cs("Expiry timestamp (RFC3339), if the key expires."),
			"last_used_at":          cs("Last-used timestamp (RFC3339), if ever used."),
			"max_llm_rpm":           ci("Most LLM requests per minute for this key, or null."),
			"max_llm_tpm":           ci("Most LLM tokens per minute for this key, or null."),
			"platform_max_llm_rpm":  ci("Requests-per-minute cap set by platform administrators, or null."),
			"platform_max_llm_tpm":  ci("Tokens-per-minute cap set by platform administrators, or null."),
			"effective_max_llm_rpm": ci("Requests-per-minute ceiling enforced, or null."),
			"effective_max_llm_tpm": ci("Tokens-per-minute ceiling enforced, or null."),
		},
	}
}

func (d *aiApiKeyDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "expected *providerData")
		return
	}
	d.p = pd
}

// aiApiKeyDataModel mirrors the non-secret fields (no key, no expires_in_days).
type aiApiKeyDataModel struct {
	Name       types.String `tfsdk:"name"`
	ID         types.String `tfsdk:"id"`
	KeyPrefix  types.String `tfsdk:"key_prefix"`
	Scopes     types.Set    `tfsdk:"scopes"`
	CreatedAt  types.String `tfsdk:"created_at"`
	ExpiresAt  types.String `tfsdk:"expires_at"`
	LastUsedAt types.String `tfsdk:"last_used_at"`

	MaxLlmRpm          types.Int64 `tfsdk:"max_llm_rpm"`
	MaxLlmTpm          types.Int64 `tfsdk:"max_llm_tpm"`
	PlatformMaxLlmRpm  types.Int64 `tfsdk:"platform_max_llm_rpm"`
	PlatformMaxLlmTpm  types.Int64 `tfsdk:"platform_max_llm_tpm"`
	EffectiveMaxLlmRpm types.Int64 `tfsdk:"effective_max_llm_rpm"`
	EffectiveMaxLlmTpm types.Int64 `tfsdk:"effective_max_llm_tpm"`
}

func (d *aiApiKeyDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg aiApiKeyDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := cfg.Name.ValueString()
	k, err := d.p.API.FindApiKeyByName(ctx, d.p.OrgID, name)
	if errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("API key not found", "no API key named "+name)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up API key", err.Error())
		return
	}

	scopes, dg := stringSliceToSet(ctx, k.Scopes)
	resp.Diagnostics.Append(dg...)
	if resp.Diagnostics.HasError() {
		return
	}
	state := aiApiKeyDataModel{
		Name:       types.StringValue(k.Name),
		ID:         types.StringValue(k.Id.String()),
		KeyPrefix:  types.StringValue(k.KeyPrefix),
		Scopes:     scopes,
		CreatedAt:  timeString(k.CreatedAt),
		ExpiresAt:  optTimeString(k.ExpiresAt),
		LastUsedAt: optTimeString(k.LastUsedAt),

		MaxLlmRpm:          optInt64FromInt32(k.MaxLlmRpm),
		MaxLlmTpm:          optInt64(k.MaxLlmTpm),
		PlatformMaxLlmRpm:  optInt64FromInt32(k.PlatformMaxLlmRpm),
		PlatformMaxLlmTpm:  optInt64(k.PlatformMaxLlmTpm),
		EffectiveMaxLlmRpm: optInt64FromInt32(k.EffectiveMaxLlmRpm),
		EffectiveMaxLlmTpm: optInt64(k.EffectiveMaxLlmTpm),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
