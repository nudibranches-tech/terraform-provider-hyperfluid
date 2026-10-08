// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/client"
	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// fakeConsole is the slice of the console API a container app touches: enough to
// drive the resource's Create / Read / Update end to end and to see exactly what
// it sends.
type fakeConsole struct {
	t      *testing.T
	org    uuid.UUID
	harbor uuid.UUID
	appID  uuid.UUID

	mu       sync.Mutex
	exists   bool
	spec     console.ContainerAppCrdSpecResponse
	version  int
	status   *console.ImageSourceStatus // what a source resolves to; nil = not synced yet
	requests []fakeRequest
}

type fakeRequest struct {
	method, path string
	body         map[string]any
}

func newFakeConsole(t *testing.T) (*fakeConsole, *providerData) {
	t.Helper()
	f := &fakeConsole{t: t, org: uuid.New(), harbor: uuid.New(), appID: uuid.New()}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	api, err := client.New(srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return f, &providerData{API: api, OrgID: f.org.String()}
}

func (f *fakeConsole) record(method, path string, body []byte) map[string]any {
	var parsed map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &parsed); err != nil {
			f.t.Errorf("%s %s: body is not JSON: %v", method, path, err)
		}
	}
	f.requests = append(f.requests, fakeRequest{method: method, path: path, body: parsed})
	return parsed
}

// calls returns the recorded requests whose path ends with suffix.
func (f *fakeConsole) calls(method, suffix string) []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeRequest
	for _, r := range f.requests {
		if r.method == method && strings.HasSuffix(r.path, suffix) {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeConsole) bump() {
	f.version++
	f.spec.ResourceVersion = strconv.Itoa(f.version)
}

func (f *fakeConsole) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	parsed := f.record(r.Method, r.URL.Path, body)
	reply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if v != nil {
			_ = json.NewEncoder(w).Encode(v)
		}
	}

	p := r.URL.Path
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/container-apps/crd"):
		var req console.CreateContainerAppCrdRequestBody
		if err := json.Unmarshal(body, &req); err != nil {
			reply(400, map[string]string{"message": err.Error()})
			return
		}
		f.exists = true
		f.spec = console.ContainerAppCrdSpecResponse{
			Enabled: true, Replicas: 1,
			BucketRefs: []console.BucketRefSpecResponse{}, CustomDomains: []console.CustomDomainResponse{},
			Env: []console.EnvVarSpecResponse{}, FileMounts: []console.FileMountResponse{},
			ImagePullSecrets: []string{}, Persistence: []console.PersistenceResponse{},
			Ports: []console.ContainerAppPortResponse{}, SecretRefs: []console.SecretRefSpecResponse{},
		}
		if req.ImageRepository != nil {
			f.spec.ImageRepository = *req.ImageRepository
		}
		if req.ImageTag != nil {
			f.spec.ImageTag = *req.ImageTag
		}
		f.attach(req.ImageSource)
		f.bump()
		reply(201, nil)
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/container-apps"):
		reply(200, []console.ContainerAppResponse{f.statusResponse()})
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/crd"):
		reply(200, f.spec)
	case r.Method == http.MethodGet:
		reply(200, f.statusResponse())
	case r.Method == http.MethodPatch && strings.HasSuffix(p, "/crd"):
		var req console.PatchContainerAppCrdRequestBody
		if err := json.Unmarshal(body, &req); err != nil {
			reply(400, map[string]string{"message": err.Error()})
			return
		}
		if (req.ImageRepository != nil || req.ImageTag != nil) && f.spec.ImageSource != nil {
			reply(409, map[string]string{"message": "the image is managed by Git; detach the source to set it here"})
			return
		}
		if req.ResourceVersion != nil && *req.ResourceVersion != f.spec.ResourceVersion {
			reply(409, map[string]string{"message": "stale resource_version"})
			return
		}
		if req.ImageRepository != nil {
			f.spec.ImageRepository = *req.ImageRepository
		}
		if req.ImageTag != nil {
			f.spec.ImageTag = *req.ImageTag
		}
		if req.Replicas != nil {
			f.spec.Replicas = *req.Replicas
		}
		if req.ImageSource != nil {
			f.attach(req.ImageSource)
		}
		f.bump()
		reply(204, nil)
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/image-source/detach"):
		if f.spec.ImageSource == nil {
			reply(409, map[string]string{"message": "no Git source is attached"})
			return
		}
		f.spec.ImageSource, f.spec.ImageSourceStatus = nil, nil
		f.bump()
		reply(204, nil)
	default:
		f.t.Errorf("unexpected request %s %s (body %v)", r.Method, p, parsed)
		reply(404, map[string]string{"message": "unexpected"})
	}
}

