// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// fakeBackupTargets serves one target, created by the first POST. A target
// created on a bucket reports the endpoint and path the platform resolved and
// no credentials, and refuses a patch of its endpoint or credentials.
func fakeBackupTargets(t *testing.T) (*fakeAPI, *backupTargetResource) {
	f, pd := newFakeAPI(t)
	var bt console.BackupTargetResponse
	internal := false
	f.on("POST", `/backup-targets/crd`, func(_ string, raw []byte) (int, any) {
		req := decodeInto[console.CreateBackupTargetCrdRequestBody](t, raw)
		bt = console.BackupTargetResponse{
			Id: uuid.New(), HarborId: f.env, OrganizationId: f.org, Name: req.Name, Slug: req.Name, Phase: "Ready",
			RetentionDays: 7, Tags: []string{}, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
		if src, err := req.Source.AsBackupTargetSourceRequest0(); err == nil && src.Mode == console.BackupTargetSourceRequest0ModeInternal {
			internal = true
			bt.EndpointUrl, bt.DestinationPath = "http://s0.storage.svc:8080", "s3://org-"+src.BucketName+"/"
			return 201, bt
		}
		src, _ := req.Source.AsBackupTargetSourceRequest1()
		bt.EndpointUrl, bt.DestinationPath = src.EndpointUrl, src.DestinationPath
		bt.AccessKeySecretName, bt.SecretAccessKeySecretName = &src.AccessKeySecretName, &src.SecretAccessKeySecretName
		return 201, bt
	})
	f.on("GET", `/backup-targets/[^/]+`, func(string, []byte) (int, any) { return 200, bt })
	f.on("PATCH", `/backup-targets/[^/]+/crd`, func(_ string, raw []byte) (int, any) {
		req := decodeInto[console.PatchBackupTargetCrdRequestBody](t, raw)
		if internal && (req.EndpointUrl != nil || req.AccessKeySecretName != nil || req.SecretAccessKeySecretName != nil) {
			return 400, map[string]string{"message": "Cannot set endpoint_url or credential fields on a bucket_ref backup target"}
		}
		if req.RetentionDays != nil {
			bt.RetentionDays = *req.RetentionDays
		}
		bt.Description = req.Description
		return 200, nil
	})
	return f, &backupTargetResource{p: pd}
}

func bucketTargetPlan(f *fakeAPI) backupTargetModel {
	return backupTargetModel{
		ID: types.StringUnknown(), Env: types.StringValue(f.env.String()), Name: types.StringValue("nightly"),
		BucketName:  types.StringValue("pg-backups"),
		EndpointURL: types.StringUnknown(), DestinationPath: types.StringUnknown(),
		AccessKeySecretName: types.StringUnknown(), SecretAccessKeySecretName: types.StringUnknown(),
		Insecure: types.BoolValue(false), RetentionDays: types.Int64Unknown(), Description: types.StringNull(),
		Tags: types.ListUnknown(types.StringType), Phase: types.StringUnknown(), Slug: types.StringUnknown(),
	}
}

// A bucket target sends the internal source, and reads back what the platform
// resolved with bucket_name carried.
func TestBackupTargetCreateOnABucket(t *testing.T) {
	f, r := fakeBackupTargets(t)
	l := newLifecycle[backupTargetModel](t, r)
	got, d := l.create(bucketTargetPlan(f))
	mustSucceed(t, d)

	src, _ := f.calls("POST", `/crd`)[0].body["source"].(map[string]any)
	if len(src) != 2 || src["mode"] != "internal" || src["bucket_name"] != "pg-backups" {
		t.Errorf("source = %v, want only mode=internal and bucket_name", src)
	}
	if got.BucketName.ValueString() != "pg-backups" || got.EndpointURL.ValueString() != "http://s0.storage.svc:8080" ||
		!got.AccessKeySecretName.IsNull() || !got.SecretAccessKeySecretName.IsNull() {
		t.Errorf("state = %+v", got)
	}
	read, _, d := l.read(got)
	mustSucceed(t, d)
	if read.BucketName.ValueString() != "pg-backups" {
		t.Errorf("read bucket_name = %v, want it carried", read.BucketName)
	}
}

// Updating a bucket target sends neither its endpoint nor credentials, which
// the platform refuses on one.
func TestBackupTargetUpdateOnABucket(t *testing.T) {
	f, r := fakeBackupTargets(t)
	l := newLifecycle[backupTargetModel](t, r)
	created, d := l.create(bucketTargetPlan(f))
	mustSucceed(t, d)

	next := created
	next.RetentionDays = types.Int64Value(14)
	got, d := l.update(next, created)
	mustSucceed(t, d)
	patch := f.calls("PATCH", `/crd`)[0].body
	for _, k := range []string{"endpoint_url", "destination_path", "access_key_secret_name", "secret_access_key_secret_name"} {
		if _, ok := patch[k]; ok {
			t.Errorf("PATCH carries %q: %v", k, patch)
		}
	}
	if got.RetentionDays.ValueInt64() != 14 {
		t.Errorf("retention_days = %v", got.RetentionDays)
	}
}

// The external variant is unchanged: its four fields are sent on create.
func TestBackupTargetCreateExternal(t *testing.T) {
	f, r := fakeBackupTargets(t)
	l := newLifecycle[backupTargetModel](t, r)
	plan := bucketTargetPlan(f)
	plan.BucketName = types.StringNull()
	plan.EndpointURL, plan.DestinationPath = types.StringValue("https://s3.example.com"), types.StringValue("s3://b/")
	plan.AccessKeySecretName, plan.SecretAccessKeySecretName = types.StringValue("ak"), types.StringValue("sk")
	got, d := l.create(plan)
	mustSucceed(t, d)
	if src, _ := f.calls("POST", `/crd`)[0].body["source"].(map[string]any); src["mode"] != "external" || src["access_key_secret_name"] != "ak" {
		t.Errorf("source = %v", src)
	}
	if !got.BucketName.IsNull() || got.AccessKeySecretName.ValueString() != "ak" {
		t.Errorf("state = %+v", got)
	}
}

// Without a bucket every external field is required; with one, TLS cannot be
// skipped.
func TestBackupTargetValidateConfig(t *testing.T) {
	r := &backupTargetResource{}
	l := newLifecycle[backupTargetModel](t, r)
	validate := func(m backupTargetModel) []string {
		resp := &fwresource.ValidateConfigResponse{}
		r.ValidateConfig(t.Context(), fwresource.ValidateConfigRequest{Config: l.config(m)}, resp)
		var out []string
		for _, e := range resp.Diagnostics.Errors() {
			out = append(out, e.Summary())
		}
		return out
	}
	f, _ := newFakeAPI(t)
	cfg := bucketTargetPlan(f)
	cfg.EndpointURL, cfg.DestinationPath = types.StringNull(), types.StringNull()
	cfg.AccessKeySecretName, cfg.SecretAccessKeySecretName = types.StringNull(), types.StringNull()
	if errs := validate(cfg); len(errs) != 0 {
		t.Errorf("bucket target: %v", errs)
	}

	insecure := cfg
	insecure.Insecure = types.BoolValue(true)
	if errs := validate(insecure); len(errs) != 1 || errs[0] != "insecure needs an external endpoint" {
		t.Errorf("insecure bucket target: %v", errs)
	}

	external := cfg
	external.BucketName = types.StringNull()
	external.EndpointURL = types.StringValue("https://s3.example.com")
	if errs := validate(external); len(errs) != 3 {
		t.Errorf("external target missing 3 fields: %v", errs)
	}
}

// Changing or adding a bucket forces a new target, except when an imported
// bucket target (no credentials in state) gets its bucket named.
func TestBackupTargetBucketNameReplacement(t *testing.T) {
	s := resourceSchema(t, &backupTargetResource{}).Schema
	cases := []struct {
		name          string
		state, plan   types.String
		stateAccessKy types.String
		want          bool
	}{
		{"another bucket", types.StringValue("a"), types.StringValue("b"), types.StringNull(), true},
		{"bucket to external", types.StringValue("a"), types.StringNull(), types.StringNull(), true},
		{"external to bucket", types.StringNull(), types.StringValue("a"), types.StringValue("ak"), true},
		{"adopting an imported bucket target", types.StringNull(), types.StringValue("a"), types.StringNull(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := newFakeAPI(t)
			l := newLifecycle[backupTargetModel](t, &backupTargetResource{})
			st := bucketTargetPlan(f)
			st.ID, st.BucketName, st.AccessKeySecretName = types.StringValue(uuid.NewString()), c.state, c.stateAccessKy
			st.EndpointURL, st.DestinationPath, st.SecretAccessKeySecretName = types.StringValue("e"), types.StringValue("d"), types.StringNull()
			st.RetentionDays, st.Tags, st.Phase, st.Slug = types.Int64Value(7), types.ListNull(types.StringType), types.StringValue("Ready"), types.StringValue("x")
			pl := st
			pl.BucketName = c.plan
			if got := requiresReplaceWith(t, s, l.state(st), l.plan(pl), c.state, c.plan); got != c.want {
				t.Errorf("requires replace = %v, want %v", got, c.want)
			}
		})
	}
}

// requiresReplaceWith runs bucket_name's plan modifiers against whole state and
// plan values, which its replacement rule reads.
func requiresReplaceWith(t *testing.T, s rschema.Schema, state tfsdk.State, plan tfsdk.Plan, stateValue, planValue types.String) bool {
	t.Helper()
	attr, ok := s.Attributes["bucket_name"].(rschema.StringAttribute)
	if !ok {
		t.Fatal("bucket_name is not a string attribute")
	}
	req := planmodifier.StringRequest{
		Path: path.Root("bucket_name"), State: state, Plan: plan, Config: tfsdk.Config(plan),
		StateValue: stateValue, PlanValue: planValue, ConfigValue: planValue,
	}
	resp := &planmodifier.StringResponse{PlanValue: planValue}
	for _, pm := range attr.PlanModifiers {
		pm.PlanModifyString(t.Context(), req, resp)
	}
	if resp.Diagnostics.HasError() {
		t.Fatalf("plan modifiers: %v", resp.Diagnostics)
	}
	return resp.RequiresReplace
}
