// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
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
	_ resource.Resource                = &openDataPipelineResource{}
	_ resource.ResourceWithConfigure   = &openDataPipelineResource{}
	_ resource.ResourceWithImportState = &openDataPipelineResource{}
	_ resource.ResourceWithModifyPlan  = &openDataPipelineResource{}
)

// openDataPipelineNameRe is the name the platform keeps: it lowercases the name
// into a Kubernetes object name, so anything else would read back as drift.
var openDataPipelineNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

func NewOpenDataPipelineResource() resource.Resource {
	return &openDataPipelineResource{}
}

type openDataPipelineResource struct {
	p *providerData
}

type openDataPipelineModel struct {
	ID                 types.String `tfsdk:"id"`
	Env                types.String `tfsdk:"env"`
	Name               types.String `tfsdk:"name"`
	ConnectorSlug      types.String `tfsdk:"connector_slug"`
	Params             types.String `tfsdk:"params"`
	DataContainerID    types.String `tfsdk:"data_container_id"`
	SchemaName         types.String `tfsdk:"schema_name"`
	TableName          types.String `tfsdk:"table_name"`
	Schedule           types.String `tfsdk:"schedule"`
	MaxDurationMinutes types.Int64  `tfsdk:"max_duration_minutes"`
	Suspended          types.Bool   `tfsdk:"suspended"`
	PipelineType       types.String `tfsdk:"pipeline_type"`
	CreatedAt          types.String `tfsdk:"created_at"`
}

func (r *openDataPipelineResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_open_data_pipeline"
}

func (r *openDataPipelineResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	notRecovered := " Not recovered by import: the first apply after an import records it from the configuration."
	resp.Schema = schema.Schema{
		MarkdownDescription: "An open data pipeline: an opendata connector (the console's Open Data page) that loads its " +
			"source into an Iceberg table of a data container, full refresh on every run. Once created it is an " +
			"ordinary pipeline, the one `hfctl pipelines` lists. Runs are actions and stay out of Terraform.\n\n" +
			"~> Only `suspended` and `max_duration_minutes` change in place: any other change deletes the pipeline and its run history.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"env": schema.StringAttribute{
				Required: true, MarkdownDescription: "Environment id the pipeline runs in. Changing this forces a new pipeline.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{isUUID()},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Pipeline name: lowercase letters, digits, `-` and `.`, starting and ending " +
					"with a letter or digit. Changing this forces a new pipeline.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{stringvalidator.RegexMatches(openDataPipelineNameRe,
					"must be lowercase letters, digits, `-` and `.`, starting and ending with a letter or digit")},
			},
			"connector_slug": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Connector to run, such as `cat_opidor` or `world_bank`: a slug of the catalogue " +
					"`GET /api/v1/opendata/connectors` serves, checked at plan time." + notRecovered,
				PlanModifiers: []planmodifier.String{requiresReplaceIfKnown()},
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"params": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The connector's parameters as a JSON object (`jsonencode`), in the shape of its " +
					"`params_schema` in the catalogue; `jsonencode({})` for a connector without any. Compared as JSON, " +
					"so key order and whitespace never plan a change." + notRecovered,
				PlanModifiers: []planmodifier.String{jsonObjectReplaceIfChanged()},
				Validators:    []validator.String{jsonObjectValidator{}},
			},
			"data_container_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Id of the destination Iceberg data container; the table is created in its " +
					"SQL engine catalog." + notRecovered,
				PlanModifiers: []planmodifier.String{requiresReplaceIfKnown()},
				Validators:    []validator.String{isUUID()},
			},
			"schema_name": schema.StringAttribute{
				Required: true, MarkdownDescription: "Schema of the destination table." + notRecovered,
				PlanModifiers: []planmodifier.String{requiresReplaceIfKnown()},
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"table_name": schema.StringAttribute{
				Required: true, MarkdownDescription: "Destination table, replaced in full on every run." + notRecovered,
				PlanModifiers: []planmodifier.String{requiresReplaceIfKnown()},
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"schedule": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "5-field cron, or `one_off` for a single run. Left out, the pipeline runs once " +
					"(`one_off`). Changing this forces a new pipeline.",
				PlanModifiers: []planmodifier.String{
					// UseStateForUnknown first: an omitted schedule is unknown in any plan that
					// changes something else, and RequiresReplace would replace on that.
					stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace(),
				},
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
			"pipeline_type": schema.StringAttribute{Computed: true, MarkdownDescription: "The pipeline's type as the platform reports it.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"created_at": schema.StringAttribute{Computed: true, MarkdownDescription: "Creation time, RFC 3339.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		},
	}
}

