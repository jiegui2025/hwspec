VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# A root-owned location: `capture --full` refuses to run a binary that a
# non-root user could replace. Build as yourself, install with sudo:
#   make build && sudo make install
PREFIX  ?= /usr/local
LDFLAGS := -s -w -X main.version=$(VERSION)

# CGO off gives a fully static binary that runs on any x86_64/arm64 distro,
# including NixOS and musl-based ones.
export CGO_ENABLED = 0

.PHONY: build test lint cover release install fetch-ids gen-ids update-ids clean

GOLANGCI_LINT_VERSION := v2.14.0
COVERAGE_MIN := $(shell sed -n 's/^  COVERAGE_MIN: "\([0-9]*\)"/\1/p' .github/workflows/ci.yml)

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o build/hwspec ./cmd/hwspec

test:
	go vet ./...
	go test ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

cover:
	HWSPEC_SKIP_HOST_TESTS=1 CGO_ENABLED=1 go test -race -count=1 -coverpkg=./... -coverprofile=coverage.out ./... # the race detector needs cgo
	scripts/coverage.sh coverage.out $(COVERAGE_MIN)

# Reproducible: the same commit, built with the same Go toolchain and gzip,
# gives byte-identical tarballs (GNU tar format, sorted entries, fixed owner
# and modes, the commit time as every file's mtime, gzip without a name or
# timestamp).
SOURCE_DATE_EPOCH ?= $(shell git log -1 --format=%ct 2>/dev/null || echo 0)

release:
	@set -e; for arch in amd64 arm64; do \
		stage=build/stage-$$arch; rm -rf $$stage; mkdir -p $$stage; \
		GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $$stage/hwspec ./cmd/hwspec; \
		install -m644 LICENSE README.md $$stage/; chmod 755 $$stage/hwspec; \
		tar -C $$stage --format=gnu --sort=name --owner=0 --group=0 --numeric-owner \
			--mtime=@$(SOURCE_DATE_EPOCH) -cf - hwspec LICENSE README.md | gzip -9 -n > build/hwspec-$(VERSION)-linux-$$arch.tar.gz; \
		rm -rf $$stage; echo "build/hwspec-$(VERSION)-linux-$$arch.tar.gz"; \
	done
	@cd build && sha256sum hwspec-$(VERSION)-*.tar.gz > SHA256SUMS

install:
	@test -x build/hwspec || { echo "run 'make build' first (as yourself), then 'sudo make install'"; exit 1; }
	install -Dm755 build/hwspec $(DESTDIR)$(PREFIX)/bin/hwspec

# ID databases. fetch-ids downloads upstream sources; gen-ids converts them
# into DIR with a manifest (PREV keeps dates for unchanged undated files).
IDS_SRC := build/ids-src
DIR     ?= internal/ids/data
PREV    ?= $(DIR)/manifest.json

# fetch(file, url[, mirror]): retry, then fall back to a mirror. hwdata
# (github.com/vcrhonek/hwdata) mirrors pci.ids, usb.ids and oui.txt.
# HTTPS only, including redirects: the output is signed and shipped.
CURL  := curl -fsSL --proto '=https' --proto-redir '=https' --retry 4 --retry-all-errors --retry-delay 5 --connect-timeout 20
HWDATA := https://raw.githubusercontent.com/vcrhonek/hwdata/master
fetch = $(CURL) -o $(IDS_SRC)/$(1) $(2) $(if $(3),|| { echo "falling back to $(3)"; $(CURL) -o $(IDS_SRC)/$(1) $(3); })

fetch-ids:
	mkdir -p $(IDS_SRC)
	$(call fetch,pci.ids,https://pci-ids.ucw.cz/v2.2/pci.ids,$(HWDATA)/pci.ids)
	# linux-usb.org has no valid HTTPS certificate; hwdata mirrors it.
	$(call fetch,usb.ids,$(HWDATA)/usb.ids)
	$(call fetch,pnp.ids,$(HWDATA)/pnp.ids)
	$(call fetch,oui.txt,https://standards-oui.ieee.org/oui/oui.txt,$(HWDATA)/oui.txt)
	$(call fetch,decode-dimms,https://git.kernel.org/pub/scm/utils/i2c-tools/i2c-tools.git/plain/eeprom/decode-dimms)
	$(call fetch,amdgpu.ids,https://gitlab.freedesktop.org/mesa/drm/-/raw/main/data/amdgpu.ids)
	$(call fetch,bluetooth.yaml,https://bitbucket.org/bluetooth-SIG/public/raw/main/assigned_numbers/company_identifiers/company_identifiers.yaml)
	$(call fetch,intel-family.h,https://raw.githubusercontent.com/torvalds/linux/master/arch/x86/include/asm/intel-family.h)
	$(call fetch,amd.c,https://raw.githubusercontent.com/torvalds/linux/master/arch/x86/kernel/cpu/amd.c)

gen-ids:
	mkdir -p $(DIR)
	go run ./tools/genids gzip  $(IDS_SRC)/pci.ids      $(DIR)/pci.ids.gz
	go run ./tools/genids gzip  $(IDS_SRC)/usb.ids      $(DIR)/usb.ids.gz
	go run ./tools/genids gzip  $(IDS_SRC)/pnp.ids      $(DIR)/pnp.ids.gz
	go run ./tools/genids gzip  $(IDS_SRC)/amdgpu.ids   $(DIR)/amdgpu.ids.gz
	go run ./tools/genids oui   $(IDS_SRC)/oui.txt      $(DIR)/oui.ids.gz
	go run ./tools/genids jedec $(IDS_SRC)/decode-dimms $(DIR)/jedec.ids.gz
	go run ./tools/genids bluetooth $(IDS_SRC)/bluetooth.yaml $(DIR)/bluetooth.ids.gz
	go run ./tools/genids cpu $(IDS_SRC)/intel-family.h $(IDS_SRC)/amd.c tools/genids/cpu-curated.ids $(DIR)/cpu.ids.gz
	go run ./tools/genids manifest $(DIR) $(PREV)

# Refresh the copies embedded in the binary. The steps run in order even
# under make -j: gen-ids reads what fetch-ids downloads.
update-ids:
	$(MAKE) fetch-ids
	$(MAKE) gen-ids
	go test ./internal/ids/

clean:
	rm -rf build
