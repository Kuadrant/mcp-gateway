# Tools

KIND = bin/kind
KIND_VERSION = v0.29.0
# default node image for KIND_VERSION above, used for CI cluster creation
# (kind-create-cluster-ci) and as the base of the baked CI node image
# (build/ci-node/Dockerfile); keep in lockstep when bumping kind, digest
# included as the kind release notes require
KIND_NODE_IMAGE = kindest/node:v1.33.1@sha256:050072256b9a903bd914c0b2866828150cb229cea0efe5892e2b644d5dd3b34f
$(KIND):
	GOBIN=$(PWD)/bin go install sigs.k8s.io/kind@$(KIND_VERSION)

.PHONY: kind
kind: $(KIND) # Download kind locally if necessary.

HELM = bin/helm
HELM_VERSION = v3.18.6
$(HELM):
	mkdir -p bin
	curl -fsSL https://get.helm.sh/helm-$(HELM_VERSION)-$(OS)-$(ARCH).tar.gz | tar -xz -C bin --strip-components=1 $(OS)-$(ARCH)/helm

.PHONY: helm
helm: $(HELM) # Download helm locally if necessary.

YQ = bin/yq
YQ_VERSION = v4.47.1
$(YQ):
	GOBIN=$(PWD)/bin go install github.com/mikefarah/yq/v4@$(YQ_VERSION)

.PHONY: yq
yq: $(YQ) # Download yq locally if necessary.

KUSTOMIZE = bin/kustomize
KUSTOMIZE_VERSION = v5.7.1
$(KUSTOMIZE):
	GOBIN=$(PWD)/bin go install sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION)

.PHONY: kustomize
kustomize: $(KUSTOMIZE) # Download kustomize locally if necessary.

# Go-built linters are named after their version and the Go toolchain that
# built them, so bumping either installs a fresh copy instead of reusing a
# stale binary (golangci-lint refuses to run when built with an older Go).
GO_VERSION := $(shell go env GOVERSION 2>/dev/null)

# go-install-tool installs package $(2) at version $(3) as binary $(1).
define go-install-tool
@set -e; tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
echo "Installing $(2)@$(3)"; \
GOBIN=$$tmp go install $(2)@$(3); \
mkdir -p bin; mv "$$tmp/$$(basename $(2))" $(1)
endef

GOLANGCI_LINT_VERSION = v2.15.0
GOLANGCI_LINT = bin/golangci-lint-$(GOLANGCI_LINT_VERSION)-$(GO_VERSION)
$(GOLANGCI_LINT):
	$(call go-install-tool,$@,github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

.PHONY: golangci-lint-bin
golangci-lint-bin: $(GOLANGCI_LINT) # Download golangci-lint locally if necessary.

# keep in step with golang.org/x/tools in go.mod
GOIMPORTS_VERSION = v0.52.0
GOIMPORTS = bin/goimports-$(GOIMPORTS_VERSION)-$(GO_VERSION)
$(GOIMPORTS):
	$(call go-install-tool,$@,golang.org/x/tools/cmd/goimports,$(GOIMPORTS_VERSION))

.PHONY: goimports-bin
goimports-bin: $(GOIMPORTS) # Download goimports locally if necessary.

KUBE_API_LINTER_VERSION ?= v0.0.0-20261030144752-d90122c89087
KUBE_API_LINTER = bin/golangci-lint-kube-api-linter-$(KUBE_API_LINTER_VERSION)-$(GO_VERSION)
$(KUBE_API_LINTER):
	$(call go-install-tool,$@,sigs.k8s.io/kube-api-linter/cmd/golangci-lint-kube-api-linter,$(KUBE_API_LINTER_VERSION))

.PHONY: kube-api-linter-bin
kube-api-linter-bin: $(KUBE_API_LINTER) # Download kube-api-linter locally if necessary.
