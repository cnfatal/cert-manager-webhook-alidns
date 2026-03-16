GO ?= $(shell which go)
OS ?= $(shell $(GO) env GOOS)
ARCH ?= $(shell $(GO) env GOARCH)

IMAGE_REGISTRY := ghcr.io
IMAGE_NAME := cnfatal/cert-manager-webhook-alidns
IMAGE_TAG := latest

FULL_IMAGE := ${IMAGE_REGISTRY}/${IMAGE_NAME}:${IMAGE_TAG}

OUT := $(shell pwd)/_out

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Tool Binaries
ENVTEST ?= $(LOCALBIN)/setup-envtest

## Kubernetes version used for envtest binaries (derived from go.mod k8s.io/api version)
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

## controller-runtime release branch for setup-envtest (derived from go.mod)
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  printf '%s\n' "$$v" | sed -E 's/^v?([0-9]+)\.([0-9]+).*/release-\1.\2/')

$(shell mkdir -p "$(OUT)")

.PHONY: all
all: build docker

.PHONY: test
test: setup-envtest
	TEST_ASSET_ETCD=$(LOCALBIN)/k8s/$(ENVTEST_K8S_VERSION)-$(OS)-$(ARCH)/etcd \
	TEST_ASSET_KUBE_APISERVER=$(LOCALBIN)/k8s/$(ENVTEST_K8S_VERSION)-$(OS)-$(ARCH)/kube-apiserver \
	TEST_ASSET_KUBECTL=$(LOCALBIN)/k8s/$(ENVTEST_K8S_VERSION)-$(OS)-$(ARCH)/kubectl \
	$(GO) test -v .

.PHONY: setup-envtest
setup-envtest: envtest
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST)
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: clean
clean:
	chmod -R u+w $(LOCALBIN) $(OUT) 2>/dev/null || true
	rm -rf $(LOCALBIN) $(OUT)

.PHONY: build
build:
	CGO_ENABLED=0 $(GO) build -o bin/webhook -ldflags '-w -extldflags "-static"'

.PHONY: docker
docker:
	docker buildx build -t $(FULL_IMAGE) --push .

.PHONY: rendered-manifest.yaml
rendered-manifest.yaml:
	helm template \
        --set image.repository=${IMAGE_REGISTRY}/${IMAGE_NAME} \
        --set image.tag=$(IMAGE_TAG) \
		--namespace=cert-manager \
		cert-manager-webhook-alidns \
        deploy/cert-manager-webhook-alidns > deploy/rendered-manifest.yaml

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef

define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef
