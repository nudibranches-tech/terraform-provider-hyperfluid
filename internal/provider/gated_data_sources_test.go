// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

func TestGatedTypesAreRegistered(t *testing.T) {
	srv, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatalf("starting the provider server: %v", err)
	}
	resp, err := srv.GetProviderSchema(t.Context(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}
	for _, name := range []string{"hyperfluid_nextflow", "hyperfluid_compute_dock", "hyperfluid_agent", "hyperfluid_virtual_machine"} {
		if _, ok := resp.ResourceSchemas[name]; !ok {
			t.Errorf("resource %q is not registered", name)
		}
		if _, ok := resp.DataSourceSchemas[name]; !ok {
			t.Errorf("data source %q is not registered", name)
		}
	}
}

func TestNextflowDataSource(t *testing.T) {
	f, r := newFakeNextflow(t)
	created, d := newLifecycle[nextflowModel](t, r).create(nextflowPlan(f.fakeAPI))
	mustSucceed(t, d)
	f.on("GET", `/harbors/[^/]+/nextflows`, func(string, []byte) (int, any) { return 200, []console.NextflowResponse{f.nf} })

	got, d := readDataSource(t, &nextflowDataSource{p: r.p}, nextflowDataSourceModel{
		Env: types.StringValue(f.env.String()), Name: types.StringValue("genomics"),
		Tags: types.ListNull(types.StringType), Buckets: types.SetNull(types.StringType),
	})
	mustSucceed(t, d)
	if got.ID != created.ID || got.Phase.ValueString() != "Ready" {
		t.Errorf("data source = %+v", got)
	}

	_, d = readDataSource(t, &nextflowDataSource{p: r.p}, nextflowDataSourceModel{
		Env: types.StringValue(f.env.String()), Name: types.StringValue("absent"),
		Tags: types.ListNull(types.StringType), Buckets: types.SetNull(types.StringType),
	})
	mustFail(t, d, "no Nextflow deployment named absent")
}

func TestComputeDockDataSource(t *testing.T) {
	f, r := newFakeComputeDock(t)
	created, d := newLifecycle[computeDockModel](t, r).create(computeDockPlan(f.fakeAPI))
	mustSucceed(t, d)
	f.on("GET", `/harbors/[^/]+/compute-docks`, func(string, []byte) (int, any) { return 200, []console.ComputeDockResponse{f.dock} })

	got, d := readDataSource(t, &computeDockDataSource{p: r.p}, computeDockDataSourceModel{
		Env: types.StringValue(f.env.String()), Name: types.StringValue("hpc"), Tags: types.ListNull(types.StringType),
	})
	mustSucceed(t, d)
	if got.ID != created.ID || got.Phase.ValueString() != "Running" {
		t.Errorf("data source = %+v", got)
	}
}

func TestAgentDataSource(t *testing.T) {
	f, r := newFakeAgent(t)
	created, d := newLifecycle[agentModel](t, r).create(agentPlan(f.fakeAPI))
	mustSucceed(t, d)
	f.on("GET", `/ai-agents`, func(string, []byte) (int, any) { return 200, []console.AiAgentResponse{f.agent} })

	got, d := readDataSource(t, &agentDataSource{p: r.p}, agentDataSourceModel{
		Env: types.StringValue(f.env.String()), Name: types.StringValue("ops"), Interfaces: types.SetNull(types.StringType),
	})
	mustSucceed(t, d)
	if got.ID != created.ID || got.Slug.ValueString() != "ops" {
		t.Errorf("data source = %+v", got)
	}
}
