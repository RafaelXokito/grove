package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ErrPermission is returned (wrapped) when the binary's directory is not
// writable.
var ErrPermission = errors.New("permission denied")

// AssetName is the release archive name for a tag and platform, as
// published by .goreleaser.yaml.
func AssetName(tag, goos, goarch string) string {
	return fmt.Sprintf("grove_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), goos, goarch)
}

// Installer replaces a grove binary with a release build (FR-24).
type Installer struct {
	Client *Client
	// Exe is the binary to replace; symlinks are resolved.
	Exe          string
	GOOS, GOARCH string
	// RunVersion runs `<path> --version` and returns its stdout.
	RunVersion func(ctx context.Context, path string) ([]byte, error)
}

// Install downloads, verifies and installs rel over in.Exe.
func (in *Installer) Install(ctx context.Context, rel Release) error {
	if !ValidTag(rel.Tag) {
		return fmt.Errorf("invalid release tag %q", sanitizeTag(rel.Tag))
	}
	name := AssetName(rel.Tag, in.GOOS, in.GOARCH)
	var archiveURL, sumsURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case name:
			archiveURL = a.URL
		case checksumsName:
			sumsURL = a.URL
		}
	}
	if archiveURL == "" {
		return fmt.Errorf("release %s has no %s (unsupported platform %s/%s?)", rel.Tag, name, in.GOOS, in.GOARCH)
	}
	if sumsURL == "" {
		return fmt.Errorf("release %s has no %s", rel.Tag, checksumsName)
	}

	exe, err := filepath.EvalSymlinks(in.Exe)
	if err != nil {
		return fmt.Errorf("locate grove binary: %w", err)
	}

	sums, err := in.Client.download(ctx, sumsURL, maxChecksums)
	if err != nil {
		return err
	}
	want, err := checksumFor(sums, name)
	if err != nil {
		return err
	}
	archive, err := in.Client.download(ctx, archiveURL, maxArchive)
	if err != nil {
		return err
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("checksum mismatch for %s: refusing to install", name)
	}
	bin, err := extractBinary(archive)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".grove-update-*")
	if err != nil {
		if isPermission(err) {
			return fmt.Errorf("%w: cannot write to %s", ErrPermission, dir)
		}
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmpName)
		}
	}()
	_, werr := tmp.Write(bin)
	cerr := tmp.Close()
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return cerr
	}
	mode := fs.FileMode(0o755)
	if fi, err := os.Stat(exe); err == nil {
		mode = fi.Mode().Perm() | 0o100
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if in.RunVersion != nil {
		vctx, cancel := context.WithTimeout(ctx, versionTimeout)
		out, err := in.RunVersion(vctx, tmpName)
		cancel()
		if err != nil {
			return fmt.Errorf("new binary failed to run: %w", err)
		}
		if strings.TrimSpace(string(out)) != "grove "+rel.Tag {
			return fmt.Errorf("new binary reports %q, want %q", sanitizeTag(strings.TrimSpace(string(out))), "grove "+rel.Tag)
		}
	}
	if err := os.Rename(tmpName, exe); err != nil {
		if isPermission(err) {
			return fmt.Errorf("%w: cannot replace %s", ErrPermission, exe)
		}
		return err
	}
	ok = true
	return nil
}

// checksumFor finds name's sha256 in a checksums.txt ("<hex>  <name>").
func checksumFor(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 || strings.TrimPrefix(f[1], "*") != name {
			continue
		}
		h := strings.ToLower(f[0])
		if b, err := hex.DecodeString(h); err != nil || len(b) != sha256.Size {
			return "", fmt.Errorf("%s: malformed checksum for %s", checksumsName, name)
		}
		return h, nil
	}
	return "", fmt.Errorf("%s: no entry for %s", checksumsName, name)
}

// extractBinary returns the contents of the regular file "grove" (at the
// archive root or one directory down). Unsafe paths and link entries for
// grove are rejected.
func extractBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("no grove binary in archive")
		}
		if err != nil {
			return nil, err
		}
		name := h.Name
		if strings.HasPrefix(name, "/") || strings.Contains(name, `\`) {
			return nil, fmt.Errorf("unsafe path %q in archive", sanitizeTag(name))
		}
		for _, part := range strings.Split(name, "/") {
			if part == ".." {
				return nil, fmt.Errorf("unsafe path %q in archive", sanitizeTag(name))
			}
		}
		clean := path.Clean(name)
		if path.Base(clean) != binaryEntryName || strings.Count(clean, "/") > 1 {
			continue
		}
		switch h.Typeflag {
		case tar.TypeReg:
		case tar.TypeSymlink, tar.TypeLink:
			return nil, fmt.Errorf("refusing link entry %q in archive", sanitizeTag(name))
		default:
			continue
		}
		if h.Size > maxBinary {
			return nil, fmt.Errorf("grove binary too large (%d bytes)", h.Size)
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxBinary+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxBinary {
			return nil, errors.New("grove binary too large")
		}
		return data, nil
	}
}

// versionTimeout bounds the new binary's --version sanity check.
const versionTimeout = 10 * time.Second

// isPermission reports errors where a hint about privileges or reinstalling
// helps: permission denied, or a read-only filesystem (e.g. /nix/store).
func isPermission(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS)
}
