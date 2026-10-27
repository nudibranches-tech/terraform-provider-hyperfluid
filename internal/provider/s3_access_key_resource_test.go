// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func s3KeyPlan(env, serviceAccountID string) s3AccessKeyModel {
	return s3AccessKeyModel{
		ID: types.StringUnknown(), Env: types.StringValue(env), StorageZoneID: types.StringNull(), Label: types.StringNull(),
		ServiceAccountID: types.StringValue(serviceAccountID), AccessKeyID: types.StringUnknown(),
		SecretAccessKey: types.StringUnknown(), Endpoint: types.StringUnknown(), Region: types.StringUnknown(),
		Tenant: types.StringUnknown(), KeyEpoch: types.Int64Unknown(), Scopes: types.ListUnknown(types.StringType),
	}
}

func TestS3AccessKeyLifecycle(t *testing.T) {
	f, pd := newFakeIAM(t)
	acct := f.addAccount("etl", "ETL")
	l := newLifecycle[s3AccessKeyModel](t, &s3AccessKeyResource{p: pd})

	plan := s3KeyPlan(f.env.String(), acct.row.Id.String())
	plan.Label = types.StringValue("nightly export")
	first, d := l.create(plan)
	mustSucceed(t, d)
	if first.SecretAccessKey.ValueString() != "s3-secret-"+first.AccessKeyID.ValueString() || first.KeyEpoch.ValueInt64() != 1 ||
		first.Endpoint.ValueString() == "" || first.Label.ValueString() != "nightly export" || len(first.Scopes.Elements()) != 1 {
		t.Fatalf("created = %+v", first)
	}
	if body := f.calls("POST", "/s3-keys")[0].body; body["rotate"] != true || body["zone_id"] != nil {
		t.Errorf("issue body = %v, want rotate and no zone", body)
	}

	// A second key for the same account gets its own epoch.
	second, d := l.create(plan)
	mustSucceed(t, d)
	if second.KeyEpoch.ValueInt64() != 2 || second.AccessKeyID == first.AccessKeyID {
		t.Fatalf("second = %+v", second)
	}

	// Read keeps the secret it cannot re-read.
	read, removed, d := l.read(first)
	mustSucceed(t, d)
	if removed || read.SecretAccessKey != first.SecretAccessKey {
		t.Errorf("read = %+v", read)
	}

	imported, d := l.importAndRead(f.env.String() + "/" + first.ID.ValueString())
	mustSucceed(t, d)
	if imported.ServiceAccountID != first.ServiceAccountID || imported.AccessKeyID != first.AccessKeyID ||
		!imported.SecretAccessKey.IsNull() {
		t.Errorf("imported = %+v", imported)
	}

	// Revoking the newer key revokes the older one too, and says so.
	d = l.destroy(second)
	mustSucceed(t, d)
	if len(d.Warnings()) != 1 {
		t.Errorf("want a warning naming the older key revoked with it, got %v", d)
	}
	// The older key, now revoked, leaves state; destroying it again is a no-op.
	if _, removed, d := l.read(first); !removed || d.HasError() {
		t.Errorf("a revoked key must leave state (removed=%v, %v)", removed, d)
	}
	mustSucceed(t, l.destroy(first))
	// Terraform never deletes a service account.
	if _, ok := f.accounts["etl"]; !ok {
		t.Error("destroying a key deleted its service account")
	}
	_, d = l.importAndRead("not-a-valid-id")
	mustFail(t, d, "<env>/<key_id>")
}
