// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

func strList(values ...string) types.List {
	elems := make([]attr.Value, 0, len(values))
	for _, v := range values {
		elems = append(elems, types.StringValue(v))
	}
	return types.ListValueMust(types.StringType, elems)
}

func attrTypes(t attr.Type) map[string]attr.Type {
	o, _ := t.(types.ObjectType)
	return o.AttrTypes
}

// obj reads a JSON object out of a decoded request body.
func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func plainVar(name, value string) containerAppEnvVarModel {
	return containerAppEnvVarModel{Name: types.StringValue(name), Value: types.StringValue(value), Secret: types.StringNull()}
}

func secretVar(name, secret string) containerAppEnvVarModel {
	return containerAppEnvVarModel{Name: types.StringValue(name), Value: types.StringNull(), Secret: types.StringValue(secret)}
}

func envList(vars ...containerAppEnvVarModel) types.List {
	elems := []attr.Value{}
	for _, v := range vars {
		elems = append(elems, types.ObjectValueMust(attrTypes(containerAppEnvVarType()),
			map[string]attr.Value{"name": v.Name, "value": v.Value, "secret": v.Secret}))
	}
	return types.ListValueMust(containerAppEnvVarType(), elems)
}

// envVars is a list of plain variables, given as name, value pairs.
func envVars(pairs ...string) types.List {
	vars := []containerAppEnvVarModel{}
	for i := 0; i+1 < len(pairs); i += 2 {
		vars = append(vars, plainVar(pairs[i], pairs[i+1]))
	}
	return envList(vars...)
}

// initContainer is an init container on the app's own image, inheriting
// everything, as the schema defaults plan it.
func initContainer(name string, edit func(*containerAppInitContainerModel)) containerAppInitContainerModel {
	m := containerAppInitContainerModel{
		Name: types.StringValue(name), ImageRepository: types.StringNull(), ImageTag: types.StringNull(),
		Command: types.ListNull(types.StringType), Args: types.ListNull(types.StringType),
		InheritEnv: types.BoolValue(true), MountPersistence: types.BoolValue(true),
	}
	if edit != nil {
		edit(&m)
	}
	return m
}

func initContainers(t *testing.T, ics ...containerAppInitContainerModel) types.List {
	t.Helper()
	l, d := types.ListValueFrom(t.Context(), containerAppInitContainerType(), ics)
	noErrors(t, d)
	return l
}

func emptyList(elem attr.Type) types.List { return types.ListValueMust(elem, []attr.Value{}) }

var workloadKeys = []string{"env", "secret_refs", "image_pull_secrets", "command", "args", "init_containers"}

// Everything the workload sets reaches the create request, and what the
// platform reports back equals the plan, so Terraform sees a consistent result.
func TestContainerAppCreateSendsWorkload(t *testing.T) {
	h := newHarness(t)
	plan := literalApp(h.f)
	plan.Slug = types.StringValue("orders-api")
	plan.EnvVars = envList(plainVar("DB_HOST", "db"), secretVar("DB_PASSWORD", "production/db"),
		plainVar("DB_URL", "postgres://$(DB_HOST)"))
	plan.ImagePullSecrets = strList("production/registry")
	plan.Command = strList("/app/server")
	plan.Args = strList("--port", "8080")
	plan.InitContainers = initContainers(t,
		initContainer("migrate", func(m *containerAppInitContainerModel) { m.Args = strList("migrate", "up") }),
		initContainer("fetch", func(m *containerAppInitContainerModel) {
			m.ImageRepository, m.ImageTag = types.StringValue("busybox"), types.StringValue("1.37")
			m.Command = strList("sh", "-c", "")
			m.InheritEnv, m.MountPersistence = types.BoolValue(false), types.BoolValue(false)
		}),
	)

	got, d := h.create(plan)
	noErrors(t, d)

	body := h.f.calls("POST", "/container-apps/crd")[0].body
	env, _ := body["env"].([]any)
	if len(env) != 2 || obj(env[0])["name"] != "DB_HOST" || obj(env[1])["value"] != "postgres://$(DB_HOST)" {
		t.Errorf("create body env = %v, want both plain variables in order", body["env"])
	}
	if refs, _ := body["secret_refs"].([]any); len(refs) != 1 ||
		obj(refs[0])["env_var_name"] != "DB_PASSWORD" || obj(refs[0])["secret_name"] != "production/db" {
		t.Errorf("create body secret_refs = %v", body["secret_refs"])
	}
	ics, _ := body["init_containers"].([]any)
	if len(ics) != 2 {
		t.Fatalf("create body init_containers = %v, want 2", body["init_containers"])
	}
	migrate := obj(ics[0])
	for _, key := range []string{"image_repository", "image_tag", "command"} {
		if _, ok := migrate[key]; ok {
			t.Errorf("an init container on the app's image sent %q: %v", key, migrate)
		}
	}
	if fetch := obj(ics[1]); fetch["image_repository"] != "busybox" || fetch["inherit_env"] != false {
		t.Errorf("init_containers[1] = %v", fetch)
	}

	for name, want := range plan.workloadLists() {
		if want.IsNull() {
			continue // unknown in a real plan: anything the API reports is accepted
		}
		if have := got.workloadLists()[name]; !have.Equal(want) {
			t.Errorf("%s = %v, want %v", name, have, want)
		}
	}
}

