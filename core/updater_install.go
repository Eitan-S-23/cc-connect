package core

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxUpdateArchive = 256 << 20
const maxUpdateBinary = 512 << 20

func closeUpdateReader(reader io.Closer) {
	if err := reader.Close(); err != nil {
		slog.Warn("updater: close reader failed", "error", err)
	}
}

func removeUpdateFile(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Warn("updater: cleanup failed", "path", path, "error", err)
	}
}

func (u *releaseUpdater) verifiedBinary(tag, goos, goarch string) ([]byte, error) {
	if !updateRepositoryPattern.MatchString(u.repository) || !u.validTag(tag) {
		return nil, fmt.Errorf("invalid release tag or update repository")
	}
	var release ReleaseInfo
	if err := u.getJSON("/releases/tags/"+url.PathEscape(tag), &release); err != nil {
		return nil, err
	}
	if release.Draft || release.TagName != tag || release.PublishedAt.IsZero() {
		return nil, fmt.Errorf("release is not published or tag does not match")
	}
	archiveName, binaryName := releaseAssetNames(tag, goos, goarch)
	archiveAsset, err := release.asset(archiveName)
	if err != nil {
		return nil, err
	}
	checksumAsset, err := release.asset("checksums.txt")
	if err != nil {
		return nil, err
	}
	if archiveAsset.Size > maxUpdateArchive || checksumAsset.Size > 1<<20 {
		return nil, fmt.Errorf("release asset exceeds size limit")
	}
	base := u.downloadBase + u.repository + "/releases/download/" + url.PathEscape(tag) + "/"
	checksums, err := u.download(base+"checksums.txt", 1<<20, 20*time.Second)
	if err != nil {
		return nil, err
	}
	archive, err := u.download(base+url.PathEscape(archiveName), maxUpdateArchive, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	if int64(len(archive)) != archiveAsset.Size || int64(len(checksums)) != checksumAsset.Size {
		return nil, fmt.Errorf("release asset size mismatch")
	}
	if err := verifyUpdateChecksum(checksums, archiveName, archive); err != nil {
		return nil, err
	}
	data, err := extractUpdateBinary(archive, binaryName, goos == "windows")
	if err != nil {
		return nil, err
	}
	if err := validateUpdateArchitecture(data, goos, goarch); err != nil {
		return nil, err
	}
	return data, nil
}

func verifyUpdateChecksum(manifest []byte, name string, data []byte) error {
	wanted := ""
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		if wanted != "" {
			return fmt.Errorf("duplicate checksum for %s", name)
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size {
			return fmt.Errorf("invalid SHA-256 for %s", name)
		}
		wanted = strings.ToLower(fields[0])
	}
	if wanted == "" || wanted != fmt.Sprintf("%x", sha256.Sum256(data)) {
		return fmt.Errorf("missing or mismatched SHA-256 for %s", name)
	}
	return nil
}

func extractUpdateBinary(archive []byte, name string, zipped bool) ([]byte, error) {
	if zipped {
		r, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		if len(r.File) != 1 || r.File[0].Name != name || !r.File[0].Mode().IsRegular() || r.File[0].UncompressedSize64 > maxUpdateBinary {
			return nil, fmt.Errorf("unexpected ZIP contents")
		}
		stream, err := r.File[0].Open()
		if err != nil {
			return nil, err
		}
		defer closeUpdateReader(stream)
		return readUpdateBinary(stream)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer closeUpdateReader(gz)
	tr := tar.NewReader(gz)
	hdr, err := tr.Next()
	if err != nil {
		return nil, err
	}
	if hdr.Name != name || hdr.Typeflag != tar.TypeReg || hdr.Size > maxUpdateBinary {
		return nil, fmt.Errorf("unexpected TAR contents")
	}
	data, err := readUpdateBinary(tr)
	if err != nil {
		return nil, err
	}
	if _, err := tr.Next(); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing TAR contents: %v", err)
	}
	if _, err := io.Copy(io.Discard, io.LimitReader(gz, 1<<20)); err != nil {
		return nil, err
	}
	return data, nil
}

func readUpdateBinary(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxUpdateBinary+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxUpdateBinary {
		return nil, fmt.Errorf("invalid executable size")
	}
	return data, nil
}

func validateUpdateArchitecture(data []byte, goos, arch string) error {
	if len(data) < 512 || (arch != "amd64" && arch != "arm64") {
		return fmt.Errorf("invalid executable or unsupported architecture")
	}
	order := binary.LittleEndian
	ok := false
	switch goos {
	case "linux":
		machine := uint16(62)
		if arch == "arm64" {
			machine = 183
		}
		ok = bytes.Equal(data[:4], []byte{0x7f, 'E', 'L', 'F'}) && data[4] == 2 && data[5] == 1 && order.Uint16(data[18:]) == machine
	case "darwin":
		machine := uint32(0x01000007)
		if arch == "arm64" {
			machine = 0x0100000c
		}
		ok = order.Uint32(data[:4]) == 0xfeedfacf && order.Uint32(data[4:8]) == machine
	case "windows":
		offset := uint64(order.Uint32(data[0x3c:]))
		machine := uint16(0x8664)
		if arch == "arm64" {
			machine = 0xaa64
		}
		ok = string(data[:2]) == "MZ" && offset >= 64 && offset+6 <= uint64(len(data)) && order.Uint32(data[offset:]) == 0x4550 && order.Uint16(data[offset+4:]) == machine
	}
	if !ok {
		return fmt.Errorf("executable does not match %s/%s", goos, arch)
	}
	return nil
}

func updateExecutablePath() (string, error) {
	target, err := os.Executable()
	if err != nil {
		return "", err
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(target, ".old") || strings.HasSuffix(target, " (deleted)") {
		return "", fmt.Errorf("running executable has already been replaced; restart first")
	}
	return target, nil
}

func isNpmExecutable(target string) bool {
	return strings.Contains(strings.ToLower(filepath.ToSlash(target)), "/node_modules/cc-connect/")
}

// The injected rename lets tests fail the install step without global hooks.
func replaceBinaryAt(target string, data []byte, rename func(string, string) error) error {
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("update target is not a regular file")
	}
	lockPath := target + ".update-lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("acquire update lock: %w", err)
	}
	if err := lock.Close(); err != nil {
		removeUpdateFile(lockPath)
		return err
	}
	defer removeUpdateFile(lockPath)
	tmp, err := os.CreateTemp(filepath.Dir(target), ".cc-connect-update-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer removeUpdateFile(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, info.Mode().Perm()|0o111); err != nil {
		return err
	}
	backup := target + ".old"
	if old, err := os.Lstat(backup); err == nil {
		if !old.Mode().IsRegular() {
			return fmt.Errorf("backup path is not a regular file")
		}
		if err := os.Remove(backup); err != nil {
			return fmt.Errorf("remove previous backup: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := rename(target, backup); err != nil {
		return fmt.Errorf("backup old executable: %w", err)
	}
	if err := rename(tmpPath, target); err != nil {
		if restoreErr := rename(backup, target); restoreErr != nil {
			return fmt.Errorf("install failed: %v; rollback failed (backup at %s): %w", err, backup, restoreErr)
		}
		return fmt.Errorf("install failed; original executable restored: %w", err)
	}
	// Keep .old for manual recovery and os.Executable() during restart.
	return nil
}
