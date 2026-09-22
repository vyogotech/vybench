package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

var (
	defaultGitHubReleasesURL     = "https://api.github.com/repos/vyogotech/fpm/releases/latest"
	defaultGitHubDownloadBaseURL = "https://github.com/vyogotech/fpm/releases/download"
)

type fpmCacheEntry struct {
	Tag       string    `json:"tag"`
	CheckedAt time.Time `json:"checked_at"`
}

// DynamicFPMDirectory returns the platform-appropriate directory for dynamically installed FPM.
func DynamicFPMDirectory() string {
	if DetectPlatform() == PlatformSnap {
		if common := os.Getenv("SNAP_COMMON"); common != "" {
			return filepath.Join(common, "bin")
		}
		if userCommon := os.Getenv("SNAP_USER_COMMON"); userCommon != "" {
			return filepath.Join(userCommon, "bin")
		}
	}
	// Brew or Native: store in user's data directory
	home := os.Getenv("HOME")
	return filepath.Join(home, ".local", "share", "vybench", "bin")
}

// DynamicFPMPath returns the expected executable path for dynamic FPM.
func DynamicFPMPath() string {
	return filepath.Join(DynamicFPMDirectory(), "fpm")
}

// FPMVersion queries an fpm binary for its version.
func FPMVersion(fpmBin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, fpmBin, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "unknown"
	}

	str := strings.TrimSpace(string(out))
	if str == "" {
		return "unknown"
	}
	re := regexp.MustCompile(`fpm\s+v?([0-9A-Za-z\.\-]+)`)
	matches := re.FindStringSubmatch(str)
	if len(matches) > 1 {
		return matches[1]
	}
	return str
}

// CheckLatestFPM checks the latest release tag of FPM from GitHub, using a 24h cache in cacheDir.
func CheckLatestFPM(ctx context.Context, client *http.Client, cacheDir string) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	cacheFile := filepath.Join(cacheDir, "fpm_release_check.json")
	if data, err := os.ReadFile(cacheFile); err == nil {
		var entry fpmCacheEntry
		if err := json.Unmarshal(data, &entry); err == nil {
			if time.Since(entry.CheckedAt) < 24*time.Hour && entry.Tag != "" {
				return entry.Tag, nil
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, defaultGitHubReleasesURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "vybench-tui")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub release check failed with status: %d", resp.StatusCode)
	}

	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}

	if payload.TagName == "" {
		return "", errors.New("empty tag_name in GitHub release")
	}

	// Save to cache
	_ = os.MkdirAll(cacheDir, 0o755)
	entry := fpmCacheEntry{
		Tag:       payload.TagName,
		CheckedAt: time.Now(),
	}
	if data, err := json.Marshal(entry); err == nil {
		_ = os.WriteFile(cacheFile, data, 0o644)
	}

	return payload.TagName, nil
}

// DownloadFPM downloads the binary for the given version into destDir.
func DownloadFPM(ctx context.Context, client *http.Client, version, destDir string) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}

	osName := runtime.GOOS
	archName := runtime.GOARCH

	downloadURL := fmt.Sprintf("%s/%s/fpm-%s-%s", defaultGitHubDownloadBaseURL, version, osName, archName)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "vybench-tui")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download FPM: status %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp(destDir, "fpm.tmp.*")
	if err != nil {
		return "", err
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}()

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		return "", err
	}
	_ = tmpFile.Close()

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return "", err
	}

	finalPath := filepath.Join(destDir, "fpm")
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return "", err
	}

	return finalPath, nil
}
