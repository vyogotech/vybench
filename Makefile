DISTRO      ?= ubuntu:24.04
FRAPPE_BRANCH ?= version-16
FRAPPE_APPS ?= erpnext

.PHONY: help payload deb rpm snap snap-remote test drift-check
help:
	@echo "payload      - build the bench payload for DISTRO=$(DISTRO)"
	@echo "deb          - build a .deb from the payload in dist/"
	@echo "rpm          - build an .rpm from the payload in dist/"
	@echo "snap         - build the snap for the host arch (needs snapcraft; Linux only)"
	@echo "snap-remote  - build amd64 AND arm64 on Launchpad (source becomes public)"
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
	mkdir -p dist && snapcraft pack --output=dist/vybench_$(shell uname -m).snap

# Builds every architecture in snapcraft.yaml's `platforms` on Canonical's
# Launchpad build farm. This is how arm64 gets built without a GitHub Team plan
# (arm64 runners are unavailable to private repos on Free) and without an arm64
# machine.
#
# It UPLOADS THE SOURCE TO LAUNCHPAD, WHERE IT IS PUBLIC. snapcraft requires
# --launchpad-accept-public-upload precisely to make that impossible to miss.
# The packaging code carries no licence grant, so being readable is not the same
# as being reusable -- but decide deliberately, not by running this by accident.
#
# First run opens a browser for Launchpad (Ubuntu One) authentication; the
# credentials are then cached under ~/.local/share/snapcraft.
snap-remote:
	@echo "This uploads the source to Launchpad, where it will be PUBLIC."
	@printf 'Continue? [y/N] ' && read ans && [ "$$ans" = "y" ]
	snapcraft remote-build --launchpad-accept-public-upload

test:
	./scripts/test-native-package.sh --distro "$(DISTRO)"

drift-check:
	./scripts/check-nginx-template-drift.sh
