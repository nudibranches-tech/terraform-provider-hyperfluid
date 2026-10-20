// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

func TestRabbitMQSchema(t *testing.T) {
	assertSchemaImplementation(t, NewRabbitMQResource())
}

func TestRabbitMQNameValidation(t *testing.T) {
	s := resourceSchema(t, NewRabbitMQResource()).Schema
	p := path.Root("name")
	cfg := nullConfig(t, s, nil)

	for _, name := range []string{"orders", "a", "events-1", strings.Repeat("a", 59)} {
		if d := validateString(t, s, cfg, p, types.StringValue(name)); d.HasError() {
			t.Errorf("name %q was refused: %v", name, d)
		}
	}
	for _, name := range []string{"", "Orders", "1orders", "orders-", "or_ders", strings.Repeat("a", 60)} {
		if d := validateString(t, s, cfg, p, types.StringValue(name)); !d.HasError() {
			t.Errorf("name %q was accepted", name)
		}
	}
}

func TestRabbitMQReplicasValidation(t *testing.T) {
	s := resourceSchema(t, NewRabbitMQResource()).Schema
	p := path.Root("replicas")
	cfg := nullConfig(t, s, nil)

	for _, n := range []int64{1, 3, 5} {
		if d := validateInt64(t, s, cfg, p, types.Int64Value(n)); d.HasError() {
			t.Errorf("replicas = %d was refused: %v", n, d)
		}
	}
	for _, n := range []int64{0, 2, 4, 6, -1} {
		if d := validateInt64(t, s, cfg, p, types.Int64Value(n)); !d.HasError() {
			t.Errorf("replicas = %d was accepted", n)
		}
	}
}

func TestRabbitMQStorageValidation(t *testing.T) {
	s := resourceSchema(t, NewRabbitMQResource()).Schema
	p := path.Root("storage")
	cfg := nullConfig(t, s, nil)

	// The same cases as the platform's HfRabbitMqSpec::validate.
	for _, v := range []string{"10Gi", "500M", "1073741824", "2Ti", "100k"} {
		if d := validateString(t, s, cfg, p, types.StringValue(v)); d.HasError() {
			t.Errorf("storage %q was refused: %v", v, d)
		}
	}
	for _, v := range []string{"", "0", "0Gi", "-1Gi", "1.5Gi", "10gi", "10 Gi", "Gi", "10GiB", "1e3"} {
		if d := validateString(t, s, cfg, p, types.StringValue(v)); !d.HasError() {
			t.Errorf("storage %q was accepted", v)
		}
	}
}

func TestRabbitMQPluginsValidation(t *testing.T) {
	s := resourceSchema(t, NewRabbitMQResource()).Schema
	a, ok := s.Attributes["plugins"].(schema.SetAttribute)
	if !ok {
		t.Fatal("plugins is not a set attribute")
	}
	cfg := nullConfig(t, s, nil)
	run := func(names ...string) bool {
		elems := make([]attr.Value, 0, len(names))
		for _, n := range names {
			elems = append(elems, types.StringValue(n))
		}
		req := validator.SetRequest{
			Path: path.Root("plugins"), PathExpression: path.Root("plugins").Expression(),
			Config: cfg, ConfigValue: types.SetValueMust(types.StringType, elems),
		}
		resp := &validator.SetResponse{}
		for _, v := range a.Validators {
			v.ValidateSet(context.Background(), req, resp)
		}
		return !resp.Diagnostics.HasError()
	}

	if !run("stream", "shovel", "federation", "mqtt") {
		t.Error("the allowlisted plugins were refused")
	}
	if !run() {
		t.Error("an empty plugin set was refused")
	}
	for _, bad := range []string{"management", "rabbitmq_stream", "STREAM", "web_mqtt"} {
		if run(bad) {
			t.Errorf("plugin %q was accepted", bad)
		}
	}
}

func TestRabbitMQReplicasNoDecrease(t *testing.T) {
	cases := []struct {
		state, plan types.Int64
		refused     bool
	}{
		{types.Int64Value(3), types.Int64Value(1), true},
		{types.Int64Value(5), types.Int64Value(3), true},
		{types.Int64Value(1), types.Int64Value(3), false},
		{types.Int64Value(3), types.Int64Value(3), false},
		{types.Int64Null(), types.Int64Value(1), false},    // create, or the platform default
		{types.Int64Value(3), types.Int64Unknown(), false}, // unknown until apply
	}
	for _, c := range cases {
		req := planmodifier.Int64Request{Path: path.Root("replicas"), StateValue: c.state, PlanValue: c.plan}
		resp := &planmodifier.Int64Response{PlanValue: c.plan}
		rabbitMQReplicasNoDecrease{}.PlanModifyInt64(context.Background(), req, resp)
		if got := resp.Diagnostics.HasError(); got != c.refused {
			t.Errorf("replicas %s -> %s: refused = %v, want %v", c.state, c.plan, got, c.refused)
		}
	}
}

