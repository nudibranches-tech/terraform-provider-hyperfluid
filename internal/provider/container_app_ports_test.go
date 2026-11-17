// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

func noPorts() types.List {
	return types.ListValueMust(containerAppPortType(), []attr.Value{})
}

func httpPort(n int64) types.List {
	return types.ListValueMust(containerAppPortType(), []attr.Value{
		types.ObjectValueMust(attrTypes(containerAppPortType()), map[string]attr.Value{
			"name": types.StringValue("http"), "port": types.Int64Value(n),
			"protocol": types.StringValue("HTTP"), "primary": types.BoolValue(true),
		}),
	})
}

// sentPorts reports whether a request body carries `ports`, and its entries.
func sentPorts(body map[string]any) ([]any, bool) {
	v, ok := body["ports"]
	if !ok {
		return nil, false
	}
	list, _ := v.([]any)
	return list, list != nil
}

// `ports = []` reaches the create request as an explicit empty array, which
// the console reads as an app listening on no port, and reads back as the
// same empty list so the next plan is clean.
func TestContainerAppCreateWithNoPortSendsEmptyList(t *testing.T) {
	h := newHarness(t)
	plan := literalApp(h.f)
	plan.Slug = types.StringValue("worker")
	plan.Ports = noPorts()

	got, d := h.create(plan)
	noErrors(t, d)

	body := h.f.calls("POST", "/container-apps/crd")[0].body
	ports, sent := sentPorts(body)
	if !sent || len(ports) != 0 {
		t.Fatalf("create body ports = %v (present: %v), want []", body["ports"], sent)
	}
	if _, ok := body["port"]; ok {
		t.Errorf("create body carries the legacy port %v next to ports", body["port"])
	}
	if !got.Ports.Equal(noPorts()) {
		t.Errorf("ports after create = %s, want an empty list", got.Ports)
	}
	if !got.Port.IsNull() {
		t.Errorf("port after create = %s, want null for an app with no port", got.Port)
	}

	read := &fwresource.ReadResponse{State: h.stateOf(got)}
	h.r.Read(t.Context(), fwresource.ReadRequest{State: h.stateOf(got)}, read)
	noErrors(t, read.Diagnostics)
	refreshed := h.load(read.State)
	if !refreshed.Ports.Equal(plan.Ports) || !refreshed.Port.Equal(got.Port) {
		t.Errorf("refresh drifted: ports %s, port %s", refreshed.Ports, refreshed.Port)
	}
}

// Leaving `ports` out sends neither field, so the console creates an app that
// listens on no port, and state records `ports = []` with a null `port`.
func TestContainerAppCreateWithoutPortsSendsNone(t *testing.T) {
	h := newHarness(t)
	plan := literalApp(h.f)
	plan.Slug = types.StringValue("orders-api")

	got, d := h.create(plan)
	noErrors(t, d)

	body := h.f.calls("POST", "/container-apps/crd")[0].body
	if _, ok := body["ports"]; ok {
		t.Errorf("create body carries ports %v although the config leaves them out", body["ports"])
	}
	if len(got.Ports.Elements()) != 0 || !got.Port.IsNull() {
		t.Errorf("ports/port after create = %s/%s, want no port", got.Ports, got.Port)
	}
}

// Moving an app to `ports = []` patches an empty array, and the stale legacy
// port the console still reports is not read as a primary port.
func TestContainerAppUpdateToNoPort(t *testing.T) {
	h := newHarness(t)
	plan := literalApp(h.f)
	plan.Slug = types.StringValue("worker")
	plan.Port = types.Int64Value(8000)

	created, d := h.create(plan)
	noErrors(t, d)

	next := created
	next.Port = types.Int64Unknown()
	next.Ports = noPorts()
	got, d := h.update(next, h.stateOf(created))
	noErrors(t, d)

	patches := h.f.calls("PATCH", "/crd")
	ports, sent := sentPorts(patches[len(patches)-1].body)
	if !sent || len(ports) != 0 {
		t.Fatalf("patch body ports = %v (present: %v), want []", patches[len(patches)-1].body["ports"], sent)
	}
	if !got.Ports.Equal(noPorts()) || !got.Port.IsNull() {
		t.Errorf("after update: ports %s, port %s; want an empty list and null", got.Ports, got.Port)
	}
}

