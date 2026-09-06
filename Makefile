BINARY := gogioia
FRONTEND := frontend
DIST := dist
# Plantilla de configuración: acompaña SIEMPRE al binario en cada paquete.
CONFIG_EXAMPLE := gogioia.env.example

# Optional: override the HTTP port, e.g. `make run PORT=9000`.
PORT ?=
PORT_FLAG := $(if $(PORT),--port $(PORT))

# ppc64le offline-deployment artefacts.
IMAGE_NAME := gogioia:ppc64le
IMAGE_TAR  := gogioia-ppc64le.tar
# Release builds strip symbols/debug (-s -w) and local paths (-trimpath).
LDFLAGS_CONTAINER := -s -w
GOBUILD_RELEASE = CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS_CONTAINER)"

.PHONY: all build build-web run server dev tidy fmt test clean \
        cross build-windows build-windows-arm64 build-linux build-linux-arm64 \
        build-darwin build-darwin-amd64 build-ppc64le \
        dist dist-windows dist-linux dist-darwin dist-ppc64le \
        image-ppc64le export-ppc64le

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

test:
	go vet ./... && go test ./...

# ── Compilación cruzada ───────────────────────────────────────────────────
# Todos los binarios son estáticos (CGO_ENABLED=0) y llevan el frontend
# embebido con //go:embed, así que cada uno es un único archivo autocontenido:
# no necesita Node, ni Go, ni Oracle Instant Client en el destino.
#
# IMPORTANTE: web/dist debe existir antes de compilar, por eso cada objetivo
# depende de build-web (//go:embed all:dist falla si falta).

# cross_build,<goos>,<goarch>,<salida>
define cross_build
	GOOS=$(1) GOARCH=$(2) $(GOBUILD_RELEASE) -o $(3) ./cmd/server
	@echo "✔ built $(3)"
endef

## build-windows: gogioia.exe para Windows x86-64 (el caso habitual)
build-windows: build-web
	$(call cross_build,windows,amd64,bin/$(BINARY)-windows-amd64.exe)

## build-windows-arm64: Windows on ARM (Surface, Dev Kit)
build-windows-arm64: build-web
	$(call cross_build,windows,arm64,bin/$(BINARY)-windows-arm64.exe)

## build-linux: Linux x86-64 estático
build-linux: build-web
	$(call cross_build,linux,amd64,bin/$(BINARY)-linux-amd64)

## build-linux-arm64: Linux ARM64 (Raspberry Pi 4/5, Graviton)
build-linux-arm64: build-web
	$(call cross_build,linux,arm64,bin/$(BINARY)-linux-arm64)

## build-darwin: macOS Apple Silicon
build-darwin: build-web
	$(call cross_build,darwin,arm64,bin/$(BINARY)-darwin-arm64)

## build-darwin-amd64: macOS Intel
build-darwin-amd64: build-web
	$(call cross_build,darwin,amd64,bin/$(BINARY)-darwin-amd64)

## build-ppc64le: cross-compile a fully static ppc64le binary (embeds frontend)
build-ppc64le: build-web
	$(call cross_build,linux,ppc64le,bin/$(BINARY)-linux-ppc64le)

## cross: compila todas las plataformas soportadas (una sola pasada de frontend)
cross: build-web
	$(call cross_build,windows,amd64,bin/$(BINARY)-windows-amd64.exe)
	$(call cross_build,windows,arm64,bin/$(BINARY)-windows-arm64.exe)
	$(call cross_build,linux,amd64,bin/$(BINARY)-linux-amd64)
	$(call cross_build,linux,arm64,bin/$(BINARY)-linux-arm64)
	$(call cross_build,linux,ppc64le,bin/$(BINARY)-linux-ppc64le)
	$(call cross_build,darwin,arm64,bin/$(BINARY)-darwin-arm64)
	$(call cross_build,darwin,amd64,bin/$(BINARY)-darwin-amd64)

# ── Paquetes de despliegue ────────────────────────────────────────────────
# Cada paquete lleva el binario renombrado a su nombre final, la plantilla
# gogioia.env.example (que se copia a gogioia.env en el destino y el binario
# carga solo, al estar en su misma carpeta) y la documentación de despliegue.

# stage,<goos>,<goarch>,<binario origen>,<nombre destino>,<docs extra>
define stage
	rm -rf $(DIST)/$(BINARY)-$(1)-$(2)
	mkdir -p $(DIST)/$(BINARY)-$(1)-$(2)
	cp $(3) $(DIST)/$(BINARY)-$(1)-$(2)/$(4)
	cp $(CONFIG_EXAMPLE) README.md $(5) $(DIST)/$(BINARY)-$(1)-$(2)/
endef

## dist-windows: bin + gogioia.env.example + guía, en un .zip listo para copiar
dist-windows: build-windows
	$(call stage,windows,amd64,bin/$(BINARY)-windows-amd64.exe,$(BINARY).exe,DEPLOY-WINDOWS.md)
	cd $(DIST) && rm -f $(BINARY)-windows-amd64.zip && \
	  zip -qr $(BINARY)-windows-amd64.zip $(BINARY)-windows-amd64
	@echo "✔ $(DIST)/$(BINARY)-windows-amd64.zip"

## dist-linux: idem para Linux x86-64 (.tar.gz)
dist-linux: build-linux
	$(call stage,linux,amd64,bin/$(BINARY)-linux-amd64,$(BINARY),deploy/gogioia.container)
	tar -czf $(DIST)/$(BINARY)-linux-amd64.tar.gz -C $(DIST) $(BINARY)-linux-amd64
	@echo "✔ $(DIST)/$(BINARY)-linux-amd64.tar.gz"

## dist-darwin: idem para macOS Apple Silicon (.tar.gz)
dist-darwin: build-darwin
	$(call stage,darwin,arm64,bin/$(BINARY)-darwin-arm64,$(BINARY),)
	tar -czf $(DIST)/$(BINARY)-darwin-arm64.tar.gz -C $(DIST) $(BINARY)-darwin-arm64
	@echo "✔ $(DIST)/$(BINARY)-darwin-arm64.tar.gz"

## dist-ppc64le: paquete sin contenedor para ppc64le (.tar.gz)
dist-ppc64le: build-ppc64le
	$(call stage,linux,ppc64le,bin/$(BINARY)-linux-ppc64le,$(BINARY),deploy/gogioia.container)
	tar -czf $(DIST)/$(BINARY)-linux-ppc64le.tar.gz -C $(DIST) $(BINARY)-linux-ppc64le
	@echo "✔ $(DIST)/$(BINARY)-linux-ppc64le.tar.gz"

## dist: todos los paquetes anteriores
dist: dist-windows dist-linux dist-darwin dist-ppc64le
	@echo ""
	@ls -lh $(DIST)/*.zip $(DIST)/*.tar.gz

# ── Deploy (ppc64le, offline, en contenedor) ──────────────────────────────

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
	rm -rf bin $(DIST) $(IMAGE_TAR)
	rm -rf $(FRONTEND)/node_modules
