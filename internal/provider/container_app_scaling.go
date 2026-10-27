// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/objectvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// container_app_scaling.go — how the app is sized, scaled, reached and
// scraped: custom resources, resource mode, autoscaling, custom domains and
// metrics.

// The platform's bounds for a custom size, those of its smallest and largest
// tiers.
const (
	minCustomCPUMillis = 50
	maxCustomCPUMillis = 8000
	minCustomMemoryMiB = 64
	maxCustomMemoryMiB = 8192
	maxAppReplicas     = 5
)

type containerAppCustomResourcesModel struct {
	CPUMillis types.Int64 `tfsdk:"cpu_millis"`
	MemoryMiB types.Int64 `tfsdk:"memory_mib"`
}

type containerAppAutoscalingModel struct {
	MinReplicas             types.Int64 `tfsdk:"min_replicas"`
	MaxReplicas             types.Int64 `tfsdk:"max_replicas"`
	TargetCPUUtilization    types.Int64 `tfsdk:"target_cpu_utilization"`
	TargetMemoryUtilization types.Int64 `tfsdk:"target_memory_utilization"`
	TargetHTTPConcurrency   types.Int64 `tfsdk:"target_http_concurrency"`
	ScaleToZeroAfterSeconds types.Int64 `tfsdk:"scale_to_zero_after_seconds"`
}

type containerAppMetricsModel struct {
	Port types.String `tfsdk:"port"`
	Path types.String `tfsdk:"path"`
}

type containerAppCustomDomainModel struct {
	FQDN          types.String `tfsdk:"fqdn"`
	TLSMode       types.String `tfsdk:"tls_mode"`
	TLSSecretName types.String `tfsdk:"tls_secret_name"`
}

type containerAppCustomDomainStatusModel struct {
	FQDN                  types.String `tfsdk:"fqdn"`
	State                 types.String `tfsdk:"state"`
	Message               types.String `tfsdk:"message"`
	Target                types.String `tfsdk:"target"`
	ObservedAddresses     types.List   `tfsdk:"observed_addresses"`
	CertificateSecretName types.String `tfsdk:"certificate_secret_name"`
}

func containerAppCustomResourcesTypes() map[string]attr.Type {
	return map[string]attr.Type{"cpu_millis": types.Int64Type, "memory_mib": types.Int64Type}
}

func containerAppAutoscalingTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"min_replicas":                types.Int64Type,
		"max_replicas":                types.Int64Type,
		"target_cpu_utilization":      types.Int64Type,
		"target_memory_utilization":   types.Int64Type,
		"target_http_concurrency":     types.Int64Type,
		"scale_to_zero_after_seconds": types.Int64Type,
	}
}

func containerAppMetricsTypes() map[string]attr.Type {
	return map[string]attr.Type{"port": types.StringType, "path": types.StringType}
}

func containerAppCustomDomainType() attr.Type {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"fqdn":            types.StringType,
		"tls_mode":        types.StringType,
		"tls_secret_name": types.StringType,
	}}
}

func containerAppCustomDomainStatusType() attr.Type {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"fqdn":                    types.StringType,
		"state":                   types.StringType,
		"message":                 types.StringType,
		"target":                  types.StringType,
		"observed_addresses":      types.ListType{ElemType: types.StringType},
		"certificate_secret_name": types.StringType,
	}}
}

var (
	// fqdnPattern is a lowercase hostname of two labels or more without the
	// trailing dot, the form the platform stores a custom domain in.
	fqdnPattern = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?\.)+[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	// metricsPathPattern is an absolute path of printable ASCII.
	metricsPathPattern = regexp.MustCompile(`^/[!-~]*$`)
)