func TestRabbitMQStorageNoShrink(t *testing.T) {
	cases := []struct {
		state, plan types.String
		refused     bool
	}{
		{types.StringValue("20Gi"), types.StringValue("10Gi"), true},
		{types.StringValue("20Gi"), types.StringValue("21G"), true}, // 21G < 20Gi across units
		{types.StringValue("20Gi"), types.StringValue("2Ti"), false},
		{types.StringValue("20Gi"), types.StringValue("20Gi"), false},
		{types.StringValue("1Gi"), types.StringValue("1073741824"), false},
		{types.StringNull(), types.StringValue("10Gi"), false},
		{types.StringValue("20Gi"), types.StringUnknown(), false},
		{types.StringValue("20Gi"), types.StringValue("bogus"), false}, // the validator's to report
	}
	for _, c := range cases {
		req := planmodifier.StringRequest{Path: path.Root("storage"), StateValue: c.state, PlanValue: c.plan}
		resp := &planmodifier.StringResponse{PlanValue: c.plan}
		rabbitMQStorageNoShrink{}.PlanModifyString(context.Background(), req, resp)
		if got := resp.Diagnostics.HasError(); got != c.refused {
			t.Errorf("storage %s -> %s: refused = %v, want %v", c.state, c.plan, got, c.refused)
		}
	}
}