// Lists the configuration leaves out are never sent, and come back as what the
// app has: nothing.
func TestContainerAppCreateLeavesOutUnsetLists(t *testing.T) {
	h := newHarness(t)
	got, d := h.create(literalApp(h.f))
	noErrors(t, d)

	body := h.f.calls("POST", "/container-apps/crd")[0].body
	for _, key := range workloadKeys {
		if _, ok := body[key]; ok {
			t.Errorf("create body carries %q, which the config does not set: %v", key, body)
		}
	}
	for name, l := range got.workloadLists() {
		if l.IsNull() || l.IsUnknown() || len(l.Elements()) != 0 {
			t.Errorf("%s = %v, want a known empty list", name, l)
		}
	}
}

// An init container an app template provisioned survives an update of a
// configuration that never managed init containers: the list is planned as the
// app has it and left out of the PATCH.
func TestContainerAppUpdateKeepsAnUnmanagedList(t *testing.T) {
	h := newHarness(t)
	created, d := h.create(literalApp(h.f))
	noErrors(t, d)

	cmd := []string{"/bin/sh", "-c"}
	h.f.spec.InitContainers = &[]console.InitContainerResponse{{
		Name: "bootstrap-oidc", Command: cmd, Args: []string{"echo bootstrap"}, InheritEnv: true, MountPersistence: true,
	}}
	h.f.spec.SecretRefs = []console.SecretRefSpecResponse{{EnvVarName: "OIDC_SECRET", SecretName: "production/oidc"}}
	refreshed, err := h.r.readState(t.Context(), created.ID.ValueString(), importedApp())
	if err != nil {
		t.Fatalf("readState: %v", err)
	}
	if n := len(refreshed.InitContainers.Elements()); n != 1 {
		t.Fatalf("read init_containers = %v, want the template's entry", refreshed.InitContainers)
	}
	if want := envList(secretVar("OIDC_SECRET", "production/oidc")); !refreshed.EnvVars.Equal(want) {
		t.Fatalf("read env_vars = %v, want the template's secret-backed variable", refreshed.EnvVars)
	}

	next := refreshed
	next.InitContainers = plannedUnsetList(refreshed.InitContainers, false)
	next.EnvVars = plannedUnsetList(refreshed.EnvVars, false)
	next.Replicas = types.Int64Value(2)
	got, d := h.update(next, h.stateOf(refreshed))
	noErrors(t, d)

	patch := h.f.calls("PATCH", "/crd")[0].body
	for _, key := range workloadKeys {
		if _, ok := patch[key]; ok {
			t.Errorf("an update that changes no workload list sent %q: %v", key, patch)
		}
	}
	if !got.InitContainers.Equal(refreshed.InitContainers) || !got.EnvVars.Equal(refreshed.EnvVars) {
		t.Errorf("init_containers = %v, env_vars = %v, want the template's entries kept", got.InitContainers, got.EnvVars)
	}
}