func (f *fakeConsole) attach(src *console.ImageSource) {
	if src == nil {
		return
	}
	f.spec.ImageSource = src
	f.spec.ImageRepository, f.spec.ImageTag = "", ""
	f.spec.ImageSourceStatus = f.status
	if f.status != nil && f.status.Resolved != nil {
		f.spec.ImageRepository = f.status.Resolved.Repository
		if f.status.Resolved.Tag != nil {
			f.spec.ImageTag = *f.status.Resolved.Tag
		}
	}
	yes := true
	f.spec.AlertOnSyncFailure = &yes
}

func (f *fakeConsole) statusResponse() console.ContainerAppResponse {
	return console.ContainerAppResponse{
		Id: f.appID, HarborId: f.harbor, OrganizationId: f.org, Name: "orders-api", Slug: "orders-api",
		DesiredReplicas: f.spec.Replicas, AvailableReplicas: f.spec.Replicas, UpdatedReadyReplicas: f.spec.Replicas,
		Replicas: f.spec.Replicas, Tags: []string{},
	}
}

func resolved() *console.ImageSourceStatus {
	at, ref, rev, tag := "2026-10-08T10:00:00Z", "main", "4f9c2e1", "1.4.2"
	return &console.ImageSourceStatus{
		Resolved:    &console.ResolvedImage{Repository: "ghcr.io/acme/orders-api", Tag: &tag},
		ResolvedRef: &ref, Revision: &rev, LastSyncedAt: &at,
	}
}

// ── model helpers ─────────────────────────────────────────────────────────

func nullApp(f *fakeConsole) containerAppModel {
	return containerAppModel{
		ID: types.StringNull(), Env: types.StringValue(f.harbor.String()), Name: types.StringValue("orders-api"),
		ImageRepository: types.StringNull(), ImageTag: types.StringNull(), Port: types.Int64Null(),
		Replicas: types.Int64Null(), Enabled: types.BoolValue(true), ExposeToInternet: types.BoolValue(false),
		ResourceTier: types.StringNull(), HealthCheckPath: types.StringNull(), HealthCheckPort: types.Int64Null(),
		ResourceVersion: types.StringNull(), CPURequest: types.StringNull(), CPULimit: types.StringNull(),
		MemoryRequest: types.StringNull(), MemoryLimit: types.StringNull(), Phase: types.StringNull(),
		Endpoint: types.StringNull(), DesiredReplicas: types.Int64Null(), AvailableReplicas: types.Int64Null(),
		Slug: types.StringNull(), Ports: types.ListNull(containerAppPortType()),
		ImageSource: nullImageSource(), ResolvedImage: types.StringNull(), ResolvedRef: types.StringNull(),
		Revision: types.StringNull(), LastSyncedAt: types.StringNull(), SyncError: types.StringNull(),
	}
}

func sourceModel(t *testing.T, edit func(*gitSourceModel)) types.Object {
	m := nullGitModel()
	m.Provider = types.StringValue("github")
	m.Repository = types.StringValue("acme/orders-api")
	if edit != nil {
		edit(&m)
	}
	return objectOf(t, m)
}

type appHarness struct {
	t *testing.T
	r *containerAppResource
	f *fakeConsole
}

func newHarness(t *testing.T) *appHarness {
	f, pd := newFakeConsole(t)
	r := &containerAppResource{p: pd}
	return &appHarness{t: t, r: r, f: f}
}

func (h *appHarness) schema() rschema.Schema {
	return resourceSchema(h.t, h.r).Schema
}

func (h *appHarness) empty() tfsdk.State {
	s := h.schema()
	return tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(h.t.Context()), nil)}
}

