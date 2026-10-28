// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/objectvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// key_value_cache_resource.go — managed Valkey key-value cache. Connection is
// surfaced via host/port + a credentials secret reference (never plaintext).
// `resources` is read from the /crd view, the only one that carries it.

var (
	_ resource.Resource                = &keyValueCacheResource{}
	_ resource.ResourceWithConfigure   = &keyValueCacheResource{}
	_ resource.ResourceWithImportState = &keyValueCacheResource{}
)

const keyValueCacheWaitTimeout = 5 * time.Minute

func NewKeyValueCacheResource() resource.Resource {
	return &keyValueCacheResource{}
}

type keyValueCacheResource struct {
	p *providerData
}

type keyValueCacheModel struct {
	ID              types.String `tfsdk:"id"`
	Env             types.String `tfsdk:"env"`
	Name            types.String `tfsdk:"name"`
	Image           types.String `tfsdk:"image"`
	Maxmemory       types.String `tfsdk:"maxmemory"`
	MaxmemoryPolicy types.String `tfsdk:"maxmemory_policy"`
	Description     types.String `tfsdk:"description"`
	Tags            types.List   `tfsdk:"tags"`
	Resources       types.Object `tfsdk:"resources"`

	// computed
	Host                  types.String `tfsdk:"host"`
	Port                  types.Int64  `tfsdk:"port"`
	CredentialsSecretName types.String `tfsdk:"credentials_secret_name"`
	Phase                 types.String `tfsdk:"phase"`
	Slug                  types.String `tfsdk:"slug"`
}

func (r *keyValueCacheResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_key_value_cache"
}

func (r *keyValueCacheResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	computedStr := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A managed Valkey key-value cache.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"env": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Environment id the cache runs in. Changing this forces a new cache.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Cache name (RFC 1035 label). Changing this forces a new cache.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"image": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Valkey container image. Defaults to the platform's pinned image.",
			},
			"maxmemory": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Valkey `maxmemory`, e.g. `256mb`. Defaults to three quarters of `resources.limits.memory` when that is set.",
			},
			"maxmemory_policy": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Valkey eviction policy, e.g. `allkeys-lru`.",
			},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Free-form description."},
			"tags": schema.ListAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				MarkdownDescription: "User-defined tags.",
			},
			"resources": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "CPU and memory requests and limits of the cache, as Kubernetes quantities. " +
					"Left out, none are set; a CPU limit is best avoided, as it throttles the cache.",
				Attributes: map[string]schema.Attribute{
					"requests": resourceQuantities("Resources guaranteed to the container.",
						objectvalidator.AtLeastOneOf(path.MatchRelative().AtParent().AtName("limits"))),
					"limits": resourceQuantities("The most the container may use."),
				},
			},

			"host":                    computedStr("Cache host, once provisioned."),
			"port":                    schema.Int64Attribute{Computed: true, MarkdownDescription: "Cache port, once provisioned."},
			"credentials_secret_name": computedStr("Name of the secret holding the connection credentials."),
			"phase":                   computedStr("Current lifecycle phase."),
			"slug":                    computedStr("Derived slug."),
		},
	}
}

func resourceQuantities(desc string, extra ...validator.Object) schema.SingleNestedAttribute {
	quantity := func(other, d string) schema.StringAttribute {
		return schema.StringAttribute{
			Optional: true, MarkdownDescription: d,
			Validators: []validator.String{
				stringvalidator.LengthAtLeast(1),
				stringvalidator.AtLeastOneOf(path.MatchRelative().AtParent().AtName(other)),
			},
		}
	}
	return schema.SingleNestedAttribute{
		Optional:            true,
		MarkdownDescription: desc,
		Validators:          extra,
		Attributes: map[string]schema.Attribute{
			"cpu":    quantity("memory", "CPU quantity, e.g. `250m` or `1`."),
			"memory": quantity("cpu", "Memory quantity, e.g. `256Mi` or `1Gi`."),
		},
	}
}