// A list Terraform applied and the configuration then dropped is planned empty,
// which the PATCH sends as [] to clear it.
func TestContainerAppUpdateClearsARemovedList(t *testing.T) {
	h := newHarness(t)
	plan := literalApp(h.f)
	plan.EnvVars = envList(plainVar("LOG_LEVEL", "debug"), secretVar("TOKEN", "production/token"))
	plan.InitContainers = initContainers(t, initContainer("migrate", nil))
	created, d := h.create(plan)
	noErrors(t, d)

	next := created
	next.EnvVars = plannedUnsetList(created.EnvVars, true)
	next.InitContainers = plannedUnsetList(created.InitContainers, true)
	got, d := h.update(next, h.stateOf(created))
	noErrors(t, d)

	patch := h.f.calls("PATCH", "/crd")[0].body
	for _, key := range []string{"env", "secret_refs", "init_containers"} {
		if v, ok := patch[key].([]any); !ok || len(v) != 0 {
			t.Errorf("PATCH %s = %v, want []", key, patch[key])
		}
	}
	if len(got.EnvVars.Elements()) != 0 || len(got.InitContainers.Elements()) != 0 {
		t.Errorf("state = %v / %v, want both cleared", got.EnvVars, got.InitContainers)
	}
}

// Changing one variable sends both halves of env_vars, since PATCH replaces
// each on its own; the other untouched lists stay out.
func TestContainerAppUpdateReplacesAChangedList(t *testing.T) {
	h := newHarness(t)
	plan := literalApp(h.f)
	plan.EnvVars = envList(plainVar("LOG_LEVEL", "debug"), secretVar("TOKEN", "production/token"))
	plan.Command = strList("/app/server")
	created, d := h.create(plan)
	noErrors(t, d)

	next := created
	next.EnvVars = envList(plainVar("LOG_LEVEL", "info"), secretVar("TOKEN", "production/token"), plainVar("REGION", "eu"))
	got, d := h.update(next, h.stateOf(created))
	noErrors(t, d)

	patch := h.f.calls("PATCH", "/crd")[0].body
	if env, _ := patch["env"].([]any); len(env) != 2 {
		t.Errorf("PATCH env = %v, want the 2 plain variables", patch["env"])
	}
	if refs, _ := patch["secret_refs"].([]any); len(refs) != 1 || obj(refs[0])["env_var_name"] != "TOKEN" {
		t.Errorf("PATCH secret_refs = %v, want the unchanged secret-backed variable", patch["secret_refs"])
	}
	if _, ok := patch["command"]; ok {
		t.Errorf("an unchanged command was re-sent: %v", patch)
	}
	if !got.EnvVars.Equal(next.EnvVars) {
		t.Errorf("env_vars = %v, want %v", got.EnvVars, next.EnvVars)
	}
}

// The API lists plain variables before secret-backed ones. A read keeps the
// order the configuration interleaved them in, and falls back to the API's
// order once the variables differ, as on import.
func TestContainerAppReadOrderOfEnvVars(t *testing.T) {
	h := newHarness(t)
	plan := literalApp(h.f)
	interleaved := envList(secretVar("TOKEN", "production/token"), plainVar("A", "1"), plainVar("B", "2"))
	plan.EnvVars = interleaved
	created, d := h.create(plan)
	noErrors(t, d)
	if !created.EnvVars.Equal(interleaved) {
		t.Fatalf("created env_vars = %v, want the configured order", created.EnvVars)
	}

	refreshed, err := h.r.readState(t.Context(), created.ID.ValueString(), created)
	if err != nil {
		t.Fatalf("readState: %v", err)
	}
	if !refreshed.EnvVars.Equal(interleaved) {
		t.Errorf("refreshed env_vars = %v, want the prior order", refreshed.EnvVars)
	}

	apiOrder := envList(plainVar("A", "1"), plainVar("B", "2"), secretVar("TOKEN", "production/token"))
	imported, err := h.r.readState(t.Context(), created.ID.ValueString(), importedApp())
	if err != nil {
		t.Fatalf("readState: %v", err)
	}
	if !imported.EnvVars.Equal(apiOrder) {
		t.Errorf("imported env_vars = %v, want plain variables first", imported.EnvVars)
	}

	h.f.spec.Env[1].Value = "changed"
	drifted, err := h.r.readState(t.Context(), created.ID.ValueString(), created)
	if err != nil {
		t.Fatalf("readState: %v", err)
	}
	want := envList(plainVar("A", "1"), plainVar("B", "changed"), secretVar("TOKEN", "production/token"))
	if !drifted.EnvVars.Equal(want) {
		t.Errorf("drifted env_vars = %v, want %v", drifted.EnvVars, want)
	}
}