func (h *appHarness) plan(m containerAppModel) tfsdk.Plan {
	p := tfsdk.Plan{Schema: h.schema(), Raw: tftypes.NewValue(h.schema().Type().TerraformType(h.t.Context()), nil)}
	if d := p.Set(h.t.Context(), m); d.HasError() {
		h.t.Fatalf("plan: %v", d.Errors())
	}
	return p
}

func (h *appHarness) config(m containerAppModel) tfsdk.Config {
	p := h.plan(m)
	return tfsdk.Config(p)
}

func (h *appHarness) create(m containerAppModel) (containerAppModel, diag.Diagnostics) {
	resp := &fwresource.CreateResponse{State: h.empty()}
	h.r.Create(h.t.Context(), fwresource.CreateRequest{Plan: h.plan(m), Config: h.config(m)}, resp)
	return h.load(resp.State), resp.Diagnostics
}

func (h *appHarness) update(plan containerAppModel, state tfsdk.State) (containerAppModel, diag.Diagnostics) {
	resp := &fwresource.UpdateResponse{State: state}
	h.r.Update(h.t.Context(), fwresource.UpdateRequest{Plan: h.plan(plan), Config: h.config(plan), State: state}, resp)
	return h.load(resp.State), resp.Diagnostics
}

func (h *appHarness) load(s tfsdk.State) containerAppModel {
	var m containerAppModel
	if !s.Raw.IsNull() {
		if d := s.Get(h.t.Context(), &m); d.HasError() {
			h.t.Fatalf("state: %v", d.Errors())
		}
	}
	return m
}

func (h *appHarness) stateOf(m containerAppModel) tfsdk.State {
	s := h.empty()
	if d := s.Set(h.t.Context(), m); d.HasError() {
		h.t.Fatalf("state: %v", d.Errors())
	}
	return s
}

func noErrors(t *testing.T, d diag.Diagnostics) {
	t.Helper()
	if d.HasError() {
		t.Fatalf("diagnostics: %v", d.Errors())
	}
}

// ── scenarios ─────────────────────────────────────────────────────────────

// A created Git-sourced app sends only what the user wrote, leaves the literal
// image out of the request and out of state, and reports what the platform
// resolved through the computed attributes.
func TestContainerAppCreateWithGitSource(t *testing.T) {
	h := newHarness(t)
	h.f.status = resolved()

	plan := nullApp(h.f)
	plan.ImageSource = sourceModel(t, nil)
	got, d := h.create(plan)
	noErrors(t, d)

	creates := h.f.calls("POST", "/container-apps/crd")
	if len(creates) != 1 {
		t.Fatalf("creates = %d, want 1", len(creates))
	}
	body := creates[0].body
	for _, key := range []string{"image_repository", "image_tag", "alert_on_sync_failure"} {
		if _, ok := body[key]; ok {
			t.Errorf("create body carries %q, which the user did not set: %v", key, body)
		}
	}
	git, _ := body["image_source"].(map[string]any)["git"].(map[string]any)
	if len(git) != 2 || git["provider"] != "github" || git["repository"] != "acme/orders-api" {
		t.Errorf("image_source.git = %v, want only provider and repository", git)
	}

	if !got.ImageRepository.IsNull() || !got.ImageTag.IsNull() {
		t.Errorf("the resolved image leaked into image_repository/image_tag: %v %v", got.ImageRepository, got.ImageTag)
	}
	if got.ResolvedImage.ValueString() != "ghcr.io/acme/orders-api:1.4.2" ||
		got.ResolvedRef.ValueString() != "main" || got.Revision.ValueString() != "4f9c2e1" ||
		got.LastSyncedAt.ValueString() != "2026-10-08T10:00:00Z" || !got.SyncError.IsNull() {
		t.Errorf("computed status = %v %v %v %v %v", got.ResolvedImage, got.ResolvedRef, got.Revision, got.LastSyncedAt, got.SyncError)
	}
	// What was planned is what is stored: nothing the user left out came back
	// filled in (the API reports the alert as on, which is the default).
	if !got.ImageSource.Equal(plan.ImageSource) {
		t.Errorf("image_source = %v, want %v", got.ImageSource, plan.ImageSource)
	}
}