// An update whose config leaves `ports` out never sends it: PATCH reads an
// absent list as "keep".
func TestContainerAppUpdateWithoutPortsKeepsThem(t *testing.T) {
	h := newHarness(t)
	plan := literalApp(h.f)
	plan.Slug = types.StringValue("orders-api")
	plan.Ports = httpPort(8080)

	created, d := h.create(plan)
	noErrors(t, d)

	next := created
	next.Replicas = types.Int64Value(2)
	cfg := next
	cfg.Ports = types.ListNull(containerAppPortType())
	resp := &fwresource.UpdateResponse{State: h.stateOf(created)}
	h.r.Update(t.Context(), fwresource.UpdateRequest{
		Plan: h.plan(next), Config: h.config(cfg), State: h.stateOf(created),
	}, resp)
	noErrors(t, resp.Diagnostics)

	patches := h.f.calls("PATCH", "/crd")
	if _, ok := patches[len(patches)-1].body["ports"]; ok {
		t.Errorf("patch body carries ports %v although the config leaves them out", patches[len(patches)-1].body["ports"])
	}
	if got := h.load(resp.State); !got.Ports.Equal(created.Ports) {
		t.Errorf("ports after update = %s, want them kept: %s", got.Ports, created.Ports)
	}
}

func TestPortInputsKeepsEmptyApartFromAbsent(t *testing.T) {
	if got, d := portInputs(t.Context(), types.ListNull(containerAppPortType())); d.HasError() || got != nil {
		t.Errorf("omitted ports = %v, want nil (not sent)", got)
	}
	got, d := portInputs(t.Context(), noPorts())
	if d.HasError() || got == nil || len(*got) != 0 {
		t.Fatalf("ports = [] gave %v, want a pointer to an empty slice", got)
	}
	raw := mustJSON(t, console.PatchContainerAppCrdRequestBody{Ports: got})
	if !strings.Contains(raw, `"ports":[]`) {
		t.Errorf("body %s does not carry \"ports\":[]", raw)
	}
}

// The computed `port` keeps its prior value only while `ports` is unchanged:
// once the list changes, the primary port it reports may move or vanish.
func TestPrimaryPortFollowsPorts(t *testing.T) {
	h := newHarness(t)
	prior := literalApp(h.f)
	prior.ID = types.StringValue(h.f.appID.String())
	prior.Ports = httpPort(8080)
	prior.Port = types.Int64Value(8080)

	for name, tc := range map[string]struct {
		config types.List
		want   types.Int64
	}{
		"ports left out":  {config: types.ListNull(containerAppPortType()), want: types.Int64Value(8080)},
		"ports unchanged": {config: httpPort(8080), want: types.Int64Value(8080)},
		"ports emptied":   {config: noPorts(), want: types.Int64Unknown()},
		"port moved":      {config: httpPort(9090), want: types.Int64Unknown()},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, planned := prior, prior
			cfg.Ports, cfg.Port = tc.config, types.Int64Null()
			planned.Port = types.Int64Unknown()
			if !tc.config.IsNull() {
				planned.Ports = tc.config
			}
			req := planmodifier.Int64Request{
				Path: path.Root("port"), Config: h.config(cfg), Plan: h.plan(planned), State: h.stateOf(prior),
				ConfigValue: types.Int64Null(), PlanValue: types.Int64Unknown(), StateValue: prior.Port,
			}
			resp := &planmodifier.Int64Response{PlanValue: req.PlanValue}
			primaryPortFollowsPorts{}.PlanModifyInt64(t.Context(), req, resp)
			noErrors(t, resp.Diagnostics)
			if !resp.PlanValue.Equal(tc.want) {
				t.Errorf("planned port = %s, want %s", resp.PlanValue, tc.want)
			}
		})
	}
}

