# Two providers, one repo (the upstream Cluster API model): the bootstrap
# provider (CAVBP) and the control plane provider (CAVCPP). Each ships its own
# CRDs, RBAC, manager and clusterctl metadata; clusterctl sees them as two
# providers whatever the repo layout.
IMG ?= ghcr.io/vatesfr/vates-kube-os-capi-providers:latest
YEAR ?= $(shell date +%Y)

ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

CONTAINER_TOOL ?= $(shell command -v podman 2>/dev/null || command -v docker 2>/dev/null)

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

CONTROLLER_GEN ?= controller-gen
KUSTOMIZE ?= kustomize

.PHONY: manifests
manifests: ## Generate CRDs and RBAC for both providers.
	"$(CONTROLLER_GEN)" crd paths="./api/bootstrap/..." output:crd:artifacts:config=config/bootstrap/crd/bases
	"$(CONTROLLER_GEN)" crd paths="./api/controlplane/..." output:crd:artifacts:config=config/controlplane/crd/bases
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role paths="./internal/bootstrap/...;./cmd/bootstrap/..." output:rbac:artifacts:config=config/bootstrap/rbac
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role paths="./internal/controlplane/...;./cmd/controlplane/..." output:rbac:artifacts:config=config/controlplane/rbac
	go run ./hack/contractlabels config/bootstrap/crd/bases
	go run ./hack/contractlabels config/controlplane/crd/bases

.PHONY: generate
generate: ## Generate DeepCopy methods.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt",year=$(YEAR) paths="./..."

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: test
test: manifests generate fmt vet ## Run tests.
	go test ./...

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build both managers.
	go build -o bin/bootstrap-manager ./cmd/bootstrap
	go build -o bin/controlplane-manager ./cmd/controlplane

.PHONY: run-bootstrap
run-bootstrap: manifests generate fmt vet
	go run ./cmd/bootstrap

.PHONY: run-controlplane
run-controlplane: manifests generate fmt vet
	go run ./cmd/controlplane

.PHONY: docker-build
docker-build: ## Build the (single) image carrying both managers.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push:
	$(CONTAINER_TOOL) push ${IMG}

##@ Release

# The two providers ship as TWO release artifacts: clusterctl identifies a
# provider by (type, name), so one repo serving both is two entries in its
# configuration, each pointed at its own components file. metadata.yaml is the
# same document for both -- same release series, same contract. The file names
# follow clusterctl's convention (bootstrap-components.yaml,
# control-plane-components.yaml); the hyphen in control-plane is clusterctl's,
# not the source directory's (config/controlplane).
.PHONY: release-manifests
release-manifests: manifests ## Generate the two providers' clusterctl artifacts.
	mkdir -p dist
	cd config/bootstrap/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	cd config/controlplane/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/bootstrap/default > dist/bootstrap-components.yaml
	"$(KUSTOMIZE)" build config/controlplane/default > dist/control-plane-components.yaml
	cp metadata.yaml dist/
	@echo "Release artifacts in dist/:"
	@ls -l dist/
