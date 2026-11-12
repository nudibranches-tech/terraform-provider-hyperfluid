// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// fakeExtensions is the console's extensions view of the one fake database:
// a small catalog, the declared list, and an operator that settles every
// declaration at once unless the name is listed in failing.
type fakeExtensions struct {
	mu       sync.Mutex
	declared []console.ExtensionRequestItem
	failing  map[string]string
	refused  map[string]string // the console answers 400 for a list naming one
	missing  bool              // the console predates the endpoint: 404
}

var fakeExtensionCatalog = []string{"citext", "pg_stat_statements", "postgis", "postgres_fdw"}

var fakeExtensionStores sync.Map // *fakeAPI → *fakeExtensions

func extensionsOf(f *fakeAPI) *fakeExtensions {
	v, _ := fakeExtensionStores.Load(f)
	x, ok := v.(*fakeExtensions)
	if !ok {
		f.t.Fatal("fakeDatabases did not register the extensions endpoints")
	}
	return x
}

// fakeExtensionsOn serves the extensions endpoints, and records the names a
// create request enables as the declared list.
func fakeExtensionsOn(f *fakeAPI) {
	x := &fakeExtensions{failing: map[string]string{}, refused: map[string]string{}}
	fakeExtensionStores.Store(f, x)
	f.t.Cleanup(func() { fakeExtensionStores.Delete(f) })

	f.on("GET", `/managed-postgresqls/[^/]+/extensions`, func(string, []byte) (int, any) {
		x.mu.Lock()
		defer x.mu.Unlock()
		if x.missing {
			return 404, map[string]string{"error": "not found"}
		}
		return 200, x.view()
	})
	f.on("PUT", `/managed-postgresqls/[^/]+/extensions`, func(_ string, raw []byte) (int, any) {
		body := decodeInto[console.SetManagedPostgresqlExtensionsRequestBody](f.t, raw)
		x.mu.Lock()
		defer x.mu.Unlock()
		for _, e := range body.Extensions {
			if reason, refused := x.refused[e.Name]; refused {
				return 400, map[string]string{"error": reason}
			}
		}
		x.declared = body.Extensions
		return 204, nil
	})
}

func (x *fakeExtensions) declare(items ...console.ExtensionRequestItem) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.declared = items
}

func (x *fakeExtensions) view() console.ManagedPostgresqlExtensionsResponse {
	out := console.ManagedPostgresqlExtensionsResponse{Engine: "postgresql", Extensions: []console.ExtensionCatalogItem{}, Uncatalogued: []console.ExtensionStatus{}}
	for _, name := range fakeExtensionCatalog {
		item := console.ExtensionCatalogItem{Name: name, Category: "utilities", Available: true, DependsOn: []string{}}
		for _, d := range x.declared {
			if d.Name != name {
				continue
			}
			ensure := console.ExtensionEnsurePresent
			if d.Ensure != nil {
				ensure = *d.Ensure
			}
			item.Declared = &console.DeclaredExtension{Ensure: ensure, Version: d.Version}
			status := console.ExtensionStatus{Name: name, Phase: console.ExtensionPhaseInstalled}
			switch msg, failing := x.failing[name]; {
			case failing:
				status.Phase, status.Message = console.ExtensionPhaseFailed, &msg
			case ensure == console.ExtensionEnsureAbsent:
				status.Phase = console.ExtensionPhaseAbsent
			default:
				v := "1.0"
				if d.Version != nil {
					v = *d.Version
				}
				status.InstalledVersion, status.AvailableVersion = &v, &v
			}
			item.Status = &status
		}
		out.Extensions = append(out.Extensions, item)
	}
	return out
}

func extensionSet(t *testing.T, entries ...extensionModel) types.Set {
	t.Helper()
	if entries == nil {
		entries = []extensionModel{} // an empty set, not a null one
	}
	s, d := types.SetValueFrom(context.Background(), extensionObjectType(), entries)
	mustSucceed(t, d)
	return s
}

func ext(name string) extensionModel {
	return extensionModel{Name: types.StringValue(name), Ensure: types.StringNull(), Version: types.StringNull()}
}

func extEnsure(name, ensure string) extensionModel {
	e := ext(name)
	e.Ensure = types.StringValue(ensure)
	return e
}

