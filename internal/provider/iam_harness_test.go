// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"strings"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// lifecycle drives one resource's Create / Read / Update / Delete / ImportState
// directly, the way Terraform would, with M as the resource's state model.
type lifecycle[M any] struct {
	t *testing.T
	r fwresource.Resource
	s rschema.Schema
}

func newLifecycle[M any](t *testing.T, r fwresource.Resource) *lifecycle[M] {
	t.Helper()
	return &lifecycle[M]{t: t, r: r, s: resourceSchema(t, r).Schema}
}

func (l *lifecycle[M]) null() tftypes.Value {
	return tftypes.NewValue(l.s.Type().TerraformType(l.t.Context()), nil)
}

func (l *lifecycle[M]) state(m M) tfsdk.State {
	l.t.Helper()
	s := tfsdk.State{Schema: l.s, Raw: l.null()}
	if d := s.Set(l.t.Context(), m); d.HasError() {
		l.t.Fatalf("state: %v", d.Errors())
	}
	return s
}

func (l *lifecycle[M]) plan(m M) tfsdk.Plan {
	s := l.state(m)
	return tfsdk.Plan(s)
}

func (l *lifecycle[M]) config(m M) tfsdk.Config {
	s := l.state(m)
	return tfsdk.Config(s)
}

// load reads a state back into the model; removed reports a state the
// resource dropped.
func (l *lifecycle[M]) load(s tfsdk.State) (m M, removed bool) {
	l.t.Helper()
	if s.Raw.IsNull() {
		return m, true
	}
	if d := s.Get(l.t.Context(), &m); d.HasError() {
		l.t.Fatalf("load state: %v", d.Errors())
	}
	return m, false
}

func (l *lifecycle[M]) create(plan M) (M, diag.Diagnostics) {
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: l.s, Raw: l.null()}}
	l.r.Create(l.t.Context(), fwresource.CreateRequest{Plan: l.plan(plan), Config: l.config(plan)}, resp)
	m, _ := l.load(resp.State)
	return m, resp.Diagnostics
}

func (l *lifecycle[M]) read(prior M) (M, bool, diag.Diagnostics) {
	return l.readState(l.state(prior))
}

func (l *lifecycle[M]) readState(prior tfsdk.State) (M, bool, diag.Diagnostics) {
	resp := &fwresource.ReadResponse{State: prior}
	l.r.Read(l.t.Context(), fwresource.ReadRequest{State: prior}, resp)
	m, removed := l.load(resp.State)
	return m, removed, resp.Diagnostics
}

func (l *lifecycle[M]) update(plan, prior M) (M, diag.Diagnostics) {
	resp := &fwresource.UpdateResponse{State: l.state(prior)}
	l.r.Update(l.t.Context(), fwresource.UpdateRequest{Plan: l.plan(plan), Config: l.config(plan), State: l.state(prior)}, resp)
	m, _ := l.load(resp.State)
	return m, resp.Diagnostics
}

func (l *lifecycle[M]) destroy(prior M) diag.Diagnostics {
	resp := &fwresource.DeleteResponse{State: l.state(prior)}
	l.r.Delete(l.t.Context(), fwresource.DeleteRequest{State: l.state(prior)}, resp)
	return resp.Diagnostics
}

// importAndRead runs ImportState with id, then the Read Terraform follows it with.
func (l *lifecycle[M]) importAndRead(id string) (M, diag.Diagnostics) {
	l.t.Helper()
	ir, ok := l.r.(fwresource.ResourceWithImportState)
	if !ok {
		l.t.Fatal("the resource does not implement ImportState")
	}
	resp := &fwresource.ImportStateResponse{State: tfsdk.State{Schema: l.s, Raw: l.null()}}
	ir.ImportState(l.t.Context(), fwresource.ImportStateRequest{ID: id}, resp)
	if resp.Diagnostics.HasError() {
		var zero M
		return zero, resp.Diagnostics
	}
	m, _, d := l.readState(resp.State)
	return m, d
}

func mustSucceed(t *testing.T, d diag.Diagnostics) {
	t.Helper()
	if d.HasError() {
		t.Fatalf("diagnostics: %v", d.Errors())
	}
}

func mustFail(t *testing.T, d diag.Diagnostics, contains string) {
	t.Helper()
	if !d.HasError() {
		t.Fatalf("expected an error containing %q", contains)
	}
	for _, e := range d.Errors() {
		if strings.Contains(e.Summary()+" "+e.Detail(), contains) {
			return
		}
	}
	t.Fatalf("no error contains %q: %v", contains, d.Errors())
}

// readDataSource runs a data source's Read against cfg, the configured
// arguments with every computed attribute null.
func readDataSource[M any](t *testing.T, ds fwdatasource.DataSource, cfg M) (M, diag.Diagnostics) {
	t.Helper()
	s := dataSourceSchema(t, ds).Schema
	null := tftypes.NewValue(s.Type().TerraformType(t.Context()), nil)
	state := tfsdk.State{Schema: s, Raw: null}
	if d := state.Set(t.Context(), cfg); d.HasError() {
		t.Fatalf("config: %v", d.Errors())
	}
	resp := &fwdatasource.ReadResponse{State: tfsdk.State{Schema: s, Raw: null}}
	ds.Read(t.Context(), fwdatasource.ReadRequest{Config: tfsdk.Config{Schema: s, Raw: state.Raw}}, resp)
	var out M
	if !resp.State.Raw.IsNull() {
		if d := resp.State.Get(t.Context(), &out); d.HasError() {
			t.Fatalf("data source state: %v", d.Errors())
		}
	}
	return out, resp.Diagnostics
}