func TestParseRabbitMQStorageBytes(t *testing.T) {
	cases := map[string]string{
		"1":          "1",
		"1Ki":        "1024",
		"20Gi":       strconv.FormatInt(20<<30, 10),
		"2Ti":        strconv.FormatInt(2<<40, 10),
		"500M":       "500000000",
		"100k":       "100000",
		"3P":         "3000000000000000",
		"1073741824": "1073741824",
	}
	for in, want := range cases {
		got, ok := parseRabbitMQStorageBytes(in)
		if !ok || got.String() != want {
			t.Errorf("parse(%q) = %v, %v; want %s", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "0", "1.5Gi", "10gi", "10GiB", "Gi"} {
		if _, ok := parseRabbitMQStorageBytes(in); ok {
			t.Errorf("parse(%q) succeeded", in)
		}
	}
}

func TestRabbitMQNameAndEnvForceReplacement(t *testing.T) {
	s := resourceSchema(t, NewRabbitMQResource()).Schema
	for _, attr := range []string{"name", "env"} {
		if !requiresReplaceString(t, s, path.Root(attr), types.StringValue("a"), types.StringValue("b")) {
			t.Errorf("changing %s does not force a new broker", attr)
		}
	}
	for _, attr := range []string{"storage", "image"} {
		if requiresReplaceString(t, s, path.Root(attr), types.StringValue("1Gi"), types.StringValue("2Gi")) {
			t.Errorf("changing %s forces a new broker", attr)
		}
	}
}

// The platform resolves an omitted image at reconcile time: the provider must
// never put a default into the configuration it sends.
func TestRabbitMQImageHasNoDefault(t *testing.T) {
	s := resourceSchema(t, NewRabbitMQResource()).Schema
	for _, name := range []string{"image", "storage"} {
		a, ok := s.Attributes[name].(schema.StringAttribute)
		if !ok {
			t.Fatalf("%s is not a string attribute", name)
		}
		if a.Default != nil {
			t.Errorf("%s declares a default", name)
		}
	}
	if a, ok := s.Attributes["replicas"].(schema.Int64Attribute); !ok || a.Default != nil {
		t.Error("replicas declares a default")
	}
}

func TestRabbitMQSecretNames(t *testing.T) {
	got := rabbitMQSecretNames("prod", "orders")
	want := rabbitMQSecrets{
		username:      "prod/hf-rabbitmq/orders/username",
		password:      "prod/hf-rabbitmq/orders/password",
		amqpURI:       "prod/hf-rabbitmq/orders/amqp-uri",
		managementURI: "prod/hf-rabbitmq/orders/management-uri",
	}
	if got != want {
		t.Errorf("rabbitMQSecretNames = %+v, want %+v", got, want)
	}
}

func TestRabbitMQReady(t *testing.T) {
	str := func(s string) *string { return &s }
	port := int32(5672)
	cases := []struct {
		name    string
		b       console.HfRabbitMqResponse
		ready   bool
		wantErr bool
	}{
		{"no phase yet", console.HfRabbitMqResponse{}, false, false},
		{"provisioning", console.HfRabbitMqResponse{Phase: str("Provisioning")}, false, false},
		{"ready without endpoint", console.HfRabbitMqResponse{Phase: str("Ready")}, false, false},
		{"ready", console.HfRabbitMqResponse{Phase: str("Ready"), Host: str("orders.ns.svc"), AmqpPort: &port}, true, false},
		{"failed", console.HfRabbitMqResponse{Phase: str("Failed"), Message: str("quota exceeded")}, false, true},
	}
	for _, c := range cases {
		ready, err := rabbitMQReady(&c.b)
		if ready != c.ready || (err != nil) != c.wantErr {
			t.Errorf("%s: ready = %v, err = %v; want ready = %v, err = %v", c.name, ready, err, c.ready, c.wantErr)
		}
		if c.wantErr && err != nil && !strings.Contains(err.Error(), "quota exceeded") {
			t.Errorf("%s: error %q does not carry the platform message", c.name, err)
		}
	}
}

func TestRabbitMQResourcesRoundTrip(t *testing.T) {
	ctx := context.Background()
	cpu, mem := "500m", "1Gi"
	in := &console.ResourceRequirementsSpec{Requests: &console.ResourceSpec{Cpu: &cpu, Memory: &mem}}

	obj, d := rabbitMQResourcesToObject(in)
	if d.HasError() {
		t.Fatalf("to object: %v", d)
	}
	out, d := rabbitMQResourcesFrom(ctx, obj)
	if d.HasError() {
		t.Fatalf("from object: %v", d)
	}
	if out.Limits != nil {
		t.Errorf("limits = %+v, want nil", out.Limits)
	}
	if out.Requests == nil || *out.Requests.Cpu != cpu || *out.Requests.Memory != mem {
		t.Errorf("requests = %+v, want cpu %s memory %s", out.Requests, cpu, mem)
	}

	if obj, _ := rabbitMQResourcesToObject(nil); !obj.IsNull() {
		t.Error("absent resources do not map to null")
	}
}

// The data source fills the resource's model, so both schemas must declare
// the same attributes.
func TestRabbitMQDataSourceMatchesResource(t *testing.T) {
	rs := resourceSchema(t, NewRabbitMQResource()).Schema
	ds := dataSourceSchema(t, NewRabbitMQDataSource()).Schema
	for name := range rs.Attributes {
		if _, ok := ds.Attributes[name]; !ok {
			t.Errorf("data source lacks %q", name)
		}
	}
	for name := range ds.Attributes {
		if _, ok := rs.Attributes[name]; !ok {
			t.Errorf("data source has %q, which the resource lacks", name)
		}
	}
}

// TestAccRabbitMQResource covers create → read → update (scale up, grow
// storage, add a plugin) → import → destroy. Skipped unless
// HYPERFLUID_CREDENTIALS is set.
func TestAccRabbitMQResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccRabbitMQConfig(1, "10Gi", `["stream"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_rabbitmq.mq", "name", "tf-acc-mq"),
					resource.TestCheckResourceAttr("hyperfluid_rabbitmq.mq", "replicas", "1"),
					resource.TestCheckNoResourceAttr("hyperfluid_rabbitmq.mq", "image"),
					resource.TestCheckResourceAttrSet("hyperfluid_rabbitmq.mq", "host"),
					resource.TestCheckResourceAttrSet("hyperfluid_rabbitmq.mq", "amqp_port"),
					resource.TestCheckResourceAttrSet("hyperfluid_rabbitmq.mq", "amqp_uri_secret_name"),
				),
			},
			{
				Config: testAccRabbitMQConfig(3, "20Gi", `["stream", "shovel"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperfluid_rabbitmq.mq", "replicas", "3"),
					resource.TestCheckResourceAttr("hyperfluid_rabbitmq.mq", "storage", "20Gi"),
					resource.TestCheckResourceAttr("hyperfluid_rabbitmq.mq", "plugins.#", "2"),
				),
			},
			{
				ResourceName:      "hyperfluid_rabbitmq.mq",
				ImportState:       true,
				ImportStateVerify: true,
				// Moves on its own as the broker converges.
				ImportStateVerifyIgnore: []string{"phase"},
			},
		},
	})
}

func testAccRabbitMQConfig(replicas int, storage, plugins string) string {
	return `
data "hyperfluid_env" "default" {
  name = "default"
}

resource "hyperfluid_rabbitmq" "mq" {
  env      = data.hyperfluid_env.default.id
  name     = "tf-acc-mq"
  replicas = ` + strconv.Itoa(replicas) + `
  storage  = "` + storage + `"
  plugins  = ` + plugins + `
}
`
}
