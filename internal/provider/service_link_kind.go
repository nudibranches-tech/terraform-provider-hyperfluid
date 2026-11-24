package provider

import (
	"encoding/json"

	"github.com/nudibranches-tech/terraform-provider-hyperfluid/internal/console"
)

// The spec renders ServiceLinkKind as a `oneOf` of single-value string enums
// (each kind carries its own doc comment), so oapi-codegen generates an opaque
// union struct instead of a string type. These helpers convert between that
// union and the plain kind string Terraform works with. The kind names are still
// spelled with the generated per-variant constants, so a kind the spec renames
// or drops breaks the build.
var (
	serviceLinkKindAirflow           = string(console.ServiceLinkKind0Airflow)
	serviceLinkKindContainerApp      = string(console.ServiceLinkKind1ContainerApp)
	serviceLinkKindHfKeyValueCache   = string(console.ServiceLinkKind2HfKeyValueCache)
	serviceLinkKindHfRabbitMq        = string(console.ServiceLinkKind3HfRabbitMq)
	serviceLinkKindManagedPostgreSQL = string(console.ServiceLinkKind4ManagedPostgreSQL)
	serviceLinkKindTrino             = string(console.ServiceLinkKind5Trino)
	serviceLinkKindKafka             = string(console.ServiceLinkKind6Kafka)
	serviceLinkKindPipeline          = string(console.ServiceLinkKind7Pipeline)
	serviceLinkKindHfVirtualMachine  = string(console.ServiceLinkKind8HfVirtualMachine)
	serviceLinkKindDevWorkstation    = string(console.ServiceLinkKind9DevWorkstation)
)

// serviceLinkAPIKinds is every kind the API knows, consumer or target.
var serviceLinkAPIKinds = []string{
	serviceLinkKindAirflow,
	serviceLinkKindContainerApp,
	serviceLinkKindHfKeyValueCache,
	serviceLinkKindHfRabbitMq,
	serviceLinkKindManagedPostgreSQL,
	serviceLinkKindTrino,
	serviceLinkKindKafka,
	serviceLinkKindPipeline,
	serviceLinkKindHfVirtualMachine,
	serviceLinkKindDevWorkstation,
}

// toServiceLinkKind wraps a kind string into the generated union. The union
// holds raw JSON, so a JSON string is the whole encoding.
func toServiceLinkKind(kind string) console.ServiceLinkKind {
	var k console.ServiceLinkKind
	raw, err := json.Marshal(kind)
	if err == nil {
		_ = k.UnmarshalJSON(raw)
	}
	return k
}

// serviceLinkKindString reads the kind string back out of the union; an empty
// or non-string union yields "".
func serviceLinkKindString(kind console.ServiceLinkKind) string {
	raw, err := kind.MarshalJSON()
	if err != nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
