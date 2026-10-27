// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

func autoscaling(t *testing.T, edit func(*containerAppAutoscalingModel)) types.Object {
	t.Helper()
	m := containerAppAutoscalingModel{
		MaxReplicas: types.Int64Value(3), MinReplicas: types.Int64Null(),
		TargetCPUUtilization: types.Int64Null(), TargetMemoryUtilization: types.Int64Null(),
		TargetHTTPConcurrency: types.Int64Null(), ScaleToZeroAfterSeconds: types.Int64Null(),
	}
	if edit != nil {
		edit(&m)
	}
	o, d := types.ObjectValueFrom(t.Context(), containerAppAutoscalingTypes(), m)
	noErrors(t, d)
	return o
}

func metrics(port, path string) types.Object {
	p := types.StringNull()
	if path != "" {
		p = types.StringValue(path)
	}
	return types.ObjectValueMust(containerAppMetricsTypes(), map[string]attr.Value{"port": types.StringValue(port), "path": p})
}

func customResources(cpu, mem int64) types.Object {
	return types.ObjectValueMust(containerAppCustomResourcesTypes(), map[string]attr.Value{
		"cpu_millis": types.Int64Value(cpu), "memory_mib": types.Int64Value(mem),
	})
}

func fileMounts(t *testing.T, mounts ...containerAppFileMountModel) types.List {
	t.Helper()
	l, d := types.ListValueFrom(t.Context(), containerAppFileMountType(), mounts)
	noErrors(t, d)
	return l
}

func inlineFile(path, content string) containerAppFileMountModel {
	return containerAppFileMountModel{MountPath: types.StringValue(path), Mode: types.StringNull(),
		Content: types.StringValue(content), SecretName: types.StringNull()}
}

func secretFile(path, secret string) containerAppFileMountModel {
	return containerAppFileMountModel{MountPath: types.StringValue(path), Mode: types.StringValue("0400"),
		Content: types.StringNull(), SecretName: types.StringValue(secret)}
}

func customDomains(t *testing.T, domains ...containerAppCustomDomainModel) types.List {
	t.Helper()
	l, d := types.ListValueFrom(t.Context(), containerAppCustomDomainType(), domains)
	noErrors(t, d)
	return l
}

func acmeDomain(fqdn string) containerAppCustomDomainModel {
	return containerAppCustomDomainModel{FQDN: types.StringValue(fqdn), TLSMode: types.StringValue("acme"), TLSSecretName: types.StringNull()}
}

func httpPorts() types.List {
	l, _ := types.ListValueFrom(context.Background(), containerAppPortType(), []containerAppPortModel{
		{Name: types.StringValue("http"), Port: types.Int64Value(8080), Protocol: types.StringValue("HTTP"), Primary: types.BoolValue(true)},
		{Name: types.StringValue("metrics"), Port: types.Int64Value(9090), Protocol: types.StringValue("TCP"), Primary: types.BoolValue(false)},
	})
	return l
}

// scaledApp configures every attribute of container_app_scaling.go and
// file_mounts.
func scaledApp(t *testing.T, f *fakeConsole) containerAppModel {
	m := literalApp(f)
	m.Slug = types.StringValue("orders-api")
	m.Ports = httpPorts()
	m.ExposeToInternet = types.BoolValue(true)
	m.CustomResources = customResources(300, 384)
	m.ResourceMode = types.StringValue("manual")
	m.Autoscaling = autoscaling(t, func(a *containerAppAutoscalingModel) {
		a.MinReplicas, a.TargetCPUUtilization = types.Int64Value(2), types.Int64Value(70)
	})
	m.Metrics = metrics("metrics", "/prom")
	m.CustomDomains = customDomains(t, acmeDomain("orders.acme.com"))
	m.FileMounts = fileMounts(t, inlineFile("/etc/app/config.yaml", "level: debug\n"), secretFile("/etc/app/token", "production/orders/token"))
	return m
}