var (
	resourceQuantitiesType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"cpu": types.StringType, "memory": types.StringType,
	}}
	cacheResourcesType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"requests": resourceQuantitiesType, "limits": resourceQuantitiesType,
	}}
)

type cacheResourcesModel struct {
	Requests types.Object `tfsdk:"requests"`
	Limits   types.Object `tfsdk:"limits"`
}

type resourceQuantitiesModel struct {
	CPU    types.String `tfsdk:"cpu"`
	Memory types.String `tfsdk:"memory"`
}

// cacheResourcesInput converts a configured `resources` into the API input,
// nil when the config leaves it out.
func cacheResourcesInput(ctx context.Context, obj types.Object) (*console.ResourceRequirementsSpec, diag.Diagnostics) {
	if obj.IsNull() || obj.IsUnknown() {
		return nil, nil
	}
	var m cacheResourcesModel
	d := obj.As(ctx, &m, basetypes.ObjectAsOptions{})
	if d.HasError() {
		return nil, d
	}
	spec := &console.ResourceRequirementsSpec{}
	for _, side := range []struct {
		obj types.Object
		out **console.ResourceSpec
	}{{m.Requests, &spec.Requests}, {m.Limits, &spec.Limits}} {
		if side.obj.IsNull() || side.obj.IsUnknown() {
			continue
		}
		var q resourceQuantitiesModel
		d.Append(side.obj.As(ctx, &q, basetypes.ObjectAsOptions{})...)
		*side.out = &console.ResourceSpec{Cpu: stringPtr(q.CPU), Memory: stringPtr(q.Memory)}
	}
	return spec, d
}

// cacheResourcesObject maps the spec's resources, null when it sets none.
func cacheResourcesObject(r *console.ResourceRequirementsSpec) types.Object {
	side := func(q *console.ResourceSpec) types.Object {
		if q == nil || (q.Cpu == nil && q.Memory == nil) {
			return types.ObjectNull(resourceQuantitiesType.AttrTypes)
		}
		return types.ObjectValueMust(resourceQuantitiesType.AttrTypes, map[string]attr.Value{
			"cpu": optString(q.Cpu), "memory": optString(q.Memory),
		})
	}
	if r == nil {
		return types.ObjectNull(cacheResourcesType.AttrTypes)
	}
	requests, limits := side(r.Requests), side(r.Limits)
	if requests.IsNull() && limits.IsNull() {
		return types.ObjectNull(cacheResourcesType.AttrTypes)
	}
	return types.ObjectValueMust(cacheResourcesType.AttrTypes, map[string]attr.Value{"requests": requests, "limits": limits})
}

