// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// fakeKeys serves one organization's API keys, with a platform cap of 600
// requests per minute on every key.
func fakeKeys(t *testing.T) (*fakeAPI, *aiApiKeyResource) {
	f, pd := newFakeAPI(t)
	platformRpm := int32(600)
	var keys []console.ApiKeyResponse
	effective := func(k *console.ApiKeyResponse) {
		k.PlatformMaxLlmRpm = &platformRpm
		k.EffectiveMaxLlmRpm = &platformRpm
		if k.MaxLlmRpm != nil && *k.MaxLlmRpm < platformRpm {
			k.EffectiveMaxLlmRpm = k.MaxLlmRpm
		}
		k.EffectiveMaxLlmTpm = k.MaxLlmTpm
	}
	f.on("POST", `/api-keys`, func(_ string, raw []byte) (int, any) {
		req := decodeInto[console.CreateApiKeyRequest](t, raw)
		k := console.ApiKeyResponse{
			Id: uuid.New(), Name: req.Name, KeyPrefix: "hf_ab", Scopes: []string{"model:*"}, CreatedAt: time.Now(),
			MaxLlmRpm: req.MaxLlmRpm, MaxLlmTpm: req.MaxLlmTpm,
		}
		effective(&k)
		keys = append(keys, k)
		return 201, console.CreateApiKeyResponse{
			Id: k.Id, Name: k.Name, Key: "hf_ab.secret", KeyPrefix: k.KeyPrefix, Scopes: k.Scopes, CreatedAt: k.CreatedAt,
		}
	})
	f.on("GET", `/api-keys`, func(string, []byte) (int, any) { return 200, keys })
	f.on("PUT", `/api-keys/[^/]+/rate-limits`, func(path string, raw []byte) (int, any) {
		req := decodeInto[console.SetApiKeyRateLimitsRequest](t, raw)
		for i := range keys {
			if path == "/api/v1/organizations/"+f.org.String()+"/api-keys/"+keys[i].Id.String()+"/rate-limits" {
				keys[i].MaxLlmRpm, keys[i].MaxLlmTpm = req.MaxLlmRpm, req.MaxLlmTpm
				effective(&keys[i])
				return 200, keys[i]
			}
		}
		return 404, map[string]string{"message": "API key not found or already revoked"}
	})
	return f, &aiApiKeyResource{p: pd}
}

func apiKeyPlan() aiApiKeyModel {
	return aiApiKeyModel{
		ID: types.StringUnknown(), Name: types.StringValue("ci"), Scopes: types.SetUnknown(types.StringType),
		ExpiresInDays: types.Int64Null(), Key: types.StringUnknown(),
		MaxLlmRpm: types.Int64Null(), MaxLlmTpm: types.Int64Null(),
		KeyPrefix: types.StringUnknown(), CreatedAt: types.StringUnknown(), ExpiresAt: types.StringUnknown(),
		LastUsedAt:        types.StringUnknown(),
		PlatformMaxLlmRpm: types.Int64Unknown(), PlatformMaxLlmTpm: types.Int64Unknown(),
		EffectiveMaxLlmRpm: types.Int64Unknown(), EffectiveMaxLlmTpm: types.Int64Unknown(),
	}
}

// The ceilings reach the create request and come back with the platform's cap
// and what the gateway enforces.
func TestAiApiKeyCreateWithCeilings(t *testing.T) {
	f, r := fakeKeys(t)
	l := newLifecycle[aiApiKeyModel](t, r)
	plan := apiKeyPlan()
	plan.MaxLlmRpm, plan.MaxLlmTpm = types.Int64Value(60), types.Int64Value(100000)

	got, d := l.create(plan)
	mustSucceed(t, d)

	body := f.calls("POST", `/api-keys`)[0].body
	if body["max_llm_rpm"] != float64(60) || body["max_llm_tpm"] != float64(100000) {
		t.Errorf("create body = %v", body)
	}
	if got.MaxLlmRpm.ValueInt64() != 60 || got.MaxLlmTpm.ValueInt64() != 100000 ||
		got.PlatformMaxLlmRpm.ValueInt64() != 600 || got.EffectiveMaxLlmRpm.ValueInt64() != 60 ||
		got.Key.ValueString() != "hf_ab.secret" {
		t.Errorf("state = %+v", got)
	}
}

