package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDynamicFPMDirectoryAndPath(t *testing.T) {
	// 1. Snap platform with SNAP_COMMON
	t.Run("Snap with SNAP_COMMON", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("SNAP", "/snap/vybench/current")
		t.Setenv("SNAP_COMMON", filepath.Join(tmp, "common"))
		t.Setenv("SNAP_USER_COMMON", filepath.Join(tmp, "user_common"))

		dir := DynamicFPMDirectory()
		expected := filepath.Join(tmp, "common", "bin")
		if dir != expected {
			t.Errorf("expected %s, got %s", expected, dir)
		}
		if DynamicFPMPath() != filepath.Join(expected, "fpm") {
			t.Errorf("unexpected DynamicFPMPath: %s", DynamicFPMPath())
		}
	})

	// 2. Snap platform with only SNAP_USER_COMMON
	t.Run("Snap with only SNAP_USER_COMMON", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("SNAP", "/snap/vybench/current")
		t.Setenv("SNAP_COMMON", "")
		t.Setenv("SNAP_USER_COMMON", filepath.Join(tmp, "user_common"))

		dir := DynamicFPMDirectory()
		expected := filepath.Join(tmp, "user_common", "bin")
		if dir != expected {
			t.Errorf("expected %s, got %s", expected, dir)
		}
	})

	// 3. Brew / Native non-snap platform
	t.Run("Non-snap fallback", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("SNAP", "")
		t.Setenv("HOME", tmp)

		dir := DynamicFPMDirectory()
		expected := filepath.Join(tmp, ".local", "share", "vybench", "bin")
		if dir != expected {
			t.Errorf("expected %s, got %s", expected, dir)
		}
	})
}

