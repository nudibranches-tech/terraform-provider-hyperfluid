// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Helpers for exercising a resource schema offline: its implementation rules
// (which the framework otherwise only checks when Terraform calls the provider)
// and the attribute validators it declares, driven against a hand-built config.

// assertSchemaImplementation runs the framework's own implementation checks —
// the rules a schema can only break at runtime, such as a write-only attribute
// with children that are not write-only, or one that is also computed.
//
// `resourceSchema` (airflow_resource_test.go) already fails the test on a bad
// implementation, so reading its schema is the whole assertion. This wrapper
// stays because it names what the call is FOR at the two sites that make it
// and nothing else with the result.
func assertSchemaImplementation(t *testing.T, r fwresource.Resource) {
	t.Helper()
	_ = resourceSchema(t, r).Schema
}

// nullConfig builds a config where every top-level attribute is null, then
// overrides the named ones. Enough to drive path-expression validators, which
// only look at sibling attributes.
func nullConfig(t *testing.T, s schema.Schema, overrides map[string]tftypes.Value) tfsdk.Config {
	t.Helper()
	ctx := context.Background()
	objType, ok := s.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatal("schema does not carry an object type")
	}
	attrs := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name, ty := range objType.AttributeTypes {
		attrs[name] = tftypes.NewValue(ty, nil)
	}
	for name, v := range overrides {
		if _, ok := objType.AttributeTypes[name]; !ok {
			t.Fatalf("no attribute named %q in the schema", name)
		}
		attrs[name] = v
	}
	return tfsdk.Config{Schema: s, Raw: tftypes.NewValue(objType, attrs)}
}

// objectValue builds a tftypes object value for the attribute at p, taking its
// type from the schema so the test cannot drift from the declared shape.
func objectValue(t *testing.T, s schema.Schema, p path.Path, members map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	ctx := context.Background()
	attr, d := s.AttributeAtPath(ctx, p)
	if d.HasError() {
		t.Fatalf("attribute %s: %v", p, d)
	}
	objType, ok := attr.GetType().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatalf("attribute %s is not an object", p)
	}
	out := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name, ty := range objType.AttributeTypes {
		out[name] = tftypes.NewValue(ty, nil)
	}
	for name, v := range members {
		if _, ok := objType.AttributeTypes[name]; !ok {
			t.Fatalf("no attribute named %q under %s", name, p)
		}
		out[name] = v
	}
	return tftypes.NewValue(objType, out)
}

// validateString runs the validators the schema declares on the string
// attribute at p, against cfg.
func validateString(t *testing.T, s schema.Schema, cfg tfsdk.Config, p path.Path, value types.String) diag.Diagnostics {
	t.Helper()
	attr, d := s.AttributeAtPath(context.Background(), p)
	if d.HasError() {
		t.Fatalf("attribute %s: %v", p, d)
	}
	sa, ok := attr.(schema.StringAttribute)
	if !ok {
		t.Fatalf("attribute %s is not a string attribute", p)
	}
	req := validator.StringRequest{
		Path:           p,
		PathExpression: p.Expression(),
		Config:         cfg,
		ConfigValue:    value,
	}
	resp := &validator.StringResponse{}
	for _, v := range sa.Validators {
		v.ValidateString(context.Background(), req, resp)
	}
	return resp.Diagnostics
}

// nullObject builds a non-null object value for the whole schema with every
// attribute null. Plan modifiers read Raw to tell create/destroy (where the
// whole object is null) from an in-place change.
func nullObject(t *testing.T, s schema.Schema) tftypes.Value {
	t.Helper()
	objType, ok := s.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("schema does not carry an object type")
	}
	attrs := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name, ty := range objType.AttributeTypes {
		attrs[name] = tftypes.NewValue(ty, nil)
	}
	return tftypes.NewValue(objType, attrs)
}

// requiresReplaceString runs the plan modifiers the schema declares on the
// string attribute at p for a change from state to plan, and reports whether
// they ask Terraform to replace the resource.
func requiresReplaceString(t *testing.T, s schema.Schema, p path.Path, state, plan types.String) bool {
	t.Helper()
	ctx := context.Background()
	attr, d := s.AttributeAtPath(ctx, p)
	if d.HasError() {
		t.Fatalf("attribute %s: %v", p, d)
	}
	sa, ok := attr.(schema.StringAttribute)
	if !ok {
		t.Fatalf("attribute %s is not a string attribute", p)
	}
	raw := nullObject(t, s)
	req := planmodifier.StringRequest{
		Path:        p,
		State:       tfsdk.State{Schema: s, Raw: raw},
		Plan:        tfsdk.Plan{Schema: s, Raw: raw},
		Config:      tfsdk.Config{Schema: s, Raw: raw},
		StateValue:  state,
		PlanValue:   plan,
		ConfigValue: plan,
	}
	resp := &planmodifier.StringResponse{PlanValue: plan}
	for _, pm := range sa.PlanModifiers {
		pm.PlanModifyString(ctx, req, resp)
	}
	if resp.Diagnostics.HasError() {
		t.Fatalf("plan modifiers on %s: %v", p, resp.Diagnostics)
	}
	return resp.RequiresReplace
}

// validateBool is validateString's counterpart for a bool attribute.
func validateBool(t *testing.T, s schema.Schema, cfg tfsdk.Config, p path.Path, value types.Bool) diag.Diagnostics {
	t.Helper()
	attr, d := s.AttributeAtPath(context.Background(), p)
	if d.HasError() {
		t.Fatalf("attribute %s: %v", p, d)
	}
	ba, ok := attr.(schema.BoolAttribute)
	if !ok {
		t.Fatalf("attribute %s is not a bool attribute", p)
	}
	req := validator.BoolRequest{
		Path:           p,
		PathExpression: p.Expression(),
		Config:         cfg,
		ConfigValue:    value,
	}
	resp := &validator.BoolResponse{}
	for _, v := range ba.Validators {
		v.ValidateBool(context.Background(), req, resp)
	}
	return resp.Diagnostics
}

// validateInt64 is validateString's counterpart for an int64 attribute.
func validateInt64(t *testing.T, s schema.Schema, cfg tfsdk.Config, p path.Path, value types.Int64) diag.Diagnostics {
	t.Helper()
	attr, d := s.AttributeAtPath(context.Background(), p)
	if d.HasError() {
		t.Fatalf("attribute %s: %v", p, d)
	}
	ia, ok := attr.(schema.Int64Attribute)
	if !ok {
		t.Fatalf("attribute %s is not an int64 attribute", p)
	}
	req := validator.Int64Request{
		Path:           p,
		PathExpression: p.Expression(),
		Config:         cfg,
		ConfigValue:    value,
	}
	resp := &validator.Int64Response{}
	for _, v := range ia.Validators {
		v.ValidateInt64(context.Background(), req, resp)
	}
	return resp.Diagnostics
}
