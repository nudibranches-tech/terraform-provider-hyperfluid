// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	pschema "github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// tfDriver drives one resource through the provider's protocol server the way
// Terraform does: validate, plan (plan modifiers, defaults, private state),
// apply, refresh, import and destroy. Values are written as Go literals: a
// map for an object, a slice for a set, a string, nil for null.
type tfDriver struct {
	t        *testing.T
	srv      tfprotov6.ProviderServer
	typeName string
	typ      tftypes.Object
	computed map[string]bool
}

// tfState is what Terraform keeps between runs: the state and the private
// state the provider attached to it.
type tfState struct {
	value   tftypes.Value
	private []byte
}

// configuredProvider wraps the provider so that it configures itself with pd
// instead of credentials.
type configuredProvider struct {
	provider.Provider
	pd *providerData
}

func (p *configuredProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = pschema.Schema{}
}

func (p *configuredProvider) Configure(_ context.Context, _ provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	resp.ResourceData, resp.DataSourceData = p.pd, p.pd
}

func newDriver(t *testing.T, pd *providerData, typeName string) *tfDriver {
	t.Helper()
	srv := providerserver.NewProtocol6(&configuredProvider{Provider: New("test")(), pd: pd})()
	ctx := t.Context()
	schemas, err := srv.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil || hasErrors(schemas.Diagnostics) {
		t.Fatalf("schema: %v %v", err, schemas.Diagnostics)
	}
	s, ok := schemas.ResourceSchemas[typeName]
	if !ok {
		t.Fatalf("no resource %s", typeName)
	}
	typ, ok := s.ValueType().(tftypes.Object)
	if !ok {
		t.Fatalf("%s: schema is not an object", typeName)
	}
	d := &tfDriver{t: t, srv: srv, typeName: typeName, typ: typ, computed: map[string]bool{}}
	for _, a := range s.Block.Attributes {
		d.computed[a.Name] = a.Computed
	}
	cfg := d.dyn(tftypes.Object{AttributeTypes: map[string]tftypes.Type{}}, tftypes.NewValue(tftypes.Object{AttributeTypes: map[string]tftypes.Type{}}, map[string]tftypes.Value{}))
	resp, err := srv.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{Config: cfg})
	if err != nil || hasErrors(resp.Diagnostics) {
		t.Fatalf("configure: %v %v", err, resp.Diagnostics)
	}
	return d
}

func hasErrors(ds []*tfprotov6.Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			return true
		}
	}
	return false
}