// What the plan modifier decides for a list the configuration leaves out,
// driven through the provider protocol with the private state an earlier apply
// recorded.
func TestContainerAppPlanOfAListLeftOutOfTheConfig(t *testing.T) {
	h := newHarness(t)
	plan := literalApp(h.f)
	plan.Slug = types.StringValue("orders-api")
	plan.EnvVars = envVars("LOG_LEVEL", "debug")
	prior, d := h.create(plan)
	noErrors(t, d)

	cfg := literalApp(h.f)
	cfg.Slug = types.StringValue("orders-api")

	for name, c := range map[string]struct {
		managed []string
		want    types.List
	}{
		"applied before":   {managed: []string{"env_vars"}, want: emptyList(containerAppEnvVarType())},
		"never applied":    {managed: nil, want: prior.EnvVars},
		"another applied":  {managed: []string{"command"}, want: prior.EnvVars},
		"no private state": {want: prior.EnvVars},
	} {
		t.Run(name, func(t *testing.T) {
			var private []byte
			if c.managed != nil {
				names, _ := json.Marshal(c.managed)
				private, _ = json.Marshal(map[string][]byte{managedKey: names})
			}
			planned := h.planThroughProtocol(prior, cfg, private)
			if !planned.EnvVars.Equal(c.want) {
				t.Errorf("planned env_vars = %v, want %v", planned.EnvVars, c.want)
			}
			if !planned.InitContainers.Equal(prior.InitContainers) {
				t.Errorf("planned init_containers = %v, want the prior value", planned.InitContainers)
			}
		})
	}
}

// The recorded private state lists exactly the workload lists the config sets.
func TestRecordManagedLists(t *testing.T) {
	h := newHarness(t)
	cfg := literalApp(h.f)
	cfg.EnvVars = envVars()
	cfg.InitContainers = initContainers(t, initContainer("migrate", nil))
	p := &fakePrivate{}
	noErrors(t, recordManagedAttributes(t.Context(), p, cfg))
	if got := string(p.data[managedKey]); got != `["env_vars","init_containers"]` {
		t.Errorf("recorded %s", got)
	}
	managed, d := managedAttributes(t.Context(), p)
	noErrors(t, d)
	if !managed["env_vars"] || !managed["init_containers"] || managed["command"] {
		t.Errorf("read back %v", managed)
	}
}

// An organization without the command override feature gets a diagnostic that
// names it, and an app that already has a command keeps updating there as
// long as the command itself does not change.
func TestContainerAppCommandOverrideRefused(t *testing.T) {
	h := newHarness(t)
	plan := literalApp(h.f)
	plan.Command = strList("/app/server")
	created, d := h.create(plan)
	noErrors(t, d)

	h.f.noCommandOverride = true
	next := created
	next.Replicas = types.Int64Value(2)
	updated, d := h.update(next, h.stateOf(created))
	noErrors(t, d)

	next = updated
	next.Args = strList("--debug")
	_, d = h.update(next, h.stateOf(updated))
	if !d.HasError() || d.Errors()[0].Summary() != "Command override not enabled" ||
		!strings.Contains(d.Errors()[0].Detail(), "container command override") {
		t.Fatalf("diagnostics = %v, want the command override refusal", d)
	}

	// An init container's command is gated the same way.
	h2 := newHarness(t)
	h2.f.noCommandOverride = true
	plan = literalApp(h2.f)
	plan.InitContainers = initContainers(t, initContainer("migrate", func(m *containerAppInitContainerModel) {
		m.Command = strList("/app/migrate")
	}))
	_, d = h2.create(plan)
	if !d.HasError() || d.Errors()[0].Summary() != "Command override not enabled" {
		t.Fatalf("diagnostics = %v, want the command override refusal", d)
	}

	// Clearing a command is always allowed.
	next = updated
	next.Command = emptyList(types.StringType)
	_, d = h.update(next, h.stateOf(updated))
	noErrors(t, d)
}

