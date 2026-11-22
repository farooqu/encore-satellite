.PHONY: check test boundaries lab-up lab-provision lab-test lab-test-modes biscuit-build biscuit-check biscuit-base biscuit-package biscuit-kernel

BISCUIT_KERNEL_ROOT ?= build/biscuit/kernel
BISCUIT_KERNEL_JOBS ?= 2

check: boundaries
	go vet ./...
	go test -race ./...

test:
	go test -race ./...

boundaries:
	python3 tools/check_boundaries.py

lab-up:
	amp orb services ensure

lab-provision:
	lab/.venv/bin/python lab/provision.py

lab-test:
	lab/.venv/bin/python lab/test_assist.py

lab-test-modes:
	lab/.venv/bin/python lab/test_modes.py

# Local development builds, not release publication or hardware qualification.
biscuit-build:
	mkdir -p build/biscuit
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o build/biscuit/encore-satellite ./devices/biscuit/cmd/encore-satellite
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o build/biscuit/wmtup ./devices/biscuit/cmd/wmtup
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o build/biscuit/btbridge ./cmd/btbridge

biscuit-check:
	python3 -m unittest discover -s devices/biscuit/packaging -p 'test_*.py' -v
	python3 -m unittest discover -s platforms/linuxsendspin -p 'test_build.py' -v
	GOOS=linux GOARCH=arm64 go list -deps ./devices/biscuit/cmd/encore-satellite | python3 -c 'import sys; deps=set(sys.stdin.read().splitlines()); root="github.com/farooqu/encore-satellite/backends/"; assert root+"incubator" in deps and root+"assist" not in deps, "Biscuit package must contain Incubator only"; print("Incubator-only Biscuit dependency closure passed")'
	@for f in base.sh slotctl init boot.sh dhcp.sh run health console recovery bluetooth network firewall sendspin.sh; do sh -n devices/biscuit/packaging/$$f || exit; done
	@for f in arm64-init arm64-boot.sh arm64-probe-init usb-rescue runtime-service stock-firmware; do sh -n devices/biscuit/packaging/$$f || exit; done

biscuit-kernel:
	@test -n "$(BISCUIT_KERNEL_SOURCE)" || { echo 'Set BISCUIT_KERNEL_SOURCE to the pinned LibreEcho source archive'; exit 1; }
	python3 devices/biscuit/packaging/arm64_probe.py --source-archive "$(BISCUIT_KERNEL_SOURCE)" --output "$(BISCUIT_KERNEL_ROOT)" --jobs "$(BISCUIT_KERNEL_JOBS)"

biscuit-base:
	mkdir -p build/biscuit
	sh devices/biscuit/packaging/base.sh build/biscuit/base.tar.gz

biscuit-package:
	@test -n "$(BISCUIT_REVISION)" && test -n "$(BISCUIT_SEALED_BASE)" && test -n "$(BISCUIT_CORE_VERSION)" && test -n "$(BISCUIT_CA_BUNDLE)" || { echo 'Set BISCUIT_REVISION, BISCUIT_SEALED_BASE, BISCUIT_CORE_VERSION and BISCUIT_CA_BUNDLE'; exit 1; }
	python3 devices/biscuit/packaging/starter_build.py --sealed-base "$(BISCUIT_SEALED_BASE)" --revision "$(BISCUIT_REVISION)" --core-version "$(BISCUIT_CORE_VERSION)" --ca-bundle "$(BISCUIT_CA_BUNDLE)" --output "build/biscuit/encore-biscuit-$(BISCUIT_REVISION)"
