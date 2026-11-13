// Pinned build-time tools for the admin API description
// (docs/internal/plans/admin-rest-api.md D21, D22).
//
// Deliberately outside /go.work, like proto/tools: the generator is not part of
// the product, and pinning it here keeps its dependency graph out of the server
// module. generate.sh builds it with GOWORK=off.
module github.com/Mark7888/two-place-paste/api/tools

go 1.26

require (
	github.com/oapi-codegen/oapi-codegen/v2 v2.8.2
	go.yaml.in/yaml/v3 v3.0.5
)

require (
	github.com/dprotaso/go-yit v0.0.0-20220510233725-9ba8df137936 // indirect
	github.com/getkin/kin-openapi v0.153.0 // indirect
	github.com/go-openapi/jsonpointer v0.23.1 // indirect
	github.com/go-openapi/swag/jsonname v0.26.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	github.com/speakeasy-api/jsonpath v0.6.3 // indirect
	github.com/speakeasy-api/openapi v1.24.0 // indirect
	github.com/vmware-labs/yaml-jsonpath v0.3.2 // indirect
	go.yaml.in/yaml/v4 v4.0.0 // indirect
	golang.org/x/mod v0.38.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	golang.org/x/tools v0.48.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