// A key without ceilings sends none and reports none of its own.
func TestAiApiKeyCreateWithoutCeilings(t *testing.T) {
	f, r := fakeKeys(t)
	l := newLifecycle[aiApiKeyModel](t, r)
	got, d := l.create(apiKeyPlan())
	mustSucceed(t, d)

	body := f.calls("POST", `/api-keys`)[0].body
	for _, k := range []string{"max_llm_rpm", "max_llm_tpm"} {
		if _, ok := body[k]; ok {
			t.Errorf("create body carries %q: %v", k, body)
		}
	}
	if !got.MaxLlmRpm.IsNull() || !got.MaxLlmTpm.IsNull() || got.EffectiveMaxLlmRpm.ValueInt64() != 600 {
		t.Errorf("state = %+v", got)
	}
}

// Changing one ceiling replaces both: the other is re-sent as it is, and a
// removed one is sent absent, which clears it.
func TestAiApiKeyUpdateReplacesCeilings(t *testing.T) {
	f, r := fakeKeys(t)
	l := newLifecycle[aiApiKeyModel](t, r)
	plan := apiKeyPlan()
	plan.MaxLlmRpm, plan.MaxLlmTpm = types.Int64Value(60), types.Int64Value(100000)
	created, d := l.create(plan)
	mustSucceed(t, d)

	next := created
	next.MaxLlmRpm = types.Int64Value(120)
	next.EffectiveMaxLlmRpm = types.Int64Unknown()
	updated, d := l.update(next, created)
	mustSucceed(t, d)
	put := f.calls("PUT", `/rate-limits`)
	if len(put) != 1 || put[0].body["max_llm_rpm"] != float64(120) || put[0].body["max_llm_tpm"] != float64(100000) {
		t.Fatalf("PUT = %v, want both ceilings", put)
	}
	if updated.MaxLlmRpm.ValueInt64() != 120 || updated.EffectiveMaxLlmRpm.ValueInt64() != 120 {
		t.Errorf("state = %+v", updated)
	}

	next = updated
	next.MaxLlmTpm = types.Int64Null()
	cleared, d := l.update(next, updated)
	mustSucceed(t, d)
	put = f.calls("PUT", `/rate-limits`)
	if _, ok := put[1].body["max_llm_tpm"]; ok || put[1].body["max_llm_rpm"] != float64(120) {
		t.Errorf("PUT = %v, want max_llm_tpm left out to clear it", put[1].body)
	}
	if !cleared.MaxLlmTpm.IsNull() {
		t.Errorf("max_llm_tpm = %v, want cleared", cleared.MaxLlmTpm)
	}

	// Nothing about the ceilings changed: no PUT.
	_, d = l.update(cleared, cleared)
	mustSucceed(t, d)
	if n := len(f.calls("PUT", `/rate-limits`)); n != 2 {
		t.Errorf("rate-limit PUTs = %d, want 2", n)
	}
}

// A ceiling set in the console shows up on refresh, so Terraform plans it away
// when the configuration does not set it.
func TestAiApiKeyReadReportsCeilings(t *testing.T) {
	_, r := fakeKeys(t)
	l := newLifecycle[aiApiKeyModel](t, r)
	created, d := l.create(apiKeyPlan())
	mustSucceed(t, d)
	if _, err := r.p.API.SetApiKeyRateLimits(t.Context(), r.p.OrgID, created.ID.ValueString(),
		console.SetApiKeyRateLimitsRequest{MaxLlmRpm: ptr(int32(30))}); err != nil {
		t.Fatal(err)
	}
	got, removed, d := l.read(created)
	mustSucceed(t, d)
	if removed || got.MaxLlmRpm.ValueInt64() != 30 || got.Key.ValueString() != "hf_ab.secret" {
		t.Errorf("state = %+v (removed %v)", got, removed)
	}
}