// The API's cross-entry rules are refused at plan time.
func TestContainerAppWorkloadValidation(t *testing.T) {
	cases := map[string]struct {
		edit func(t *testing.T, m *containerAppModel)
		want string
	}{
		"init container named after the app": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.InitContainers = initContainers(t, initContainer("orders-api", nil))
			},
			want: "Init container named after the app",
		},
		"duplicate init containers": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.InitContainers = initContainers(t, initContainer("migrate", nil), initContainer("migrate", nil))
			},
			want: "Duplicate init container",
		},
		"image without a tag": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.InitContainers = initContainers(t, initContainer("fetch", func(m *containerAppInitContainerModel) {
					m.ImageRepository = types.StringValue("busybox")
				}))
			},
			want: "Invalid Attribute Combination",
		},
		"blank init container executable": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.InitContainers = initContainers(t, initContainer("migrate", func(m *containerAppInitContainerModel) {
					m.Command = strList(" ", "up")
				}))
			},
			want: "Blank executable",
		},
		"empty init container command": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.InitContainers = initContainers(t, initContainer("migrate", func(m *containerAppInitContainerModel) {
					m.Command = strList()
				}))
			},
			want: "Invalid Attribute Value",
		},
		"invalid init container name": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.InitContainers = initContainers(t, initContainer("Migrate_DB", nil))
			},
			want: "Invalid Attribute Value Match",
		},
		"five init containers": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.InitContainers = initContainers(t, initContainer("a", nil), initContainer("b", nil),
					initContainer("c", nil), initContainer("d", nil), initContainer("e", nil))
			},
			want: "Invalid Attribute Value",
		},
		"blank main executable": {
			edit: func(t *testing.T, m *containerAppModel) { m.Command = strList("") },
			want: "Blank executable",
		},
		"variable set twice": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.EnvVars = envList(plainVar("TOKEN", "x"), secretVar("TOKEN", "production/token"))
			},
			want: "Duplicate environment variable",
		},
		"private secret reference": {
			edit: func(t *testing.T, m *containerAppModel) { m.EnvVars = envList(secretVar("TOKEN", "users/alice/token")) },
			want: "Private secret",
		},
		"variable with a value and a secret": {
			edit: func(t *testing.T, m *containerAppModel) {
				v := plainVar("TOKEN", "x")
				v.Secret = types.StringValue("production/token")
				m.EnvVars = envList(v)
			},
			want: "Invalid Attribute Combination",
		},
		"variable with neither a value nor a secret": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.EnvVars = envList(containerAppEnvVarModel{Name: types.StringValue("TOKEN"), Value: types.StringNull(), Secret: types.StringNull()})
			},
			want: "Invalid Attribute Combination",
		},
		"value referencing a secret-backed variable": {
			edit: func(t *testing.T, m *containerAppModel) {
				m.EnvVars = envList(plainVar("DB_URL", "postgres://app:$(DB_PASSWORD)@db"), secretVar("DB_PASSWORD", "production/db"))
			},
			want: "Reference to a secret-backed variable",
		},
		"health check port without a path": {
			edit: func(t *testing.T, m *containerAppModel) { m.HealthCheckPort = types.Int64Value(8081) },
			want: "Invalid Attribute Combination",
		},
		"private pull secret": {
			edit: func(t *testing.T, m *containerAppModel) { m.ImagePullSecrets = strList("users") },
			want: "Private secret",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			cfg := literalApp(h.f)
			cfg.Slug = types.StringValue("orders-api")
			c.edit(t, &cfg)
			diags := h.validateThroughProtocol(cfg)
			var summaries []string
			for _, d := range diags {
				if d.Severity == tfprotov6.DiagnosticSeverityError {
					summaries = append(summaries, d.Summary)
				}
			}
			if len(summaries) != 1 || summaries[0] != c.want {
				t.Errorf("errors = %q, want exactly %q", summaries, c.want)
			}
		})
	}

	// The valid configuration the cases start from passes.
	h := newHarness(t)
	cfg := literalApp(h.f)
	cfg.Slug = types.StringValue("orders-api")
	cfg.EnvVars = envList(plainVar("A", "1"), secretVar("B", "production/b"), plainVar("C", "$(A) $$(B)"))
	cfg.InitContainers = initContainers(t, initContainer("migrate", func(m *containerAppInitContainerModel) {
		m.Command = strList("sh", "-c", "")
	}))
	for _, d := range h.validateThroughProtocol(cfg) {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Errorf("unexpected error %s: %s", d.Summary, d.Detail)
		}
	}
}

