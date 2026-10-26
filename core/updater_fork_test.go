package core

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func forkTestUpdater(t *testing.T, handler http.HandlerFunc) *releaseUpdater {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return &releaseUpdater{repository: "owner/cc-connect", apiBase: s.URL + "/repos/", downloadBase: s.URL + "/", client: s.Client()}
}

func forkTestRelease(tag string, day int) ReleaseInfo {
	return ReleaseInfo{TagName: tag, Prerelease: true, PublishedAt: time.Date(2026, 10, day, 0, 0, 0, 0, time.UTC)}
}

func TestForkUpdateSelectionUsesPublicationOrder(t *testing.T) {
	old := forkTestRelease("fork-v1.5.1-beta.1+notify.1", 20)
	newer := forkTestRelease("fork-v1.5.1-beta.1+autoretry.1", 21)
	future := forkTestRelease("fork-v1.5.1-beta.1+autoretry.2", 22)
	unpublished := forkTestRelease("fork-v9.0.0", 23)
	unpublished.Draft = true
	inherited := forkTestRelease("v99.0.0", 24)
	u := forkTestUpdater(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/cc-connect/releases" {
			t.Errorf("unexpected route: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode([]ReleaseInfo{newer, unpublished, old, future, inherited}); err != nil {
			t.Error(err)
		}
	})
	for _, current := range []string{old.TagName, strings.TrimPrefix(old.TagName, "fork-"), strings.TrimPrefix(newer.TagName, "fork-")} {
		got, err := u.check(current, false)
		if err != nil || got == nil || got.TagName != future.TagName {
			t.Fatalf("current %s: %+v %v", current, got, err)
		}
	}
	for _, current := range []string{future.TagName, "v2.0.0"} {
		got, err := u.check(current, false)
		if err != nil || got != nil {
			t.Fatalf("downgrade/reinstall %s: %+v %v", current, got, err)
		}
	}
}

func TestForkUpdateCurrentOutsideListAndFailClosed(t *testing.T) {
	for _, status := range []int{200, 404, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			old := forkTestRelease("fork-v1.5.1-beta.1+notify.1", 20)
			newer := forkTestRelease("fork-v1.5.1-beta.1+autoretry.1", 21)
			u := forkTestUpdater(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/tags/") {
					w.WriteHeader(status)
					if status == 200 {
						if err := json.NewEncoder(w).Encode(old); err != nil {
							t.Error(err)
						}
					}
					return
				}
				if err := json.NewEncoder(w).Encode([]ReleaseInfo{newer}); err != nil {
					t.Error(err)
				}
			})
			got, err := u.check(strings.TrimPrefix(old.TagName, "fork-"), false)
			if status == 200 {
				if err != nil || got == nil {
					t.Fatal(got, err)
				}
			} else if err == nil || got != nil {
				t.Fatal("unknown history must not guess", got, err)
			}
		})
	}
}

func TestForkUpdateDoesNotFallBackToOfficial(t *testing.T) {
	var calls atomic.Int32
	u := forkTestUpdater(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "offline", http.StatusServiceUnavailable)
	})
	if _, err := u.check("v1.0.0", false); err == nil {
		t.Fatal("expected failure")
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	if newReleaseUpdater().repository != "Eitan-S-23/cc-connect" {
		t.Fatal("wrong default repository")
	}
}

func TestUpdateOfficialPrereleaseFlag(t *testing.T) {
	u := forkTestUpdater(t, func(w http.ResponseWriter, r *http.Request) {
		stable := forkTestRelease("v1.0.0", 20)
		stable.Prerelease = false
		if err := json.NewEncoder(w).Encode([]ReleaseInfo{stable, forkTestRelease("v2.0.0-beta.1", 21)}); err != nil {
			t.Error(err)
		}
	})
	u.repository = officialUpdateRepository
	for _, pre := range []bool{false, true} {
		got, err := u.check("v0.9.0", pre)
		if err != nil || got == nil {
			t.Fatal(got, err)
		}
		want := "v1.0.0"
		if pre {
			want = "v2.0.0-beta.1"
		}
		if got.TagName != want {
			t.Fatal(got.TagName)
		}
	}
}