func TestContainerAppCreateSendsScaling(t *testing.T) {
	h := newHarness(t)
	h.f.autoscaledTo = 2
	plan := scaledApp(t, h.f)
	got, d := h.create(plan)
	noErrors(t, d)

	body := h.f.calls("POST", "/container-apps/crd")[0].body
	if a := obj(body["autoscaling"]); a["maxReplicas"] != float64(3) || a["minReplicas"] != float64(2) || a["targetCpuUtilization"] != float64(70) {
		t.Errorf("autoscaling = %v", body["autoscaling"])
	}
	if c := obj(body["custom_resources"]); c["cpu_millis"] != float64(300) || c["memory_mib"] != float64(384) {
		t.Errorf("custom_resources = %v", body["custom_resources"])
	}
	if _, ok := body["resource_tier"]; ok {
		t.Errorf("resource_tier sent next to custom_resources: %v", body)
	}
	mounts, _ := body["file_mounts"].([]any)
	if len(mounts) != 2 || obj(obj(mounts[0])["source"])["type"] != "Inline" ||
		obj(obj(mounts[1])["source"])["secret_name"] != "production/orders/token" {
		t.Errorf("file_mounts = %v", body["file_mounts"])
	}
	if body["resource_mode"] != "manual" || obj(body["metrics"])["port"] != "metrics" {
		t.Errorf("resource_mode/metrics = %v / %v", body["resource_mode"], body["metrics"])
	}

	for name, pair := range map[string][2]attr.Value{
		"custom_resources": {got.CustomResources, plan.CustomResources},
		"resource_mode":    {got.ResourceMode, plan.ResourceMode},
		"autoscaling":      {got.Autoscaling, plan.Autoscaling},
		"metrics":          {got.Metrics, plan.Metrics},
		"custom_domains":   {got.CustomDomains, plan.CustomDomains},
		"file_mounts":      {got.FileMounts, plan.FileMounts},
	} {
		if !pair[0].Equal(pair[1]) {
			t.Errorf("%s = %v, want %v", name, pair[0], pair[1])
		}
	}
	var statuses []containerAppCustomDomainStatusModel
	noErrors(t, got.CustomDomainStatuses.ElementsAs(t.Context(), &statuses, false))
	if len(statuses) != 1 || statuses[0].FQDN.ValueString() != "orders.acme.com" || statuses[0].State.ValueString() != "pendingVerification" {
		t.Errorf("custom_domain_statuses = %v", got.CustomDomainStatuses)
	}
	if got.TagIsFloating.IsNull() || !got.ImageDigest.IsNull() {
		t.Errorf("tag_is_floating/image_digest = %v / %v", got.TagIsFloating, got.ImageDigest)
	}
}

// A tier app sells a size too: custom_resources stays null unless it was set.
func TestContainerAppTierAppHasNoCustomResources(t *testing.T) {
	h := newHarness(t)
	plan := literalApp(h.f)
	plan.ResourceTier = types.StringValue("small")
	created, d := h.create(plan)
	noErrors(t, d)
	cpu, mem := int64(1000), int64(1024*1024*1024)
	h.f.spec.EntitlementCpuMillis, h.f.spec.MemoryRequestBytes = &cpu, &mem

	got, err := h.r.readState(t.Context(), created.ID.ValueString(), created)
	if err != nil {
		t.Fatalf("readState: %v", err)
	}
	if !got.CustomResources.IsNull() || got.ResourceTier.ValueString() != "small" {
		t.Errorf("custom_resources/resource_tier = %v / %v", got.CustomResources, got.ResourceTier)
	}
}

// Autoscaling and metrics Terraform applied are removed with their clear_*
// flag; unchanged or never-managed ones are not sent.
func TestContainerAppUpdateClearsAutoscalingAndMetrics(t *testing.T) {
	h := newHarness(t)
	h.f.autoscaledTo = 2
	created, d := h.create(scaledApp(t, h.f))
	noErrors(t, d)

	next := created
	next.Description = types.StringValue("changed")
	updated, d := h.update(next, h.stateOf(created))
	noErrors(t, d)
	patch := h.f.calls("PATCH", "/crd")[0].body
	for _, key := range []string{"autoscaling", "clear_autoscaling", "metrics", "clear_metrics", "custom_domains", "file_mounts", "resource_mode"} {
		if _, ok := patch[key]; ok {
			t.Errorf("an update that changes none of them sent %q: %v", key, patch)
		}
	}

	next = updated
	next.Autoscaling = plannedUnsetObject(updated.Autoscaling, true)
	next.Metrics = plannedUnsetObject(updated.Metrics, true)
	next.CustomDomains = plannedUnsetList(updated.CustomDomains, true)
	next.Replicas = types.Int64Value(1)
	got, d := h.update(next, h.stateOf(updated))
	noErrors(t, d)
	patch = h.f.calls("PATCH", "/crd")[1].body
	if patch["clear_autoscaling"] != true || patch["clear_metrics"] != true {
		t.Errorf("PATCH = %v, want clear_autoscaling and clear_metrics", patch)
	}
	if domains, ok := patch["custom_domains"].([]any); !ok || len(domains) != 0 {
		t.Errorf("PATCH custom_domains = %v, want []", patch["custom_domains"])
	}
	if !got.Autoscaling.IsNull() || !got.Metrics.IsNull() || len(got.CustomDomainStatuses.Elements()) != 0 {
		t.Errorf("state = %v / %v / %v", got.Autoscaling, got.Metrics, got.CustomDomainStatuses)
	}
}