func scalingAttributes() map[string]schema.Attribute {
	optInt := func(desc string, v ...validator.Int64) schema.Int64Attribute {
		return schema.Int64Attribute{Optional: true, MarkdownDescription: desc, Validators: v}
	}
	return map[string]schema.Attribute{
		"custom_resources": schema.SingleNestedAttribute{
			Optional:            true,
			MarkdownDescription: "Custom size of each replica, between two tiers. Conflicts with `resource_tier`.",
			Validators:          []validator.Object{objectvalidator.ConflictsWith(path.MatchRoot("resource_tier"))},
			Attributes: map[string]schema.Attribute{
				"cpu_millis": schema.Int64Attribute{
					Required:            true,
					MarkdownDescription: fmt.Sprintf("CPU sold, in millicores, %d to %d.", minCustomCPUMillis, maxCustomCPUMillis),
					Validators:          []validator.Int64{int64validator.Between(minCustomCPUMillis, maxCustomCPUMillis)},
				},
				"memory_mib": schema.Int64Attribute{
					Required:            true,
					MarkdownDescription: fmt.Sprintf("Memory, in MiB, %d to %d.", minCustomMemoryMiB, maxCustomMemoryMiB),
					Validators:          []validator.Int64{int64validator.Between(minCustomMemoryMiB, maxCustomMemoryMiB)},
				},
			},
		},
		"resource_mode": schema.StringAttribute{
			Optional: true, Computed: true,
			MarkdownDescription: "Whether the console shows CPU rightsizing advice: `advisory` (default) shows it, " +
				"`manual` and `managed` hide it.",
			Validators:    []validator.String{stringvalidator.OneOf("advisory", "manual", "managed")},
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"autoscaling": schema.SingleNestedAttribute{
			Optional: true, Computed: true,
			MarkdownDescription: "Autoscaling between `min_replicas` and `max_replicas`, on CPU unless another " +
				"target is set. Conflicts with `replicas`.",
			PlanModifiers: []planmodifier.Object{unsetObject("autoscaling")},
			Validators:    []validator.Object{objectvalidator.ConflictsWith(path.MatchRoot("replicas"))},
			Attributes: map[string]schema.Attribute{
				"max_replicas": schema.Int64Attribute{
					Required:            true,
					MarkdownDescription: fmt.Sprintf("Most replicas, 1 to %d.", maxAppReplicas),
					Validators:          []validator.Int64{int64validator.Between(1, maxAppReplicas)},
				},
				"min_replicas": optInt("Fewest replicas, defaults to 1. 0 lets the app sleep, which needs "+
					"`expose_to_internet` and an HTTP port.", int64validator.Between(0, maxAppReplicas)),
				"target_cpu_utilization": optInt("Target average CPU use, in percent of the request (1 to 100).",
					int64validator.Between(1, 100)),
				"target_memory_utilization": optInt("Target average memory use, in percent of the request (1 to 100).",
					int64validator.Between(1, 100)),
				"target_http_concurrency": optInt("Target in-flight requests per replica, at least 1. Needs "+
					"`expose_to_internet` and an HTTP port.",
					int64validator.AtLeast(1)),
				"scale_to_zero_after_seconds": optInt("Idle seconds before the app sleeps when `min_replicas` is 0, "+
					"30 to 86400.",
					int64validator.Between(30, 86400)),
			},
		},
		"metrics": schema.SingleNestedAttribute{
			Optional: true, Computed: true,
			MarkdownDescription: "Where the app serves Prometheus metrics for the platform to scrape.",
			PlanModifiers:       []planmodifier.Object{unsetObject("metrics")},
			Attributes: map[string]schema.Attribute{
				"port": schema.StringAttribute{
					Required:            true,
					MarkdownDescription: "Name of the entry of `ports` serving the metrics.",
					Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
				},
				"path": schema.StringAttribute{
					Optional:            true,
					MarkdownDescription: "Path to scrape, starting with `/`. Defaults to `/metrics`.",
					Validators: []validator.String{
						stringvalidator.LengthAtMost(256),
						stringvalidator.RegexMatches(metricsPathPattern, "must start with / and contain no whitespace"),
					},
				},
			},
		},
		"custom_domains": schema.ListNestedAttribute{
			Optional: true, Computed: true,
			MarkdownDescription: "Your own domains the app answers on, each verified by the organization " +
				"beforehand. Needs `expose_to_internet`.",
			PlanModifiers: []planmodifier.List{unsetList("custom_domains")},
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					"fqdn": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "Domain, lowercase and without a trailing dot, such as `app.acme.com`.",
						Validators: []validator.String{
							stringvalidator.LengthAtMost(253),
							stringvalidator.RegexMatches(fqdnPattern, "must be a lowercase hostname of at least two labels, without a trailing dot"),
						},
					},
					"tls_mode": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "`acme` for a certificate the platform obtains, or `secret` for the one in `tls_secret_name`.",
						Validators:          []validator.String{stringvalidator.OneOf("acme", "secret")},
					},
					"tls_secret_name": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Secret holding the certificate. Required with `tls_mode = \"secret\"` only.",
						Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
					},
				},
			},
		},
		"custom_domain_statuses": schema.ListNestedAttribute{
			Computed:            true,
			MarkdownDescription: "Status of each custom domain.",
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					"fqdn": schema.StringAttribute{Computed: true, MarkdownDescription: "The domain."},
					"state": schema.StringAttribute{Computed: true, MarkdownDescription: "`pendingVerification`, " +
						"`conflict`, `pendingDns`, `provisioning`, `ready` or `error`."},
					"message":                 schema.StringAttribute{Computed: true, MarkdownDescription: "Why the domain is not ready, when it is not."},
					"target":                  schema.StringAttribute{Computed: true, MarkdownDescription: "Where the domain's DNS record must point."},
					"observed_addresses":      schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Addresses the domain resolved to at the last check."},
					"certificate_secret_name": schema.StringAttribute{Computed: true, MarkdownDescription: "Secret holding the certificate served."},
				},
			},
		},
		"image_digest": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Digest the main image is pinned to, when it is.",
		},
		"tag_is_floating": schema.BoolAttribute{
			Computed:            true,
			MarkdownDescription: "Whether the image tag can be re-pushed upstream, such as `latest` or `15`.",
		},
	}
}