func updateTestExecutable(goos, arch string) []byte {
	data := make([]byte, 512)
	switch goos {
	case "linux":
		copy(data, []byte{0x7f, 'E', 'L', 'F', 2, 1})
		m := uint16(62)
		if arch == "arm64" {
			m = 183
		}
		binary.LittleEndian.PutUint16(data[18:], m)
	case "darwin":
		binary.LittleEndian.PutUint32(data, 0xfeedfacf)
		m := uint32(0x01000007)
		if arch == "arm64" {
			m = 0x0100000c
		}
		binary.LittleEndian.PutUint32(data[4:], m)
	case "windows":
		copy(data, "MZ")
		binary.LittleEndian.PutUint32(data[0x3c:], 128)
		binary.LittleEndian.PutUint32(data[128:], 0x4550)
		m := uint16(0x8664)
		if arch == "arm64" {
			m = 0xaa64
		}
		binary.LittleEndian.PutUint16(data[132:], m)
	}
	return data
}

func updateTestArchive(t *testing.T, name string, data []byte, zipped bool) []byte {
	t.Helper()
	var b bytes.Buffer
	if zipped {
		z := zip.NewWriter(&b)
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(data); err != nil {
			t.Fatal(err)
		}
		if err = z.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gz := gzip.NewWriter(&b)
		tr := tar.NewWriter(gz)
		if err := tr.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tr.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := tr.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return b.Bytes()
}

func TestForkUpdateDownloadsSixTargetsAndChecksIntegrity(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(goos+"/"+arch, func(t *testing.T) {
				tag := "fork-v1.5.1-beta.1+autoretry.1"
				archiveName, binaryName := releaseAssetNames(tag, goos, arch)
				if strings.Contains(archiveName, "fork-") {
					t.Fatal("fork prefix leaked into asset name")
				}
				data := updateTestExecutable(goos, arch)
				archive := updateTestArchive(t, binaryName, data, goos == "windows")
				manifest := []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), archiveName))
				release := forkTestRelease(tag, 21)
				release.Assets = []ReleaseAsset{{archiveName, int64(len(archive))}, {"checksums.txt", int64(len(manifest))}}
				var corrupt atomic.Bool
				u := forkTestUpdater(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/repos/owner/cc-connect/releases/tags/" + tag:
						if err := json.NewEncoder(w).Encode(release); err != nil {
							t.Error(err)
						}
					case "/owner/cc-connect/releases/download/" + tag + "/checksums.txt":
						if _, err := w.Write(manifest); err != nil {
							t.Error(err)
						}
					case "/owner/cc-connect/releases/download/" + tag + "/" + archiveName:
						payload := append([]byte(nil), archive...)
						if corrupt.Load() {
							payload[len(payload)-1] ^= 1
						}
						if _, err := w.Write(payload); err != nil {
							t.Error(err)
						}
					default:
						t.Errorf("wrong URL: %s", r.URL.String())
						http.NotFound(w, r)
					}
				})
				got, err := u.verifiedBinary(tag, goos, arch)
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("%v", err)
				}
				corrupt.Store(true)
				if _, err := u.verifiedBinary(tag, goos, arch); err == nil || !strings.Contains(err.Error(), "SHA-256") {
					t.Fatalf("corrupt archive: %v", err)
				}
			})
		}
	}
}

func TestUpdateRejectsMalformedAssetsAndArchives(t *testing.T) {
	r := &ReleaseInfo{Assets: []ReleaseAsset{{"x", 1}, {"x", 1}}}
	if _, err := r.asset("x"); err == nil {
		t.Fatal("duplicate asset accepted")
	}
	if _, err := r.asset("checksums.txt"); err == nil {
		t.Fatal("missing checksum accepted")
	}
	data := []byte("data")
	entry := fmt.Sprintf("%x  x\n", sha256.Sum256(data))
	for _, manifest := range []string{"", entry + entry, "invalid x", fmt.Sprintf("%064d x", 0)} {
		if err := verifyUpdateChecksum([]byte(manifest), "x", data); err == nil {
			t.Fatal("bad manifest accepted")
		}
	}
	for _, zipped := range []bool{false, true} {
		for _, name := range []string{"../cc-connect", "cc-connect-other", "dir/cc-connect"} {
			archive := updateTestArchive(t, name, data, zipped)
			if _, err := extractUpdateBinary(archive, "cc-connect", zipped); err == nil {
				t.Fatal("wrong archive member accepted", name)
			}
		}
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		if err := validateUpdateArchitecture(updateTestExecutable(goos, "arm64"), goos, "amd64"); err == nil {
			t.Fatal("wrong architecture accepted")
		}
		if err := validateUpdateArchitecture([]byte("html error page"), goos, "amd64"); err == nil {
			t.Fatal("non-executable accepted")
		}
	}
}

