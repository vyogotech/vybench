DISTRO      ?= ubuntu:24.04
FRAPPE_BRANCH ?= version-16
FRAPPE_APPS ?= erpnext

.PHONY: help payload deb rpm snap test drift-check
help:
	@echo "payload      - build the bench payload for DISTRO=$(DISTRO)"
	@echo "deb          - build a .deb from the payload in dist/"
	@echo "rpm          - build an .rpm from the payload in dist/"
	@echo "snap         - build the snap (needs snapcraft; Linux only)"
	@echo "test         - install the built package in a clean container and verify"
	@echo "drift-check  - compare the vendored nginx template against upstream"

payload:
	./scripts/build-bench-payload.sh --distro "$(DISTRO)" \
		--frappe-branch "$(FRAPPE_BRANCH)" --apps "$(FRAPPE_APPS)" --output ./dist

deb:
	./scripts/package-deb.sh --output ./dist

rpm:
	./scripts/package-rpm.sh --output ./dist

snap:
	mkdir -p dist && snapcraft pack --output=dist/frappista_amd64.snap

test:
	./scripts/test-native-package.sh --distro "$(DISTRO)"

drift-check:
	./scripts/check-nginx-template-drift.sh