// ── request bodies ────────────────────────────────────────────────────────

// scalingInputs holds the fields of this file a create or patch sends; nil is
// left out.
type scalingInputs struct {
	CustomResources  *console.CustomResourceInput
	ResourceMode     *console.ResourceMode
	Autoscaling      *console.AutoscalingSpec
	ClearAutoscaling *bool
	Metrics          *console.ContainerAppMetrics
	ClearMetrics     *bool
	CustomDomains    *[]console.CustomDomainInput
}

// scalingInputsFor mirrors workloadInputsFor. A removed autoscaling or metrics
// is sent as its clear_* flag, since the API reads an absent one as "keep".
func scalingInputsFor(ctx context.Context, plan containerAppModel, prior *containerAppModel) (scalingInputs, diag.Diagnostics) {
	var (
		in    scalingInputs
		diags diag.Diagnostics
	)
	known := func(v attr.Value) bool { return !v.IsNull() && !v.IsUnknown() }
	changed := func(p, s attr.Value) bool { return prior == nil || !p.Equal(s) }

	if known(plan.CustomResources) {
		var m containerAppCustomResourcesModel
		diags.Append(plan.CustomResources.As(ctx, &m, objectAsOptions)...)
		in.CustomResources = &console.CustomResourceInput{CpuMillis: m.CPUMillis.ValueInt64(), MemoryMib: m.MemoryMiB.ValueInt64()}
	}
	if known(plan.ResourceMode) && (prior == nil || !plan.ResourceMode.Equal(prior.ResourceMode)) {
		in.ResourceMode = ptr(console.ResourceMode(plan.ResourceMode.ValueString()))
	}

	var priorAutoscaling, priorMetrics types.Object
	var priorDomains types.List
	if prior != nil {
		priorAutoscaling, priorMetrics, priorDomains = prior.Autoscaling, prior.Metrics, prior.CustomDomains
	}
	switch {
	case known(plan.Autoscaling) && changed(plan.Autoscaling, priorAutoscaling):
		var m containerAppAutoscalingModel
		diags.Append(plan.Autoscaling.As(ctx, &m, objectAsOptions)...)
		in.Autoscaling = &console.AutoscalingSpec{
			MaxReplicas:             int32(m.MaxReplicas.ValueInt64()),
			MinReplicas:             int32PtrFromInt64(m.MinReplicas),
			TargetCpuUtilization:    int32PtrFromInt64(m.TargetCPUUtilization),
			TargetMemoryUtilization: int32PtrFromInt64(m.TargetMemoryUtilization),
			TargetHttpConcurrency:   int32PtrFromInt64(m.TargetHTTPConcurrency),
			ScaleToZeroAfterSeconds: int32PtrFromInt64(m.ScaleToZeroAfterSeconds),
		}
	case prior != nil && plan.Autoscaling.IsNull() && !priorAutoscaling.IsNull():
		in.ClearAutoscaling = ptr(true)
	}
	switch {
	case known(plan.Metrics) && changed(plan.Metrics, priorMetrics):
		var m containerAppMetricsModel
		diags.Append(plan.Metrics.As(ctx, &m, objectAsOptions)...)
		in.Metrics = &console.ContainerAppMetrics{Port: m.Port.ValueString(), Path: stringPtr(m.Path)}
	case prior != nil && plan.Metrics.IsNull() && !priorMetrics.IsNull():
		in.ClearMetrics = ptr(true)
	}
	if known(plan.CustomDomains) && changed(plan.CustomDomains, priorDomains) {
		var models []containerAppCustomDomainModel
		diags.Append(plan.CustomDomains.ElementsAs(ctx, &models, false)...)
		domains := make([]console.CustomDomainInput, 0, len(models))
		for _, m := range models {
			domains = append(domains, console.CustomDomainInput{
				Fqdn:          m.FQDN.ValueString(),
				TlsMode:       console.CustomDomainTlsModeInput(m.TLSMode.ValueString()),
				TlsSecretName: stringPtr(m.TLSSecretName),
			})
		}
		in.CustomDomains = &domains
	}
	return in, diags
}