// A first check that fails leaves the app created, with the reason in state and a
// warning, instead of failing the apply and orphaning the app.
func TestContainerAppCreateWithFailingSource(t *testing.T) {
	h := newHarness(t)
	msg := "401 Unauthorized"
	h.f.status = &console.ImageSourceStatus{Error: &msg}

	plan := nullApp(h.f)
	plan.ImageSource = sourceModel(t, nil)
	got, d := h.create(plan)
	noErrors(t, d)

	if len(d.Warnings()) == 0 {
		t.Error("a failing first check must be surfaced as a warning")
	}
	if got.ID.IsNull() || got.SyncError.ValueString() != msg || !got.ResolvedImage.IsNull() {
		t.Errorf("state = id %v, sync_error %v, resolved_image %v", got.ID, got.SyncError, got.ResolvedImage)
	}
}

func TestContainerAppCreateWithLiteralImageIsUnchanged(t *testing.T) {
	h := newHarness(t)
	plan := nullApp(h.f)
	plan.ImageRepository = types.StringValue("nginx")
	plan.ImageTag = types.StringValue("1.27")
	got, d := h.create(plan)
	noErrors(t, d)

	body := h.f.calls("POST", "/container-apps/crd")[0].body
	if body["image_repository"] != "nginx" || body["image_tag"] != "1.27" {
		t.Errorf("create body = %v", body)
	}
	if _, ok := body["image_source"]; ok {
		t.Errorf("a literal image must not send image_source: %v", body)
	}
	if got.ImageRepository.ValueString() != "nginx" || got.ImageTag.ValueString() != "1.27" ||
		!got.ImageSource.IsNull() || !got.ResolvedImage.IsNull() {
		t.Errorf("state = %v %v %v %v", got.ImageRepository, got.ImageTag, got.ImageSource, got.ResolvedImage)
	}
}

func TestContainerAppUpdateEditsTheSource(t *testing.T) {
	h := newHarness(t)
	h.f.status = resolved()
	plan := nullApp(h.f)
	plan.ImageSource = sourceModel(t, nil)
	created, d := h.create(plan)
	noErrors(t, d)

	// Same source, another setting: the source is not re-sent.
	next := created
	next.Replicas = types.Int64Value(2)
	updated, d := h.update(next, h.stateOf(created))
	noErrors(t, d)
	patch := h.f.calls("PATCH", "/crd")[0].body
	for _, key := range []string{"image_source", "image_repository", "image_tag", "alert_on_sync_failure"} {
		if _, ok := patch[key]; ok {
			t.Errorf("an unrelated update sent %q: %v", key, patch)
		}
	}

	// A new branch: the source is replaced as a whole, the literal image never sent.
	next = updated
	next.ImageSource = sourceModel(t, func(m *gitSourceModel) { m.Branch = types.StringValue("release") })
	updated, d = h.update(next, h.stateOf(updated))
	noErrors(t, d)
	patch = h.f.calls("PATCH", "/crd")[1].body
	git, _ := patch["image_source"].(map[string]any)["git"].(map[string]any)
	ref, _ := git["ref"].(map[string]any)
	if ref["type"] != "Branch" || ref["name"] != "release" {
		t.Errorf("image_source.git.ref = %v", ref)
	}
	if _, ok := patch["image_repository"]; ok {
		t.Errorf("the literal image was sent next to a source: %v", patch)
	}
	if !updated.ImageSource.Equal(next.ImageSource) {
		t.Errorf("image_source = %v, want %v", updated.ImageSource, next.ImageSource)
	}
}

func TestContainerAppUpdateDropsAnExplicitAlertSetting(t *testing.T) {
	h := newHarness(t)
	h.f.status = resolved()
	off := sourceModel(t, func(m *gitSourceModel) { m.AlertOnSyncFailure = types.BoolValue(false) })
	plan := nullApp(h.f)
	plan.ImageSource = off
	created, d := h.create(plan)
	noErrors(t, d)
	if v := h.f.calls("POST", "/container-apps/crd")[0].body["alert_on_sync_failure"]; v != false {
		t.Errorf("alert_on_sync_failure = %v, want false", v)
	}

	// The setting is removed from the config: the platform default (on) is
	// restored explicitly, since an omitted field would leave it as it was.
	next := created
	next.ImageSource = sourceModel(t, nil)
	_, d = h.update(next, h.stateOf(created))
	noErrors(t, d)
	patch := h.f.calls("PATCH", "/crd")[0].body
	if patch["alert_on_sync_failure"] != true {
		t.Errorf("PATCH = %v, want alert_on_sync_failure=true", patch)
	}
}