func TestUpdateReplacePreservesBackupAndRollsBack(t *testing.T) {
	for _, failure := range []string{"none", "backup", "install", "rollback", "lock"} {
		t.Run(failure, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "cc-connect.exe")
			if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
				t.Fatal(err)
			}
			if failure == "lock" {
				if err := os.WriteFile(target+".update-lock", nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			renamer := func(src, dst string) error {
				if failure == "backup" && src == target {
					return errors.New("backup failed")
				}
				if (failure == "install" || failure == "rollback") && strings.Contains(filepath.Base(src), ".cc-connect-update-") {
					return errors.New("install failed")
				}
				if failure == "rollback" && src == target+".old" {
					return errors.New("rollback failed")
				}
				return os.Rename(src, dst)
			}
			err := replaceBinaryAt(target, []byte("new"), renamer)
			if (err == nil) != (failure == "none") {
				t.Fatal(err)
			}
			if failure == "rollback" {
				data, _ := os.ReadFile(target + ".old")
				if string(data) != "old" || !strings.Contains(err.Error(), "rollback failed") {
					t.Fatal("backup not retained", err)
				}
				return
			}
			data, readErr := os.ReadFile(target)
			if readErr != nil {
				t.Fatal(readErr)
			}
			want := "old"
			if failure == "none" {
				want = "new"
				backup, _ := os.ReadFile(target + ".old")
				if string(backup) != "old" {
					t.Fatal("missing backup")
				}
			}
			if string(data) != want {
				t.Fatal(string(data))
			}
			leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".cc-connect-update-*"))
			if len(leftovers) > 0 {
				t.Fatal(leftovers)
			}
		})
	}
}

func TestUpdateVersionMetadataIsNotSemverOrdering(t *testing.T) {
	if semverCompare("fork-v1.5.1-beta.1+autoretry.2", "v1.5.1-beta.1+notify.1") != 0 {
		t.Fatal("metadata affected semver")
	}
	if semverCompare("v1.2.3-beta.10", "v1.2.3-beta.2") <= 0 {
		t.Fatal("numeric prerelease order")
	}
	if semverCompare("v1.2.3-1", "v1.2.3-alpha") >= 0 {
		t.Fatal("numeric prerelease must sort first")
	}
	if !isNpmExecutable(filepath.FromSlash("C:/npm/node_modules/cc-connect/bin/cc-connect.exe")) {
		t.Fatal("npm target not detected")
	}
}

type recordingUpdateService struct {
	release  ReleaseInfo
	err      error
	checks   int
	installs []string
}

func (u *recordingUpdateService) Check(string, bool) (*ReleaseInfo, error) {
	u.checks++
	return &u.release, nil
}
func (u *recordingUpdateService) Install(tag string) error {
	u.installs = append(u.installs, tag)
	return u.err
}

