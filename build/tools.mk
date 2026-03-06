# Tools

KIND = bin/kind
KIND_VERSION = v0.29.0
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

GOLANGCI_LINT = bin/golangci-lint
GOLANGCI_LINT_VERSION = v2.4.0
$(GOLANGCI_LINT):
	GOBIN=$(PWD)/bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: golangci-lint-bin
golangci-lint-bin: $(GOLANGCI_LINT) # Download golangci-lint locally if necessary.

OPERATOR_SDK = bin/operator-sdk
OPERATOR_SDK_VERSION = v1.38.0
$(OPERATOR_SDK):
	@mkdir -p bin
	curl -fsSLo $(OPERATOR_SDK) https://github.com/operator-framework/operator-sdk/releases/download/$(OPERATOR_SDK_VERSION)/operator-sdk_$(OS)_$(ARCH)
	chmod +x $(OPERATOR_SDK)

.PHONY: operator-sdk
operator-sdk: $(OPERATOR_SDK) ## Download operator-sdk locally if necessary.

OPM = bin/opm
OPM_VERSION = v1.52.0
$(OPM):
	@mkdir -p bin
	curl -fsSLo $(OPM) https://github.com/operator-framework/operator-registry/releases/download/$(OPM_VERSION)/$(OS)-$(ARCH)-opm
	chmod +x $(OPM)

.PHONY: opm
opm: $(OPM) ## Download opm locally if necessary.

.PHONY: olm-tools
olm-tools: operator-sdk opm kustomize controller-gen yq ## Install all tools needed for OLM bundle/catalog generation
