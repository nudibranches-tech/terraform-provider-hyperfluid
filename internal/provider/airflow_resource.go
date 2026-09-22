// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// airflow_resource.go — a managed Airflow environment (ADR-0019/0020/0021).
//
// Two API reads back one resource: the DB projection (`GetAirflow`) carries
// identity, tags and the resolved metadata cluster, while the live CRD
// (`GetAirflowCrd`) is the only source for sleep_mode, egress, the airflow.cfg
// overrides, the task-pod quota and every status field. Reading just one of
// them makes every plan after the first report drift on whatever the other
// owns, so `readInto` always does both.

var (
	_ resource.Resource                   = &airflowResource{}
	_ resource.ResourceWithConfigure      = &airflowResource{}
	_ resource.ResourceWithImportState    = &airflowResource{}
	_ resource.ResourceWithValidateConfig = &airflowResource{}
)

// airflowWaitTimeout is deliberately far above the container-app/cache ceiling:
// an environment provisions its own ManagedPostgreSQL and DAG bucket, runs the
// metadata-DB migration and only then starts four components.
const airflowWaitTimeout = 15 * time.Minute

// Phases the operator publishes on the CR status.
const (
	airflowPhaseRunning  = "Running"
	airflowPhaseSleeping = "Sleeping"
	airflowPhaseError    = "Error"
)

// airflowNodeTiers is the published tier catalogue. There is deliberately no
// `nano`: 512Mi cannot hold an Airflow 3 triggerer (it settles around 620Mi
// idle), so every environment created at that size crash-looped from birth and
// the tier was removed rather than documented as a trap.
var airflowNodeTiers = []string{"micro", "small", "medium", "large"}

// airflowTierResources maps each tier onto the cpu/memory the platform resolves
// it into. The API takes `node_tier` on write but never echoes it — only the
// resolved requests and limits come back — so this table is also read
// backwards, by airflowNodeTierFromResources, to recover the tier on a refresh
// or an import. Kept in step with the console's own catalogue; a tier whose
// numbers have moved simply stops being recognised and falls back to the value
// already in state, so a stale row degrades rather than lying.
var airflowTierResources = map[string]struct{ cpuRequest, cpuLimit, memory string }{
	"micro":  {"500m", "1000m", "1Gi"},
	"small":  {"1000m", "2000m", "2Gi"},
	"medium": {"2000m", "4000m", "4Gi"},
	"large":  {"4000m", "8000m", "8Gi"},
}

// airflowLinkKinds are the service kinds an environment's task pods may be
// granted egress to BY HAND — the kinds the platform can resolve into a pod
// selector and a port. Only `Pipeline` is absent, and for a reason of its own:
// nothing connects *to* a pipeline, so it can never be a link target.
//
// This has to stay the WHOLE set the API accepts as a target, not a curated
// subset of it. `egress.in_cluster` is sent with REPLACE semantics — one apply
// carries the complete set and anything left out is removed — so a kind
// missing here is not "a kind Terraform declines to offer", it is a kind that
// gets deleted from `spec.egress.inCluster` the next time anyone applies this
// resource, silently closing a hole the user declared through the console or
// `hfctl` (both of which accept every value of `AirflowLinkKind`).
//
// `Trino` in particular is hand-declarable on its own terms: it opens the
// coordinator's HTTPS port, which is the same single hole a managed
// `hyperfluid_airflow_connection` to that dock implies — one dock is one link
// — so declaring it by hand and connecting to it stay one entry rather than
// two. Governed SQL from DAG code still goes through Bifrost, which is part of
// the non-editable baseline and needs no declaration at all.
var airflowLinkKinds = []string{
	string(console.AirflowLinkKindManagedPostgreSQL),
	string(console.AirflowLinkKindContainerApp),
	string(console.AirflowLinkKindKafka),
	string(console.AirflowLinkKindHfKeyValueCache),
	string(console.AirflowLinkKindTrino),
}

// Ceilings the API enforces; mirrored here so an over-long list is a plan error
// rather than a 400 halfway through an apply.
const (
	airflowMaxEgressFqdns     = 100
	airflowMaxInClusterLinks  = 20
	airflowMaxEgressAllowlist = 10
	airflowMaxConfigEntries   = 100
	airflowMaxConfigValueLen  = 4096
	airflowMaxTaskQuotaPods   = 10000
	airflowMaxRuntimeImageLen = 512
)

