##@ OLM

PROJECT_PATH ?= $(shell pwd)

BUNDLE_CHANNELS := --channels=$(CHANNELS)
BUNDLE_DEFAULT_CHANNEL := --default-channel=$(DEFAULT_CHANNEL)
BUNDLE_METADATA_OPTS ?= $(BUNDLE_CHANNELS) $(BUNDLE_DEFAULT_CHANNEL)

CSV_BASE = config/manifests/bases/mcp-gateway.clusterserviceversion.yaml
CONTROLLER_IMG ?= ghcr.io/kuadrant/mcp-controller:$(IMAGE_TAG)
RELATED_IMAGE_ROUTER_BROKER ?= ghcr.io/kuadrant/mcp-gateway:$(IMAGE_TAG)
CONTROLLER_DEPLOYMENT = config/mcp-gateway/components/controller/deployment-controller.yaml

# file-based catalog (FBC)
CATALOG_DIR = catalog/mcp-gateway-catalog
CATALOG_FILE = $(CATALOG_DIR)/operator.yaml
CATALOG_DOCKERFILE = catalog/mcp-gateway-catalog.Dockerfile

define update-csv-config
	V="$1" $(YQ) eval '$3 = strenv(V)' -i $2
endef

.PHONY: bundle
bundle: operator-sdk kustomize yq generate-crds ## Generate OLM bundle manifests
	$(call update-csv-config,mcp-gateway.v$(BUNDLE_VERSION),$(CSV_BASE),.metadata.name)
	$(call update-csv-config,$(BUNDLE_VERSION),$(CSV_BASE),.spec.version)
	$(call update-csv-config,$(CONTROLLER_IMG),$(CSV_BASE),.metadata.annotations.containerImage)
	V="$(RELATED_IMAGE_ROUTER_BROKER)" \
	$(YQ) eval '(select(.kind == "Deployment").spec.template.spec.containers[].env[] | select(.name == "RELATED_IMAGE_ROUTER_BROKER").value) = strenv(V)' -i $(CONTROLLER_DEPLOYMENT)
	$(KUSTOMIZE) build config/manifests | $(OPERATOR_SDK) generate bundle \
		-q --overwrite \
		--version $(BUNDLE_VERSION) \
		$(BUNDLE_METADATA_OPTS) \
		--package mcp-gateway \
		--output-dir bundle
	$(MAKE) bundle-post-generate
	$(OPERATOR_SDK) bundle validate ./bundle
	$(MAKE) bundle-ignore-createdAt

.PHONY: bundle-post-generate
bundle-post-generate: yq
	$(YQ) eval-all 'select(fileIndex == 0).metadata.annotations."alm-examples" = (select(fileIndex == 1).metadata.annotations."alm-examples") | select(fileIndex == 0)' \
		bundle/manifests/mcp-gateway.clusterserviceversion.yaml $(CSV_BASE) > bundle/manifests/mcp-gateway.clusterserviceversion.yaml.tmp
	mv bundle/manifests/mcp-gateway.clusterserviceversion.yaml.tmp bundle/manifests/mcp-gateway.clusterserviceversion.yaml

.PHONY: bundle-ignore-createdAt
bundle-ignore-createdAt:
	git diff --quiet -I'^    createdAt: ' ./bundle && git checkout ./bundle || true

.PHONY: bundle-build
bundle-build: ## Build the OLM bundle image
	$(CONTAINER_ENGINE) build $(CONTAINER_ENGINE_EXTRA_FLAGS) -f bundle.Dockerfile -t $(BUNDLE_IMG) .

.PHONY: bundle-push
bundle-push: ## Push the OLM bundle image
	$(CONTAINER_ENGINE) push $(BUNDLE_IMG)

$(CATALOG_DOCKERFILE): $(OPM)
	-mkdir -p $(CATALOG_DIR)
	cd catalog && $(PROJECT_PATH)/$(OPM) generate dockerfile mcp-gateway-catalog

.PHONY: catalog
catalog: opm yq $(CATALOG_DOCKERFILE) ## Generate FBC catalog content
	-rm -rf $(CATALOG_DIR)
	-mkdir -p $(CATALOG_DIR)
	$(PROJECT_PATH)/utils/generate-catalog.sh $(PROJECT_PATH)/$(OPM) $(PROJECT_PATH)/$(YQ) $(BUNDLE_IMG) $(CHANNELS) $(CATALOG_FILE)
	cd catalog && $(PROJECT_PATH)/$(OPM) validate mcp-gateway-catalog

.PHONY: catalog-build
catalog-build: ## Build the OLM catalog image
	$(CONTAINER_ENGINE) build $(CONTAINER_ENGINE_EXTRA_FLAGS) catalog -f $(CATALOG_DOCKERFILE) -t $(CATALOG_IMG)

.PHONY: catalog-push
catalog-push: ## Push the OLM catalog image
	$(CONTAINER_ENGINE) push $(CATALOG_IMG)

CATALOG_PULL_POLICY ?=

.PHONY: deploy-catalog
deploy-catalog: kustomize yq ## Deploy controller via OLM catalog
	V="$(CATALOG_IMG)" $(YQ) eval '.spec.image = strenv(V)' -i config/deploy/olm/catalogsource.yaml
ifneq ($(CATALOG_PULL_POLICY),)
	V="$(CATALOG_PULL_POLICY)" $(YQ) eval '.spec.grpcPodConfig.imagePullPolicy = strenv(V)' -i config/deploy/olm/catalogsource.yaml
endif
	$(KUSTOMIZE) build config/deploy/olm | kubectl apply -f -

.PHONY: olm-install
olm-install: operator-sdk ## Install OLM on the cluster
	@if $(OPERATOR_SDK) olm status 2>/dev/null; then \
		echo "OLM is already installed"; \
	else \
		$(OPERATOR_SDK) olm install; \
	fi

.PHONY: olm-uninstall
olm-uninstall: operator-sdk ## Uninstall OLM from the cluster
	$(OPERATOR_SDK) olm uninstall

.PHONY: deploy-olm
deploy-olm: olm-install ## Deploy controller via OLM on local cluster
	"$(MAKE)" bundle
	"$(MAKE)" bundle-build
	"$(MAKE)" catalog
	"$(MAKE)" catalog-build
	$(call load-image,$(CATALOG_IMG))
	"$(MAKE)" deploy-catalog CATALOG_PULL_POLICY=Never

.PHONY: undeploy-olm
undeploy-olm: operator-sdk ## Remove OLM-deployed controller
	$(OPERATOR_SDK) cleanup mcp-gateway --namespace $(MCP_GATEWAY_NAMESPACE)
