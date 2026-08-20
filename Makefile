DISTRO      ?= ubuntu:24.04
FRAPPE_BRANCH ?= version-16
FRAPPE_APPS ?= erpnext

.PHONY: help payload deb rpm snap snap-postgres snap-remote brew brew-audit brew-test brew-local-test test drift-check validate-snap validate-snap-postgres
help:
	@echo "payload      - build the bench payload for DISTRO=$(DISTRO)"
	@echo "deb          - build a .deb from the payload in dist/"
	@echo "rpm          - build an .rpm from the payload in dist/"
	@echo "snap         - build the MariaDB snap for the host arch (needs snapcraft; Linux only)"
	@echo "snap-postgres - build the PostgreSQL develop snap for the host arch (needs snapcraft; Linux only)"
	@echo "snap-remote  - build amd64 AND arm64 on Launchpad (source becomes public)"
	@echo "brew         - install the Homebrew formula from source (macOS only)"
	@echo "brew-audit   - run brew audit checks on the formula"
	@echo "brew-test    - run brew test on the installed formula"
	@echo "test         - install the built package in a clean container and verify"
	@echo "drift-check  - compare the vendored nginx template against upstream"
	@echo "validate-snap - run pre-flight snap validation checks"
	@echo "validate-snap-postgres - run pre-flight snap validation checks for postgres snap"

payload:
	./scripts/build-bench-payload.sh --distro "$(DISTRO)" \
		--frappe-branch "$(FRAPPE_BRANCH)" --apps "$(FRAPPE_APPS)" --output ./dist

deb:
	./scripts/package-deb.sh --output ./dist

rpm:
	./scripts/package-rpm.sh --output ./dist

brew:
	@mkdir -p "$$(brew --repository vyogotech/tap)/Formula"
	cp brew/Formula/vybench.rb "$$(brew --repository vyogotech/tap)/Formula/"
	brew install --build-from-source vyogotech/tap/vybench

brew-audit:
	brew audit --new --strict vyogotech/tap/vybench-local --except urls

brew-test:
	brew test vyogotech/tap/vybench

# Rebuild the local-test tarball, update its SHA256 in the formula, and install.
# Safe to re-run: brew reinstalls if already present.
brew-local-test:
	@echo "==> Building local test tarball..."
	rm -rf /tmp/vybench-16.0.0
	mkdir /tmp/vybench-16.0.0
	rsync -a --exclude='.git' --exclude='dist/*.deb' \
	         --exclude='dist/*.tar.gz' --exclude='dist/*.snap' \
	         . /tmp/vybench-16.0.0/
	cd /tmp && tar czf vybench-test.tar.gz vybench-16.0.0/
	@SHA=$$(shasum -a 256 /tmp/vybench-test.tar.gz | awk '{print $$1}'); \
	  echo "==> SHA256: $$SHA"; \
	  python3 -c "import re; p='brew/Formula/vybench-local.rb'; c=open(p).read(); open(p,'w').write(re.sub(r'(url \"file://[^\"]+\"[\s\S]*?sha256 \")[0-9a-fA-F]+(\")', r'\g<1>' + '$$SHA' + r'\2', c, count=1))"
	@mkdir -p "$$(brew --repository vyogotech/tap)/Formula"
	cp brew/Formula/vybench-local.rb "$$(brew --repository vyogotech/tap)/Formula/"
	@echo "==> Installing vybench-local (this takes ~10 min on first run)..."
	brew uninstall --ignore-dependencies vybench-local 2>/dev/null || true
	brew install --build-from-source vyogotech/tap/vybench-local

snap:
	mkdir -p dist && snapcraft pack --output=dist/vybench_$(shell uname -m).snap

snap-postgres:
	cp snap/snapcraft.postgres.yaml snap/snapcraft.yaml
	mkdir -p dist && snapcraft pack --output=dist/vybench-postgres_$(shell uname -m).snap

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

validate-snap:
	./scripts/validate-snap.sh

validate-snap-postgres:
	./scripts/validate-snap.sh snap/snapcraft.postgres.yaml

