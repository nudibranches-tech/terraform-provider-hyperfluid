// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

var (
	_ resource.Resource                = &nextflowResource{}
	_ resource.ResourceWithConfigure   = &nextflowResource{}
	_ resource.ResourceWithImportState = &nextflowResource{}
)

const nextflowWaitTimeout = 15 * time.Minute

var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func NewNextflowResource() resource.Resource {
	return &nextflowResource{}
}

type nextflowResource struct {
	p        *providerData
	interval time.Duration
}

type nextflowModel struct {
	ID           types.String `tfsdk:"id"`
	Env          types.String `tfsdk:"env"`
	Name         types.String `tfsdk:"name"`
	Description  types.String `tfsdk:"description"`
	Tags         types.List   `tfsdk:"tags"`
	RuntimeImage types.String `tfsdk:"runtime_image"`
	Config       types.String `tfsdk:"config"`
	Buckets      types.Set    `tfsdk:"buckets"`
	Egress       types.Object `tfsdk:"egress"`
	Head         types.Object `tfsdk:"head"`
	TaskQuota    types.Object `tfsdk:"task_quota"`
	Storage      types.Object `tfsdk:"storage"`
	Scm          types.Object `tfsdk:"scm"`

	Slug                 types.String `tfsdk:"slug"`
	Phase                types.String `tfsdk:"phase"`
	TaskNamespace        types.String `tfsdk:"task_namespace"`
	ResolvedRuntimeImage types.String `tfsdk:"resolved_runtime_image"`
	WorkVolumeCapacity   types.String `tfsdk:"work_volume_capacity"`
}

type nextflowEgressModel struct {
	Fqdns      types.Set `tfsdk:"fqdns"`
	Allowlists types.Set `tfsdk:"allowlists"`
}

type nextflowHeadModel struct {
	CPU    types.String `tfsdk:"cpu"`
	Memory types.String `tfsdk:"memory"`
}

type nextflowTaskQuotaModel struct {
	CPU     types.String `tfsdk:"cpu"`
	Memory  types.String `tfsdk:"memory"`
	MaxPods types.Int64  `tfsdk:"max_pods"`
}

type nextflowStorageModel struct {
	Size             types.String `tfsdk:"size"`
	StorageClassName types.String `tfsdk:"storage_class_name"`
}

type nextflowScmModel struct {
	Platform    types.String `tfsdk:"platform"`
	TokenSecret types.String `tfsdk:"token_secret"`
	Server      types.String `tfsdk:"server"`
	User        types.String `tfsdk:"user"`
}

var (
	nextflowEgressType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"fqdns": types.SetType{ElemType: types.StringType}, "allowlists": types.SetType{ElemType: types.StringType},
	}}
	nextflowHeadType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"cpu": types.StringType, "memory": types.StringType,
	}}
	nextflowTaskQuotaType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"cpu": types.StringType, "memory": types.StringType, "max_pods": types.Int64Type,
	}}
	nextflowStorageType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"size": types.StringType, "storage_class_name": types.StringType,
	}}
	nextflowScmType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"platform": types.StringType, "token_secret": types.StringType, "server": types.StringType, "user": types.StringType,
	}}
)

func (r *nextflowResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_nextflow"
}

// atLeastOneSibling makes an empty nested object an error: the platform
// stores it as absent, so it would read back null.
func atLeastOneSibling(names ...string) []path.Expression {
	out := make([]path.Expression, 0, len(names))
	for _, n := range names {
		out = append(out, path.MatchRelative().AtParent().AtName(n))
	}
	return out
}

