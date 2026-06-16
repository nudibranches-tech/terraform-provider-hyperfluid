default: fmt lint install generate

build:
	go build -v ./...

install: build
	go install -v ./...

lint:
	golangci-lint run

# Refresh the vendored OpenAPI spec from the monorepo (needs gh auth + access).
# Deliberately NOT a prerequisite of `generate`: codegen must run offline from
# the committed spec so CI never needs cross-repo credentials.
fetch-spec:
	./tools/fetch-spec.sh

generate:
	cd tools; go generate ./...

fmt:
	gofmt -s -w -e .

test:
	go test -v -cover -timeout=120s -parallel=10 ./...

testacc:
	TF_ACC=1 go test -v -cover -timeout 120m ./...

.PHONY: fmt lint test testacc build install generate fetch-spec
