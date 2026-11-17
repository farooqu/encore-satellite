.PHONY: check test boundaries lab-up lab-provision lab-test lab-test-modes biscuit-build biscuit-check biscuit-base biscuit-package biscuit-kernel biscuit-sendspin

BISCUIT_KERNEL_ROOT ?= build/biscuit/kernel
BISCUIT_SENDSPIN_ROOT ?= build/biscuit/sendspin

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
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -o build/biscuit/encore-satellite ./devices/biscuit/cmd/encore-satellite
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -o build/biscuit/wmtup ./devices/biscuit/cmd/wmtup
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -o build/biscuit/btbridge ./cmd/btbridge

biscuit-check:
	python3 -m unittest discover -s devices/biscuit/packaging -p 'test_*.py' -v
	python3 -m unittest discover -s platforms/linuxsendspin -p 'test_build.py' -v
	GOOS=linux GOARCH=arm GOARM=7 go list -deps ./devices/biscuit/cmd/encore-satellite | python3 -c 'import sys; deps=set(sys.stdin.read().splitlines()); root="github.com/farooqu/encore-satellite/backends/"; assert root+"incubator" in deps and root+"assist" not in deps, "Biscuit package must contain Incubator only"; print("Incubator-only Biscuit dependency closure passed")'
	@for f in base.sh slotctl init boot.sh dhcp.sh run health console recovery bluetooth ssh network firewall sendspin.sh; do sh -n devices/biscuit/packaging/$$f || exit; done
	bash -n devices/biscuit/packaging/kernel.sh

biscuit-kernel:
	bash devices/biscuit/packaging/kernel.sh "$(BISCUIT_KERNEL_ROOT)"

biscuit-base:
	mkdir -p build/biscuit
	sh devices/biscuit/packaging/base.sh build/biscuit/base.tar.gz

biscuit-sendspin:
	sh devices/biscuit/packaging/sendspin.sh --target armv7 --out "$(BISCUIT_SENDSPIN_ROOT)"

biscuit-package: biscuit-build biscuit-kernel biscuit-sendspin
	@test -n "$(BISCUIT_REVISION)" || { echo 'Set BISCUIT_REVISION to a new positive local development revision'; exit 1; }
	python3 devices/biscuit/packaging/package.py --base build/biscuit/base.tar.gz --binary build/biscuit/encore-satellite --wmtup build/biscuit/wmtup --btbridge build/biscuit/btbridge --kernel-artifact "$$(python3 devices/biscuit/packaging/kernel_artifact.py path --root "$(BISCUIT_KERNEL_ROOT)")" --sendspin-artifact "$(BISCUIT_SENDSPIN_ROOT)" --revision "$(BISCUIT_REVISION)" --output build/biscuit/encore-biscuit-$(BISCUIT_REVISION).zip
