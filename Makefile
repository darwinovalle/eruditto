# Eruditto top-level Makefile
#
# Conventions:
#   - Recipes use tabs (not spaces) for indentation.
#   - .PHONY is used for every recipe so behavior is stable regardless of
#     stray files in the working tree.

VERSION ?= 0.1.0
DEB_ROOT  := packaging/deb/eruditto
DEB_FILE  := build/eruditto_$(VERSION)_amd64.deb

.PHONY: help smoke build test vet fmt clean deb

help:
	@echo "Eruditto - available targets:"
	@echo "  make smoke       - run the Phase 0 environment smoke test (Fyne, CGO, X11)"
	@echo "  make build       - build the eruditto binary into ./build/"
	@echo "  make test        - run go test ./..."
	@echo "  make vet         - run go vet ./..."
	@echo "  make fmt         - run gofmt -w on the whole tree"
	@echo "  make deb         - build stripped binary + package as .deb"
	@echo "  make clean       - remove ./build/ and test artifacts"

smoke:
	@bash scripts/smoke-test.sh

build:
	@mkdir -p build
	@go build -o build/eruditto ./cmd/eruditto

test:
	@go test ./...

vet:
	@go vet ./...

fmt:
	@gofmt -w .

deb: vet test
	@echo "Building eruditto $(VERSION) .deb..."
	@mkdir -p build
	# Stage the .deb install root: DEBIAN/control, postinst, postrm, icons.
	# The source files live in packaging/deb/ (the maintainer's working copy),
	# and dpkg-deb --build expects them inside the install root at
	# packaging/deb/eruditto/DEBIAN/. Copy them fresh every build so a stale
	# install root can never sneak in.
	@mkdir -p $(DEB_ROOT)/DEBIAN
	@mkdir -p $(DEB_ROOT)/usr/share/icons/hicolor/32x32/apps
	@mkdir -p $(DEB_ROOT)/usr/share/icons/hicolor/64x64/apps
	@mkdir -p $(DEB_ROOT)/usr/share/icons/hicolor/128x128/apps
	@mkdir -p $(DEB_ROOT)/usr/share/applications
	@cp packaging/deb/control $(DEB_ROOT)/DEBIAN/control
	@cp packaging/deb/postinst $(DEB_ROOT)/DEBIAN/postinst
	@cp packaging/deb/postrm $(DEB_ROOT)/DEBIAN/postrm
	@chmod 0755 $(DEB_ROOT)/DEBIAN/postinst $(DEB_ROOT)/DEBIAN/postrm
	@cp assets/icons/small_white_icon.png  $(DEB_ROOT)/usr/share/icons/hicolor/32x32/apps/eruditto.png
	@cp assets/icons/medium_white_icon.png $(DEB_ROOT)/usr/share/icons/hicolor/64x64/apps/eruditto.png
	@cp assets/icons/larger_white_icon.png $(DEB_ROOT)/usr/share/icons/hicolor/128x128/apps/eruditto.png
	# Build the binary into the install root.
	@go build -ldflags "-s -w -X main.version=$(VERSION)" -o $(DEB_ROOT)/usr/bin/eruditto ./cmd/eruditto
	# Stamp the version into DEBIAN/control.
	@sed -i 's/^Version: .*/Version: $(VERSION)/' $(DEB_ROOT)/DEBIAN/control
	@dpkg-deb --build $(DEB_ROOT) $(DEB_FILE)
	@echo "Package ready: $(DEB_FILE) ($$(du -h $(DEB_FILE) | cut -f1))"

clean:
	@rm -rf build
	@rm -f coverage.out coverage.html
