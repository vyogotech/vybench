#!/usr/bin/env bash
# Generate multi-distribution APT repository index and publish to Cloudflare R2 (apt.vyogo.tech).
#
# Supports:
#   - Debian 12 (bookworm): deb [trusted=yes] https://apt.vyogo.tech bookworm main
#   - Ubuntu 24.04 (noble):  deb [trusted=yes] https://apt.vyogo.tech noble main
#   - Stable alias:          deb [trusted=yes] https://apt.vyogo.tech stable main
#
# Usage:
#   ./scripts/publish-apt-repo.sh [path/to/dist]
set -euo pipefail

DIST_DIR="${1:-dist}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REPO_DIR="$ROOT/.build-cache/apt-repo"

echo "=== Building APT Repository from $DIST_DIR ==="

if [ ! -d "$DIST_DIR" ]; then
  echo "error: dist directory not found at $DIST_DIR" >&2
  exit 1
fi

DEB_COUNT="$(find "$DIST_DIR" -maxdepth 1 -name '*.deb' | wc -l | tr -d ' ')"
if [ "$DEB_COUNT" -eq 0 ]; then
  echo "warning: no .deb files found in $DIST_DIR; skipping APT publish"
  exit 0
fi

# Clean and prepare directory tree
rm -rf "$REPO_DIR"
mkdir -p "$REPO_DIR/pool/main/v/vybench"

# Distribution Suites
mkdir -p "$REPO_DIR/dists/bookworm/main/binary-amd64"
mkdir -p "$REPO_DIR/dists/noble/main/binary-amd64"
mkdir -p "$REPO_DIR/dists/noble/main/binary-arm64"
mkdir -p "$REPO_DIR/dists/stable/main/binary-amd64"
mkdir -p "$REPO_DIR/dists/stable/main/binary-arm64"