func (r *openDataPipelineResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *openDataPipelineResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan openDataPipelineModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	slug, err := r.p.API.GetOrganizationSlug(ctx, r.p.OrgID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read the organization", err.Error())
		return
	}
	body, err := openDataPipelineBody(plan, r.p.OrgID, slug)
	if err != nil {
		resp.Diagnostics.AddError("Invalid open data pipeline", err.Error())
		return
	}
	created, err := r.p.API.CreateConnectorIngestPipeline(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create open data pipeline", err.Error())
		return
	}
	id := created.Id.String()
	state, err := r.readInto(ctx, id, plan)
	if err != nil {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
		resp.Diagnostics.AddError("Failed to read open data pipeline after create", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// ModifyPlan checks the connector slug against the catalogue when a pipeline
// is about to be created with it. Nothing else does before the first run: the
// ingestion endpoint copies the slug into the pipeline unchecked, and only the
// run fails on it. The catalogue is public, so failing to read it is refused
// rather than skipped.
func (r *openDataPipelineResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.p == nil || req.Plan.Raw.IsNull() {
		return
	}
	var slug types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("connector_slug"), &slug)...)
	if resp.Diagnostics.HasError() || slug.IsNull() || slug.IsUnknown() {
		return
	}
	if !req.State.Raw.IsNull() {
		var prior types.String
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("connector_slug"), &prior)...)
		// The same connector, or one adopted onto an existing pipeline: no
		// pipeline is created with it.
		if resp.Diagnostics.HasError() || prior.IsNull() || prior.Equal(slug) {
			return
		}
	}
	connectors, err := r.p.API.ListOpenDataConnectors(ctx)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("connector_slug"), "Failed to read the connector catalogue",
			"The connector could not be checked against GET /api/v1/opendata/connectors: "+err.Error())
		return
	}
	if err := unknownConnector(slug.ValueString(), connectors); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("connector_slug"), "Unknown connector", err.Error())
	}
}

// unknownConnector refuses a slug the connector catalogue does not list,
// naming the ones it does.
func unknownConnector(slug string, connectors []console.ConnectorDescriptorResponse) error {
	slugs := make([]string, 0, len(connectors))
	for _, c := range connectors {
		if c.Slug == slug {
			return nil
		}
		slugs = append(slugs, c.Slug)
	}
	sort.Strings(slugs)
	available := strings.Join(slugs, ", ")
	if available == "" {
		available = "none"
	}
	return fmt.Errorf("the platform has no opendata connector %q. Available connectors: %s", slug, available)
}

func (r *openDataPipelineResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior openDataPipelineModel
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
		resp.Diagnostics.AddError("Failed to read open data pipeline", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *openDataPipelineResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state openDataPipelineModel
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
			resp.Diagnostics.AddError("Failed to update open data pipeline", err.Error())
			return
		}
	}
	newState, err := r.readInto(ctx, id, plan)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read open data pipeline after update", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *openDataPipelineResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state openDataPipelineModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.p.API.DeletePipeline(ctx, r.p.OrgID, id); err != nil {
		resp.Diagnostics.AddError("Failed to delete open data pipeline", err.Error())
		return
	}
	if err := pollGoneOn404(ctx, pipelineWaitTimeout, func() error {
		_, err := r.p.API.GetPipeline(ctx, r.p.OrgID, id)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Open data pipeline still present after delete", err.Error())
	}
}