func TestFPMVersion(t *testing.T) {
	tmp := t.TempDir()

	// 1. Valid fpm binary
	mockScript := filepath.Join(tmp, "mock_fpm")
	content := "#!/bin/sh\necho 'fpm 4.6.0 (commit c7b4a97)'\n"
	if err := os.WriteFile(mockScript, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	ver := FPMVersion(mockScript)
	if ver != "4.6.0" {
		t.Errorf("expected 4.6.0, got %q", ver)
	}

	// 2. Mock binary with v-prefix
	mockScript2 := filepath.Join(tmp, "mock_fpm_v")
	content2 := "#!/bin/sh\necho 'fpm v4.6.1-dev'\n"
	if err := os.WriteFile(mockScript2, []byte(content2), 0o755); err != nil {
		t.Fatal(err)
	}

	ver2 := FPMVersion(mockScript2)
	if ver2 != "4.6.1-dev" {
		t.Errorf("expected 4.6.1-dev, got %q", ver2)
	}

	// 3. Non-existent binary
	ver3 := FPMVersion(filepath.Join(tmp, "nonexistent"))
	if ver3 != "unknown" {
		t.Errorf("expected 'unknown', got %q", ver3)
	}

	// 4. Failing binary
	mockFail := filepath.Join(tmp, "mock_fail")
	if err := os.WriteFile(mockFail, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ver4 := FPMVersion(mockFail)
	if ver4 != "unknown" {
		t.Errorf("expected 'unknown', got %q", ver4)
	}

	// 5. Binary with empty output
	mockEmpty := filepath.Join(tmp, "mock_empty")
	if err := os.WriteFile(mockEmpty, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ver5 := FPMVersion(mockEmpty)
	if ver5 != "unknown" {
		t.Errorf("expected 'unknown', got %q", ver5)
	}

	// 6. Binary with custom output
	mockCustom := filepath.Join(tmp, "mock_custom")
	if err := os.WriteFile(mockCustom, []byte("#!/bin/sh\necho 'custom-build-xyz'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ver6 := FPMVersion(mockCustom)
	if ver6 != "custom-build-xyz" {
		t.Errorf("expected 'custom-build-xyz', got %q", ver6)
	}
}

func TestCheckLatestFPM(t *testing.T) {
	ctx := context.Background()

	// 1. Fresh fetch from server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/vyogotech/fpm/releases/latest" {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"tag_name": "v4.6.0",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	tmp := t.TempDir()
	origURL := defaultGitHubReleasesURL
	defaultGitHubReleasesURL = ts.URL + "/repos/vyogotech/fpm/releases/latest"
	defer func() { defaultGitHubReleasesURL = origURL }()

	tag, err := CheckLatestFPM(ctx, ts.Client(), tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "v4.6.0" {
		t.Errorf("expected v4.6.0, got %q", tag)
	}

	// 2. Cache hit (server should not be hit if we close it or point to invalid URL)
	defaultGitHubReleasesURL = "http://invalid.127.0.0.1.nil"
	cachedTag, err := CheckLatestFPM(ctx, ts.Client(), tmp)
	if err != nil {
		t.Fatalf("unexpected error on cache hit: %v", err)
	}
	if cachedTag != "v4.6.0" {
		t.Errorf("expected cached tag v4.6.0, got %q", cachedTag)
	}

	// 3. Expired cache
	cacheFile := filepath.Join(tmp, "fpm_release_check.json")
	expiredData, _ := json.Marshal(fpmCacheEntry{
		Tag:       "v4.1.0",
		CheckedAt: time.Now().Add(-25 * time.Hour),
	})
	_ = os.WriteFile(cacheFile, expiredData, 0o644)

	// Server error on expired cache fetch
	tsError := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	defer tsError.Close()
	defaultGitHubReleasesURL = tsError.URL

	_, err = CheckLatestFPM(ctx, tsError.Client(), tmp)
	if err == nil {
		t.Errorf("expected error on rate limit, got nil")
	}

	// 4. Invalid JSON from server
	tsBadJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer tsBadJSON.Close()
	defaultGitHubReleasesURL = tsBadJSON.URL

	_ = os.Remove(cacheFile)
	_, err = CheckLatestFPM(ctx, tsBadJSON.Client(), tmp)
	if err == nil {
		t.Errorf("expected error on invalid JSON, got nil")
	}

	// 5. Empty tag_name from server
	tsEmptyTag := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"tag_name": "",
		})
	}))
	defer tsEmptyTag.Close()
	defaultGitHubReleasesURL = tsEmptyTag.URL

	_, err = CheckLatestFPM(ctx, tsEmptyTag.Client(), tmp)
	if err == nil {
		t.Errorf("expected error on empty tag, got nil")
	}

	// 6. nil client (uses default http.Client)
	defaultGitHubReleasesURL = ts.URL + "/repos/vyogotech/fpm/releases/latest"
	tagNilClient, err := CheckLatestFPM(ctx, nil, t.TempDir())
	if err != nil || tagNilClient != "v4.6.0" {
		t.Errorf("expected v4.6.0 with nil client, got %q (err: %v)", tagNilClient, err)
	}

	// 7. Network connection error
	defaultGitHubReleasesURL = "http://127.0.0.1:1/unreachable"
	_, err = CheckLatestFPM(ctx, ts.Client(), t.TempDir())
	if err == nil {
		t.Errorf("expected error on connection refused, got nil")
	}

	// 8. Invalid request URL parsing error
	defaultGitHubReleasesURL = "http://\x7f"
	_, err = CheckLatestFPM(ctx, ts.Client(), t.TempDir())
	if err == nil {
		t.Errorf("expected error on invalid control character in URL, got nil")
	}
}

func TestDownloadFPM(t *testing.T) {
	ctx := context.Background()

	// Mock release download server
	expectedOS := runtime.GOOS
	expectedArch := runtime.GOARCH

	expectedBinaryContent := "#!/bin/sh\necho 'fpm 4.6.0'\n"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := fmt.Sprintf("/releases/download/v4.6.0/fpm-%s-%s", expectedOS, expectedArch)
		if r.URL.Path == expectedPath {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(expectedBinaryContent))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	origBaseURL := defaultGitHubDownloadBaseURL
	defaultGitHubDownloadBaseURL = ts.URL + "/releases/download"
	defer func() { defaultGitHubDownloadBaseURL = origBaseURL }()

	tmp := t.TempDir()

	// 1. Successful download
	targetPath, err := DownloadFPM(ctx, ts.Client(), "v4.6.0", tmp)
	if err != nil {
		t.Fatalf("unexpected download error: %v", err)
	}
	expectedFile := filepath.Join(tmp, "fpm")
	if targetPath != expectedFile {
		t.Errorf("expected path %s, got %s", expectedFile, targetPath)
	}

	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("failed to stat installed binary: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("installed binary is not executable: mode %v", info.Mode())
	}

	// 2. 404 / Server error
	_, err = DownloadFPM(ctx, ts.Client(), "v99.99.99", tmp)
	if err == nil {
		t.Errorf("expected error for non-existent release, got nil")
	}

	// 3. Invalid destination directory (e.g. parent is a regular file)
	regularFile := filepath.Join(tmp, "file_not_dir")
	_ = os.WriteFile(regularFile, []byte("test"), 0o644)
	_, err = DownloadFPM(ctx, ts.Client(), "v4.6.0", filepath.Join(regularFile, "sub"))
	if err == nil {
		t.Errorf("expected error for invalid destDir, got nil")
	}

	// 4. Cancelled context
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = DownloadFPM(canceledCtx, ts.Client(), "v4.6.0", tmp)
	if err == nil {
		t.Errorf("expected error for canceled context, got nil")
	}

	// 5. nil client (uses default http.Client)
	defaultGitHubDownloadBaseURL = ts.URL + "/releases/download"
	downloadedNil, err := DownloadFPM(ctx, nil, "v4.6.0", t.TempDir())
	if err != nil || downloadedNil == "" {
		t.Errorf("expected successful download with nil client, got %q (err: %v)", downloadedNil, err)
	}

	// 6. Network connection error
	defaultGitHubDownloadBaseURL = "http://127.0.0.1:1/unreachable"
	_, err = DownloadFPM(ctx, ts.Client(), "v4.6.0", t.TempDir())
	if err == nil {
		t.Errorf("expected error on connection refused, got nil")
	}

	// 7. Invalid download URL parsing error
	defaultGitHubDownloadBaseURL = "http://\x7f"
	_, err = DownloadFPM(ctx, ts.Client(), "v4.6.0", t.TempDir())
	if err == nil {
		t.Errorf("expected error on invalid control character in download URL, got nil")
	}

	// 8. Truncated response body (io.Copy error)
	tsTruncated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("short"))
	}))
	defer tsTruncated.Close()
	defaultGitHubDownloadBaseURL = tsTruncated.URL
	_, err = DownloadFPM(ctx, tsTruncated.Client(), "v4.6.0", t.TempDir())
	if err == nil {
		t.Errorf("expected error on truncated download body, got nil")
	}

	// 9. Rename failure (target 'fpm' is a non-empty directory)
	dirDest := t.TempDir()
	conflictDir := filepath.Join(dirDest, "fpm")
	_ = os.MkdirAll(filepath.Join(conflictDir, "child"), 0o755)
	defaultGitHubDownloadBaseURL = ts.URL + "/releases/download"
	_, err = DownloadFPM(ctx, ts.Client(), "v4.6.0", dirDest)
	if err == nil {
		t.Errorf("expected error on rename conflict with directory, got nil")
	}
}

