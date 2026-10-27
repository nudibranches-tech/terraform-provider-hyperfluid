// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// fakeCatalogs serves the catalogs of one cluster, Ready as soon as created.
func fakeCatalogs(t *testing.T) (*fakeAPI, *sqlEngineCatalogResource, string) {
	f, pd := newFakeAPI(t)
	cluster := uuid.New().String()
	catalogs := map[string]console.DataDockCatalogInfo{}
	ready := "Ready"
	f.on("POST", `/data-docks/[^/]+/catalogs`, func(_ string, raw []byte) (int, any) {
		req := decodeInto[console.CreateDataDockCatalogRequest](t, raw)
		catalogs[req.Name] = console.DataDockCatalogInfo{Name: req.Name, Kind: strings.ToLower(string(req.Kind)), Phase: &ready}
		return 201, console.CreateDataDockCatalogResponse{CatalogName: req.Name, DataDockSlug: "analytics"}
	})
	f.on("GET", `/data-docks/[^/]+/catalogs`, func(string, []byte) (int, any) {
		out := []console.DataDockCatalogInfo{}
		for _, c := range catalogs {
			out = append(out, c)
		}
		return 200, out
	})
	f.on("DELETE", `/data-docks/[^/]+/catalogs/[^/]+`, func(p string, _ []byte) (int, any) {
		delete(catalogs, p[strings.LastIndex(p, "/")+1:])
		return 204, nil
	})
	return f, &sqlEngineCatalogResource{p: pd}, cluster
}

func catalogPlan(cluster, name, kind string) sqlEngineCatalogModel {
	return sqlEngineCatalogModel{
		ID: types.StringUnknown(), ClusterID: types.StringValue(cluster), Name: types.StringValue(name),
		Kind: types.StringValue(kind), Phase: types.StringUnknown(), Message: types.StringUnknown(),
	}
}

func TestSqlEngineCatalogSchema(t *testing.T) {
	assertSchemaImplementation(t, NewSqlEngineCatalogResource())
}

// The Terraform kind maps onto the API enum; a source the read does not
// report is carried, and the id is `<cluster>/<name>`.
func TestSqlEngineCatalogLifecycle(t *testing.T) {
	f, r, cluster := fakeCatalogs(t)
	l := newLifecycle[sqlEngineCatalogModel](t, r)
	plan := catalogPlan(cluster, "lake", "iceberg")
	plan.BucketName = types.StringValue("warehouse")

	got, d := l.create(plan)
	mustSucceed(t, d)
	body := f.calls("POST", `/catalogs`)[0].body
	if body["kind"] != "Iceberg" || body["bucket_name"] != "warehouse" {
		t.Errorf("create body = %v", body)
	}
	for _, k := range []string{"connection_secret_name", "managed_postgresql_user_id"} {
		if _, ok := body[k]; ok {
			t.Errorf("create body sets %s", k)
		}
	}
	if got.ID.ValueString() != cluster+"/lake" || got.Phase.ValueString() != "Ready" || got.Kind.ValueString() != "iceberg" {
		t.Errorf("state = %+v", got)
	}

	read, removed, d := l.read(got)
	mustSucceed(t, d)
	if removed || read.BucketName.ValueString() != "warehouse" {
		t.Errorf("read = %+v, want bucket_name carried", read)
	}

	imported, d := l.importAndRead(cluster + "/lake")
	mustSucceed(t, d)
	if imported.Kind.ValueString() != "iceberg" || !imported.BucketName.IsNull() {
		t.Errorf("imported = %+v", imported)
	}

	mustSucceed(t, l.destroy(got))
	_, removed, d = l.read(got)
	mustSucceed(t, d)
	if !removed {
		t.Error("a deleted catalog stayed in state")
	}
}

func TestSqlEngineCatalogImportIDFormat(t *testing.T) {
	_, r, _ := fakeCatalogs(t)
	l := newLifecycle[sqlEngineCatalogModel](t, r)
	_, d := l.importAndRead("no-slash")
	mustFail(t, d, "expected import id")
}

// A source recorded by import is adopted in place; one that changes replaces.
func TestSqlEngineCatalogSourceReplacement(t *testing.T) {
	s := resourceSchema(t, NewSqlEngineCatalogResource()).Schema
	p := path.Root("connection_secret")
	if requiresReplaceString(t, s, p, types.StringNull(), types.StringValue("pg-bundle")) {
		t.Error("adopting a source after import replaces the catalog")
	}
	if !requiresReplaceString(t, s, p, types.StringValue("a"), types.StringValue("b")) {
		t.Error("changing the source does not replace the catalog")
	}
}

func TestSqlEngineCatalogValidation(t *testing.T) {
	str := types.StringValue
	cases := []struct {
		name string
		m    sqlEngineCatalogModel
		want string
	}{
		{"iceberg needs a bucket", sqlEngineCatalogModel{Kind: str("iceberg")}, "bucket_name"},
		{"iceberg takes no secret", sqlEngineCatalogModel{Kind: str("iceberg"), BucketName: str("b"), ConnectionSecret: str("s")}, "connection_secret"},
		{"postgresql needs a source", sqlEngineCatalogModel{Kind: str("postgresql")}, "exactly one"},
		{"postgresql takes one source", sqlEngineCatalogModel{Kind: str("postgresql"), ManagedPostgresqlUserID: str(uuid.NewString()), ConnectionSecret: str("s")}, "exactly one"},
		{"mysql needs a secret", sqlEngineCatalogModel{Kind: str("mysql")}, "connection_secret"},
		{"clickhouse takes no managed user", sqlEngineCatalogModel{Kind: str("clickhouse"), ConnectionSecret: str("s"), ManagedPostgresqlUserID: str(uuid.NewString())}, "managed_postgresql_user_id"},
		{"tpch takes nothing", sqlEngineCatalogModel{Kind: str("tpch"), ConnectionSecret: str("s")}, "connection_secret"},
		{"iceberg", sqlEngineCatalogModel{Kind: str("iceberg"), BucketName: str("b")}, ""},
		{"managed postgresql", sqlEngineCatalogModel{Kind: str("postgresql"), ManagedPostgresqlUserID: str(uuid.NewString())}, ""},
		{"external postgresql", sqlEngineCatalogModel{Kind: str("postgresql"), ConnectionSecret: str("s")}, ""},
		{"mariadb", sqlEngineCatalogModel{Kind: str("mariadb"), ConnectionSecret: str("s")}, ""},
		{"faker", sqlEngineCatalogModel{Kind: str("faker")}, ""},
		{"unknown secret is not judged", sqlEngineCatalogModel{Kind: str("postgresql"), ConnectionSecret: types.StringUnknown()}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var d diag.Diagnostics
			validateCatalogSources(c.m, &d)
			if c.want == "" {
				mustSucceed(t, d)
				return
			}
			mustFail(t, d, c.want)
		})
	}
}
