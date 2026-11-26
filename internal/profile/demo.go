package profile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	DemoID       = "demo-gk80-control"
	PreviewID    = "demo-gk80-preview"
	DatabaseFile = "demo.sqlite"
)

var ErrSeparateProfiles = errors.New("IAB preview and desktop profiles must be separate")

var profileEntries = [...]string{DatabaseFile, DatabaseFile + "-wal", DatabaseFile + "-shm", lockFileName, "logs"}

func demoRoot(local string) string {
	return filepath.Join(local, "GK80", DemoID)
}

// DemoDir resolves the shared Windows and IAB profile, preserving its existing default.
func DemoDir(local, configured string) (string, error) {
	if configured == "" {
		if !filepath.IsAbs(local) {
			return "", errors.New("local data directory unavailable")
		}
		configured = filepath.Join(demoRoot(local), "manual-parameters")
	}
	if !filepath.IsAbs(configured) {
		return "", errors.New("invalid demo profile directory")
	}
	dir := filepath.Clean(configured)
	if filepath.IsAbs(local) {
		preview := filepath.Join(demoRoot(local), "iab-preview")
		if err := requireSeparate(dir, preview); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// PreviewDir resolves a fixed profile. The desktop override is only used to detect collisions.
func PreviewDir(local, configured string) (string, error) {
	if !filepath.IsAbs(local) {
		return "", errors.New("local data directory unavailable")
	}
	dir := filepath.Join(demoRoot(local), "iab-preview")
	for _, name := range profileEntries {
		entry := filepath.Join(dir, name)
		info, err := os.Lstat(entry)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if (name == "logs" && !info.IsDir()) || (name != "logs" && !info.Mode().IsRegular()) {
			return "", ErrSeparateProfiles
		}
	}
	if err := requireSeparate(dir, filepath.Join(demoRoot(local), "manual-parameters")); err != nil {
		return "", err
	}
	if filepath.IsAbs(configured) {
		if err := requireSeparate(dir, configured); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func requireSeparate(first, second string) error {
	if err := requireDistinct(first, second); err != nil {
		return err
	}
	for _, name := range profileEntries {
		if err := requireDistinct(filepath.Join(first, name), filepath.Join(second, name)); err != nil {
			return err
		}
	}
	return nil
}

func requireDistinct(first, second string) error {
	if strings.EqualFold(filepath.Clean(first), filepath.Clean(second)) {
		return ErrSeparateProfiles
	}
	firstInfo, err := os.Stat(first)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	secondInfo, err := os.Stat(second)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if os.SameFile(firstInfo, secondInfo) {
		return ErrSeparateProfiles
	}
	return nil
}