// ── reading back ──────────────────────────────────────────────────────────

type scalingState struct {
	CustomResources, Autoscaling, Metrics types.Object
	ResourceMode, ImageDigest             types.String
	TagIsFloating                         types.Bool
	CustomDomains, CustomDomainStatuses   types.List
}

// scalingFrom maps the spec onto this file's attributes. The size the app
// sells is reported as custom_resources only when priorCustom is set: a tier
// app sells a size too, which would otherwise read as custom.
func scalingFrom(ctx context.Context, spec *console.ContainerAppCrdSpecResponse, priorCustom types.Object) (scalingState, diag.Diagnostics) {
	var (
		s     scalingState
		diags diag.Diagnostics
		d     diag.Diagnostics
	)
	s.CustomResources = priorCustom
	if !priorCustom.IsNull() && spec.EntitlementCpuMillis != nil && spec.MemoryRequestBytes != nil {
		s.CustomResources, d = types.ObjectValue(containerAppCustomResourcesTypes(), map[string]attr.Value{
			"cpu_millis": types.Int64Value(*spec.EntitlementCpuMillis),
			"memory_mib": types.Int64Value(*spec.MemoryRequestBytes / (1024 * 1024)),
		})
		diags.Append(d...)
	}
	s.ResourceMode = types.StringNull()
	if spec.ResourceMode != "" {
		s.ResourceMode = types.StringValue(string(spec.ResourceMode))
	}
	s.ImageDigest = optString(spec.ImageDigest)
	s.TagIsFloating = types.BoolValue(spec.TagIsFloating)

	s.Autoscaling = types.ObjectNull(containerAppAutoscalingTypes())
	if a := spec.Autoscaling; a != nil {
		s.Autoscaling, d = types.ObjectValueFrom(ctx, containerAppAutoscalingTypes(), containerAppAutoscalingModel{
			MaxReplicas:             types.Int64Value(int64(a.MaxReplicas)),
			MinReplicas:             optInt64FromInt32(a.MinReplicas),
			TargetCPUUtilization:    optInt64FromInt32(a.TargetCpuUtilization),
			TargetMemoryUtilization: optInt64FromInt32(a.TargetMemoryUtilization),
			TargetHTTPConcurrency:   optInt64FromInt32(a.TargetHttpConcurrency),
			ScaleToZeroAfterSeconds: optInt64FromInt32(a.ScaleToZeroAfterSeconds),
		})
		diags.Append(d...)
	}
	s.Metrics = types.ObjectNull(containerAppMetricsTypes())
	if m := spec.Metrics; m != nil {
		s.Metrics, d = types.ObjectValueFrom(ctx, containerAppMetricsTypes(), containerAppMetricsModel{
			Port: types.StringValue(m.Port), Path: optString(m.Path),
		})
		diags.Append(d...)
	}

	domains := make([]containerAppCustomDomainModel, 0, len(spec.CustomDomains))
	statuses := make([]containerAppCustomDomainStatusModel, 0, len(spec.CustomDomains))
	for _, cd := range spec.CustomDomains {
		domains = append(domains, containerAppCustomDomainModel{
			FQDN: types.StringValue(cd.Fqdn), TLSMode: types.StringValue(cd.TlsMode), TLSSecretName: optString(cd.TlsSecretName),
		})
		statuses = append(statuses, containerAppCustomDomainStatusModel{
			FQDN: types.StringValue(cd.Fqdn), State: optString(cd.State), Message: optString(cd.Message),
			Target: optString(cd.Target), ObservedAddresses: stringsList(ctx, deref(cd.ObservedAddresses), &diags),
			CertificateSecretName: optString(cd.CertificateSecretName),
		})
	}
	s.CustomDomains, d = types.ListValueFrom(ctx, containerAppCustomDomainType(), domains)
	diags.Append(d...)
	s.CustomDomainStatuses, d = types.ListValueFrom(ctx, containerAppCustomDomainStatusType(), statuses)
	diags.Append(d...)
	return s, diags
}