func portsValue(t *testing.T, entries ...map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	appType, _, _ := containerAppObjectType(t)
	listType, ok := appType.AttributeTypes["ports"].(tftypes.List)
	if !ok {
		t.Fatal("ports is not a list")
	}
	objType, ok := listType.ElementType.(tftypes.Object)
	if !ok {
		t.Fatal("ports elements are not objects")
	}
	elems := []tftypes.Value{}
	for _, e := range entries {
		elems = append(elems, fillNulls(objType, e))
	}
	return tftypes.NewValue(listType, elems)
}

func TestContainerAppNoPortValidation(t *testing.T) {
	literal := map[string]tftypes.Value{"image_repository": str("worker"), "image_tag": str("1")}
	with := func(extra map[string]tftypes.Value) tftypes.Value {
		members := map[string]tftypes.Value{}
		for k, v := range literal {
			members[k] = v
		}
		for k, v := range extra {
			members[k] = v
		}
		return appConfig(t, members)
	}

	if errs := validateContainerApp(t, with(map[string]tftypes.Value{"ports": portsValue(t)})); len(errs) != 0 {
		t.Errorf("ports = [] refused:\n%s", diagText(errs))
	}

	exposed := with(map[string]tftypes.Value{
		"ports": portsValue(t), "expose_to_internet": tftypes.NewValue(tftypes.Bool, true),
	})
	if errs := validateContainerApp(t, exposed); !strings.Contains(diagText(errs), "listens on no port") {
		t.Errorf("exposing an app with no port was not refused as such:\n%s", diagText(errs))
	}

	// The rules for a non-empty list still apply.
	two := portsValue(t,
		map[string]tftypes.Value{"name": str("http"), "port": tftypes.NewValue(tftypes.Number, 8080)},
		map[string]tftypes.Value{"name": str("admin"), "port": tftypes.NewValue(tftypes.Number, 9090)},
	)
	if errs := validateContainerApp(t, with(map[string]tftypes.Value{"ports": two})); !strings.Contains(diagText(errs), "One port must be primary") {
		t.Errorf("two HTTP ports with no primary were accepted:\n%s", diagText(errs))
	}
}

// A health check path on an app with no port has nothing to probe unless it
// names a port of its own: the console refuses it, so the plan does too.
func TestContainerAppNoPortHealthCheckIsRefused(t *testing.T) {
	h := newHarness(t)
	cfg := literalApp(h.f)
	cfg.Ports = noPorts()
	cfg.HealthCheckPath = types.StringValue("/healthz")

	resp := &fwresource.ValidateConfigResponse{}
	h.r.ValidateConfig(t.Context(), fwresource.ValidateConfigRequest{Config: h.config(cfg)}, resp)
	if resp.Diagnostics.ErrorsCount() != 1 {
		t.Fatalf("want one error, got %v", resp.Diagnostics)
	}

	cfg.HealthCheckPort = types.Int64Value(8081)
	resp = &fwresource.ValidateConfigResponse{}
	h.r.ValidateConfig(t.Context(), fwresource.ValidateConfigRequest{Config: h.config(cfg)}, resp)
	if len(resp.Diagnostics) != 0 {
		t.Errorf("a health check with its own port still warns: %v", resp.Diagnostics)
	}
}

// A new app declared with neither `ports` nor `port` listens on no port, so
// its plan refuses a health check without a port; an existing app keeps its
// ports, and is left alone.
func TestNewAppWithoutPortsRefusesAHealthCheckWithoutPort(t *testing.T) {
	h := newHarness(t)
	cfg := literalApp(h.f)
	cfg.HealthCheckPath = types.StringValue("/healthz")

	modify := func(state tfsdk.State) diag.Diagnostics {
		resp := &fwresource.ModifyPlanResponse{Plan: h.plan(cfg)}
		h.r.ModifyPlan(t.Context(), fwresource.ModifyPlanRequest{Config: h.config(cfg), Plan: h.plan(cfg), State: state}, resp)
		return resp.Diagnostics
	}
	if d := modify(h.empty()); d.ErrorsCount() != 1 {
		t.Fatalf("new app without ports: want one error, got %v", d)
	}
	existing := tfsdk.State(h.plan(cfg))
	if d := modify(existing); d.HasError() {
		t.Errorf("existing app: want no error, got %v", d)
	}
	cfg.Port = types.Int64Value(8080)
	if d := modify(h.empty()); d.HasError() {
		t.Errorf("new app with the deprecated port: want no error, got %v", d)
	}
}