// airflowFqdnPattern accepts exactly the FQDNs the platform does: lowercase,
// at least two labels, an optional leading `*.` that must still leave two
// labels behind it, and a final label starting with a letter (which is what
// rules out IPv4 literals). Matching the server's rule here matters beyond
// error quality: the API normalises what it stores (trim + lowercase), so an
// entry that needed normalising would come back different from the one that was
// written and Terraform would fail the apply as an inconsistent result.
var airflowFqdnPattern = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]([a-z0-9-]*[a-z0-9])?$`)

// airflowDNSLabelPattern is the DNS-1123 label the platform requires of an
// in-cluster target's slug and of an egress allow-list's name.
var airflowDNSLabelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// airflowConfigKeyPattern is the `section.key` shape of an airflow.cfg
// override. Uppercase is refused rather than folded, so the stored key is
// always the canonical spelling.
var airflowConfigKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

func NewAirflowResource() resource.Resource {
	return &airflowResource{}
}

type airflowResource struct {
	p *providerData
}

type airflowModel struct {
	ID               types.String `tfsdk:"id"`
	Env              types.String `tfsdk:"env"`
	Name             types.String `tfsdk:"name"`
	PostgresRef      types.String `tfsdk:"postgres_ref"`
	DagBucketRef     types.String `tfsdk:"dag_bucket_ref"`
	NodeTier         types.String `tfsdk:"node_tier"`
	TriggererEnabled types.Bool   `tfsdk:"triggerer_enabled"`
	SleepMode        types.Bool   `tfsdk:"sleep_mode"`
	RuntimeImage     types.String `tfsdk:"runtime_image"`
	TaskQuotaMaxPods types.Int64  `tfsdk:"task_quota_max_pods"`
	Description      types.String `tfsdk:"description"`
	Tags             types.List   `tfsdk:"tags"`

	// Framework container types, not Go maps and slices: `egress` is a nested
	// object whose absence is meaningful (it is what clears the grants), and
	// `config` may be assigned from another resource's computed output.
	Egress types.Object `tfsdk:"egress"`
	Config types.Map    `tfsdk:"config"`

	// computed
	Slug                  types.String `tfsdk:"slug"`
	Phase                 types.String `tfsdk:"phase"`
	WebURL                types.String `tfsdk:"web_url"`
	PublicHost            types.String `tfsdk:"public_host"`
	NetworkMode           types.String `tfsdk:"network_mode"`
	TaskNamespace         types.String `tfsdk:"task_namespace"`
	DagBucket             types.String `tfsdk:"dag_bucket"`
	ServiceAccount        types.String `tfsdk:"service_account"`
	ManagedPostgresqlName types.String `tfsdk:"managed_postgresql_name"`
	UnresolvedAllowlists  types.List   `tfsdk:"unresolved_allowlists"`
	CPURequest            types.String `tfsdk:"cpu_request"`
	CPULimit              types.String `tfsdk:"cpu_limit"`
	MemoryRequest         types.String `tfsdk:"memory_request"`
	MemoryLimit           types.String `tfsdk:"memory_limit"`
}

type airflowEgressModel struct {
	Fqdns      types.Set `tfsdk:"fqdns"`
	InCluster  types.Set `tfsdk:"in_cluster"`
	Allowlists types.Set `tfsdk:"allowlists"`
}

type airflowInClusterLinkModel struct {
	Kind types.String `tfsdk:"kind"`
	Name types.String `tfsdk:"name"`
}

func airflowInClusterLinkType() attr.Type {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"kind": types.StringType,
		"name": types.StringType,
	}}
}

func airflowEgressAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"fqdns":      types.SetType{ElemType: types.StringType},
		"in_cluster": types.SetType{ElemType: airflowInClusterLinkType()},
		"allowlists": types.SetType{ElemType: types.StringType},
	}
}

func (r *airflowResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_airflow"
}

func (r *airflowResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	computedStr := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A managed Apache Airflow 3 environment: an api-server, a scheduler, a " +
			"dag-processor and (unless disabled) a triggerer, with a metadata PostgreSQL cluster and a " +
			"DAG bucket of their own, and a dedicated task namespace the KubernetesExecutor runs task " +
			"pods in.\n\n" +
			"DAGs are delivered through the bucket: upload them under the bucket's `dags/` prefix and " +
			"the platform mirrors them into the dag-processor. Nothing outside that prefix is picked " +
			"up.\n\n" +
			"~> **`postgres_ref` and `dag_bucket_ref` are create-only.** Both are references the " +
			"platform resolves once, at creation; leave either out and the environment provisions its " +
			"own. Changing or adding one afterwards forces a new environment, which means a new " +
			"metadata database — the run history in the old one does not come along.\n\n" +
			"Task-pod egress is denied by default except for a fixed platform baseline (the Airflow " +
			"execution API, object storage, Bifrost and DNS). The `egress` block grants what a DAG " +
			"needs on top of that baseline; the baseline itself is not editable and never appears here.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Environment id. This is the value a `hyperfluid_airflow_connection`'s `airflow` takes.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"env": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Environment (harbor) id the Airflow environment runs in. Changing this forces a new environment.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Environment name. Must be a slug: lowercase letters, digits and hyphens, " +
					"not starting or ending with a hyphen. It has to be unique across the organization's " +
					"workload namespace, not merely within the harbor, because every per-environment child " +
					"object is named after it. Changing this forces a new environment.",
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 63),
					stringvalidator.RegexMatches(airflowDNSLabelPattern,
						"must be lowercase letters, digits and hyphens, not starting or ending with a hyphen"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"postgres_ref": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Name of an existing `hyperfluid_managed_postgresql` in the same harbor to " +
					"host Airflow's metadata database. Omit it and the platform provisions a dedicated cluster " +
					"for the environment. Create-only: changing it forces a new environment.",
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 63)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"dag_bucket_ref": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Name of an existing `hyperfluid_bucket` in the same harbor to deliver DAGs " +
					"through. Omit it and the platform provisions `<name>-airflow`; either way the bucket " +
					"actually in use is reported as `dag_bucket`. Create-only: changing it forces a new " +
					"environment.",
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 63)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"node_tier": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Size of the four Airflow components. One of `" +
					strings.Join(airflowNodeTiers, "`, `") + "` (defaults to `small`); memory request and " +
					"limit are always equal, and the resolved values are reported as `cpu_request`, " +
					"`cpu_limit`, `memory_request` and `memory_limit`. There is no `nano` tier: 512Mi " +
					"cannot hold the triggerer, so the catalogue starts one tier up. Changing the tier " +
					"restarts the components.",
				Validators: []validator.String{stringvalidator.OneOf(airflowNodeTiers...)},
			},
			"triggerer_enabled": schema.BoolAttribute{
				Optional: true, Computed: true,
				// A framework Default is what makes removing the line mean
				// "back to true". Without one, Terraform fills a null config
				// value for an Optional+Computed attribute from prior state, so
				// the plan equals the state, nothing is sent, and the attribute
				// is stuck at whatever it was last set to — the same trap the
				// runtime_image comment below describes, which is why that one
				// is not Computed at all. Here the documented default is a real
				// platform default, so declaring it is the honest fix.
				Default: booldefault.StaticBool(true),
				MarkdownDescription: "Whether the triggerer runs (defaults to `true`). The triggerer is what " +
					"makes deferrable operators and sensors give their worker slot back while they wait; " +
					"without it a deferring task stays deferred forever. Turn it off only for an " +
					"environment whose DAGs use neither.",
			},
			"sleep_mode": schema.BoolAttribute{
				Optional: true, Computed: true,
				// See triggerer_enabled: without a Default, deleting
				// `sleep_mode = true` from a configuration plans no change and
				// the environment never wakes up again.
				Default: booldefault.StaticBool(false),
				MarkdownDescription: "Scale every component to zero (defaults to `false`). A sleeping " +
					"environment fires no schedules and serves no UI, but keeps its metadata database, its " +
					"DAG bucket and its connections, so waking it up resumes where it left off. Reported " +
					"as phase `Sleeping`.",
			},
			"runtime_image": schema.StringAttribute{
				// Optional and deliberately NOT Computed. The pin has to be
				// removable, and a Computed attribute keeps its prior value
				// when the config stops naming one — so dropping the line
				// would silently hold the environment on the old image forever
				// instead of handing it back to the platform default.
				Optional: true,
				MarkdownDescription: "Pin the Airflow runtime image, e.g. to one built with extra Python " +
					"dependencies. Omit it — or remove it from a configuration that had it — and the " +
					"environment tracks the platform's own image, which is what picks up security updates; " +
					"removing the attribute clears the pin rather than keeping the last value.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, airflowMaxRuntimeImageLen)},
			},
			"task_quota_max_pods": schema.Int64Attribute{
				Optional: true,
				MarkdownDescription: "Ceiling on how many task pods the environment's task namespace may run " +
					"at once. Omit it to leave the platform default in charge; removing it hands the ceiling " +
					"back to that default. Zero is not a value — it is how the API spells \"no ceiling of " +
					"your own\" — so use omission for that and this attribute for a real limit.",
				Validators: []validator.Int64{int64validator.Between(1, airflowMaxTaskQuotaPods)},
			},
			"description": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Free-form description. Stored by the console, not in the environment " +
					"itself. Omit it for no description — the empty string is not a value here.",
				// LengthBetween, not LengthAtMost: the console's column is NOT
				// NULL, so it stores "" verbatim and the read maps "" back to
				// null. An explicit `description = ""` would therefore fail
				// every apply with "inconsistent result after apply". Omission
				// is the only way to say "none", so the empty string is refused
				// at plan time instead.
				Validators: []validator.String{stringvalidator.LengthBetween(1, 500)},
			},
			"tags": schema.ListAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				MarkdownDescription: "User-defined tags. Stored by the console, not in the environment itself.",
			},

			"egress": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Network egress the environment's task pods are granted **on top of** the " +
					"platform baseline (execution API, object storage, Bifrost, DNS). The baseline is not " +
					"editable and is never reported here.\n\n" +
					"~> **The block is replaced wholesale, not merged.** Whatever it contains is the complete " +
					"set of extra grants; removing the block revokes all of them and leaves the environment " +
					"on the baseline alone.",
				Attributes: map[string]schema.Attribute{
					"fqdns": schema.SetAttribute{
						ElementType: types.StringType, Optional: true,
						MarkdownDescription: "Public hostnames task pods may reach on 443, e.g. " +
							"`api.example.com` or `*.example.org`. Lowercase, at least two labels, and a " +
							"leading `*.` must still leave two labels behind it — no bare wildcards and no " +
							"IP literals. A few platform-reserved suffixes are refused by the API.",
						Validators: []validator.Set{
							setvalidator.SizeBetween(1, airflowMaxEgressFqdns),
							setvalidator.ValueStringsAre(stringvalidator.RegexMatches(airflowFqdnPattern,
								"must be a lowercase hostname of at least two labels, optionally prefixed with \"*.\", and not an IP literal")),
						},
					},
					"in_cluster": schema.SetNestedAttribute{
						Optional: true,
						MarkdownDescription: "Services in the same harbor that task pods may reach, by kind " +
							"and name. This is the Airflow equivalent of a `hyperfluid_service_link`, " +
							"declared on the environment rather than as its own resource.",
						Validators: []validator.Set{setvalidator.SizeBetween(1, airflowMaxInClusterLinks)},
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"kind": schema.StringAttribute{
									Required: true,
									MarkdownDescription: "Kind of service. One of `" +
										strings.Join(airflowLinkKinds, "`, `") + "`.",
									Validators: []validator.String{stringvalidator.OneOf(airflowLinkKinds...)},
								},
								"name": schema.StringAttribute{
									Required: true,
									MarkdownDescription: "The target's slug in the same harbor — the `slug` " +
										"attribute of the resource, not its display name.",
									Validators: []validator.String{
										stringvalidator.RegexMatches(airflowDNSLabelPattern,
											"must be a DNS-1123 label: lowercase letters, digits and hyphens, 1-63 characters"),
									},
								},
							},
						},
					},
					"allowlists": schema.SetAttribute{
						ElementType: types.StringType, Optional: true,
						MarkdownDescription: "Names of the harbor's shared egress allow-lists to attach — the " +
							"same named lists dev workstations and CI runners attach. A name that does not " +
							"resolve to an allow-list of this harbor reaches the task policy as nothing at " +
							"all, and is reported back in `unresolved_allowlists`.",
						Validators: []validator.Set{
							setvalidator.SizeBetween(1, airflowMaxEgressAllowlist),
							setvalidator.ValueStringsAre(stringvalidator.RegexMatches(airflowDNSLabelPattern,
								"must be a DNS-1123 label: lowercase letters, digits and hyphens, 1-63 characters")),
						},
					},
				},
			},
			"config": schema.MapAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "`airflow.cfg` overrides, keyed `section.key` — for example " +
					"`{ \"core.parallelism\" = \"64\" }`, which the platform renders as " +
					"`AIRFLOW__CORE__PARALLELISM`. Keys are lowercase letters, digits and underscores on " +
					"both sides of a single dot.\n\n" +
					"~> **The map is replaced wholesale, not merged**, and removing it restores every " +
					"default. Settings the platform owns — the executor, the auth manager, the execution " +
					"API, the metadata database, remote logging and the secrets backend — are refused with " +
					"a 400 naming them, because overriding one would either leak a platform secret or " +
					"detach the environment from the platform that runs it.",
				Validators: []validator.Map{
					mapvalidator.SizeBetween(1, airflowMaxConfigEntries),
					mapvalidator.KeysAre(stringvalidator.RegexMatches(airflowConfigKeyPattern,
						"must be \"<section>.<key>\" using only lowercase letters, digits and underscores, e.g. \"core.parallelism\"")),
					mapvalidator.ValueStringsAre(stringvalidator.LengthAtMost(airflowMaxConfigValueLen)),
				},
			},

			"slug":  computedStr("Derived slug. This is the name an `egress.in_cluster` entry elsewhere would target."),
			"phase": computedStr("Live lifecycle phase: `Pending`, `Provisioning`, `Running`, `Sleeping`, `Error` or `Unknown`."),
			"web_url": computedStr("Public HTTPS URL of the Airflow UI. Absent until the route is actually " +
				"serving, and for a sleeping environment."),
			"public_host":    computedStr("Public hostname the UI is published under, identical in both network modes."),
			"network_mode":   computedStr("How the UI is exposed: `ingress` or `httproute`. A property of the platform, not of this resource."),
			"task_namespace": computedStr("Dedicated Kubernetes namespace the environment's task pods run in."),
			"dag_bucket": computedStr("Bucket DAGs are delivered through — the one named by `dag_bucket_ref`, " +
				"or the one the platform provisioned. Upload DAGs under its `dags/` prefix."),
			"service_account":         computedStr("Data-plane service account every task pod runs as."),
			"managed_postgresql_name": computedStr("Name of the metadata PostgreSQL cluster, whether referenced or provisioned."),
			"unresolved_allowlists": schema.ListAttribute{
				ElementType: types.StringType, Computed: true,
				MarkdownDescription: "Names from `egress.allowlists` the last reconcile could not resolve to " +
					"an allow-list of this harbor. Empty is the healthy case; anything listed here is a " +
					"grant that silently is not in force.",
			},
			"cpu_request":    computedStr("CPU request per component, resolved from `node_tier`."),
			"cpu_limit":      computedStr("CPU limit per component, resolved from `node_tier`."),
			"memory_request": computedStr("Memory request per component, resolved from `node_tier`."),
			"memory_limit":   computedStr("Memory limit per component, resolved from `node_tier` (equal to the request)."),
		},
	}
}