// ImportState takes the pipeline id, as hyperfluid_pipeline does, so a pipeline
// created in the console imports here.
func (r *openDataPipelineResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// readInto maps the pipeline onto the model, keeping from keep what no read
// reports: the connector, its params, the destination and the run budget.
func (r *openDataPipelineResource) readInto(ctx context.Context, id string, keep openDataPipelineModel) (openDataPipelineModel, error) {
	p, err := r.p.API.GetPipeline(ctx, r.p.OrgID, id)
	if err != nil {
		return openDataPipelineModel{}, err
	}
	return openDataPipelineModel{
		ID:                 types.StringValue(id),
		Env:                types.StringValue(p.HarborId.String()),
		Name:               types.StringValue(p.Name),
		ConnectorSlug:      keep.ConnectorSlug,
		Params:             keep.Params,
		DataContainerID:    keep.DataContainerID,
		SchemaName:         keep.SchemaName,
		TableName:          keep.TableName,
		Schedule:           types.StringValue(p.Schedule),
		MaxDurationMinutes: keep.MaxDurationMinutes,
		Suspended:          types.BoolValue(p.Suspended),
		PipelineType:       types.StringValue(p.Type),
		CreatedAt:          timeString(p.CreatedAt),
	}, nil
}

// openDataPipelineBody assembles the create request. Optional settings left
// out stay absent, so the platform resolves them.
func openDataPipelineBody(m openDataPipelineModel, orgID, orgSlug string) (console.CreateConnectorIngestRequest, error) {
	var out console.CreateConnectorIngestRequest
	params, err := decodeJSONObject(m.Params.ValueString())
	if err != nil {
		return out, fmt.Errorf("params: %w", err)
	}
	org, err := uuid.Parse(orgID)
	if err != nil {
		return out, fmt.Errorf("organization_id: %w", err)
	}
	harbor, err := uuid.Parse(m.Env.ValueString())
	if err != nil {
		return out, fmt.Errorf("env: %w", err)
	}
	container, err := uuid.Parse(m.DataContainerID.ValueString())
	if err != nil {
		return out, fmt.Errorf("data_container_id: %w", err)
	}
	meta := console.PipelineMetadata{
		Name:               m.Name.ValueString(),
		OrganizationId:     org,
		OrganizationSlug:   orgSlug,
		HarborId:           harbor,
		Schedule:           stringPtr(m.Schedule),
		MaxDurationMinutes: int32PtrFromInt64(m.MaxDurationMinutes),
	}
	if b := boolPtr(m.Suspended); b != nil {
		enabled := !*b
		meta.Enabled = &enabled
	}
	return console.CreateConnectorIngestRequest{
		Pipeline:        meta,
		ConnectorSlug:   m.ConnectorSlug.ValueString(),
		Params:          params,
		DataContainerId: container,
		SchemaName:      m.SchemaName.ValueString(),
		TableName:       m.TableName.ValueString(),
	}, nil
}

// decodeJSONObject parses a JSON object, keeping numbers as written so a large
// integer is sent back exactly.
func decodeJSONObject(s string) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.UseNumber()
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}
	if v == nil {
		return nil, errors.New("not a JSON object: null")
	}
	if dec.More() {
		return nil, errors.New("not a JSON object: trailing data after the object")
	}
	return v, nil
}

// jsonEqual reports whether two JSON documents hold the same value, whatever
// their key order and whitespace. Numbers compare by their written form.
func jsonEqual(a, b string) bool {
	da, err := decodeJSONObject(a)
	if err != nil {
		return false
	}
	db, err := decodeJSONObject(b)
	if err != nil {
		return false
	}
	return reflect.DeepEqual(da, db)
}

// jsonObjectReplaceIfChanged keeps the recorded value when the configuration
// only reformats it, and otherwise replaces the resource, unless nothing was
// recorded (after an import), in which case the configuration is adopted.
func jsonObjectReplaceIfChanged() planmodifier.String {
	return jsonObjectReplaceIfChangedModifier{}
}

type jsonObjectReplaceIfChangedModifier struct{}

func (jsonObjectReplaceIfChangedModifier) Description(_ context.Context) string {
	return "Compares the JSON values, not the text; a change of value, or a value only known at apply time, forces a new resource, unless the value was never recorded (after an import)."
}

func (m jsonObjectReplaceIfChangedModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (jsonObjectReplaceIfChangedModifier) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// Creating, destroying, or adopting after an import.
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || req.PlanValue.IsNull() || req.StateValue.IsNull() {
		return
	}
	// A value known only at apply time may differ from the recorded one, and
	// no update can carry params: planning it in place would let the state
	// drift from the platform for good.
	if req.PlanValue.IsUnknown() {
		resp.RequiresReplace = true
		return
	}
	if jsonEqual(req.StateValue.ValueString(), req.PlanValue.ValueString()) {
		resp.PlanValue = req.StateValue
		return
	}
	resp.RequiresReplace = true
}

// jsonObjectValidator requires a JSON object.
type jsonObjectValidator struct{}

func (jsonObjectValidator) Description(_ context.Context) string {
	return "must be a JSON object"
}

func (v jsonObjectValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (jsonObjectValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := decodeJSONObject(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid JSON", "must be a JSON object (use jsonencode): "+err.Error())
	}
}
