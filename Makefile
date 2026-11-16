.PHONY: check test boundaries lab-up lab-provision lab-test lab-test-modes

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