func TestFindFPMWithDynamicPaths(t *testing.T) {
	tmp := t.TempDir()

	// 1. Dynamic path in SNAP_COMMON takes precedence over bundled snap binary
	t.Run("Snap dynamic precedence", func(t *testing.T) {
		t.Setenv("VYBENCH_FPM", "")
		snapDir := filepath.Join(tmp, "snap")
		snapCommon := filepath.Join(tmp, "snap_common")
		t.Setenv("SNAP", snapDir)
		t.Setenv("SNAP_COMMON", snapCommon)

		bundledFPM := filepath.Join(snapDir, "bin", "fpm-wrapper")
		_ = os.MkdirAll(filepath.Dir(bundledFPM), 0o755)
		_ = os.WriteFile(bundledFPM, []byte("#!/bin/sh\nexit 0\n"), 0o755)

		dynamicFPM := filepath.Join(snapCommon, "bin", "fpm")
		_ = os.MkdirAll(filepath.Dir(dynamicFPM), 0o755)
		_ = os.WriteFile(dynamicFPM, []byte("#!/bin/sh\nexit 0\n"), 0o755)

		found, err := FindFPM()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if found != bundledFPM {
			t.Errorf("expected %s, got %s", bundledFPM, found)
		}

		// When wrapper is absent, fallback to dynamic binary directly
		_ = os.Remove(bundledFPM)
		foundFallback, err := FindFPM()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if foundFallback != dynamicFPM {
			t.Errorf("expected fallback %s, got %s", dynamicFPM, foundFallback)
		}
	})

	// 2. Non-snap dynamic path precedence
	t.Run("Brew/Native dynamic precedence", func(t *testing.T) {
		t.Setenv("VYBENCH_FPM", "")
		t.Setenv("SNAP", "")
		homeDir := filepath.Join(tmp, "home")
		t.Setenv("HOME", homeDir)

		dynamicFPM := filepath.Join(homeDir, ".local", "share", "vybench", "bin", "fpm")
		_ = os.MkdirAll(filepath.Dir(dynamicFPM), 0o755)
		_ = os.WriteFile(dynamicFPM, []byte("#!/bin/sh\nexit 0\n"), 0o755)

		found, err := FindFPM()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if found != dynamicFPM {
			t.Errorf("expected %s, got %s", dynamicFPM, found)
		}
	})
}
