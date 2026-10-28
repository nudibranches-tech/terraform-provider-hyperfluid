// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

var (
	_ resource.Resource                = &pipelineResource{}
	_ resource.ResourceWithConfigure   = &pipelineResource{}
	_ resource.ResourceWithImportState = &pipelineResource{}
)

const pipelineWaitTimeout = 5 * time.Minute

// pipelineKinds are the `type` tags of the create request, one per kind.
var pipelineKinds = []string{
	"trino", "file_sorter", "file_router", "copy", "s3_copy", "page_splitter",
	"context_enricher", "g_drive", "share_point", "inbound_email", "open_data",
}

func NewPipelineResource() resource.Resource {
	return &pipelineResource{}
}

type pipelineResource struct {
	p *providerData
}

type pipelineModel struct {
	ID                 types.String `tfsdk:"id"`
	Env                types.String `tfsdk:"env"`
	Name               types.String `tfsdk:"name"`
	Type               types.String `tfsdk:"type"`
	Spec               types.String `tfsdk:"spec"`
	Schedule           types.String `tfsdk:"schedule"`
	MaxDurationMinutes types.Int64  `tfsdk:"max_duration_minutes"`
	Suspended          types.Bool   `tfsdk:"suspended"`
	PipelineType       types.String `tfsdk:"pipeline_type"`
	CreatedAt          types.String `tfsdk:"created_at"`
}

func (r *pipelineResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pipeline"
}

func (r *pipelineResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A data pipeline, declared as the body `hfctl pipelines create --spec` takes. Runs are actions and stay out of Terraform.\n\n" +
			"~> Only `suspended` and `max_duration_minutes` change in place: any other change deletes the pipeline and its run history.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"env": schema.StringAttribute{
				Required: true, MarkdownDescription: "Environment id the pipeline runs in. Changing this forces a new pipeline.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required: true, MarkdownDescription: "Pipeline name. Changing this forces a new pipeline.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"type": schema.StringAttribute{
				Required: true, MarkdownDescription: "Kind of pipeline, the request's `type`: `trino`, `s3_copy`, `g_drive`… Not recovered by import.",
				PlanModifiers: []planmodifier.String{requiresReplaceIfKnown()},
				Validators:    []validator.String{stringvalidator.OneOf(pipelineKinds...)},
			},
			"spec": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The kind's request body as JSON (`jsonencode`), without `type` and `pipeline`; " +
					"`harbor_bucket` references default to `env`. Not recovered by import.",
				PlanModifiers: []planmodifier.String{requiresReplaceIfKnown()},
				Validators:    []validator.String{pipelineSpecValidator{}},
			},
			"schedule": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "5-field cron, or `one_off` for a single run. Defaults to the kind's own cadence. Changing this forces a new pipeline.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
			},
			"max_duration_minutes": schema.Int64Attribute{
				Optional: true, MarkdownDescription: "Budget of one run in minutes, 1 to 1440. Left out, it follows the schedule. Not recovered by import.",
				Validators: []validator.Int64{int64validator.Between(1, 1440)},
			},
			"suspended": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Whether scheduled runs are paused. Left out, it is not managed.",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"pipeline_type": schema.StringAttribute{Computed: true, MarkdownDescription: "What the pipeline runs, such as `markitdown`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"created_at": schema.StringAttribute{Computed: true, MarkdownDescription: "Creation time, RFC 3339.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		},
	}
}

