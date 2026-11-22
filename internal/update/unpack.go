package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	maxUnpacked = 1 << 30 // all files of one archive
	maxEntries  = 1000
)

// Unpack extracts a hub archive (.tar.gz or .zip) into dir and returns the
// directory the archive holds (crossstream-hub-vX.Y.Z-<os>-<arch>). Only
// regular files and directories are accepted, with relative names that stay
// inside dir.
func Unpack(archive, dir string) (string, error) {
	var top string
	var err error
	switch {
	case strings.HasSuffix(archive, ".tar.gz"):
		top, err = untar(archive, dir)
	case strings.HasSuffix(archive, ".zip"):
		top, err = unzip(archive, dir)
	default:
		return "", fmt.Errorf("unpack %s: unknown archive type", archive)
	}
	if err != nil {
		return "", fmt.Errorf("unpack %s: %w", filepath.Base(archive), err)
	}
	if top == "" {
		return "", fmt.Errorf("unpack %s: empty archive", filepath.Base(archive))
	}
	return filepath.Join(dir, top), nil
}

// entry checks one archive name and returns its top-level directory.
func entry(name string, top *string) (string, error) {
	clean := path.Clean(strings.TrimPrefix(name, "./"))
	if name == "" || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(name, `\`) {
		return "", fmt.Errorf("bad entry name %q", name)
	}
	first, _, _ := strings.Cut(clean, "/")
	if *top == "" {
		*top = first
	} else if *top != first {
		return "", fmt.Errorf("entry %q outside %s/", name, *top)
	}
	return clean, nil
}

type limits struct{ entries, bytes int64 }

func (l *limits) add(size int64) error {
	l.entries++
	l.bytes += size
	if l.entries > maxEntries || l.bytes > maxUnpacked || size < 0 {
		return errors.New("archive too large")
	}
	return nil
}

func untar(archive, dir string) (string, error) {
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(gz)
	var top string
	var lim limits
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return top, nil
		}
		if err != nil {
			return "", err
		}
		name, err := entry(h.Name, &top)
		if err != nil {
			return "", err
		}
		if err := lim.add(h.Size); err != nil {
			return "", err
		}
		dst := filepath.Join(dir, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if err := write(dst, tr, h.Size, os.FileMode(h.Mode)&0o755|0o600); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("entry %q: only files and directories", h.Name)
		}
	}
}

func unzip(archive, dir string) (string, error) {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return "", err
	}
	defer func() { _ = z.Close() }()
	var top string
	var lim limits
	for _, zf := range z.File {
		name, err := entry(zf.Name, &top)
		if err != nil {
			return "", err
		}
		if err := lim.add(int64(zf.UncompressedSize64)); err != nil {
			return "", err
		}
		dst := filepath.Join(dir, filepath.FromSlash(name))
		mode := zf.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return "", err
			}
		case mode.IsRegular():
			r, err := zf.Open()
			if err != nil {
				return "", err
			}
			err = write(dst, r, int64(zf.UncompressedSize64), 0o755)
			_ = r.Close()
			if err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("entry %q: only files and directories", zf.Name)
		}
	}
	return top, nil
}

func write(dst string, r io.Reader, size int64, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != size {
		err = fmt.Errorf("%s: size %d, header says %d", filepath.Base(dst), n, size)
	}
	return err
}