# Copy deb files to pool
cp "$DIST_DIR"/*.deb "$REPO_DIR/pool/main/v/vybench/"

# Ensure both ~ and . naming conventions exist in pool for reliable download URLs
for f in "$REPO_DIR/pool/main/v/vybench"/*.deb; do
  [ -f "$f" ] || continue
  alt="${f//.ubuntu/~ubuntu}"
  alt="${alt//.debian/~debian}"
  if [ "$alt" != "$f" ] && [ ! -f "$alt" ]; then
    cp "$f" "$alt"
  fi
  alt2="${f//~ubuntu/.ubuntu}"
  alt2="${alt2//~debian/.debian}"
  if [ "$alt2" != "$f" ] && [ ! -f "$alt2" ]; then
    cp "$f" "$alt2"
  fi
done

# Scan Bookworm (Debian 12)
mkdir -p "$REPO_DIR/.tmp-bookworm/pool/main/v/vybench"
cp "$REPO_DIR/pool/main/v/vybench/"*debian12* "$REPO_DIR/.tmp-bookworm/pool/main/v/vybench/" 2>/dev/null || true
if command -v dpkg-scanpackages >/dev/null 2>&1; then
  (
    cd "$REPO_DIR/.tmp-bookworm"
    dpkg-scanpackages --arch amd64 pool/ > "$REPO_DIR/dists/bookworm/main/binary-amd64/Packages" 2>/dev/null || true
    gzip -9nc "$REPO_DIR/dists/bookworm/main/binary-amd64/Packages" > "$REPO_DIR/dists/bookworm/main/binary-amd64/Packages.gz"
    
    # Copy Debian 12 amd64 to stable amd64
    cp "$REPO_DIR/dists/bookworm/main/binary-amd64/Packages" "$REPO_DIR/dists/stable/main/binary-amd64/Packages"
    cp "$REPO_DIR/dists/bookworm/main/binary-amd64/Packages.gz" "$REPO_DIR/dists/stable/main/binary-amd64/Packages.gz"
  )
fi
rm -rf "$REPO_DIR/.tmp-bookworm"

# Scan Noble (Ubuntu 24.04)
mkdir -p "$REPO_DIR/.tmp-noble/pool/main/v/vybench"
cp "$REPO_DIR/pool/main/v/vybench/"*ubuntu2404* "$REPO_DIR/.tmp-noble/pool/main/v/vybench/" 2>/dev/null || true
if command -v dpkg-scanpackages >/dev/null 2>&1; then
  (
    cd "$REPO_DIR/.tmp-noble"
    dpkg-scanpackages --arch amd64 pool/ > "$REPO_DIR/dists/noble/main/binary-amd64/Packages" 2>/dev/null || true
    gzip -9nc "$REPO_DIR/dists/noble/main/binary-amd64/Packages" > "$REPO_DIR/dists/noble/main/binary-amd64/Packages.gz"
    
    dpkg-scanpackages --arch arm64 pool/ > "$REPO_DIR/dists/noble/main/binary-arm64/Packages" 2>/dev/null || true
    gzip -9nc "$REPO_DIR/dists/noble/main/binary-arm64/Packages" > "$REPO_DIR/dists/noble/main/binary-arm64/Packages.gz"

    # Copy Ubuntu 24.04 arm64 to stable arm64
    cp "$REPO_DIR/dists/noble/main/binary-arm64/Packages" "$REPO_DIR/dists/stable/main/binary-arm64/Packages"
    cp "$REPO_DIR/dists/noble/main/binary-arm64/Packages.gz" "$REPO_DIR/dists/stable/main/binary-arm64/Packages.gz"
  )
fi
rm -rf "$REPO_DIR/.tmp-noble"

# Generate Release metadata file for each suite
python3 - "$REPO_DIR" <<'PY'
import os, sys, hashlib, datetime

repo = sys.argv[1]
suites = [
    ("bookworm", "Debian 12 (Bookworm)", "amd64"),
    ("noble", "Ubuntu 24.04 (Noble)", "amd64 arm64"),
    ("stable", "Vybench Stable Suite", "amd64 arm64"),
]

now = datetime.datetime.now(datetime.timezone.utc).strftime("%a, %d %b %Y %H:%M:%S UTC")

for suite_name, desc, archs in suites:
    suite_dir = os.path.join(repo, "dists", suite_name)
    release_file = os.path.join(suite_dir, "Release")
    if not os.path.exists(suite_dir):
        continue

    header = f"""Origin: Vyogo Technologies
Label: Vybench
Suite: {suite_name}
Codename: {suite_name}
Version: 16.0
Architectures: {archs}
Components: main
Description: {desc} - Vybench Frappe Bench & ERPNext Stack
Date: {now}
"""

    entries = []
    for root, dirs, files in os.walk(suite_dir):
        for f in sorted(files):
            if f == "Release":
                continue
            full_path = os.path.join(root, f)
            rel_path = os.path.relpath(full_path, suite_dir)
            size = os.path.getsize(full_path)
            with open(full_path, "rb") as fh:
                data = fh.read()
                sha256 = hashlib.sha256(data).hexdigest()
                md5 = hashlib.md5(data).hexdigest()
            entries.append((rel_path, size, sha256, md5))

    release_content = header + "MD5Sum:\n"
    for rel, size, sha, md in entries:
        release_content += f" {md} {size} {rel}\n"

    release_content += "SHA256:\n"
    for rel, size, sha, md in entries:
        release_content += f" {sha} {size} {rel}\n"

    with open(release_file, "w") as fh:
        fh.write(release_content)

    print(f"Generated {release_file}")
PY

# Create clean landing page
cat <<'HTML' > "$REPO_DIR/index.html"
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Vybench APT Repository — Vyogo Technologies</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; max-width: 800px; margin: 40px auto; padding: 0 20px; line-height: 1.6; color: #1f2937; }
    h1 { color: #111827; }
    pre { background: #f3f4f6; padding: 16px; border-radius: 8px; overflow-x: auto; font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; }
    code { background: #f3f4f6; padding: 2px 6px; border-radius: 4px; }
    .badge { display: inline-block; background: #e0e7ff; color: #3730a3; padding: 4px 10px; border-radius: 9999px; font-size: 0.875rem; font-weight: 600; }
  </style>
</head>
<body>
  <span class="badge">Official APT Repository</span>
  <h1>Vybench Linux Package Repository</h1>
  <p>Install and manage <strong>Frappe Bench v16 & ERPNext v16</strong> natively on Debian and Ubuntu.</p>
  
  <h2>Installation Instructions</h2>
  
  <h3>For Debian 12 (Bookworm):</h3>
  <pre><code># 1. Add repository
echo "deb [trusted=yes] https://apt.vyogo.tech bookworm main" | sudo tee /etc/apt/sources.list.d/vybench.list

# 2. Update & Install
sudo apt update
sudo apt install -y vybench

# 3. Setup site
sudo vybench-setup --site dev.localhost --admin-password admin</code></pre>

  <h3>For Ubuntu 24.04 (Noble):</h3>
  <pre><code># 1. Add repository
echo "deb [trusted=yes] https://apt.vyogo.tech noble main" | sudo tee /etc/apt/sources.list.d/vybench.list

# 2. Update & Install
sudo apt update
sudo apt install -y vybench

# 3. Setup site
sudo vybench-setup --site dev.localhost --admin-password admin</code></pre>

  <p>For documentation and source, visit <a href="https://github.com/vyogotech/vybench">github.com/vyogotech/vybench</a>.</p>
</body>
</html>
HTML

echo "=== APT Repository generated successfully ==="
find "$REPO_DIR/dists" -type f

# Publish to Cloudflare R2 if credentials are provided
if [ -n "${R2_ACCOUNT_ID:-}" ] && [ -n "${R2_ACCESS_KEY_ID:-}" ] && [ -n "${R2_SECRET_ACCESS_KEY:-}" ]; then
  BUCKET="${R2_BUCKET:-vybench-packages}"
  ENDPOINT="https://${R2_ACCOUNT_ID}.r2.cloudflarestorage.com"
  
  echo "=== Syncing APT repository to Cloudflare R2 ($BUCKET) ==="
  if ! command -v aws >/dev/null 2>&1; then
    echo "Installing AWS CLI via pip..."
    pip install --break-system-packages --quiet awscli || pip install --quiet awscli || true
  fi

  export AWS_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID"
  export AWS_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY"
  export AWS_DEFAULT_REGION="auto"
  
  aws s3 sync "$REPO_DIR" "s3://$BUCKET" \
    --endpoint-url "$ENDPOINT" \
    --no-progress
  
  echo "=== Successfully published to https://apt.vyogo.tech ==="
else
  echo "R2 credentials not set in environment; skipping S3 upload."
fi
