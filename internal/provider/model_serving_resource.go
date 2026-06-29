// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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

// model_serving_resource.go — a Sapience model serving (OpenAI-compatible vLLM/TGI
// inference endpoint). Org-scoped, addressed by the CRD resource `name` (derived
// server-side from display_name). The serving is almost entirely immutable: only
// `replicas` is patchable in place; every other field forces a replacement.
//
// M2 read-mapping: the create DTO is flat (`gpu`, `memory`, `max_model_len`) but
// the read DTO nests them (`resources.gpu`, `resources.memory`, `config.maxModelLen`)
// and reports `total_replicas` rather than `replicas`. readInto folds them back.
//
// `replicas` is not accepted at create (the API defaults it), so it is modelled
// Optional+Computed and applied with a follow-up PATCH when configured.

var (
	_ resource.Resource                = &modelServingResource{}
	_ resource.ResourceWithConfigure   = &modelServingResource{}
	_ resource.ResourceWithImportState = &modelServingResource{}
)

const modelServingWaitTimeout = 15 * time.Minute

func NewModelServingResource() resource.Resource {
	return &modelServingResource{}
}

type modelServingResource struct {
	p *providerData
}

type modelServingModel struct {
	ID          types.String `tfsdk:"id"`            // = name
	Name        types.String `tfsdk:"name"`          // computed CRD name
	DisplayName types.String `tfsdk:"display_name"`  // ForceNew
	ModelID     types.String `tfsdk:"model_id"`      // ForceNew
	ModelType   types.String `tfsdk:"model_type"`    // ForceNew
	Runtime     types.String `tfsdk:"runtime"`       // ForceNew
	Gpu         types.Int64  `tfsdk:"gpu"`           // ForceNew, opt+computed
	Memory      types.String `tfsdk:"memory"`        // ForceNew, opt+computed
	MaxModelLen types.Int64  `tfsdk:"max_model_len"` // ForceNew, opt+computed
	Replicas    types.Int64  `tfsdk:"replicas"`      // in-place, opt+computed

	// computed
	Cpu           types.String `tfsdk:"cpu"`
	Endpoint      types.String `tfsdk:"endpoint"`
	Phase         types.String `tfsdk:"phase"`
	ReadyReplicas types.Int64  `tfsdk:"ready_replicas"`
}

func (r *modelServingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_model_serving"
}

func (r *modelServingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	// forceNewInt: optional+computed integer fixed at creation (omitted → server
	// default, held in state; an explicit change forces a new serving).
	forceNewInt := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{
			Optional: true, Computed: true, MarkdownDescription: desc,
			PlanModifiers: []planmodifier.Int64{
				int64planmodifier.UseStateForUnknown(),
				int64planmodifier.RequiresReplaceIfConfigured(),
			},
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Sapience model serving — an OpenAI-compatible inference endpoint (vLLM/TGI). " +
			"Almost every attribute is immutable: only `replicas` updates in place; changing the model, " +
			"runtime, or resources forces a new serving.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Resource identifier (the server-assigned serving name).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Server-assigned CRD resource name (derived from display_name). Used as the import id.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"display_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Human-readable display name. Changing this forces a new serving.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"model_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Model identifier, e.g. \"meta-llama/Llama-3.1-8B-Instruct\". Changing this forces a new serving.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"model_type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Model type: `generation` or `embedding`. Changing this forces a new serving.",
				Validators:          []validator.String{stringvalidator.OneOf("generation", "embedding")},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"runtime": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Serving runtime: `vllm` or `tgi`. Changing this forces a new serving.",
				Validators:          []validator.String{stringvalidator.OneOf("vllm", "tgi")},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"gpu":           forceNewInt("GPU count. Changing this forces a new serving."),
			"max_model_len": forceNewInt("Maximum model context length (generation models). Changing this forces a new serving."),
			"memory": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Memory request, e.g. \"8Gi\". Changing this forces a new serving.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"replicas": schema.Int64Attribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Desired replica count. The one attribute that updates in place (scaling).",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},

			"cpu":            schema.StringAttribute{Computed: true, MarkdownDescription: "CPU request resolved by the platform."},
			"endpoint":       schema.StringAttribute{Computed: true, MarkdownDescription: "OpenAI-compatible endpoint, once provisioned."},
			"phase":          schema.StringAttribute{Computed: true, MarkdownDescription: "Current lifecycle phase."},
			"ready_replicas": schema.Int64Attribute{Computed: true, MarkdownDescription: "Ready replica count reported by the platform."},
		},
	}
}