func (r *pipelineResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *pipelineResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan pipelineModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	slug, err := r.p.API.GetOrganizationSlug(ctx, r.p.OrgID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read the organization", err.Error())
		return
	}
	body, err := pipelineBody(plan, r.p.OrgID, slug)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("spec"), "Invalid spec", err.Error())
		return
	}
	created, err := r.p.API.CreatePipeline(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create pipeline", err.Error())
		return
	}
	id := created.Id.String()
	state, err := r.readInto(ctx, id, plan)
	if err != nil {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
		resp.Diagnostics.AddError("Failed to read pipeline after create", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *pipelineResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior pipelineModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.readInto(ctx, prior.ID.ValueString(), prior)
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read pipeline", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *pipelineResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state pipelineModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	suspended := state.Suspended
	if !plan.Suspended.IsUnknown() {
		suspended = plan.Suspended
	}
	// Both fields state the value to end up with, so both are always sent.
	if !suspended.Equal(state.Suspended) || !plan.MaxDurationMinutes.Equal(state.MaxDurationMinutes) {
		err := r.p.API.UpdatePipeline(ctx, r.p.OrgID, id, console.UpdatePipelineRequest{
			Suspend:            suspended.ValueBool(),
			MaxDurationMinutes: int32PtrFromInt64(plan.MaxDurationMinutes),
		})
		if err != nil {
			resp.Diagnostics.AddError("Failed to update pipeline", err.Error())
			return
		}
	}
	newState, err := r.readInto(ctx, id, plan)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read pipeline after update", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *pipelineResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state pipelineModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.p.API.DeletePipeline(ctx, r.p.OrgID, id); err != nil {
		resp.Diagnostics.AddError("Failed to delete pipeline", err.Error())
		return
	}
	if err := pollGoneOn404(ctx, pipelineWaitTimeout, func() error {
		_, err := r.p.API.GetPipeline(ctx, r.p.OrgID, id)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Pipeline still present after delete", err.Error())
	}
}

func (r *pipelineResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// readInto maps the pipeline onto the model, keeping from keep what no read
// reports: the kind, its spec and the run budget.
func (r *pipelineResource) readInto(ctx context.Context, id string, keep pipelineModel) (pipelineModel, error) {
	p, err := r.p.API.GetPipeline(ctx, r.p.OrgID, id)
	if err != nil {
		return pipelineModel{}, err
	}
	return pipelineModel{
		ID:                 types.StringValue(id),
		Env:                types.StringValue(p.HarborId.String()),
		Name:               types.StringValue(p.Name),
		Type:               keep.Type,
		Spec:               keep.Spec,
		Schedule:           types.StringValue(p.Schedule),
		MaxDurationMinutes: keep.MaxDurationMinutes,
		Suspended:          types.BoolValue(p.Suspended),
		PipelineType:       types.StringValue(p.Type),
		CreatedAt:          timeString(p.CreatedAt),
	}, nil
}

// pipelineBody assembles the create request: the spec, its `type`, and the
// `pipeline` block the provider owns. Optional settings left out stay absent.
func pipelineBody(m pipelineModel, orgID, orgSlug string) (map[string]any, error) {
	var body map[string]any
	if err := json.Unmarshal([]byte(m.Spec.ValueString()), &body); err != nil {
		return nil, fmt.Errorf("spec is not a JSON object: %w", err)
	}
	harbor := m.Env.ValueString()
	meta := map[string]any{
		"name":              m.Name.ValueString(),
		"organization_id":   orgID,
		"organization_slug": orgSlug,
		"harbor_id":         harbor,
	}
	if s := stringPtr(m.Schedule); s != nil {
		meta["schedule"] = *s
	}
	if n := int32PtrFromInt64(m.MaxDurationMinutes); n != nil {
		meta["max_duration_minutes"] = *n
	}
	if b := boolPtr(m.Suspended); b != nil {
		meta["enabled"] = !*b
	}
	body["type"] = m.Type.ValueString()
	body["pipeline"] = meta
	injectHarborIntoBucketRefs(body, harbor)
	return body, nil
}

// injectHarborIntoBucketRefs gives every `harbor_bucket` reference without a
// `harbor_id` the pipeline's environment, as hfctl does for a spec file.
func injectHarborIntoBucketRefs(v any, harbor string) {
	switch t := v.(type) {
	case map[string]any:
		if t["type"] == "harbor_bucket" {
			if _, ok := t["harbor_id"]; !ok {
				t["harbor_id"] = harbor
			}
		}
		for _, child := range t {
			injectHarborIntoBucketRefs(child, harbor)
		}
	case []any:
		for _, child := range t {
			injectHarborIntoBucketRefs(child, harbor)
		}
	}
}

// pipelineSpecValidator requires a JSON object that leaves `type` and
// `pipeline` to their own attributes.
type pipelineSpecValidator struct{}

func (pipelineSpecValidator) Description(_ context.Context) string {
	return "must be a JSON object without `type` or `pipeline`"
}

func (v pipelineSpecValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (pipelineSpecValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(req.ConfigValue.ValueString()), &body); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid spec", "spec must be a JSON object: "+err.Error())
		return
	}
	for _, key := range []string{"type", "pipeline"} {
		if _, ok := body[key]; ok {
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid spec",
				fmt.Sprintf("spec must not set %q: the resource sets it from its own attributes.", key))
		}
	}
}