// Removing the health check path removes the health check: PATCH leaves an
// omitted path as it is, so the removal is sent as clear_health_check.
func TestContainerAppUpdateRemovesTheHealthCheck(t *testing.T) {
	h := newHarness(t)
	plan := literalApp(h.f)
	plan.HealthCheckPath = types.StringValue("/healthz")
	plan.HealthCheckPort = types.Int64Value(8081)
	created, d := h.create(plan)
	noErrors(t, d)

	next := created
	next.Replicas = types.Int64Value(2)
	updated, d := h.update(next, h.stateOf(created))
	noErrors(t, d)
	if _, ok := h.f.calls("PATCH", "/crd")[0].body["clear_health_check"]; ok {
		t.Error("an update keeping the health check sent clear_health_check")
	}

	next = updated
	next.HealthCheckPath, next.HealthCheckPort = types.StringNull(), types.Int64Null()
	got, d := h.update(next, h.stateOf(updated))
	noErrors(t, d)
	if patch := h.f.calls("PATCH", "/crd")[1].body; patch["clear_health_check"] != true {
		t.Errorf("PATCH = %v, want clear_health_check", patch)
	}
	if !got.HealthCheckPath.IsNull() || !got.HealthCheckPort.IsNull() {
		t.Errorf("health check = %v:%v, want none", got.HealthCheckPath, got.HealthCheckPort)
	}
}

// ── protocol helpers ──────────────────────────────────────────────────────

type fakePrivate struct{ data map[string][]byte }

func (p *fakePrivate) GetKey(_ context.Context, key string) ([]byte, diag.Diagnostics) {
	return p.data[key], nil
}

func (p *fakePrivate) SetKey(_ context.Context, key string, value []byte) diag.Diagnostics {
	if p.data == nil {
		p.data = map[string][]byte{}
	}
	p.data[key] = value
	return nil
}

func (h *appHarness) server() tfprotov6.ProviderServer {
	srv, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		h.t.Fatalf("provider server: %v", err)
	}
	return srv
}

func (h *appHarness) dynamic(m containerAppModel) *tfprotov6.DynamicValue {
	s := h.stateOf(m)
	dv, err := tfprotov6.NewDynamicValue(h.schema().Type().TerraformType(h.t.Context()), s.Raw)
	if err != nil {
		h.t.Fatalf("dynamic value: %v", err)
	}
	return &dv
}

func (h *appHarness) validateThroughProtocol(cfg containerAppModel) []*tfprotov6.Diagnostic {
	resp, err := h.server().ValidateResourceConfig(h.t.Context(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: "hyperfluid_container_app", Config: h.dynamic(cfg),
	})
	if err != nil {
		h.t.Fatalf("validate: %v", err)
	}
	return resp.Diagnostics
}

// planThroughProtocol plans cfg against prior as Terraform would, with the
// proposed new state taking every value the config leaves out from prior.
func (h *appHarness) planThroughProtocol(prior, cfg containerAppModel, private []byte) containerAppModel {
	resp, err := h.server().PlanResourceChange(h.t.Context(), &tfprotov6.PlanResourceChangeRequest{
		TypeName: "hyperfluid_container_app", PriorState: h.dynamic(prior), ProposedNewState: h.dynamic(prior),
		Config: h.dynamic(cfg), PriorPrivate: private,
	})
	if err != nil {
		h.t.Fatalf("plan: %v", err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			h.t.Fatalf("plan: %s: %s", d.Summary, d.Detail)
		}
	}
	raw, err := resp.PlannedState.Unmarshal(h.schema().Type().TerraformType(h.t.Context()))
	if err != nil {
		h.t.Fatalf("planned state: %v", err)
	}
	return h.load(tfsdk.State{Schema: h.schema(), Raw: raw})
}
