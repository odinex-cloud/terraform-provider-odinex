# Terraform/OpenTofu provider for Odinex Cloud.
#
#   make generate   regenerate the API client from ../api/openapi.yaml
#   make build      build terraform-provider-odinex into ./bin
#   make test       unit tests
#   make testacc    acceptance tests (needs ODINEX_TOKEN, ODINEX_ENDPOINT)
#   make docs       regenerate docs/ with tfplugindocs

OPENAPI   ?= ../api/openapi.yaml
BINARY    := terraform-provider-odinex
VERSION   ?= dev

.PHONY: generate build install test testacc vet fmt docs

generate:
	cd internal/client && go tool oapi-codegen -config oapi-codegen.yaml ../../$(OPENAPI)

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/$(BINARY) .

# Installs into GOBIN for use with a dev_overrides block in ~/.terraformrc.
install:
	go install -ldflags "-X main.version=$(VERSION)" .

vet:
	go vet ./...

fmt:
	gofmt -w main.go internal/provider internal/client/helpers.go internal/client/helpers_test.go
	terraform fmt -recursive examples

test:
	go test ./...

testacc:
	TF_ACC=1 go test ./internal/provider -run TestAcc -v -timeout 60m $(TESTARGS)

docs:
	go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@latest generate --provider-name odinex