func (r *keyValueCacheResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *keyValueCacheResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan keyValueCacheModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tags, d := listToStringSlice(ctx, plan.Tags)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := console.CreateHfKeyValueCacheCrdRequestBody{
		Name:            plan.Name.ValueString(),
		Image:           stringPtr(plan.Image),
		Maxmemory:       stringPtr(plan.Maxmemory),
		MaxmemoryPolicy: stringPtr(plan.MaxmemoryPolicy),
		Description:     stringPtr(plan.Description),
	}
	if tags != nil {
		body.Tags = &tags
	}
	body.Resources, d = cacheResourcesInput(ctx, plan.Resources)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.p.API.CreateKeyValueCache(ctx, r.p.OrgID, plan.Env.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create key-value cache", err.Error())
		return
	}
	id := created.Id.String()

	if err := r.waitReady(ctx, id); err != nil {
		resp.Diagnostics.AddError("Cache did not become ready", err.Error())
		return
	}
	state, err := r.readInto(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read cache after create", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *keyValueCacheResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior keyValueCacheModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.readInto(ctx, prior.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read key-value cache", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *keyValueCacheResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state keyValueCacheModel
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

	body := console.PatchHfKeyValueCacheCrdRequestBody{
		Image:           stringPtr(plan.Image),
		Maxmemory:       stringPtr(plan.Maxmemory),
		MaxmemoryPolicy: stringPtr(plan.MaxmemoryPolicy),
		Description:     stringPtr(plan.Description),
	}
	if tags != nil {
		body.Tags = &tags
	}
	// An omitted `resources` keeps the cache's, so a removed one is sent empty,
	// which clears it.
	if !plan.Resources.Equal(state.Resources) {
		body.Resources, d = cacheResourcesInput(ctx, plan.Resources)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		if body.Resources == nil {
			body.Resources = &console.ResourceRequirementsSpec{}
		}
	}

	id := state.ID.ValueString()
	if err := r.p.API.PatchKeyValueCache(ctx, r.p.OrgID, id, body); err != nil {
		resp.Diagnostics.AddError("Failed to update key-value cache", err.Error())
		return
	}
	newState, err := r.readInto(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read cache after update", err.Error())
		return
	}
	// In-place fields reflect the plan (the operator converges): a maxmemory /
	// image change is eventually consistent, so the read can still report the
	// old value right after PATCH. Computed fields come from the read.
	if !plan.Image.IsUnknown() {
		newState.Image = plan.Image
	}
	if !plan.Maxmemory.IsUnknown() {
		newState.Maxmemory = plan.Maxmemory
	}
	if !plan.MaxmemoryPolicy.IsUnknown() {
		newState.MaxmemoryPolicy = plan.MaxmemoryPolicy
	}
	if !plan.Tags.IsUnknown() {
		newState.Tags = plan.Tags
	}
	newState.Description = plan.Description
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *keyValueCacheResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state keyValueCacheModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.p.API.DeleteKeyValueCache(ctx, r.p.OrgID, id); err != nil {
		resp.Diagnostics.AddError("Failed to delete key-value cache", err.Error())
		return
	}
	if err := pollGoneOn404(ctx, keyValueCacheWaitTimeout, func() error {
		_, err := r.p.API.GetKeyValueCache(ctx, r.p.OrgID, id)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Cache still present after delete", err.Error())
	}
}

func (r *keyValueCacheResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *keyValueCacheResource) waitReady(ctx context.Context, id string) error {
	_, err := waitForReady(ctx, keyValueCacheWaitTimeout, func() (*console.HfKeyValueCacheResponse, bool, error) {
		c, err := r.p.API.GetKeyValueCache(ctx, r.p.OrgID, id)
		if err != nil {
			return nil, false, err
		}
		// connection info populated == provisioned and reachable.
		ready := c.Host != nil && c.Port != nil
		return c, ready, nil
	})
	return err
}

func (r *keyValueCacheResource) readInto(ctx context.Context, id string) (keyValueCacheModel, error) {
	c, err := r.p.API.GetKeyValueCache(ctx, r.p.OrgID, id)
	if err != nil {
		return keyValueCacheModel{}, err
	}
	tags, d := stringSliceToList(ctx, c.Tags)
	if d.HasError() {
		return keyValueCacheModel{}, errors.New("failed to convert tags")
	}
	spec, err := r.p.API.GetKeyValueCacheSpec(ctx, r.p.OrgID, id)
	if err != nil {
		return keyValueCacheModel{}, err
	}

	return keyValueCacheModel{
		ID:                    types.StringValue(id),
		Env:                   types.StringValue(c.HarborId.String()),
		Name:                  types.StringValue(c.Name),
		Image:                 types.StringValue(c.Image),
		Maxmemory:             optString(c.Maxmemory),
		MaxmemoryPolicy:       optString(c.MaxmemoryPolicy),
		Description:           optString(c.Description),
		Tags:                  tags,
		Resources:             cacheResourcesObject(spec.Resources),
		Host:                  optString(c.Host),
		Port:                  optInt64FromInt32(c.Port),
		CredentialsSecretName: optString(c.CredentialsSecretName),
		Phase:                 optString(c.Phase),
		Slug:                  types.StringValue(c.Slug),
	}, nil
}
