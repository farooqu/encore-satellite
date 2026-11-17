.PHONY: check test boundaries lab-up lab-provision lab-test lab-test-modes dot-build dot-check dot-base dot-package dot-kernel dot-sendspin

DOT_KERNEL_ROOT ?= build/echo-dot-2/kernel
DOT_SENDSPIN_ROOT ?= build/echo-dot-2/sendspin

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
dot-build:
	mkdir -p build/echo-dot-2
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -o build/echo-dot-2/encore-satellite ./devices/echo-dot-2/cmd/encore-satellite
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -o build/echo-dot-2/wmtup ./devices/echo-dot-2/cmd/wmtup
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -o build/echo-dot-2/btbridge ./cmd/btbridge

dot-check:
	python3 -m unittest discover -s devices/echo-dot-2/packaging -p 'test_*.py' -v
	python3 -m unittest discover -s platforms/linuxsendspin -p 'test_build.py' -v
	GOOS=linux GOARCH=arm GOARM=7 go list -deps ./devices/echo-dot-2/cmd/encore-satellite | python3 -c 'import sys; deps=set(sys.stdin.read().splitlines()); root="github.com/farooqu/encore-satellite/backends/"; assert root+"incubator" in deps and root+"assist" not in deps, "Dot package must contain Incubator only"; print("Incubator-only Dot dependency closure passed")'
	@for f in base.sh slotctl init boot.sh dhcp.sh run health console recovery bluetooth ssh network firewall sendspin.sh; do sh -n devices/echo-dot-2/packaging/$$f || exit; done
	bash -n devices/echo-dot-2/packaging/kernel.sh

dot-kernel:
	bash devices/echo-dot-2/packaging/kernel.sh "$(DOT_KERNEL_ROOT)"

dot-base:
	mkdir -p build/echo-dot-2
	sh devices/echo-dot-2/packaging/base.sh build/echo-dot-2/base.tar.gz

dot-sendspin:
	sh devices/echo-dot-2/packaging/sendspin.sh --target armv7 --out "$(DOT_SENDSPIN_ROOT)"

dot-package: dot-build dot-kernel dot-sendspin
	@test -n "$(DOT_REVISION)" || { echo 'Set DOT_REVISION to a new positive local development revision'; exit 1; }
	python3 devices/echo-dot-2/packaging/package.py --base build/echo-dot-2/base.tar.gz --binary build/echo-dot-2/encore-satellite --wmtup build/echo-dot-2/wmtup --btbridge build/echo-dot-2/btbridge --kernel-artifact "$$(python3 devices/echo-dot-2/packaging/kernel_artifact.py path --root "$(DOT_KERNEL_ROOT)")" --sendspin-artifact "$(DOT_SENDSPIN_ROOT)" --revision "$(DOT_REVISION)" --output build/echo-dot-2/encore-dot-$(DOT_REVISION).zip