func TestContainerAppUpdateAttachesASource(t *testing.T) {
	h := newHarness(t)
	plan := nullApp(h.f)
	plan.ImageRepository = types.StringValue("nginx")
	plan.ImageTag = types.StringValue("1.27")
	created, d := h.create(plan)
	noErrors(t, d)

	h.f.status = resolved()
	next := created
	next.ImageRepository, next.ImageTag = types.StringNull(), types.StringNull()
	next.ImageSource = sourceModel(t, nil)
	got, d := h.update(next, h.stateOf(created))
	noErrors(t, d)

	patch := h.f.calls("PATCH", "/crd")[0].body
	if _, ok := patch["image_source"]; !ok {
		t.Errorf("PATCH = %v, want image_source", patch)
	}
	if _, ok := patch["image_repository"]; ok {
		t.Errorf("PATCH = %v, must not carry the literal image", patch)
	}
	if got.ResolvedImage.ValueString() != "ghcr.io/acme/orders-api:1.4.2" || !got.ImageRepository.IsNull() {
		t.Errorf("state = %v / %v", got.ResolvedImage, got.ImageRepository)
	}
}

// Removing the block detaches the source first (the platform refuses image
// fields while one is attached), then applies the literal image on the new
// spec generation.
func TestContainerAppUpdateDetachesTheSource(t *testing.T) {
	h := newHarness(t)
	h.f.status = resolved()
	plan := nullApp(h.f)
	plan.ImageSource = sourceModel(t, nil)
	created, d := h.create(plan)
	noErrors(t, d)

	next := created
	next.ImageSource = nullImageSource()
	next.ImageRepository = types.StringValue("ghcr.io/acme/orders-api")
	next.ImageTag = types.StringValue("1.5.0")
	got, d := h.update(next, h.stateOf(created))
	noErrors(t, d)

	if n := len(h.f.calls("POST", "/image-source/detach")); n != 1 {
		t.Fatalf("detach calls = %d, want 1", n)
	}
	patch := h.f.calls("PATCH", "/crd")[0].body
	if patch["image_repository"] != "ghcr.io/acme/orders-api" || patch["image_tag"] != "1.5.0" {
		t.Errorf("PATCH = %v", patch)
	}
	if _, ok := patch["image_source"]; ok {
		t.Errorf("PATCH = %v, must not carry a source", patch)
	}
	if got.ImageRepository.ValueString() != "ghcr.io/acme/orders-api" || got.ImageTag.ValueString() != "1.5.0" ||
		!got.ImageSource.IsNull() || !got.ResolvedImage.IsNull() {
		t.Errorf("state = %v %v %v %v", got.ImageRepository, got.ImageTag, got.ImageSource, got.ResolvedImage)
	}
}

// Import has no prior block to keep the user's spelling from: what the platform
// stores is what the state gets, with the default alert left null.
func TestContainerAppReadOfAnImportedGitApp(t *testing.T) {
	h := newHarness(t)
	h.f.status = resolved()
	plan := nullApp(h.f)
	plan.ImageSource = sourceModel(t, func(m *gitSourceModel) { m.Branch = types.StringValue("main") })
	created, d := h.create(plan)
	noErrors(t, d)

	imported, err := h.r.readState(t.Context(), created.ID.ValueString(), types.StringNull(), nullImageSource())
	if err != nil {
		t.Fatalf("readState: %v", err)
	}
	if !imported.ImageSource.Equal(created.ImageSource) {
		t.Errorf("imported image_source = %v, want %v", imported.ImageSource, created.ImageSource)
	}
	if imported.ResolvedImage != created.ResolvedImage || !imported.ImageRepository.IsNull() {
		t.Errorf("imported = %v / %v", imported.ResolvedImage, imported.ImageRepository)
	}
}