func extVersion(name, version string) extensionModel {
	e := ext(name)
	e.Version = types.StringValue(version)
	return e
}

func requestNames(t *testing.T, body map[string]any) []string {
	t.Helper()
	raw, _ := json.Marshal(body["extensions"])
	var items []console.ExtensionRequestItem
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("extensions in %v: %v", body, err)
	}
	var names []string
	for _, i := range items {
		n := i.Name
		if i.Ensure != nil {
			n += "=" + string(*i.Ensure)
		}
		if i.Version != nil {
			n += "@" + *i.Version
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func statusPhases(t *testing.T, l types.List) map[string]string {
	t.Helper()
	var entries []extensionStatusModel
	mustSucceed(t, l.ElementsAs(context.Background(), &entries, false))
	out := map[string]string{}
	for _, e := range entries {
		out[e.Name.ValueString()] = e.Phase.ValueString()
	}
	return out
}

// A database whose configuration leaves `extensions` out never declares any,
// and keeps the attribute null across reads.
func TestManagedPostgresqlWithoutExtensions(t *testing.T) {
	f, r := fakeDatabases(t)
	l := newLifecycle[managedPostgresqlModel](t, r)

	got, d := l.create(databasePlan(f))
	mustSucceed(t, d)
	if _, ok := f.calls("POST", `/crd`)[0].body["extensions"]; ok {
		t.Error("create body carries extensions, which the config does not set")
	}
	if n := len(f.calls("PUT", `/extensions`)); n != 0 {
		t.Errorf("%d PUT /extensions, want none", n)
	}
	if !got.Extensions.IsNull() {
		t.Errorf("extensions = %v, want null", got.Extensions)
	}

	// Declared elsewhere (the console): still not managed here.
	extensionsOf(f).declare(console.ExtensionRequestItem{Name: "citext"})
	read, _, d := l.read(got)
	mustSucceed(t, d)
	if !read.Extensions.IsNull() {
		t.Errorf("read extensions = %v, want null: the list is not managed by Terraform", read.Extensions)
	}
	if got := statusPhases(t, read.ExtensionStatus); got["citext"] != "installed" {
		t.Errorf("extension_status = %v, want citext installed", got)
	}
}

// Plain names ride on the create request; no follow-up PUT.
func TestManagedPostgresqlCreateWithExtensionNames(t *testing.T) {
	f, r := fakeDatabases(t)
	l := newLifecycle[managedPostgresqlModel](t, r)
	plan := databasePlan(f)
	plan.Extensions = extensionSet(t, ext("postgres_fdw"), ext("citext"))

	// The fake's create declares nothing itself: stand in for the console.
	extensionsOf(f).declare(console.ExtensionRequestItem{Name: "citext"}, console.ExtensionRequestItem{Name: "postgres_fdw"})
	created, d := l.create(plan)
	mustSucceed(t, d)
	body := f.calls("POST", `/crd`)[0].body
	names, _ := body["extensions"].([]any)
	if len(names) != 2 || names[0] != "citext" || names[1] != "postgres_fdw" {
		t.Errorf("create extensions = %v, want [citext postgres_fdw]", body["extensions"])
	}
	if n := len(f.calls("PUT", `/extensions`)); n != 0 {
		t.Errorf("%d PUT /extensions, want none", n)
	}
	if !created.Extensions.Equal(plan.Extensions) {
		t.Errorf("extensions = %v, want %v", created.Extensions, plan.Extensions)
	}
	if got := statusPhases(t, created.ExtensionStatus); got["citext"] != "installed" || got["postgres_fdw"] != "installed" {
		t.Errorf("extension_status = %v", got)
	}
}

// A pinned version cannot ride on the create request, so the list is PUT once
// the cluster exists.
func TestManagedPostgresqlCreateWithPinnedExtension(t *testing.T) {
	f, r := fakeDatabases(t)
	l := newLifecycle[managedPostgresqlModel](t, r)
	plan := databasePlan(f)
	plan.Extensions = extensionSet(t, extVersion("postgis", "3.5.0"), ext("citext"))

	created, d := l.create(plan)
	mustSucceed(t, d)
	if _, ok := f.calls("POST", `/crd`)[0].body["extensions"]; ok {
		t.Error("create body carries extensions although the list pins a version")
	}
	puts := f.calls("PUT", `/extensions`)
	if len(puts) != 1 {
		t.Fatalf("%d PUT /extensions, want 1", len(puts))
	}
	if got := strings.Join(requestNames(t, puts[0].body), ","); got != "citext,postgis@3.5.0" {
		t.Errorf("PUT extensions = %s", got)
	}
	if !created.Extensions.Equal(plan.Extensions) {
		t.Errorf("extensions = %v, want %v", created.Extensions, plan.Extensions)
	}
}

// A failed extension at create is a warning: an error would taint the new
// cluster, and the next apply would replace the database to retry.
func TestManagedPostgresqlCreateExtensionFailureDoesNotTaint(t *testing.T) {
	f, r := fakeDatabases(t)
	l := newLifecycle[managedPostgresqlModel](t, r)
	extensionsOf(f).failing["postgis"] = "needs the postgis engine"
	plan := databasePlan(f)
	plan.Extensions = extensionSet(t, extVersion("postgis", "3.5.0"))

	created, d := l.create(plan)
	if d.HasError() {
		t.Fatalf("create errored, which taints the cluster: %v", d)
	}
	if d.WarningsCount() == 0 || !strings.Contains(d.Warnings()[0].Detail(), "needs the postgis engine") {
		t.Errorf("warnings = %v, want the operator's reason", d.Warnings())
	}
	if got := statusPhases(t, created.ExtensionStatus); got["postgis"] != "failed" {
		t.Errorf("extension_status = %v, want postgis failed", got)
	}
}

// The declared set is PUT only when it changes; dropping takes `absent`; a set
// removed from the configuration is left alone.
func TestManagedPostgresqlUpdateExtensions(t *testing.T) {
	f, r := fakeDatabases(t)
	l := newLifecycle[managedPostgresqlModel](t, r)
	created, d := l.create(databasePlan(f))
	mustSucceed(t, d)

	next := created
	next.Extensions = extensionSet(t, ext("pg_stat_statements"), ext("citext"))
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	puts := f.calls("PUT", `/extensions`)
	if len(puts) != 1 || strings.Join(requestNames(t, puts[0].body), ",") != "citext,pg_stat_statements" {
		t.Fatalf("PUT /extensions = %v", puts)
	}
	if !updated.Extensions.Equal(next.Extensions) {
		t.Errorf("extensions = %v, want %v", updated.Extensions, next.Extensions)
	}
	read, _, d := l.read(updated)
	mustSucceed(t, d)
	if !read.Extensions.Equal(next.Extensions) {
		t.Errorf("read extensions = %v, want %v (an omitted ensure stays null)", read.Extensions, next.Extensions)
	}

	// Unchanged: no PUT.
	same := read
	same.Description = types.StringValue("orders")
	_, d = l.update(same, read)
	mustSucceed(t, d)
	if n := len(f.calls("PUT", `/extensions`)); n != 1 {
		t.Errorf("%d PUT /extensions after an unchanged set, want 1", n)
	}

	// Drop one explicitly.
	drop := read
	drop.Extensions = extensionSet(t, ext("pg_stat_statements"), extEnsure("citext", "absent"))
	dropped, d := l.update(drop, read)
	mustSucceed(t, d)
	puts = f.calls("PUT", `/extensions`)
	if got := strings.Join(requestNames(t, puts[len(puts)-1].body), ","); got != "citext=absent,pg_stat_statements" {
		t.Errorf("PUT extensions = %s", got)
	}
	if got := statusPhases(t, dropped.ExtensionStatus); got["citext"] != "absent" {
		t.Errorf("extension_status = %v, want citext absent", got)
	}

	// Removed from the configuration: not managed any more, nothing sent.
	unmanaged := dropped
	unmanaged.Extensions = types.SetNull(extensionObjectType())
	after, d := l.update(unmanaged, dropped)
	mustSucceed(t, d)
	if n := len(f.calls("PUT", `/extensions`)); n != 2 {
		t.Errorf("%d PUT /extensions after removing the attribute, want 2", n)
	}
	if !after.Extensions.IsNull() {
		t.Errorf("extensions = %v, want null", after.Extensions)
	}
}

// A failed extension on update is an error, with the declaration kept in state
// so the next plan does not re-send it unchanged.
func TestManagedPostgresqlUpdateExtensionFailure(t *testing.T) {
	f, r := fakeDatabases(t)
	l := newLifecycle[managedPostgresqlModel](t, r)
	created, d := l.create(databasePlan(f))
	mustSucceed(t, d)
	extensionsOf(f).failing["postgres_fdw"] = "CloudNativePG could not apply it"

	next := created
	next.Extensions = extensionSet(t, ext("postgres_fdw"))
	updated, d := l.update(next, created)
	if !d.HasError() || !strings.Contains(d.Errors()[0].Detail(), "CloudNativePG could not apply it") {
		t.Fatalf("diagnostics = %v, want the operator's reason", d)
	}
	if !updated.Extensions.Equal(next.Extensions) {
		t.Errorf("extensions = %v, want the declaration kept", updated.Extensions)
	}
}

// A change made outside Terraform shows up as drift on a managed set.
func TestManagedPostgresqlExtensionDrift(t *testing.T) {
	f, r := fakeDatabases(t)
	l := newLifecycle[managedPostgresqlModel](t, r)
	plan := databasePlan(f)
	plan.Extensions = extensionSet(t, ext("citext"))
	extensionsOf(f).declare(console.ExtensionRequestItem{Name: "citext"})
	created, d := l.create(plan)
	mustSucceed(t, d)

	v := "1.6"
	extensionsOf(f).declare(console.ExtensionRequestItem{Name: "citext", Version: &v}, console.ExtensionRequestItem{Name: "postgis"})
	read, _, d := l.read(created)
	mustSucceed(t, d)
	want := extensionSet(t, extVersion("citext", "1.6"), ext("postgis"))
	if !read.Extensions.Equal(want) {
		t.Errorf("read extensions = %v, want %v", read.Extensions, want)
	}
}

// A console without the endpoint is fine until the configuration asks for
// extensions.
func TestManagedPostgresqlExtensionsEndpointMissing(t *testing.T) {
	f, r := fakeDatabases(t)
	l := newLifecycle[managedPostgresqlModel](t, r)
	extensionsOf(f).missing = true

	created, d := l.create(databasePlan(f))
	mustSucceed(t, d)
	if !created.ExtensionStatus.IsNull() {
		t.Errorf("extension_status = %v, want null", created.ExtensionStatus)
	}

	managed := created
	managed.Extensions = extensionSet(t)
	if _, _, d := l.read(managed); !d.HasError() {
		t.Error("a managed set read from a console without extensions did not error")
	}
}

func TestExtensionNamesAreUnique(t *testing.T) {
	ctx := context.Background()
	check := func(set types.Set) bool {
		resp := &validator.SetResponse{}
		uniqueExtensionNames{}.ValidateSet(ctx, validator.SetRequest{Path: path.Root("extensions"), ConfigValue: set}, resp)
		return resp.Diagnostics.HasError()
	}
	if !check(extensionSet(t, ext("citext"), extVersion("citext", "1.6"))) {
		t.Error("two entries for citext were accepted")
	}
	if check(extensionSet(t, ext("citext"), ext("postgis"))) {
		t.Error("distinct names were refused")
	}
}

// A list the console refuses once the cluster exists keeps the cluster in
// state: the declared set reads back empty, so the next plan retries it.
func TestManagedPostgresqlCreateExtensionsRefused(t *testing.T) {
	f, r := fakeDatabases(t)
	l := newLifecycle[managedPostgresqlModel](t, r)
	extensionsOf(f).refused["postgis"] = "extension 'postgis' requires the postgis engine, this database is postgresql"
	plan := databasePlan(f)
	plan.Extensions = extensionSet(t, extVersion("postgis", "3.5.0"))

	created, d := l.create(plan)
	if d.HasError() {
		t.Fatalf("create errored, which loses or taints the cluster: %v", d)
	}
	if d.WarningsCount() == 0 || !strings.Contains(d.Warnings()[0].Detail(), "requires the postgis engine") {
		t.Errorf("warnings = %v, want the console's reason", d.Warnings())
	}
	if created.ID.IsNull() {
		t.Fatal("the created cluster is not in state")
	}
	if want := extensionSet(t); !created.Extensions.Equal(want) {
		t.Errorf("extensions = %v, want the empty set actually declared", created.Extensions)
	}
}