func (r *nextflowResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	computedStr := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	nonEmptySet := func(desc string, v ...validator.Set) schema.SetAttribute {
		return schema.SetAttribute{
			ElementType: types.StringType, Optional: true, MarkdownDescription: desc,
			Validators: append([]validator.Set{setvalidator.SizeAtLeast(1)}, v...),
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "The Nextflow deployment of an environment; runs are launched with `hfctl`. Needs the organization's `nextflow_enabled` feature.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"env": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Environment id. Changing this forces a new deployment.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Name: lowercase letters, digits and hyphens. Changing this forces a new deployment.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.RegexMatches(slugPattern, "must be lowercase letters, digits and hyphens")},
			},
			"description": schema.StringAttribute{
				Optional: true, MarkdownDescription: "Free-form description.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 500)},
			},
			"tags": schema.ListAttribute{
				ElementType: types.StringType, Optional: true, MarkdownDescription: "User-defined tags.",
				Validators: []validator.List{listvalidator.SizeAtLeast(1)},
			},
			"runtime_image": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Runtime image replacing the platform's.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"config": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Nextflow configuration applied to every run.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"buckets": nonEmptySet("Environment buckets the pipelines may read and write."),
			"egress": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Egress granted on top of the platform baseline.",
				Attributes: map[string]schema.Attribute{
					"fqdns": nonEmptySet("Public hostnames reachable on port 443, in lowercase.",
						setvalidator.AtLeastOneOf(atLeastOneSibling("allowlists")...),
						setvalidator.ValueStringsAre(stringvalidator.RegexMatches(regexp.MustCompile(`^[^A-Z]+$`), "must be lowercase"))),
					"allowlists": nonEmptySet("Names of the environment's egress allow-lists to attach."),
				},
			},
			"head": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Head pod sizing; an omitted value uses the platform default.",
				Attributes: map[string]schema.Attribute{
					"cpu": schema.StringAttribute{
						Optional: true, MarkdownDescription: "CPU, as a Kubernetes quantity.",
						Validators: []validator.String{stringvalidator.AtLeastOneOf(atLeastOneSibling("memory")...)},
					},
					"memory": schema.StringAttribute{Optional: true, MarkdownDescription: "Memory, as a Kubernetes quantity."},
				},
			},
			"task_quota": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Ceiling on the tasks of all runs; an omitted value uses the platform default.",
				Attributes: map[string]schema.Attribute{
					"cpu": schema.StringAttribute{
						Optional: true, MarkdownDescription: "Total CPU, as a Kubernetes quantity.",
						Validators: []validator.String{stringvalidator.AtLeastOneOf(atLeastOneSibling("memory", "max_pods")...)},
					},
					"memory": schema.StringAttribute{Optional: true, MarkdownDescription: "Total memory, as a Kubernetes quantity."},
					"max_pods": schema.Int64Attribute{
						Optional: true, MarkdownDescription: "Maximum number of task pods.",
						Validators: []validator.Int64{int64validator.AtLeast(1)},
					},
				},
			},
			"storage": schema.SingleNestedAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Shared work volume; omitted values use the platform default, and removing the block keeps the volume.",
				PlanModifiers:       []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
				Attributes: map[string]schema.Attribute{
					"size": schema.StringAttribute{
						Optional: true, MarkdownDescription: "Size, as a Kubernetes quantity; it can grow but not shrink.",
						Validators: []validator.String{stringvalidator.AtLeastOneOf(atLeastOneSibling("storage_class_name")...)},
					},
					"storage_class_name": schema.StringAttribute{
						Optional: true, MarkdownDescription: "`ReadWriteMany` storage class. Changing this forces a new deployment.",
						PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
					},
				},
			},
			"scm": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Private Git provider pipelines are pulled from.",
				Attributes: map[string]schema.Attribute{
					"platform": schema.StringAttribute{
						Required: true, MarkdownDescription: "`github`, `gitlab`, `gitea` or `bitbucket`.",
						Validators: []validator.String{stringvalidator.OneOf("github", "gitlab", "gitea", "bitbucket")},
					},
					"token_secret": schema.StringAttribute{Required: true, MarkdownDescription: "Name of the secret holding the access token."},
					"server":       schema.StringAttribute{Optional: true, MarkdownDescription: "`https://` URL of a self-hosted server."},
					"user":         schema.StringAttribute{Optional: true, MarkdownDescription: "User the token belongs to."},
				},
			},

			"slug":                   computedStr("Derived slug."),
			"phase":                  computedStr("Lifecycle phase: `Pending`, `Provisioning`, `Ready` or `Error`."),
			"task_namespace":         computedStr("Namespace the tasks run in."),
			"resolved_runtime_image": computedStr("Runtime image in use."),
			"work_volume_capacity":   computedStr("Bound capacity of the work volume."),
		},
	}
}