func TestForkUpdatePublishedArchiveFixtures(t *testing.T) {
	dir := os.Getenv("CC_UPDATE_ARCHIVE_DIR")
	if dir == "" {
		t.Skip("set CC_UPDATE_ARCHIVE_DIR for offline published-archive validation")
	}
	tag := "fork-v1.5.1-beta.1+autoretry.1"
	manifest, err := os.ReadFile(filepath.Join(dir, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, goos := range []string{"windows", "linux", "darwin"} {
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(goos+"/"+arch, func(t *testing.T) {
				name, bin := releaseAssetNames(tag, goos, arch)
				archive, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				if err := verifyUpdateChecksum(manifest, name, archive); err != nil {
					t.Fatal(err)
				}
				data, err := extractUpdateBinary(archive, bin, goos == "windows")
				if err != nil {
					t.Fatal(err)
				}
				if err := validateUpdateArchitecture(data, goos, arch); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestUpdateCardUsesSharedForkBackend(t *testing.T) {
	previous := CurrentVersion
	CurrentVersion = "v1.5.1-beta.1+notify.1"
	t.Cleanup(func() { CurrentVersion = previous })
	e := newTestEngine()
	t.Cleanup(func() { _ = e.Stop() })
	u := &recordingUpdateService{release: ReleaseInfo{TagName: "fork-v1.5.1-beta.1+autoretry.1"}}
	e.updateService = u
	if text := e.renderUpgradeCard().RenderText(); !strings.Contains(text, u.release.TagName) {
		t.Fatal(text)
	}
	if u.checks != 1 || len(u.installs) != 0 {
		t.Fatal("card performed an install")
	}
}

func TestUpdateInstallGuardsBeforeIO(t *testing.T) {
	updateMu.Lock()
	err := SelfUpdate("fork-v1.0.0")
	updateMu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatal(err)
	}
	updateMu.Lock()
	previous := updateInstalled
	updateInstalled = true
	updateMu.Unlock()
	t.Cleanup(func() { updateMu.Lock(); updateInstalled = previous; updateMu.Unlock() })
	if err := SelfUpdate("fork-v1.0.0"); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatal(err)
	}
}

func TestForkUpdateUnusableReleaseStopsBeforeDownload(t *testing.T) {
	for _, mode := range []string{"draft", "tag-mismatch", "missing-checksum", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			tag := "fork-v1.0.0+test.1"
			release := forkTestRelease(tag, 21)
			archive, _ := releaseAssetNames(tag, "windows", "amd64")
			release.Assets = []ReleaseAsset{{archive, 512}, {"checksums.txt", 99}}
			switch mode {
			case "draft":
				release.Draft = true
			case "tag-mismatch":
				release.TagName = "fork-v1.0.0+other.1"
			case "missing-checksum":
				release.Assets = release.Assets[:1]
			case "oversize":
				release.Assets[0].Size = maxUpdateArchive + 1
			}
			var calls atomic.Int32
			u := forkTestUpdater(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if err := json.NewEncoder(w).Encode(release); err != nil {
					t.Error(err)
				}
			})
			if _, err := u.verifiedBinary(tag, "windows", "amd64"); err == nil {
				t.Fatal("unusable release accepted")
			}
			if calls.Load() != 1 {
				t.Fatal("download attempted before metadata validation")
			}
		})
	}
}

func TestUpdateArchiveRejectsLinksAndExtraMembers(t *testing.T) {
	for _, link := range []bool{false, true} {
		var zipBytes bytes.Buffer
		z := zip.NewWriter(&zipBytes)
		hdr := &zip.FileHeader{Name: "cc-connect"}
		if link {
			hdr.SetMode(os.ModeSymlink | 0o777)
		}
		w, err := z.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("target")); err != nil {
			t.Fatal(err)
		}
		if !link {
			w, err = z.Create("extra")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte("extra")); err != nil {
				t.Fatal(err)
			}
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := extractUpdateBinary(zipBytes.Bytes(), "cc-connect", true); err == nil {
			t.Fatal("ZIP link/extra accepted")
		}
		var tarBytes bytes.Buffer
		gz := gzip.NewWriter(&tarBytes)
		tr := tar.NewWriter(gz)
		th := &tar.Header{Name: "cc-connect", Size: 4, Mode: 0o755, Typeflag: tar.TypeReg}
		if link {
			th.Typeflag = tar.TypeSymlink
			th.Linkname = "target"
			th.Size = 0
		}
		if err := tr.WriteHeader(th); err != nil {
			t.Fatal(err)
		}
		if !link {
			if _, err := tr.Write([]byte("data")); err != nil {
				t.Fatal(err)
			}
			if err := tr.WriteHeader(&tar.Header{Name: "extra", Mode: 0o644, Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
		}
		if err := tr.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := extractUpdateBinary(tarBytes.Bytes(), "cc-connect", false); err == nil {
			t.Fatal("TAR link/extra accepted")
		}
	}
}
