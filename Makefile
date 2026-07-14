BINARY := gogioia
FRONTEND := frontend

# Optional: override the HTTP port, e.g. `make run PORT=9000`.
PORT ?=
PORT_FLAG := $(if $(PORT),--port $(PORT))

# ppc64le offline-deployment artefacts.
IMAGE_NAME := gogioia:ppc64le
IMAGE_TAR  := gogioia-ppc64le.tar
# Container builds strip symbols/debug (-s -w) and local paths (-trimpath).
LDFLAGS_CONTAINER := -s -w

.PHONY: all build build-web run server dev tidy fmt clean \
        build-ppc64le image-ppc64le export-ppc64le

all: build

## build-web: install deps and compile the Vue frontend into web/dist
build-web:
	cd $(FRONTEND) && npm install && npm run build

## build: build the frontend then compile the single Go binary
build: build-web
	go build -o bin/$(BINARY) ./cmd/server
	@echo "✔ built bin/$(BINARY)"

## run: build everything and start the server (override with PORT=9000)
run: build
	./bin/$(BINARY) $(PORT_FLAG)

## server: run the Go server only (assumes web/dist already built)
server:
	go run ./cmd/server $(PORT_FLAG)

## dev: install frontend deps and start Vite dev server (proxies /api to :8080).
## Run `make server` in another terminal for the backend.
dev:
	cd $(FRONTEND) && npm install && npm run dev

tidy:
	go mod tidy

fmt:
	go fmt ./...

# ── Deploy (ppc64le, offline) ─────────────────────────────────────────────

## build-ppc64le: cross-compile a fully static ppc64le binary (embeds frontend)
build-ppc64le: build-web
	GOOS=linux GOARCH=ppc64le CGO_ENABLED=0 \
	  go build -trimpath -ldflags "$(LDFLAGS_CONTAINER)" -o bin/$(BINARY)-linux-ppc64le ./cmd/server
	@echo "✔ built bin/$(BINARY)-linux-ppc64le (static ppc64le ELF)"

## image-ppc64le: build the FROM-scratch OCI image (clean single-arch, no qemu
## needed — the Alpine stage runs on $BUILDPLATFORM and the scratch stage only
## copies files). --provenance=false keeps it a single manifest so `podman load`
## on the target is predictable.
image-ppc64le: build-ppc64le
	docker build --provenance=false --platform linux/ppc64le \
	  --file deploy/Dockerfile.ppc64le --tag $(IMAGE_NAME) --no-cache .

## export-ppc64le: save the image to a tar for offline transfer to the target
export-ppc64le: image-ppc64le
	docker save --output $(IMAGE_TAR) $(IMAGE_NAME)
	@echo ""
	@echo "  Artefacto: $(IMAGE_TAR)  ($$(du -sh $(IMAGE_TAR) | cut -f1))"
	@echo "  Cargar en destino:  podman load -i $(IMAGE_TAR)"

clean:
	rm -rf bin $(IMAGE_TAR)
	rm -rf $(FRONTEND)/node_modules