// ── plan-time validation ──────────────────────────────────────────────────

// validateScaling applies the API's rules that span attributes.
func validateScaling(ctx context.Context, cfg containerAppModel, diags *diag.Diagnostics) {
	var ports []containerAppPortModel
	portsKnown := !cfg.Ports.IsNull() && !cfg.Ports.IsUnknown()
	if portsKnown {
		diags.Append(cfg.Ports.ElementsAs(ctx, &ports, false)...)
	}

	if !cfg.Autoscaling.IsNull() && !cfg.Autoscaling.IsUnknown() {
		var a containerAppAutoscalingModel
		diags.Append(cfg.Autoscaling.As(ctx, &a, objectAsOptions)...)
		at := path.Root("autoscaling")
		if known64(a.MinReplicas) && known64(a.MaxReplicas) && a.MinReplicas.ValueInt64() > a.MaxReplicas.ValueInt64() {
			diags.AddAttributeError(at.AtName("min_replicas"), "Invalid autoscaling range",
				fmt.Sprintf("min_replicas (%d) cannot exceed max_replicas (%d).", a.MinReplicas.ValueInt64(), a.MaxReplicas.ValueInt64()))
		}
		wakesThroughRoute := (known64(a.MinReplicas) && a.MinReplicas.ValueInt64() == 0) || known64(a.TargetHTTPConcurrency)
		if wakesThroughRoute {
			exposed := cfg.ExposeToInternet.IsUnknown() || cfg.ExposeToInternet.ValueBool()
			httpPort := !portsKnown
			for _, p := range ports {
				if p.Protocol.IsNull() || p.Protocol.IsUnknown() || p.Protocol.ValueString() == "HTTP" {
					httpPort = true
				}
			}
			if !exposed || !httpPort {
				diags.AddAttributeError(at, "Autoscaling needs a public HTTP route",
					"Scaling to zero or on target_http_concurrency wakes the app with the request that comes through "+
						"its route, so the app needs expose_to_internet = true and an HTTP port.")
			}
		}
	}

	if !cfg.Metrics.IsNull() && !cfg.Metrics.IsUnknown() && portsKnown {
		var m containerAppMetricsModel
		diags.Append(cfg.Metrics.As(ctx, &m, objectAsOptions)...)
		if !m.Port.IsNull() && !m.Port.IsUnknown() {
			found := false
			for _, p := range ports {
				if p.Name.ValueString() == m.Port.ValueString() || p.Name.IsUnknown() {
					found = true
				}
			}
			if !found {
				diags.AddAttributeError(path.Root("metrics").AtName("port"), "Unknown metrics port",
					fmt.Sprintf("%q names no entry of ports. Set it to the name of the port serving the metrics.", m.Port.ValueString()))
			}
		}
	}

	if cfg.CustomDomains.IsNull() || cfg.CustomDomains.IsUnknown() {
		return
	}
	var domains []containerAppCustomDomainModel
	diags.Append(cfg.CustomDomains.ElementsAs(ctx, &domains, false)...)
	seen := map[string]bool{}
	for i, cd := range domains {
		at := path.Root("custom_domains").AtListIndex(i)
		if cd.TLSMode.ValueString() == "secret" && cd.TLSSecretName.IsNull() {
			diags.AddAttributeError(at.AtName("tls_secret_name"), "Missing certificate secret",
				"tls_mode = \"secret\" serves the certificate in tls_secret_name, which must be set.")
		}
		if cd.TLSMode.ValueString() == "acme" && !cd.TLSSecretName.IsNull() {
			diags.AddAttributeError(at.AtName("tls_secret_name"), "Certificate secret not used",
				"With tls_mode = \"acme\" the platform obtains the certificate itself; remove tls_secret_name.")
		}
		if cd.FQDN.IsNull() || cd.FQDN.IsUnknown() {
			continue
		}
		if seen[cd.FQDN.ValueString()] {
			diags.AddAttributeError(at.AtName("fqdn"), "Duplicate custom domain",
				fmt.Sprintf("%q is listed twice.", cd.FQDN.ValueString()))
		}
		seen[cd.FQDN.ValueString()] = true
	}
}

var objectAsOptions = basetypes.ObjectAsOptions{}

func known64(v types.Int64) bool { return !v.IsNull() && !v.IsUnknown() }
