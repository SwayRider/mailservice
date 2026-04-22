# Makefile

SERVICE := mailservice

REGISTRY := ghcr.io/swayrider
IMAGE := $(REGISTRY)/$(SERVICE)
VERSION := $(shell git describe --tags --abbrev=0 2>/dev/null || echo latest)

all: debug release

debug:
	mkdir -p build/debug
	go build -o build/debug/$(SERVICE) cmd/$(SERVICE)/main.go

release:
	mkdir -p build/release
	go build -ldflags "-s -w" -o build/release/$(SERVICE) cmd/$(SERVICE)/main.go

install:
	go install -ldflags "-s -w" hevanto-it.com/swayrider/$(SERVICE)/cmd/$(SESRVICE)

uninstall:
	rm -f ~/go/bin/$(SERVICE)

registry-login:
	docker login docker-registry.hevanto-it.com

container-build: registry-login
	@echo "Building version $(VERSION)"
	docker buildx build \
		--progress=plain \
		--platform linux/amd64,linux/arm64 \
		-t $(IMAGE):latest \
		-t $(IMAGE):$(VERSION) \
		--push .
	@echo "Image pushed with tags latest and $(VERSION)"

list-containers:
	@read -p "Username: " username ; \
	read -s -p "Password: " password ; \
	curl -u "$${username}:$${password}" -X GET https://docker-registry.hevanto-it.com/v2/_catalog | jq

list-tags:
	@read -p "Username: " username ; \
	read -s -p "Password: " password ; \
	curl -u "$${username}:$${password}" -X GET https://docker-registry.hevanto-it.com/v2/swayrider/$(SERVICE)/tags/list | jq

.PHONY: container-build

