package updater

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/version"
)

type Release struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

// CheckForUpdates queries GitHub for the latest release.
// Returns the new version tag if an update is available, empty string otherwise.
func CheckForUpdates() (string, string, error) {
	// Set a short timeout to avoid blocking startup for too long
	client := http.Client{
		Timeout: 2 * time.Second,
	}

	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/Dicklesworthstone/beads_viewer/releases/latest", nil)
	if err != nil {
		return "", "", err
	}
	// GitHub recommends sending a UA; some endpoints 403 without it.
	req.Header.Set("User-Agent", "beads-viewer-update-check")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// For rate/abuse limits, avoid treating as fatal; just skip update.
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return "", "", nil
		}
		return "", "", fmt.Errorf("github api returned status: %s", resp.Status)
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", "", err
	}

	// Compare versions
	// Assumes SemVer with 'v' prefix
	if compareVersions(rel.TagName, version.Version) > 0 {
		return rel.TagName, rel.HTMLURL, nil
	}

	return "", "", nil
}

// compareVersions compares semver-ish strings with optional leading 'v'.
// Returns 1 if v1>v2, -1 if v1<v2, 0 if equal. Falls back to lexicographic
// comparison only if parsing fails.
func compareVersions(v1, v2 string) int {
	parse := func(v string) []int {
		v = strings.TrimPrefix(v, "v")
		parts := strings.Split(v, ".")
		res := make([]int, 3)
		for i := 0; i < len(res) && i < len(parts); i++ {
			if n, err := strconv.Atoi(parts[i]); err == nil {
				res[i] = n
			} else {
				return nil
			}
		}
		return res
	}

	p1 := parse(v1)
	p2 := parse(v2)

	if p1 != nil && p2 != nil {
		for i := 0; i < 3; i++ {
			if p1[i] > p2[i] {
				return 1
			}
			if p1[i] < p2[i] {
				return -1
			}
		}
		return 0
	}

	// Fallback: lexicographic
	v1 = strings.TrimPrefix(v1, "v")
	v2 = strings.TrimPrefix(v2, "v")
	if v1 > v2 {
		return 1
	} else if v1 < v2 {
		return -1
	}
	return 0
}
