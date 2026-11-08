// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/google/uuid"
)

// A schedule left out of the configuration is Optional+Computed: in any plan
// that changes something else the framework marks it unknown before the plan
// modifiers run. These drive both pipeline resources through the protocol, as
// Terraform does, and check that such a plan never replaces the pipeline.

func pipelineScheduleCases(t *testing.T, d *tfDriver, base map[string]any) {
	t.Helper()
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	t.Run("toggle suspended, schedule omitted", func(t *testing.T) {
		s := d.mustApply(tfState{}, d.config(base))
		p := d.plan(s, d.config(with(map[string]any{"suspended": true})))
		if p.err != nil || p.replace {
			t.Fatalf("plan: err %v, replace %v", p.err, p.replace)
		}
	})

	t.Run("set max_duration_minutes, schedule omitted", func(t *testing.T) {
		s := d.mustApply(tfState{}, d.config(base))
		p := d.plan(s, d.config(with(map[string]any{"max_duration_minutes": 30})))
		if p.err != nil || p.replace {
			t.Fatalf("plan: err %v, replace %v", p.err, p.replace)
		}
	})

	t.Run("import a cron pipeline, schedule omitted", func(t *testing.T) {
		s := d.mustApply(tfState{}, d.config(with(map[string]any{"schedule": "0 6 * * *"})))
		imported, err := d.importState(idOf(t, s))
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if got := get(imported, "schedule"); got != "0 6 * * *" {
			t.Fatalf("imported schedule = %v", got)
		}
		p := d.plan(imported, d.config(with(map[string]any{"max_duration_minutes": 30})))
		if p.err != nil || p.replace {
			t.Fatalf("plan: err %v, replace %v", p.err, p.replace)
		}
	})
}

func TestPipelineScheduleOmittedNeverReplaces(t *testing.T) {
	f, r := fakePipelines(t)
	d := newDriver(t, r.p, "hyperfluid_pipeline")
	pipelineScheduleCases(t, d, map[string]any{
		"env": f.env.String(), "name": "invoices", "type": "trino",
		"spec": `{"pipeline_type":"markitdown","source":{"type":"harbor_bucket","bucket_name":"inbox"}}`,
	})
}

func TestOpenDataPipelineScheduleOmittedNeverReplaces(t *testing.T) {
	f, r, _ := fakeOpenDataPipelines(t)
	d := newDriver(t, r.p, "hyperfluid_open_data_pipeline")
	pipelineScheduleCases(t, d, map[string]any{
		"env": f.env.String(), "name": "opendata-cat-opidor", "connector_slug": "cat_opidor",
		"params":            `{"include_stopped":false,"scope":"services_structures_ateliers"}`,
		"data_container_id": uuid.NewString(), "schema_name": "directory", "table_name": "cat_opidor",
	})
}
