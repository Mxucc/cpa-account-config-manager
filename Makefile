PLUGIN_ID := cpa-account-config-manager
DIST_DIR := $(CURDIR)/dist
WEB_DIR := $(CURDIR)/web
GIT_RELEASE_TAG := $(shell git describe --tags --exact-match --match "v[0-9]*.[0-9]*.[0-9]*" 2>/dev/null)
VERSION ?= $(or $(patsubst v%,%,$(GIT_RELEASE_TAG)),0.0.0-dev)
REPOSITORY ?=

PLUGIN_LDFLAGS := -X cpa-account-config-manager/internal/manager.PluginVersion=$(VERSION)
ifneq ($(strip $(REPOSITORY)),)
PLUGIN_LDFLAGS += -X cpa-account-config-manager/internal/manager.PluginRepository=$(REPOSITORY)
endif

UNAME_S := $(shell uname -s)
ifeq ($(OS),Windows_NT)
PLUGIN_EXT := dll
else ifeq ($(UNAME_S),Darwin)
PLUGIN_EXT := dylib
else
PLUGIN_EXT := so
endif

.PHONY: build web plugin package test version-check verify clean

build: plugin

web:
	cd $(WEB_DIR) && npm run build

plugin: web
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=1 go build -buildvcs=false -ldflags "$(PLUGIN_LDFLAGS)" -buildmode=c-shared -o $(DIST_DIR)/$(PLUGIN_ID).$(PLUGIN_EXT) .
	rm -f $(DIST_DIR)/$(PLUGIN_ID).h

package: plugin
	go run ./cmd/releasepack \
		-id $(PLUGIN_ID) \
		-version $(VERSION) \
		-goos $$(go env GOOS) \
		-goarch $$(go env GOARCH) \
		-library $(DIST_DIR)/$(PLUGIN_ID).$(PLUGIN_EXT) \
		-out $(DIST_DIR)/release

test:
	go test ./...
	cd $(WEB_DIR) && npm test -- --run

version-check:
	printf '%s\n' '$(VERSION)' | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$$'
	grep -Fq 'PluginVersion    = "0.0.0-dev"' internal/manager/app.go
	test -z "$$(grep -E '$(PLUGIN_ID)[-_]v?[0-9]+\.[0-9]+\.[0-9]+' README.md README_CN.md)"

verify: version-check
	test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './web/node_modules/*'))"
	go test ./...
	go test -race ./...
	go vet ./...
	cd $(WEB_DIR) && npm run typecheck
	cd $(WEB_DIR) && npm test -- --run
	$(MAKE) build

clean:
	rm -rf $(DIST_DIR)