func (r *airflowResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *airflowResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan airflowModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// ValidateConfig makes the same judgement at plan time, but it cannot make
	// it about a grant that was still unknown then; this is where that value is
	// finally a value. Before the create, so an environment is never built from
	// a declaration that cannot round-trip.
	if !r.checkEgressGrants(ctx, plan.Egress, &resp.Diagnostics) {
		return
	}

	body, d := airflowCreateBody(ctx, plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.p.API.CreateAirflow(ctx, r.p.OrgID, plan.Env.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create Airflow environment", err.Error())
		return
	}
	id := created.Id.String()

	settled, err := r.waitReady(ctx, id)
	if err != nil {
		// The environment EXISTS from here on: only the wait failed. Store what
		// we can read before returning the error, so state knows about it — a
		// bare `return` leaves an environment (with its metadata PostgreSQL and
		// its DAG bucket) that `destroy` cannot remove and the next plan cannot
		// refresh. Provisioning is the slow part here, so a wait that times out
		// on a healthy environment is the likely case, and it must not cost the
		// user a hand-cleanup.
		// nil, not the wait's last projection: the wait did NOT approve it, so
		// the freshest read available is the honest thing to store here.
		if partial, readErr := r.readInto(ctx, id, plan.NodeTier, nil); readErr == nil {
			resp.Diagnostics.Append(resp.State.Set(ctx, partial)...)
		} else {
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
		}
		resp.Diagnostics.AddError(
			"Airflow environment did not become ready",
			err.Error()+"\n\nThe environment was created and is now tracked in state, so it is not "+
				"orphaned: re-apply once it settles, or run `terraform destroy` to remove it.")
		return
	}
	state, err := r.readInto(ctx, id, plan.NodeTier, settled)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Airflow environment after create", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// ValidateConfig rejects an `egress` block that grants nothing, as early as the
// block can be judged. It is not a stylistic objection: the API treats an empty
// grant set as no grant set at all and stores nothing, so the block reads back
// absent and the apply fails as an inconsistent result — with no hint that the
// empty block was the cause.
//
// Plan time is as early as that is possible only when the grants are literals.
// `allowlists = <a computed set of another resource>` is unknown here, and an
// unknown collection is not an absent one — judging it now would reject a
// configuration that is going to be perfectly good. Create and Update therefore
// repeat the check on the resolved plan, which is the first moment such a value
// is a value; see checkEgressGrants.
func (r *airflowResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg airflowModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	verdict, d := airflowEgressVerdictOf(ctx, cfg.Egress)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() || verdict != airflowEgressEmpty {
		return
	}
	summary, detail := airflowEmptyEgressDiagnostic(false)
	resp.Diagnostics.AddAttributeError(path.Root("egress"), summary, detail)
}

// checkEgressGrants is the apply-time half of the empty-block rule: it reports
// false, having added the diagnostic, when the resolved plan's `egress` block
// grants nothing. Terraform resolves every dependency before calling Create or
// Update, so a collection that was unknown at plan time is known here — and an
// `egress` whose only grant resolved to an empty set is exactly the case
// ValidateConfig had to let through.
//
// Catching it here is what turns Terraform's own "Provider produced inconsistent
// result after apply: .egress: was cty.ObjectVal(...), but now null" — emitted
// after the environment has already been created, and naming no cause — into a
// diagnostic that names the block and stops before writing anything.
func (r *airflowResource) checkEgressGrants(ctx context.Context, egress types.Object, diags *diag.Diagnostics) bool {
	verdict, d := airflowEgressVerdictOf(ctx, egress)
	diags.Append(d...)
	if diags.HasError() {
		return false
	}
	if verdict != airflowEgressEmpty {
		return true
	}
	summary, detail := airflowEmptyEgressDiagnostic(true)
	diags.AddAttributeError(path.Root("egress"), summary, detail)
	return false
}

// airflowEgressVerdict is what one `egress` object says about its grants.
type airflowEgressVerdict int

const (
	// airflowEgressAbsent: no block at all — the platform baseline, always fine.
	airflowEgressAbsent airflowEgressVerdict = iota
	// airflowEgressGranting: at least one collection is known and non-empty.
	airflowEgressGranting
	// airflowEgressEmpty: every collection is known, and every one is empty.
	airflowEgressEmpty
	// airflowEgressUndecidable: nothing grants yet, but a collection is still
	// unknown and may well grant once it resolves.
	airflowEgressUndecidable
)

// airflowEgressVerdictOf classifies an `egress` object. It looks at all three
// collections before deciding: stopping at the first unknown one would let a
// block that is unknown in one place and empty in the other two read the same
// as a block that genuinely grants something.
func airflowEgressVerdictOf(ctx context.Context, obj types.Object) (airflowEgressVerdict, diag.Diagnostics) {
	var diags diag.Diagnostics
	if obj.IsNull() {
		return airflowEgressAbsent, diags
	}
	if obj.IsUnknown() {
		return airflowEgressUndecidable, diags
	}
	var egress airflowEgressModel
	diags.Append(obj.As(ctx, &egress, basetypes.ObjectAsOptions{})...)
	if diags.HasError() {
		return airflowEgressUndecidable, diags
	}
	undecidable := false
	for _, set := range []types.Set{egress.Fqdns, egress.InCluster, egress.Allowlists} {
		switch {
		case set.IsUnknown():
			undecidable = true
		case !set.IsNull() && len(set.Elements()) > 0:
			return airflowEgressGranting, diags
		}
	}
	if undecidable {
		return airflowEgressUndecidable, diags
	}
	return airflowEgressEmpty, diags
}

// airflowEmptyEgressDiagnostic explains a block that grants nothing. atApply
// adds why the objection arrives this late, which is the difference between a
// message that reads as a provider bug and one the user can act on.
func airflowEmptyEgressDiagnostic(atApply bool) (summary, detail string) {
	detail = "The egress block names no grant. An empty grant set and no grant set mean the same thing " +
		"to the platform — baseline egress only — so remove the whole block instead of leaving " +
		"it empty, or give it at least one of fqdns, in_cluster or allowlists."
	if atApply {
		detail += "\n\nThis could not be reported at plan time: the grants come from a value that was " +
			"still unknown then — a computed attribute of another resource — and only resolved to an " +
			"empty set during this apply. Nothing was sent to the platform."
	}
	return "Empty egress block", detail
}

func (r *airflowResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior airflowModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.readInto(ctx, prior.ID.ValueString(), prior.NodeTier, nil)
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Airflow environment", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *airflowResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state airflowModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Same reason as in Create, and the same failure it prevents: a grant that
	// resolved to an empty set would clear the block, read back absent, and end
	// the apply on an inconsistent result — after the patch had already landed.
	if !r.checkEgressGrants(ctx, plan.Egress, &resp.Diagnostics) {
		return
	}

	body, d := airflowPatchBody(ctx, plan, state)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	if err := r.p.API.PatchAirflow(ctx, r.p.OrgID, id, body); err != nil {
		resp.Diagnostics.AddError("Failed to update Airflow environment", err.Error())
		return
	}
	settled, err := r.waitReady(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Airflow environment did not become ready after update", err.Error())
		return
	}
	newState, err := r.readInto(ctx, id, plan.NodeTier, settled)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Airflow environment after update", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *airflowResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state airflowModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.p.API.DeleteAirflow(ctx, r.p.OrgID, id); err != nil {
		resp.Diagnostics.AddError("Failed to delete Airflow environment", err.Error())
		return
	}
	if err := pollGoneOn404(ctx, airflowWaitTimeout, func() error {
		_, err := r.p.API.GetAirflow(ctx, r.p.OrgID, id)
		return err
	}); err != nil {
		resp.Diagnostics.AddError("Airflow environment still present after delete", err.Error())
	}
}

func (r *airflowResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// waitReady blocks until the environment settles, and hands the settled CRD
// back so the caller does not have to read the same object again. What "settled"
// means, and why it is gated on `spec_observed`, is airflowSettled below.
func (r *airflowResource) waitReady(ctx context.Context, id string) (*console.AirflowCrdSpecResponse, error) {
	var last *console.AirflowCrdSpecResponse
	settled, err := waitForReady(ctx, airflowWaitTimeout, func() (*console.AirflowCrdSpecResponse, bool, error) {
		crd, err := r.p.API.GetAirflowCrd(ctx, r.p.OrgID, id)
		if err != nil {
			return nil, false, err
		}
		last = crd
		done, err := airflowSettled(crd)
		if err != nil {
			return nil, false, err
		}
		return crd, done, nil
	})
	if err == nil {
		return settled, nil
	}
	if last != nil {
		// A wait that ran out while the platform had still not looked at the
		// current declaration is a different story from one that ran out on a
		// phase published for it, and the phase alone cannot tell them apart.
		stale := ""
		if !last.Status.SpecObserved {
			stale = "; the platform had not yet observed the current declaration"
		}
		return nil, fmt.Errorf("%w (last reported phase %q: %s%s)",
			err, last.Status.Phase, airflowOrNoMessage(airflowStatusMessage(last)), stale)
	}
	return nil, err
}

// airflowSettled reads one CRD projection for the wait: (true, nil) to stop and
// keep it, (false, nil) to poll again, (false, err) to give up.
//
// `spec_observed` gates every verdict, and it is what makes the wait after an
// Update a wait at all. A PATCH bumps the object's generation, and until the
// operator has looked at that generation the status still describes the
// PREVIOUS declaration: `phase` reads `Running` for the components that were
// running before the patch. Settling on that would make the wait a no-op — the
// apply would report success while the new components crash-loop, and because
// every non-computed attribute reads back from `spec` and not from `status`,
// nothing else in the apply would catch it either. The environment would then
// simply be found in `Error` on some later refresh.
//
// A stale `Error` is gated for the same reason in the other direction: aborting
// on the phase the previous declaration ended in would fail the apply on a
// state the new declaration may never reach — the patch may be precisely the
// fix. The first projection worth believing is the first one published for the
// current generation.
//
// `spec_observed` is `status.observedGeneration == metadata.generation`, and the
// operator stamps the generation it read onto every status it writes, so a
// phase for the current generation always arrives with it — the gate cannot
// deadlock on a platform that is making progress. A create has no previous
// generation to go stale: a fresh object has no status at all, so
// `spec_observed` is false and the wait polls, which is what it did anyway
// while the phase read `Pending`.
//
// `Sleeping` settles: an environment created (or patched) with sleep_mode never
// reaches `Running`, and waiting for it would be a guaranteed timeout. An
// observed `Error` aborts at once rather than burning the whole 15-minute
// ceiling, carrying the CR's own status message — the only place the reason for
// a stuck provision is written down.
func airflowSettled(crd *console.AirflowCrdSpecResponse) (bool, error) {
	if crd == nil || !crd.Status.SpecObserved {
		return false, nil
	}
	switch crd.Status.Phase {
	case airflowPhaseRunning, airflowPhaseSleeping:
		return true, nil
	case airflowPhaseError:
		return false, fmt.Errorf("the environment reported phase Error: %s", airflowOrNoMessage(airflowStatusMessage(crd)))
	default:
		return false, nil
	}
}

// airflowStatusMessage is the CR's status message, or "" when it carries none.
func airflowStatusMessage(crd *console.AirflowCrdSpecResponse) string {
	if crd == nil || crd.Status.Message == nil {
		return ""
	}
	return *crd.Status.Message
}

func airflowOrNoMessage(message string) string {
	if message == "" {
		return "no status message was reported"
	}
	return message
}

// readInto builds the model from both views of the environment. fallbackTier is
// used when the resolved cpu/memory do not match a known tier, so an unknown
// catalogue row keeps whatever the configuration or prior state says instead of
// blanking the attribute. settled is the CRD the caller already holds — the one
// waitReady stopped on — or nil to read it here.
func (r *airflowResource) readInto(ctx context.Context, id string, fallbackTier types.String, settled *console.AirflowCrdSpecResponse) (airflowModel, error) {
	return airflowReadModel(ctx, id, fallbackTier, settled,
		func() (*console.AirflowResponse, error) { return r.p.API.GetAirflow(ctx, r.p.OrgID, id) },
		func() (*console.AirflowCrdSpecResponse, error) { return r.p.API.GetAirflowCrd(ctx, r.p.OrgID, id) },
	)
}

// airflowReadModel is readInto's logic over injected reads. settled is the CRD
// the caller already has: waitReady polls until the object settles and returns
// the very projection it settled on, so re-fetching it here would be a second
// round-trip on every create and update, and a window in which the two reads
// can disagree — the write path would then store a status the wait never
// approved. nil means there is nothing to reuse (a refresh, or a wait that
// failed and wants the freshest view it can get).
func airflowReadModel(
	ctx context.Context,
	id string,
	fallbackTier types.String,
	settled *console.AirflowCrdSpecResponse,
	getInstance func() (*console.AirflowResponse, error),
	getCrd func() (*console.AirflowCrdSpecResponse, error),
) (airflowModel, error) {
	instance, err := getInstance()
	if err != nil {
		return airflowModel{}, err
	}
	// sleep_mode, egress, config, the task quota and every status field exist
	// only here. Skipping this read is what makes each plan after the first
	// report drift on all of them.
	crd := settled
	if crd == nil {
		crd, err = getCrd()
		if err != nil {
			return airflowModel{}, err
		}
	}

	var diags diag.Diagnostics
	tags, d := stringSliceToList(ctx, instance.Tags)
	diags.Append(d...)
	unresolved, d := stringSliceToList(ctx, crd.Status.UnresolvedAllowlists)
	diags.Append(d...)
	egress, d := airflowEgressToObject(ctx, crd.Egress)
	diags.Append(d...)
	config, d := airflowConfigToMap(ctx, crd.Config)
	diags.Append(d...)
	if err := airflowMappingError(diags); err != nil {
		return airflowModel{}, err
	}

	return airflowModel{
		ID:               types.StringValue(id),
		Env:              types.StringValue(instance.HarborId.String()),
		Name:             types.StringValue(instance.Name),
		PostgresRef:      optString(crd.PostgresRef),
		DagBucketRef:     optString(crd.DagBucketRef),
		NodeTier:         airflowNodeTierFromResources(crd.CpuRequest, crd.CpuLimit, crd.MemoryLimit, fallbackTier),
		TriggererEnabled: types.BoolValue(crd.TriggererEnabled),
		SleepMode:        types.BoolValue(crd.SleepMode),
		RuntimeImage:     optString(crd.RuntimeImage),
		TaskQuotaMaxPods: optInt64FromInt32(crd.TaskQuotaMaxPods),
		// The description column is NOT NULL, so an environment without one
		// reads back as "". Mapping that to null is what keeps a configuration
		// that never set a description from planning a change on every run.
		Description:           airflowEmptyToNull(instance.Description),
		Tags:                  tags,
		Egress:                egress,
		Config:                config,
		Slug:                  types.StringValue(instance.Slug),
		Phase:                 types.StringValue(crd.Status.Phase),
		WebURL:                optString(crd.Status.WebUrl),
		PublicHost:            optString(crd.Status.PublicHost),
		NetworkMode:           optString(crd.Status.NetworkMode),
		TaskNamespace:         optString(crd.Status.TaskNamespace),
		DagBucket:             optString(crd.Status.DagBucket),
		ServiceAccount:        optString(crd.Status.ServiceAccount),
		ManagedPostgresqlName: optString(instance.ManagedPostgresqlName),
		UnresolvedAllowlists:  unresolved,
		CPURequest:            optString(crd.CpuRequest),
		CPULimit:              optString(crd.CpuLimit),
		MemoryRequest:         optString(crd.MemoryRequest),
		MemoryLimit:           optString(crd.MemoryLimit),
	}, nil
}

// airflowMappingError collapses conversion diagnostics into one error, so the
// read path can stay a plain (model, error) pair and Read can keep using
// errors.Is to tell a deleted environment from a broken one.
func airflowMappingError(diags diag.Diagnostics) error {
	if !diags.HasError() {
		return nil
	}
	reasons := make([]string, 0, len(diags.Errors()))
	for _, e := range diags.Errors() {
		reasons = append(reasons, e.Summary()+": "+e.Detail())
	}
	return fmt.Errorf("failed to map the Airflow environment into state: %s", strings.Join(reasons, "; "))
}

func airflowEmptyToNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// airflowNodeTierFromResources recovers the configured tier from the resolved
// requests and limits, which is all the API echoes back. An unrecognised
// combination falls back to the caller's value (the plan on write, prior state
// on refresh) so a catalogue that has moved on degrades to "unchanged" rather
// than to a wrong tier.
func airflowNodeTierFromResources(cpuRequest, cpuLimit, memoryLimit *string, fallback types.String) types.String {
	if fallback.IsUnknown() {
		// An unknown can never reach state; the caller had no value to offer.
		fallback = types.StringNull()
	}
	if cpuRequest == nil || cpuLimit == nil || memoryLimit == nil {
		return fallback
	}
	for tier, want := range airflowTierResources {
		if want.cpuRequest == *cpuRequest && want.cpuLimit == *cpuLimit && want.memory == *memoryLimit {
			return types.StringValue(tier)
		}
	}
	return fallback
}

// ── request bodies ────────────────────────────────────────────────────────

// airflowCreateBody builds the create payload. Every field whose absence means
// "the platform decides" is left out rather than sent at its default value:
// `sleep_mode: false`, an empty `egress`, an empty `config` and a zero task
// quota would each be written into the CRD spec and frozen there, so a later
// change to the platform's own default could never reach the environment.
func airflowCreateBody(ctx context.Context, plan airflowModel) (console.CreateAirflowCrdRequestBody, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := console.CreateAirflowCrdRequestBody{
		Name:         plan.Name.ValueString(),
		PostgresRef:  stringPtr(plan.PostgresRef),
		DagBucketRef: stringPtr(plan.DagBucketRef),
		RuntimeImage: stringPtr(plan.RuntimeImage),
		Description:  stringPtr(plan.Description),
	}
	if airflowKnown(plan.NodeTier) {
		tier := console.AirflowNodeTier(plan.NodeTier.ValueString())
		body.NodeTier = &tier
	}
	if !plan.TriggererEnabled.IsNull() && !plan.TriggererEnabled.IsUnknown() {
		body.TriggererEnabled = boolPtr(plan.TriggererEnabled)
	}
	// Only "create it asleep" is worth sending; awake is the platform default.
	if !plan.SleepMode.IsNull() && !plan.SleepMode.IsUnknown() && plan.SleepMode.ValueBool() {
		body.SleepMode = ptr(true)
	}
	if !plan.TaskQuotaMaxPods.IsNull() && !plan.TaskQuotaMaxPods.IsUnknown() {
		body.TaskQuotaMaxPods = int32PtrFromInt64(plan.TaskQuotaMaxPods)
	}
	if !plan.Tags.IsNull() && !plan.Tags.IsUnknown() {
		tags, d := listToStringSlice(ctx, plan.Tags)
		diags.Append(d...)
		if tags == nil {
			tags = []string{}
		}
		body.Tags = &tags
	}
	if !plan.Egress.IsNull() && !plan.Egress.IsUnknown() {
		egress, d := airflowEgressFromObject(ctx, plan.Egress)
		diags.Append(d...)
		if egress != nil && !airflowEgressIsEmpty(egress) {
			body.Egress = egress
		}
	}
	if !plan.Config.IsNull() && !plan.Config.IsUnknown() {
		config, d := airflowConfigFromMap(ctx, plan.Config)
		diags.Append(d...)
		if len(config) > 0 {
			body.Config = &config
		}
	}
	return body, diags
}

// airflowPatchBody builds the update payload, sending only what actually
// changed. The API distinguishes three things for `runtime_image`, `egress`,
// `config` and `task_quota_max_pods` — omitted means unchanged, a clearing
// sentinel means "back to the platform default", and a value means that value —
// so each of them is translated explicitly instead of being sent unconditionally.
func airflowPatchBody(ctx context.Context, plan, state airflowModel) (console.PatchAirflowCrdRequestBody, diag.Diagnostics) {
	var diags diag.Diagnostics
	var body console.PatchAirflowCrdRequestBody

	if !plan.NodeTier.Equal(state.NodeTier) && airflowKnown(plan.NodeTier) {
		tier := console.AirflowNodeTier(plan.NodeTier.ValueString())
		body.NodeTier = &tier
	}
	if !plan.TriggererEnabled.Equal(state.TriggererEnabled) && !plan.TriggererEnabled.IsUnknown() {
		body.TriggererEnabled = boolPtr(plan.TriggererEnabled)
	}
	// Unlike create, `false` here is the user asking to wake the environment up
	// and is sent as such.
	if !plan.SleepMode.Equal(state.SleepMode) && !plan.SleepMode.IsUnknown() {
		body.SleepMode = boolPtr(plan.SleepMode)
	}
	// An empty string is how the API spells "drop the pin"; a nil pointer would
	// only mean "leave it alone".
	if !plan.RuntimeImage.Equal(state.RuntimeImage) {
		body.RuntimeImage = airflowClearableString(plan.RuntimeImage)
	}
	if !plan.Description.Equal(state.Description) {
		body.Description = airflowClearableString(plan.Description)
	}
	// Zero clears the quota back to the platform default; the schema refuses it
	// as an input, so it can only ever get here from a removed attribute.
	if !plan.TaskQuotaMaxPods.Equal(state.TaskQuotaMaxPods) {
		if plan.TaskQuotaMaxPods.IsNull() {
			body.TaskQuotaMaxPods = ptr(int32(0))
		} else {
			body.TaskQuotaMaxPods = int32PtrFromInt64(plan.TaskQuotaMaxPods)
		}
	}
	if !plan.Tags.Equal(state.Tags) && !plan.Tags.IsUnknown() {
		tags, d := listToStringSlice(ctx, plan.Tags)
		diags.Append(d...)
		if tags == nil {
			tags = []string{}
		}
		body.Tags = &tags
	}
	// Replace-not-merge: an object replaces the whole grant set, and an empty
	// object is what revokes it back to the baseline.
	if !plan.Egress.Equal(state.Egress) && !plan.Egress.IsUnknown() {
		if plan.Egress.IsNull() {
			body.Egress = &console.AirflowEgressRequest{}
		} else {
			egress, d := airflowEgressFromObject(ctx, plan.Egress)
			diags.Append(d...)
			if egress == nil {
				egress = &console.AirflowEgressRequest{}
			}
			body.Egress = egress
		}
	}
	// Same contract for the overrides: an empty map restores every default.
	if !plan.Config.Equal(state.Config) && !plan.Config.IsUnknown() {
		config, d := airflowConfigFromMap(ctx, plan.Config)
		diags.Append(d...)
		if config == nil {
			config = map[string]string{}
		}
		body.Config = &config
	}
	return body, diags
}

func airflowKnown(v types.String) bool {
	return !v.IsNull() && !v.IsUnknown()
}

// airflowClearableString maps an Optional-only string onto the API's
// double-option: the configured value when there is one, and the empty string —
// which the API reads as "clear" — when the attribute was removed.
func airflowClearableString(plan types.String) *string {
	if plan.IsNull() || plan.IsUnknown() {
		return ptr("")
	}
	return ptr(plan.ValueString())
}

// ── egress / config conversions ───────────────────────────────────────────

func airflowEgressFromObject(ctx context.Context, obj types.Object) (*console.AirflowEgressRequest, diag.Diagnostics) {
	var diags diag.Diagnostics
	if obj.IsNull() || obj.IsUnknown() {
		return nil, diags
	}
	var model airflowEgressModel
	diags.Append(obj.As(ctx, &model, basetypes.ObjectAsOptions{})...)
	if diags.HasError() {
		return nil, diags
	}

	out := &console.AirflowEgressRequest{}

	fqdns, d := setToStringSlice(ctx, model.Fqdns)
	diags.Append(d...)
	if len(fqdns) > 0 {
		out.Fqdns = &fqdns
	}

	allowlists, d := setToStringSlice(ctx, model.Allowlists)
	diags.Append(d...)
	if len(allowlists) > 0 {
		out.Allowlists = &allowlists
	}

	if !model.InCluster.IsNull() && !model.InCluster.IsUnknown() {
		var links []airflowInClusterLinkModel
		diags.Append(model.InCluster.ElementsAs(ctx, &links, false)...)
		if len(links) > 0 {
			wire := make([]console.AirflowInClusterLinkRequest, 0, len(links))
			for _, l := range links {
				wire = append(wire, console.AirflowInClusterLinkRequest{
					Kind: console.AirflowLinkKind(l.Kind.ValueString()),
					Name: l.Name.ValueString(),
				})
			}
			out.InCluster = &wire
		}
	}
	return out, diags
}

func airflowEgressIsEmpty(e *console.AirflowEgressRequest) bool {
	if e == nil {
		return true
	}
	return (e.Fqdns == nil || len(*e.Fqdns) == 0) &&
		(e.Allowlists == nil || len(*e.Allowlists) == 0) &&
		(e.InCluster == nil || len(*e.InCluster) == 0)
}

// airflowEgressToObject maps the CRD's egress back into state. An absent — or
// entirely empty — grant set becomes a null object, matching a configuration
// with no `egress` block; within a present block each empty collection becomes
// null too, so naming only `fqdns` does not plan a change on the other two.
func airflowEgressToObject(ctx context.Context, e *console.AirflowEgressRequest) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	attrTypes := airflowEgressAttrTypes()
	if airflowEgressIsEmpty(e) {
		return types.ObjectNull(attrTypes), diags
	}

	fqdns := types.SetNull(types.StringType)
	if e.Fqdns != nil && len(*e.Fqdns) > 0 {
		v, d := stringSliceToSet(ctx, *e.Fqdns)
		diags.Append(d...)
		fqdns = v
	}
	allowlists := types.SetNull(types.StringType)
	if e.Allowlists != nil && len(*e.Allowlists) > 0 {
		v, d := stringSliceToSet(ctx, *e.Allowlists)
		diags.Append(d...)
		allowlists = v
	}
	inCluster := types.SetNull(airflowInClusterLinkType())
	if e.InCluster != nil && len(*e.InCluster) > 0 {
		models := make([]airflowInClusterLinkModel, 0, len(*e.InCluster))
		for _, l := range *e.InCluster {
			models = append(models, airflowInClusterLinkModel{
				Kind: types.StringValue(string(l.Kind)),
				Name: types.StringValue(l.Name),
			})
		}
		v, d := types.SetValueFrom(ctx, airflowInClusterLinkType(), models)
		diags.Append(d...)
		inCluster = v
	}

	obj, d := types.ObjectValue(attrTypes, map[string]attr.Value{
		"fqdns":      fqdns,
		"in_cluster": inCluster,
		"allowlists": allowlists,
	})
	diags.Append(d...)
	return obj, diags
}

func airflowConfigFromMap(ctx context.Context, m types.Map) (map[string]string, diag.Diagnostics) {
	var diags diag.Diagnostics
	if m.IsNull() || m.IsUnknown() {
		return nil, diags
	}
	out := map[string]string{}
	diags.Append(m.ElementsAs(ctx, &out, false)...)
	return out, diags
}

func airflowConfigToMap(ctx context.Context, config *map[string]string) (types.Map, diag.Diagnostics) {
	var diags diag.Diagnostics
	if config == nil || len(*config) == 0 {
		return types.MapNull(types.StringType), diags
	}
	v, d := types.MapValueFrom(ctx, types.StringType, *config)
	diags.Append(d...)
	return v, diags
}