// diagErr folds error diagnostics into one error, nil when there are none.
func diagErr(ds []*tfprotov6.Diagnostic) error {
	var msgs []string
	for _, d := range ds {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			msgs = append(msgs, d.Summary+": "+d.Detail)
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}

func (d *tfDriver) dyn(typ tftypes.Type, v tftypes.Value) *tfprotov6.DynamicValue {
	d.t.Helper()
	dv, err := tfprotov6.NewDynamicValue(typ, v)
	if err != nil {
		d.t.Fatalf("encode: %v", err)
	}
	return &dv
}

func (d *tfDriver) undyn(dv *tfprotov6.DynamicValue) tftypes.Value {
	d.t.Helper()
	if dv == nil {
		return tftypes.NewValue(d.typ, nil)
	}
	v, err := dv.Unmarshal(d.typ)
	if err != nil {
		d.t.Fatalf("decode: %v", err)
	}
	return v
}

// value builds a value of typ from a Go literal.
func value(typ tftypes.Type, v any) tftypes.Value {
	if v == nil {
		return tftypes.NewValue(typ, nil)
	}
	switch typ := typ.(type) {
	case tftypes.Object:
		m, _ := v.(map[string]any)
		attrs := map[string]tftypes.Value{}
		for name, at := range typ.AttributeTypes {
			attrs[name] = value(at, m[name])
		}
		for name := range m {
			if _, ok := typ.AttributeTypes[name]; !ok {
				panic("no attribute " + name)
			}
		}
		return tftypes.NewValue(typ, attrs)
	case tftypes.Set:
		var elems []tftypes.Value
		l, _ := v.([]any)
		for _, e := range l {
			elems = append(elems, value(typ.ElementType, e))
		}
		return tftypes.NewValue(typ, elems)
	}
	return tftypes.NewValue(typ, v)
}

// config builds a resource configuration; attributes it leaves out are null.
func (d *tfDriver) config(attrs map[string]any) tftypes.Value { return value(d.typ, attrs) }

// proposed is Terraform's proposed new state: the configuration, with the
// prior value of every computed attribute the configuration leaves null.
func (d *tfDriver) proposed(prior, config tftypes.Value) tftypes.Value {
	if prior.IsNull() {
		return config
	}
	var p, c map[string]tftypes.Value
	_ = prior.As(&p)
	_ = config.As(&c)
	out := map[string]tftypes.Value{}
	for name, v := range c {
		out[name] = v
		if v.IsNull() && d.computed[name] {
			out[name] = p[name]
		}
	}
	return tftypes.NewValue(d.typ, out)
}

type planResult struct {
	planned tftypes.Value
	private []byte
	replace bool
	err     error
}

// noop reports a plan with nothing to change.
func (p planResult) noop(prior tfState) bool { return p.planned.Equal(prior.value) }

func (d *tfDriver) validate(config tftypes.Value) error {
	resp, err := d.srv.ValidateResourceConfig(d.t.Context(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: d.typeName, Config: d.dyn(d.typ, config),
	})
	if err != nil {
		return err
	}
	return diagErr(resp.Diagnostics)
}

func (d *tfDriver) plan(prior tfState, config tftypes.Value) planResult {
	d.t.Helper()
	if err := d.validate(config); err != nil {
		return planResult{err: err}
	}
	if prior.value.Type() == nil {
		prior.value = tftypes.NewValue(d.typ, nil)
	}
	resp, err := d.srv.PlanResourceChange(d.t.Context(), &tfprotov6.PlanResourceChangeRequest{
		TypeName: d.typeName, PriorState: d.dyn(d.typ, prior.value), Config: d.dyn(d.typ, config),
		ProposedNewState: d.dyn(d.typ, d.proposed(prior.value, config)), PriorPrivate: prior.private,
	})
	if err != nil {
		return planResult{err: err}
	}
	if err := diagErr(resp.Diagnostics); err != nil {
		return planResult{err: err}
	}
	return planResult{planned: d.undyn(resp.PlannedState), private: resp.PlannedPrivate, replace: len(resp.RequiresReplace) > 0}
}

// apply plans config against prior and applies the plan; the returned state
// is what Terraform saves, also when the apply failed.
func (d *tfDriver) apply(prior tfState, config tftypes.Value) (tfState, error) {
	d.t.Helper()
	p := d.plan(prior, config)
	if p.err != nil {
		return prior, p.err
	}
	if p.replace {
		d.t.Fatalf("unexpected replacement planned")
	}
	if prior.value.Type() == nil {
		prior.value = tftypes.NewValue(d.typ, nil)
	}
	resp, err := d.srv.ApplyResourceChange(d.t.Context(), &tfprotov6.ApplyResourceChangeRequest{
		TypeName: d.typeName, PriorState: d.dyn(d.typ, prior.value), PlannedState: d.dyn(d.typ, p.planned),
		Config: d.dyn(d.typ, config), PlannedPrivate: p.private,
	})
	if err != nil {
		return prior, err
	}
	return tfState{value: d.undyn(resp.NewState), private: resp.Private}, diagErr(resp.Diagnostics)
}

// mustApply applies config and checks the next plan is empty.
func (d *tfDriver) mustApply(prior tfState, config tftypes.Value) tfState {
	d.t.Helper()
	s, err := d.apply(prior, config)
	if err != nil {
		d.t.Fatalf("apply: %v", err)
	}
	s = d.refresh(s)
	if p := d.plan(s, config); p.err != nil || !p.noop(s) {
		d.t.Fatalf("plan after apply is not empty (err %v):\nstate %v\nplan  %v", p.err, s.value, p.planned)
	}
	return s
}

func (d *tfDriver) refresh(s tfState) tfState {
	d.t.Helper()
	resp, err := d.srv.ReadResource(d.t.Context(), &tfprotov6.ReadResourceRequest{
		TypeName: d.typeName, CurrentState: d.dyn(d.typ, s.value), Private: s.private,
	})
	if err != nil || hasErrors(resp.Diagnostics) {
		d.t.Fatalf("refresh: %v %v", err, diagErr(resp.Diagnostics))
	}
	return tfState{value: d.undyn(resp.NewState), private: resp.Private}
}

func (d *tfDriver) importState(id string) (tfState, error) {
	d.t.Helper()
	resp, err := d.srv.ImportResourceState(d.t.Context(), &tfprotov6.ImportResourceStateRequest{TypeName: d.typeName, ID: id})
	if err != nil {
		return tfState{}, err
	}
	if err := diagErr(resp.Diagnostics); err != nil {
		return tfState{}, err
	}
	imported := tfState{value: d.undyn(resp.ImportedResources[0].State), private: resp.ImportedResources[0].Private}
	s := d.refresh(imported)
	if s.value.IsNull() {
		return s, fmt.Errorf("imported object does not exist")
	}
	return s, nil
}

func (d *tfDriver) destroy(prior tfState) error {
	d.t.Helper()
	null := tftypes.NewValue(d.typ, nil)
	plan, err := d.srv.PlanResourceChange(d.t.Context(), &tfprotov6.PlanResourceChangeRequest{
		TypeName: d.typeName, PriorState: d.dyn(d.typ, prior.value), Config: d.dyn(d.typ, null),
		ProposedNewState: d.dyn(d.typ, null), PriorPrivate: prior.private,
	})
	if err != nil {
		return err
	}
	if err := diagErr(plan.Diagnostics); err != nil {
		return err
	}
	resp, err := d.srv.ApplyResourceChange(d.t.Context(), &tfprotov6.ApplyResourceChangeRequest{
		TypeName: d.typeName, PriorState: d.dyn(d.typ, prior.value), PlannedState: d.dyn(d.typ, null),
		Config: d.dyn(d.typ, null), PlannedPrivate: plan.PlannedPrivate,
	})
	if err != nil {
		return err
	}
	return diagErr(resp.Diagnostics)
}

// get reads attribute name of a state as a Go literal: a string, a sorted
// []string for a set of strings, or a sorted []string of "a|b|c" rows for a
// set of objects, its attributes in name order.
func get(s tfState, name string) any {
	var attrs map[string]tftypes.Value
	_ = s.value.As(&attrs)
	return literal(attrs[name])
}

func literal(v tftypes.Value) any {
	if v.IsNull() {
		return nil
	}
	if !v.IsKnown() {
		return "<unknown>"
	}
	switch {
	case v.Type().Is(tftypes.String):
		var s string
		_ = v.As(&s)
		return s
	case v.Type().Is(tftypes.Number):
		var n big.Float
		_ = v.As(&n)
		return n.String()
	case v.Type().Is(tftypes.Set{}):
		var elems []tftypes.Value
		_ = v.As(&elems)
		out := []string{}
		for _, e := range elems {
			out = append(out, row(e))
		}
		sort.Strings(out)
		return out
	}
	return v.String()
}

// row renders a set element: a string as itself, an object as its attribute
// values joined by "|" in attribute-name order, a null one as "".
func row(v tftypes.Value) string {
	if v.Type().Is(tftypes.String) {
		var s string
		_ = v.As(&s)
		return s
	}
	var attrs map[string]tftypes.Value
	_ = v.As(&attrs)
	names := make([]string, 0, len(attrs))
	for n := range attrs {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, n := range names {
		if s, ok := literal(attrs[n]).(string); ok {
			parts[i] = s
		}
	}
	return strings.Join(parts, "|")
}

func idOf(t *testing.T, s tfState) string {
	t.Helper()
	id, ok := get(s, "id").(string)
	if !ok {
		t.Fatal("no id in state")
	}
	return id
}

func wantRows(t *testing.T, s tfState, name string, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	sort.Strings(want)
	got, _ := get(s, name).([]string)
	if got == nil {
		got = []string{}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s =\n  %s\nwant\n  %s", name, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func wantErr(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Fatalf("error = %v, want one containing %q", err, contains)
	}
}
