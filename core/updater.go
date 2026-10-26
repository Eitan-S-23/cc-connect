package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// UpdateRepository can be overridden by the release workflow with -ldflags -X.
// Never fall back to another repository: that would remove fork-only features.
var UpdateRepository = "Eitan-S-23/cc-connect"

const officialUpdateRepository = "chenhg5/cc-connect"

type ReleaseAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type ReleaseInfo struct {
	TagName     string         `json:"tag_name"`
	Name        string         `json:"name"`
	Body        string         `json:"body"`
	HTMLURL     string         `json:"html_url"`
	Prerelease  bool           `json:"prerelease"`
	Draft       bool           `json:"draft"`
	CreatedAt   string         `json:"created_at"`
	PublishedAt time.Time      `json:"published_at"`
	Assets      []ReleaseAsset `json:"assets"`
}

type releaseUpdater struct {
	repository   string
	apiBase      string
	downloadBase string
	client       *http.Client
}

var updateRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+/[A-Za-z0-9_.-]+$`)
var updateMu sync.Mutex
var updateInstalled bool // protected by updateMu; a running process may install only once

type releaseUpdateService interface {
	Check(string, bool) (*ReleaseInfo, error)
	Install(string) error
}

type builtinReleaseUpdateService struct{}

func (builtinReleaseUpdateService) Check(current string, pre bool) (*ReleaseInfo, error) {
	return CheckForUpdate(current, pre)
}
func (builtinReleaseUpdateService) Install(tag string) error { return SelfUpdate(tag) }
func (e *Engine) releaseUpdates() releaseUpdateService {
	if e.updateService != nil {
		return e.updateService
	}
	return builtinReleaseUpdateService{}
}

func newReleaseUpdater() *releaseUpdater {
	return &releaseUpdater{
		repository:   UpdateRepository,
		apiBase:      "https://api.github.com/repos/",
		downloadBase: "https://github.com/",
		client:       &http.Client{Timeout: 5 * time.Minute},
	}
}

func (u *releaseUpdater) fork() bool {
	return !strings.EqualFold(u.repository, officialUpdateRepository)
}

func (u *releaseUpdater) validTag(tag string) bool {
	if u.fork() && !strings.HasPrefix(tag, "fork-v") {
		return false
	}
	if !u.fork() && strings.HasPrefix(tag, "fork-") {
		return false
	}
	return semverRe.MatchString(strings.TrimPrefix(tag, "fork-"))
}

// CheckForUpdate is shared by the CLI, chat command and upgrade card.
// Fork releases include prereleases by default; official builds honor the flag.
func CheckForUpdate(currentVersion string, includePrereleases bool) (*ReleaseInfo, error) {
	return newReleaseUpdater().check(currentVersion, includePrereleases)
}

func (u *releaseUpdater) check(current string, includePre bool) (*ReleaseInfo, error) {
	if !updateRepositoryPattern.MatchString(u.repository) {
		return nil, fmt.Errorf("invalid update repository")
	}
	var releases []ReleaseInfo
	if err := u.getJSON("/releases?per_page=100", &releases); err != nil {
		return nil, err
	}
	var best *ReleaseInfo
	for i := range releases {
		r := &releases[i]
		if r.Draft || !u.validTag(r.TagName) || r.PublishedAt.IsZero() || (!u.fork() && r.Prerelease && !includePre) {
			continue
		}
		if best == nil || semverCompare(r.TagName, best.TagName) > 0 ||
			(u.fork() && semverCompare(r.TagName, best.TagName) == 0 && r.PublishedAt.After(best.PublishedAt)) {
			best = r
		}
	}
	if best == nil {
		return nil, nil
	}
	if strings.HasPrefix(current, "dev") {
		return best, nil
	}
	if !semverRe.MatchString(strings.TrimPrefix(strings.TrimSpace(current), "fork-")) {
		return nil, fmt.Errorf("cannot compare current version %q", current)
	}
	if normalizeVersion(best.TagName) == normalizeVersion(current) || semverCompare(best.TagName, current) < 0 {
		return nil, nil
	}
	if semverCompare(best.TagName, current) > 0 {
		return best, nil
	}
	if !u.fork() {
		return nil, nil
	}
	// Metadata is not semver precedence: +notify and +autoretry must be ordered
	// by publication time, not lexicographically by feature name.
	currentTag := "fork-" + normalizeVersion(current)
	var previous *ReleaseInfo
	for i := range releases {
		if releases[i].TagName == currentTag {
			previous = &releases[i]
			break
		}
	}
	if previous == nil {
		var r ReleaseInfo
		if err := u.getJSON("/releases/tags/"+url.PathEscape(currentTag), &r); err != nil {
			return nil, fmt.Errorf("cannot establish current fork release: %w", err)
		}
		previous = &r
	}
	if previous.TagName != currentTag || previous.Draft || previous.PublishedAt.IsZero() {
		return nil, fmt.Errorf("current fork release has no publication timestamp")
	}
	if best.PublishedAt.After(previous.PublishedAt) {
		return best, nil
	}
	return nil, nil
}

func (u *releaseUpdater) getJSON(path string, target any) error {
	data, err := u.download(u.apiBase+u.repository+path, 4<<20, 20*time.Second)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode release metadata: %w", err)
	}
	return nil
}

func (u *releaseUpdater) download(address string, limit int64, timeout time.Duration) ([]byte, error) {
	client := *u.client
	client.Timeout = timeout
	response, err := client.Get(address)
	if err != nil {
		return nil, fmt.Errorf("update download: %w", err)
	}
	defer closeUpdateReader(response.Body)
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update download returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read update: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("update download exceeds size limit")
	}
	return data, nil
}

func releaseAssetNames(tag, goos, goarch string) (string, string) {
	binary := fmt.Sprintf("cc-connect-%s-%s-%s", strings.TrimPrefix(tag, "fork-"), goos, goarch)
	if goos == "windows" {
		return binary + ".zip", binary + ".exe"
	}
	return binary + ".tar.gz", binary
}

func (r *ReleaseInfo) asset(name string) (ReleaseAsset, error) {
	var found ReleaseAsset
	count := 0
	for _, asset := range r.Assets {
		if asset.Name == name {
			found = asset
			count++
		}
	}
	if count != 1 || found.Size <= 0 {
		return ReleaseAsset{}, fmt.Errorf("release requires exactly one nonempty %s asset", name)
	}
	return found, nil
}

// SelfUpdate pins the exact tag returned by the check. Verification completes
// before touching the executable; a newer release cannot change this install.
func SelfUpdate(tag string) error {
	if !updateMu.TryLock() {
		return fmt.Errorf("another update is already running")
	}
	defer updateMu.Unlock()
	if updateInstalled {
		return fmt.Errorf("update already installed; restart before updating again")
	}
	u := newReleaseUpdater()
	if !updateRepositoryPattern.MatchString(u.repository) || !u.validTag(tag) {
		return fmt.Errorf("invalid release tag or update repository")
	}
	target, err := updateExecutablePath()
	if err != nil {
		return err
	}
	if u.fork() && isNpmExecutable(target) {
		return fmt.Errorf("fork updates require a standalone binary; refusing to replace the official npm package")
	}
	binary, err := u.verifiedBinary(tag, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if err := replaceBinaryAt(target, binary, os.Rename); err != nil {
		return err
	}
	updateInstalled = true
	return nil
}