func TestPrimaryPortOfAnAppWithNoPort(t *testing.T) {
	stale := int32(8080)
	spec := &console.ContainerAppCrdSpecResponse{Port: &stale, Ports: []console.ContainerAppPortResponse{}}
	if got := primaryPort(spec); !got.IsNull() {
		t.Errorf("primary port of an app with ports = [] = %s, want null", got)
	}
	spec.Ports = []console.ContainerAppPortResponse{{ContainerPort: 8080, Primary: true}}
	if got := primaryPort(spec); got.ValueInt64() != 8080 {
		t.Errorf("primary port of a legacy app = %s, want 8080", got)
	}
}

// TestAccContainerAppNoPortPlans drives a container app through the real
// Terraform CLI against the fake console, which only a plan can show: an app
// created with `ports = []` plans nothing on refresh, gaining and then dropping
// its only port applies cleanly, and leaving `ports` out afterwards plans
// nothing. It needs a Terraform CLI, so it runs with TF_ACC, but no credentials.
func TestAccContainerAppNoPortPlans(t *testing.T) {
	f, _ := newFakeConsole(t)
	creds := fakeConsoleCredentials(t)
	addr := "hyperfluid_container_app.worker"

	config := func(ports string) string {
		return fmt.Sprintf(`
provider "hyperfluid" {
  endpoint         = %q
  organization_id  = %q
  credentials_file = %q
}

resource "hyperfluid_container_app" "worker" {
  env              = %q
  slug             = "worker"
  image_repository = "ghcr.io/acme/worker"
  image_tag        = "1.0.0"
%s}
`, f.url, f.org, creds, f.harbor, ports)
	}
	noPortList := "  ports = []\n"
	onePort := "  ports = [{ name = \"http\", port = 8080 }]\n"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() {},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(noPortList),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "ports.#", "0"),
					resource.TestCheckNoResourceAttr(addr, "port"),
				),
			},
			{
				Config: config(onePort),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectUnknownValue(addr, tfjsonpath.New("port"))},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "ports.#", "1"),
					resource.TestCheckResourceAttr(addr, "ports.0.primary", "true"),
					resource.TestCheckResourceAttr(addr, "port", "8080"),
				),
			},
			{
				Config: config(noPortList),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectUnknownValue(addr, tfjsonpath.New("port"))},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "ports.#", "0"),
					resource.TestCheckNoResourceAttr(addr, "port"),
				),
			},
			{
				Config: config(""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
	if patches := f.calls("PATCH", "/crd"); len(patches) != 2 {
		t.Errorf("patched the spec %d times, want 2 (add the port, then drop it)", len(patches))
	}
}

// A port whose protocol is left to the schema default is sent as HTTP: the
// config carries no default, and the console reads an absent protocol as TCP.
func TestPortInputsSendsTheDefaultProtocol(t *testing.T) {
	cfg := types.ListValueMust(containerAppPortType(), []attr.Value{
		types.ObjectValueMust(attrTypes(containerAppPortType()), map[string]attr.Value{
			"name": types.StringValue("http"), "port": types.Int64Value(8080),
			"protocol": types.StringNull(), "primary": types.BoolNull(),
		}),
	})
	got, d := portInputs(t.Context(), cfg)
	if d.HasError() || got == nil || len(*got) != 1 {
		t.Fatalf("portInputs = %v, %v", got, d)
	}
	p := (*got)[0]
	if p.Protocol == nil || *p.Protocol != console.PortProtocolTCP || p.AppProtocol == nil || *p.AppProtocol != console.AppProtocolHttp {
		t.Errorf("default protocol sent as %v/%v, want TCP/http", p.Protocol, p.AppProtocol)
	}
}