func (r *modelServingResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *modelServingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan modelServingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := console.CreateModelServingRequest{
		DisplayName: plan.DisplayName.ValueString(),
		ModelId:     plan.ModelID.ValueString(),
		ModelType:   console.ModelType(plan.ModelType.ValueString()),
		Runtime:     console.ModelRuntime(plan.Runtime.ValueString()),
		Gpu:         int32PtrFromInt64(plan.Gpu),
		Memory:      stringPtr(plan.Memory),
		MaxModelLen: int32PtrFromInt64(plan.MaxModelLen),
	}
	created, err := r.p.API.CreateModelServing(ctx, r.p.OrgID, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create model serving", err.Error())
		return
	}
	name := created.Name

	// replicas is not accepted at create — apply it with a PATCH when configured.
	if !plan.Replicas.IsNull() && !plan.Replicas.IsUnknown() {
		if err := r.p.API.PatchModelServing(ctx, r.p.OrgID, name, console.PatchModelServingRequest{
			Replicas: int32PtrFromInt64(plan.Replicas),
		}); err != nil {
			resp.Diagnostics.AddError("Failed to set model serving replicas", err.Error())
			return
		}
	}

	if _, err := r.waitReady(ctx, name); err != nil {
		resp.Diagnostics.AddError("Model serving did not become ready", err.Error())
		return
	}
	state, err := r.readInto(ctx, name)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read model serving after create", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *modelServingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior modelServingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.readInto(ctx, prior.Name.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read model serving", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Update only ever changes replicas — every other attribute forces replacement.
func (r *modelServingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state modelServingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := state.Name.ValueString()
	if err := r.p.API.PatchModelServing(ctx, r.p.OrgID, name, console.PatchModelServingRequest{
		Replicas: int32PtrFromInt64(plan.Replicas),
	}); err != nil {
		resp.Diagnostics.AddError("Failed to update model serving", err.Error())
		return
	}
	if _, err := r.waitReady(ctx, name); err != nil {
		resp.Diagnostics.AddError("Model serving did not become ready after update", err.Error())
		return
	}
	newState, err := r.readInto(ctx, name)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read model serving after update", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *modelServingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state modelServingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.Name.ValueString()
	if err := r.p.API.DeleteModelServing(ctx, r.p.OrgID, name); err != nil && !errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Failed to delete model serving", err.Error())
		return
	}
	if err := pollGoneOn404(ctx, modelServingWaitTimeout, func() error {
		_, err := r.p.API.GetModelServing(ctx, r.p.OrgID, name)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Model serving still present after delete", err.Error())
	}
}

func (r *modelServingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

func (r *modelServingResource) waitReady(ctx context.Context, name string) (*console.ModelServingResponse, error) {
	return waitForReady(ctx, modelServingWaitTimeout, func() (*console.ModelServingResponse, bool, error) {
		m, err := r.p.API.GetModelServing(ctx, r.p.OrgID, name)
		if err != nil {
			return nil, false, err
		}
		ready := m.Phase != nil && *m.Phase == console.ModelServingPhaseReady &&
			m.TotalReplicas > 0 && m.ReadyReplicas == m.TotalReplicas
		return m, ready, nil
	})
}

// readInto folds the flat create-time fields back out of the nested read DTO (M2):
// gpu/memory ← resources, max_model_len ← config, replicas ← total_replicas.
func (r *modelServingResource) readInto(ctx context.Context, name string) (modelServingModel, error) {
	m, err := r.p.API.GetModelServing(ctx, r.p.OrgID, name)
	if err != nil {
		return modelServingModel{}, err
	}
	phase := types.StringNull()
	if m.Phase != nil {
		phase = types.StringValue(string(*m.Phase))
	}
	return modelServingModel{
		ID:            types.StringValue(m.Name),
		Name:          types.StringValue(m.Name),
		DisplayName:   types.StringValue(m.DisplayName),
		ModelID:       types.StringValue(m.ModelId),
		ModelType:     types.StringValue(string(m.ModelType)),
		Runtime:       types.StringValue(string(m.Runtime)),
		Gpu:           optInt64FromInt32(m.Resources.Gpu),
		Memory:        optString(m.Resources.Memory),
		MaxModelLen:   optInt64FromInt32(m.Config.MaxModelLen),
		Replicas:      types.Int64Value(int64(m.TotalReplicas)),
		Cpu:           optString(m.Resources.Cpu),
		Endpoint:      optString(m.Endpoint),
		Phase:         phase,
		ReadyReplicas: types.Int64Value(int64(m.ReadyReplicas)),
	}, nil
}