// The object counterpart of TestContainerAppPlanOfAListLeftOutOfTheConfig.
func TestContainerAppPlanOfAnObjectLeftOutOfTheConfig(t *testing.T) {
	h := newHarness(t)
	h.f.autoscaledTo = 2
	plan := literalApp(h.f)
	plan.Slug = types.StringValue("orders-api")
	plan.Autoscaling = autoscaling(t, nil)
	prior, d := h.create(plan)
	noErrors(t, d)

	cfg := literalApp(h.f)
	cfg.Slug = types.StringValue("orders-api")
	for name, c := range map[string]struct {
		managed []string
		want    types.Object
	}{
		"applied before": {managed: []string{"autoscaling"}, want: types.ObjectNull(containerAppAutoscalingTypes())},
		"never applied":  {want: prior.Autoscaling},
	} {
		t.Run(name, func(t *testing.T) {
			var private []byte
			if c.managed != nil {
				names, _ := json.Marshal(c.managed)
				private, _ = json.Marshal(map[string][]byte{managedKey: names})
			}
			planned := h.planThroughProtocol(prior, cfg, private)
			if !planned.Autoscaling.Equal(c.want) {
				t.Errorf("planned autoscaling = %v, want %v", planned.Autoscaling, c.want)
			}
		})
	}
}

// An autoscaled app is ready once its rollout converges on the count the
// autoscaler chose, not on `replicas`.
func TestContainerAppWaitsForTheAutoscaledCount(t *testing.T) {
	h := newHarness(t)
	h.f.autoscaledTo = 4
	plan := literalApp(h.f)
	plan.Autoscaling = autoscaling(t, func(a *containerAppAutoscalingModel) { a.MaxReplicas = types.Int64Value(5) })
	got, d := h.create(plan)
	noErrors(t, d)
	if got.DesiredReplicas.ValueInt64() != 4 {
		t.Errorf("desired_replicas = %v, want the autoscaler's 4", got.DesiredReplicas)
	}
}

