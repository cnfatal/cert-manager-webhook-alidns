OS ?= $(shell go env GOOS)
ARCH ?= $(shell go env GOARCH)

IMAGE_REGISTRY := ghcr.io
IMAGE_NAME := cnfatal/cert-manager-webhook-alidns
IMAGE_TAG := latest

FULL_IMAGE := ${IMAGE_REGISTRY}/${IMAGE_NAME}:${IMAGE_TAG}

OUT := $(shell pwd)/_out

KUBEBUILDER_VERSION=2.3.2

$(shell mkdir -p "$(OUT)")

all: build docker

test: _test/kubebuilder
	go test -v .

_test/kubebuilder:
	curl -fsSL https://github.com/kubernetes-sigs/kubebuilder/releases/download/v$(KUBEBUILDER_VERSION)/kubebuilder_$(KUBEBUILDER_VERSION)_$(OS)_$(ARCH).tar.gz -o kubebuilder-tools.tar.gz
	mkdir -p _test/kubebuilder
	tar -xvf kubebuilder-tools.tar.gz
	mv kubebuilder_$(KUBEBUILDER_VERSION)_$(OS)_$(ARCH)/bin _test/kubebuilder/
	rm kubebuilder-tools.tar.gz
	rm -R kubebuilder_$(KUBEBUILDER_VERSION)_$(OS)_$(ARCH)

clean: clean-kubebuilder

clean-kubebuilder:
	rm -Rf _test/kubebuilder

build:
	CGO_ENABLED=0 go build -o bin/webhook -ldflags '-w -extldflags "-static"'

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
