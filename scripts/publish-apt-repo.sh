#!/usr/bin/env bash
# Generate APT repository index and publish to Cloudflare R2 (apt.vyogo.tech).
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
mkdir -p "$REPO_DIR/dists/stable/main/binary-amd64"
mkdir -p "$REPO_DIR/dists/stable/main/binary-arm64"

# Copy deb files to pool
cp "$DIST_DIR"/*.deb "$REPO_DIR/pool/main/v/vybench/"

# Ensure both ~ and . naming conventions exist in pool
for f in "$REPO_DIR/pool/main/v/vybench"/*.deb; do
  [ -f "$f" ] || continue
  alt="${f//.ubuntu/~ubuntu}"
  alt="${alt//.debian/~debian}"
  if [ "$alt" != "$f" ]; then
    cp "$f" "$alt"
  fi
  alt2="${f//~ubuntu/.ubuntu}"
  alt2="${alt2//~debian/.debian}"
  if [ "$alt2" != "$f" ]; then
    cp "$f" "$alt2"
  fi
done

# Generate Packages & Packages.gz for amd64
cd "$REPO_DIR"
if command -v dpkg-scanpackages >/dev/null 2>&1; then
  dpkg-scanpackages --arch amd64 pool/ > dists/stable/main/binary-amd64/Packages 2>/dev/null || true
  if [ -s dists/stable/main/binary-amd64/Packages ]; then
    gzip -9nc dists/stable/main/binary-amd64/Packages > dists/stable/main/binary-amd64/Packages.gz
  fi

  dpkg-scanpackages --arch arm64 pool/ > dists/stable/main/binary-arm64/Packages 2>/dev/null || true
  if [ -s dists/stable/main/binary-arm64/Packages ]; then
    gzip -9nc dists/stable/main/binary-arm64/Packages > dists/stable/main/binary-arm64/Packages.gz
  fi
fi

# Generate Release metadata file with SHA256 checksums
python3 - "$REPO_DIR" <<'PY'
import os, sys, hashlib, datetime

repo = sys.argv[1]
stable_dir = os.path.join(repo, "dists", "stable")
release_file = os.path.join(stable_dir, "Release")

now = datetime.datetime.now(datetime.timezone.utc).strftime("%a, %d %b %Y %H:%M:%S UTC")

header = f"""Origin: Vyogo Technologies
Label: Vybench
Suite: stable
Codename: stable
Version: 16.0
Architectures: amd64 arm64
Components: main
Description: Vybench Frappe Bench & ERPNext Single-Node Stack for Linux
Date: {now}
"""

entries = []
for root, dirs, files in os.walk(stable_dir):
    for f in sorted(files):
        if f == "Release":
            continue
        full_path = os.path.join(root, f)
        rel_path = os.path.relpath(full_path, stable_dir)
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
  <pre><code># 1. Add the Vybench repository
echo "deb [trusted=yes] https://apt.vyogo.tech stable main" | sudo tee /etc/apt/sources.list.d/vybench.list

# 2. Update package index
sudo apt update

# 3. Install Vybench
sudo apt install -y vybench

# 4. Initialize site
sudo vybench-setup --site mysite.localhost --admin-password admin</code></pre>

  <p>For documentation and source, visit <a href="https://github.com/vyogotech/vybench">github.com/vyogotech/vybench</a>.</p>
</body>
</html>
HTML

echo "=== APT Repository generated successfully ==="
find "$REPO_DIR" -type f

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
    --no-progress \
    --delete
  
  echo "=== Successfully published to https://apt.vyogo.tech ==="
else
  echo "R2 credentials not set in environment; skipping S3 upload."
fi