func TestContainerAppScalingValidation(t *testing.T) {
	big := strings.Repeat("x", maxInlineFileBytes+1)
	sixK := strings.Repeat("x", 6*1024)
	cases := map[string]struct {
		edit func(t *testing.T, m *containerAppModel)
		want string
	}{
		"file too large": {
			edit: func(t *testing.T, m *containerAppModel) { m.FileMounts = fileMounts(t, inlineFile("/etc/a", big)) },
			want: "File too large",
		},
		"files too large together": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.FileMounts = fileMounts(t, inlineFile("/a/1", sixK), inlineFile("/a/2", sixK), inlineFile("/a/3", sixK),
					inlineFile("/a/4", sixK), inlineFile("/a/5", sixK), inlineFile("/a/6", sixK))
			},
			want: "File mounts too large",
		},
		"two files at one path": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.FileMounts = fileMounts(t, inlineFile("/etc/a", "1"), secretFile("/etc/a", "production/a"))
			},
			want: "Duplicate file mount",
		},
		"file under /proc": {
			edit: func(t *testing.T, m *containerAppModel) { m.FileMounts = fileMounts(t, inlineFile("/proc/x", "1")) },
			want: "Invalid mount path",
		},
		"relative file path": {
			edit: func(t *testing.T, m *containerAppModel) { m.FileMounts = fileMounts(t, inlineFile("etc/a", "1")) },
			want: "Invalid mount path",
		},
		"file mode above 0777": {
			edit: func(t *testing.T, m *containerAppModel) {
				f := inlineFile("/etc/a", "1")
				f.Mode = types.StringValue("1777")
				m.FileMounts = fileMounts(t, f)
			},
			want: "Invalid Attribute Value Match",
		},
		"file with content and a secret": {
			edit: func(t *testing.T, m *containerAppModel) {
				f := inlineFile("/etc/a", "1")
				f.SecretName = types.StringValue("production/a")
				m.FileMounts = fileMounts(t, f)
			},
			want: "Invalid Attribute Combination",
		},
		"private secret file": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.FileMounts = fileMounts(t, secretFile("/etc/a", "users/alice/a"))
			},
			want: "Private secret",
		},
		"uppercase domain": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.CustomDomains = customDomains(t, acmeDomain("Orders.acme.com"))
			},
			want: "Invalid Attribute Value Match",
		},
		"domain with a trailing dot": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.CustomDomains = customDomains(t, acmeDomain("orders.acme.com."))
			},
			want: "Invalid Attribute Value Match",
		},
		"domain listed twice": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.CustomDomains = customDomains(t, acmeDomain("orders.acme.com"), acmeDomain("orders.acme.com"))
			},
			want: "Duplicate custom domain",
		},
		"secret tls without a secret": {
			edit: func(t *testing.T, m *containerAppModel) {
				cd := acmeDomain("orders.acme.com")
				cd.TLSMode = types.StringValue("secret")
				m.CustomDomains = customDomains(t, cd)
			},
			want: "Missing certificate secret",
		},
		"acme tls with a secret": {
			edit: func(t *testing.T, m *containerAppModel) {
				cd := acmeDomain("orders.acme.com")
				cd.TLSSecretName = types.StringValue("production/tls")
				m.CustomDomains = customDomains(t, cd)
			},
			want: "Certificate secret not used",
		},
		"min above max": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.Autoscaling = autoscaling(t, func(a *containerAppAutoscalingModel) { a.MinReplicas = types.Int64Value(4) })
			},
			want: "Invalid autoscaling range",
		},
		"too many replicas": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.Autoscaling = autoscaling(t, func(a *containerAppAutoscalingModel) { a.MaxReplicas = types.Int64Value(6) })
			},
			want: "Invalid Attribute Value",
		},
		"autoscaling with replicas": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.Autoscaling = autoscaling(t, nil)
				m.Replicas = types.Int64Value(2)
			},
			want: "Invalid Attribute Combination",
		},
		"scale to zero on a private app": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.ExposeToInternet = types.BoolValue(false)
				m.Autoscaling = autoscaling(t, func(a *containerAppAutoscalingModel) { a.MinReplicas = types.Int64Value(0) })
			},
			want: "Autoscaling needs a public HTTP route",
		},
		"metrics on an undeclared port": {
			edit: func(t *testing.T, m *containerAppModel) { m.Metrics = metrics("prom", "") },
			want: "Unknown metrics port",
		},
		"metrics path with a space": {
			edit: func(t *testing.T, m *containerAppModel) { m.Metrics = metrics("metrics", "/my metrics") },
			want: "Invalid Attribute Value Match",
		},
		"custom size next to a tier": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.CustomResources = customResources(300, 384)
				m.ResourceTier = types.StringValue("small")
			},
			want: "Invalid Attribute Combination",
		},
		"custom size too small": {
			edit: func(t *testing.T, m *containerAppModel) { m.CustomResources = customResources(10, 384) },
			want: "Invalid Attribute Value",
		},
		"unknown resource mode": {
			edit: func(t *testing.T, m *containerAppModel) { m.ResourceMode = types.StringValue("auto") },
			want: "Invalid Attribute Value Match",
		},
	}
	base := func(h *appHarness) containerAppModel {
		m := literalApp(h.f)
		m.Slug = types.StringValue("orders-api")
		m.Ports = httpPorts()
		m.ExposeToInternet = types.BoolValue(true)
		return m
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			cfg := base(h)
			c.edit(t, &cfg)
			var summaries []string
			for _, d := range h.validateThroughProtocol(cfg) {
				if d.Severity == tfprotov6.DiagnosticSeverityError {
					summaries = append(summaries, d.Summary)
				}
			}
			if len(summaries) != 1 || summaries[0] != c.want {
				t.Errorf("errors = %q, want exactly %q", summaries, c.want)
			}
		})
	}

	h := newHarness(t)
	cfg := scaledApp(t, h.f)
	for _, d := range h.validateThroughProtocol(cfg) {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Errorf("unexpected error %s: %s", d.Summary, d.Detail)
		}
	}
}