func (r *nextflowResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *nextflowResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan nextflowModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var diags diag.Diagnostics
	body := console.CreateNextflowCrdRequestBody{
		Name:         plan.Name.ValueString(),
		Description:  stringPtr(plan.Description),
		RuntimeImage: stringPtr(plan.RuntimeImage),
		Config:       stringPtr(plan.Config),
		Tags:         strListPtr(ctx, plan.Tags, &diags),
		Buckets:      strSetPtr(ctx, plan.Buckets, &diags),
		Egress:       nextflowEgressRequest(ctx, plan.Egress, &diags),
		Head:         nextflowHeadRequest(ctx, plan.Head, &diags),
		TaskQuota:    nextflowTaskQuotaRequest(ctx, plan.TaskQuota, &diags),
		Storage:      nextflowStorageRequest(ctx, plan.Storage, &diags),
		Scm:          nextflowScmRequest(ctx, plan.Scm, &diags),
	}
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.p.API.CreateNextflow(ctx, r.p.OrgID, plan.Env.ValueString(), body)
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to create Nextflow", featureNextflow, err)
		return
	}
	id := created.Id.String()
	// Saved before the wait, so a deployment that never becomes ready is
	// still tracked and destroyed by the next apply.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if err := r.waitReady(ctx, id); err != nil {
		resp.Diagnostics.AddError("Nextflow did not become ready", err.Error())
		return
	}
	state, err := r.readInto(ctx, id)
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to read Nextflow after create", featureNextflow, err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *nextflowResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior nextflowModel
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
		addGatedError(&resp.Diagnostics, "Failed to read Nextflow", featureNextflow, err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *nextflowResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state nextflowModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, nulls, diags := nextflowPatch(ctx, plan, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.p.API.PatchNextflow(ctx, r.p.OrgID, id, body, nulls...); err != nil {
		addGatedError(&resp.Diagnostics, "Failed to update Nextflow", featureNextflow, err)
		return
	}
	if err := r.waitReady(ctx, id); err != nil {
		resp.Diagnostics.AddError("Nextflow did not become ready after update", err.Error())
		return
	}
	newState, err := r.readInto(ctx, id)
	if err != nil {
		addGatedError(&resp.Diagnostics, "Failed to read Nextflow after update", featureNextflow, err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *nextflowResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state nextflowModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.p.API.DeleteNextflow(ctx, r.p.OrgID, id); err != nil {
		addGatedError(&resp.Diagnostics, "Failed to delete Nextflow", featureNextflow, err)
		return
	}
	if err := pollGoneOn404Every(ctx, nextflowWaitTimeout, orDefaultInterval(r.interval), func() error {
		_, err := r.p.API.GetNextflow(ctx, r.p.OrgID, id)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Nextflow still present after delete", err.Error())
	}
}

func (r *nextflowResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// waitReady waits for the operator to observe the current spec and settle;
// before it observes it, a previous `Ready` says nothing about this one.
func (r *nextflowResource) waitReady(ctx context.Context, id string) error {
	_, err := waitForReadyEvery(ctx, nextflowWaitTimeout, orDefaultInterval(r.interval), func() (*console.NextflowCrdResponse, bool, error) {
		crd, err := r.p.API.GetNextflowCrd(ctx, r.p.OrgID, id)
		if err != nil {
			return nil, false, err
		}
		if !crd.Status.SpecObserved {
			return crd, false, nil
		}
		if crd.Status.Phase == "Error" {
			msg := "the deployment entered the Error phase"
			if crd.Status.Message != nil && *crd.Status.Message != "" {
				msg += ": " + *crd.Status.Message
			}
			return nil, false, errors.New(msg)
		}
		return crd, crd.Status.Phase == "Ready", nil
	})
	return err
}

func (r *nextflowResource) readInto(ctx context.Context, id string) (nextflowModel, error) {
	nf, err := r.p.API.GetNextflow(ctx, r.p.OrgID, id)
	if err != nil {
		return nextflowModel{}, err
	}
	crd, err := r.p.API.GetNextflowCrd(ctx, r.p.OrgID, id)
	if err != nil {
		return nextflowModel{}, err
	}
	var diags diag.Diagnostics
	m := nextflowModel{
		ID:                   types.StringValue(id),
		Env:                  types.StringValue(nf.HarborId.String()),
		Name:                 types.StringValue(nf.Name),
		Description:          nullIfEmptyString(nf.Description),
		Tags:                 nullIfEmptyList(ctx, nf.Tags, &diags),
		RuntimeImage:         optNonEmpty(crd.RuntimeImage),
		Config:               nullIfEmptyString(crd.Config),
		Buckets:              nullIfEmptySet(ctx, crd.Buckets, &diags),
		Egress:               nextflowEgressObject(ctx, crd.Egress, &diags),
		Head:                 nextflowHeadObject(ctx, crd.Head, &diags),
		TaskQuota:            nextflowTaskQuotaObject(ctx, crd.TaskQuota, &diags),
		Storage:              nextflowStorageObject(ctx, crd.Storage, &diags),
		Scm:                  nextflowScmObject(ctx, crd.Scm, &diags),
		Slug:                 types.StringValue(nf.Slug),
		Phase:                types.StringValue(crd.Status.Phase),
		TaskNamespace:        optString(crd.Status.TaskNamespace),
		ResolvedRuntimeImage: optString(firstSetString(crd.Status.RuntimeImage, nf.ResolvedRuntimeImage)),
		WorkVolumeCapacity:   optString(crd.Status.WorkVolumeCapacity),
	}
	if diags.HasError() {
		return nextflowModel{}, fmt.Errorf("mapping Nextflow %s: %v", id, diags.Errors())
	}
	return m, nil
}

// nextflowPatch builds the PATCH from what changed, and names the fields to
// send as null. A removed block or value is cleared the way the API takes it:
// an empty object, list or string, and null for scm. Storage cannot be cleared.
func nextflowPatch(ctx context.Context, plan, state nextflowModel) (console.PatchNextflowCrdRequestBody, []string, diag.Diagnostics) {
	var (
		diags diag.Diagnostics
		body  console.PatchNextflowCrdRequestBody
		nulls []string
	)
	changed := func(p, s attr.Value) bool { return !p.IsUnknown() && !p.Equal(s) }
	empty := ""
	if changed(plan.Description, state.Description) {
		body.Description = orElse(stringPtr(plan.Description), &empty)
	}
	if changed(plan.Tags, state.Tags) {
		body.Tags = orElse(strListPtr(ctx, plan.Tags, &diags), &[]string{})
	}
	if changed(plan.RuntimeImage, state.RuntimeImage) {
		body.RuntimeImage = orElse(stringPtr(plan.RuntimeImage), &empty)
	}
	if changed(plan.Config, state.Config) {
		body.Config = orElse(stringPtr(plan.Config), &empty)
	}
	if changed(plan.Buckets, state.Buckets) {
		body.Buckets = orElse(strSetPtr(ctx, plan.Buckets, &diags), &[]string{})
	}
	if changed(plan.Egress, state.Egress) {
		body.Egress = orElse(nextflowEgressRequest(ctx, plan.Egress, &diags), &console.NextflowEgressRequest{})
	}
	if changed(plan.Head, state.Head) {
		body.Head = orElse(nextflowHeadRequest(ctx, plan.Head, &diags), &console.NextflowHeadRequest{})
	}
	if changed(plan.TaskQuota, state.TaskQuota) {
		body.TaskQuota = orElse(nextflowTaskQuotaRequest(ctx, plan.TaskQuota, &diags), &console.NextflowTaskQuotaRequest{})
	}
	if changed(plan.Storage, state.Storage) && !plan.Storage.IsNull() {
		body.Storage = nextflowStorageRequest(ctx, plan.Storage, &diags)
	}
	if changed(plan.Scm, state.Scm) {
		body.Scm = nextflowScmRequest(ctx, plan.Scm, &diags)
		if body.Scm == nil {
			nulls = append(nulls, "scm")
		}
	}
	return body, nulls, diags
}

// ── request builders (nil for a null or unknown value) ─────────────────────

func objectAsModel[T any](ctx context.Context, obj types.Object, diags *diag.Diagnostics) (T, bool) {
	var m T
	if obj.IsNull() || obj.IsUnknown() {
		return m, false
	}
	d := obj.As(ctx, &m, basetypes.ObjectAsOptions{})
	diags.Append(d...)
	return m, !d.HasError()
}

func nextflowEgressRequest(ctx context.Context, obj types.Object, diags *diag.Diagnostics) *console.NextflowEgressRequest {
	m, ok := objectAsModel[nextflowEgressModel](ctx, obj, diags)
	if !ok {
		return nil
	}
	return &console.NextflowEgressRequest{Fqdns: strSetPtr(ctx, m.Fqdns, diags), Allowlists: strSetPtr(ctx, m.Allowlists, diags)}
}

func nextflowHeadRequest(ctx context.Context, obj types.Object, diags *diag.Diagnostics) *console.NextflowHeadRequest {
	m, ok := objectAsModel[nextflowHeadModel](ctx, obj, diags)
	if !ok {
		return nil
	}
	return &console.NextflowHeadRequest{Cpu: stringPtr(m.CPU), Memory: stringPtr(m.Memory)}
}

func nextflowTaskQuotaRequest(ctx context.Context, obj types.Object, diags *diag.Diagnostics) *console.NextflowTaskQuotaRequest {
	m, ok := objectAsModel[nextflowTaskQuotaModel](ctx, obj, diags)
	if !ok {
		return nil
	}
	return &console.NextflowTaskQuotaRequest{Cpu: stringPtr(m.CPU), Memory: stringPtr(m.Memory), MaxPods: int32PtrFromInt64(m.MaxPods)}
}

func nextflowStorageRequest(ctx context.Context, obj types.Object, diags *diag.Diagnostics) *console.NextflowStorageRequest {
	m, ok := objectAsModel[nextflowStorageModel](ctx, obj, diags)
	if !ok {
		return nil
	}
	return &console.NextflowStorageRequest{Size: stringPtr(m.Size), StorageClassName: stringPtr(m.StorageClassName)}
}

func nextflowScmRequest(ctx context.Context, obj types.Object, diags *diag.Diagnostics) *console.NextflowScmRequest {
	m, ok := objectAsModel[nextflowScmModel](ctx, obj, diags)
	if !ok {
		return nil
	}
	return &console.NextflowScmRequest{
		Platform:    console.NextflowScmPlatform(m.Platform.ValueString()),
		TokenSecret: m.TokenSecret.ValueString(),
		Server:      stringPtr(m.Server),
		User:        stringPtr(m.User),
	}
}

// ── read mappers (an absent or empty block reads back null) ────────────────

func nextflowEgressObject(ctx context.Context, e *console.NextflowEgressRequest, diags *diag.Diagnostics) types.Object {
	if e == nil {
		return types.ObjectNull(nextflowEgressType.AttrTypes)
	}
	m := nextflowEgressModel{Fqdns: nullIfEmptySet(ctx, derefStrings(e.Fqdns), diags), Allowlists: nullIfEmptySet(ctx, derefStrings(e.Allowlists), diags)}
	if m.Fqdns.IsNull() && m.Allowlists.IsNull() {
		return types.ObjectNull(nextflowEgressType.AttrTypes)
	}
	return objectFromModel(ctx, nextflowEgressType, m, diags)
}

func nextflowHeadObject(ctx context.Context, h *console.NextflowHeadRequest, diags *diag.Diagnostics) types.Object {
	if h == nil || (h.Cpu == nil && h.Memory == nil) {
		return types.ObjectNull(nextflowHeadType.AttrTypes)
	}
	return objectFromModel(ctx, nextflowHeadType, nextflowHeadModel{CPU: optString(h.Cpu), Memory: optString(h.Memory)}, diags)
}

func nextflowTaskQuotaObject(ctx context.Context, q *console.NextflowTaskQuotaRequest, diags *diag.Diagnostics) types.Object {
	if q == nil || (q.Cpu == nil && q.Memory == nil && q.MaxPods == nil) {
		return types.ObjectNull(nextflowTaskQuotaType.AttrTypes)
	}
	return objectFromModel(ctx, nextflowTaskQuotaType, nextflowTaskQuotaModel{
		CPU: optString(q.Cpu), Memory: optString(q.Memory), MaxPods: optInt64FromInt32(q.MaxPods),
	}, diags)
}

func nextflowStorageObject(ctx context.Context, s *console.NextflowStorageRequest, diags *diag.Diagnostics) types.Object {
	if s == nil || (s.Size == nil && s.StorageClassName == nil) {
		return types.ObjectNull(nextflowStorageType.AttrTypes)
	}
	return objectFromModel(ctx, nextflowStorageType, nextflowStorageModel{
		Size: optString(s.Size), StorageClassName: optString(s.StorageClassName),
	}, diags)
}

func nextflowScmObject(ctx context.Context, s *console.NextflowScmRequest, diags *diag.Diagnostics) types.Object {
	if s == nil {
		return types.ObjectNull(nextflowScmType.AttrTypes)
	}
	return objectFromModel(ctx, nextflowScmType, nextflowScmModel{
		Platform: types.StringValue(string(s.Platform)), TokenSecret: types.StringValue(s.TokenSecret),
		Server: optString(s.Server), User: optString(s.User),
	}, diags)
}

// ── small conversions shared by the Nextflow, Compute Dock and agent files ──

func objectFromModel(ctx context.Context, t types.ObjectType, v any, diags *diag.Diagnostics) types.Object {
	o, d := types.ObjectValueFrom(ctx, t.AttrTypes, v)
	diags.Append(d...)
	return o
}

func strListPtr(ctx context.Context, l types.List, diags *diag.Diagnostics) *[]string {
	s, d := listToStringSlice(ctx, l)
	diags.Append(d...)
	if s == nil {
		return nil
	}
	return &s
}

func strSetPtr(ctx context.Context, set types.Set, diags *diag.Diagnostics) *[]string {
	s, d := setToStringSlice(ctx, set)
	diags.Append(d...)
	if s == nil {
		return nil
	}
	return &s
}

func nullIfEmptyList(ctx context.Context, s []string, diags *diag.Diagnostics) types.List {
	if len(s) == 0 {
		return types.ListNull(types.StringType)
	}
	l, d := stringSliceToList(ctx, s)
	diags.Append(d...)
	return l
}

func nullIfEmptySet(ctx context.Context, s []string, diags *diag.Diagnostics) types.Set {
	if len(s) == 0 {
		return types.SetNull(types.StringType)
	}
	set, d := stringSliceToSet(ctx, s)
	diags.Append(d...)
	return set
}

func nullIfEmptyString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

func orElse[T any](v, empty *T) *T {
	if v == nil {
		return empty
	}
	return v
}

func derefStrings(s *[]string) []string {
	if s == nil {
		return nil
	}
	return *s
}

func firstSetString(vals ...*string) *string {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}
