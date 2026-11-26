package profile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDemoAndPreviewProfileSelection(t *testing.T) {
	local := t.TempDir()
	override := filepath.Join(local, "configured demo")
	wantDemo := filepath.Join(local, "GK80", "demo-gk80-control", "manual-parameters")
	wantPreview := filepath.Join(local, "GK80", "demo-gk80-control", "iab-preview")
	if dir, err := DemoDir(local, ""); err != nil || dir != wantDemo {
		t.Fatalf("default demo: %q, %v", dir, err)
	}
	if dir, err := DemoDir(local, override); err != nil || dir != override {
		t.Fatalf("configured demo: %q, %v", dir, err)
	}
	if _, err := DemoDir(local, "relative"); err == nil {
		t.Fatal("demo accepted a relative override")
	}
	for _, configured := range []string{"", override, "relative"} {
		if dir, err := PreviewDir(local, configured); err != nil || dir != wantPreview {
			t.Fatalf("preview with override %q: %q, %v", configured, dir, err)
		}
	}
	if _, err := PreviewDir(local, wantPreview); !errors.Is(err, ErrSeparateProfiles) {
		t.Fatalf("preview accepted a coincident desktop override: %v", err)
	}
	if _, err := DemoDir(local, wantPreview); !errors.Is(err, ErrSeparateProfiles) {
		t.Fatalf("demo accepted the reserved preview directory: %v", err)
	}
}

func TestPreviewRejectsSharedDatabaseThroughHardLink(t *testing.T) {
	local := t.TempDir()
	shared, err := DemoDir(local, "")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewDir(local, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{shared, preview} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(shared, DatabaseFile)
	if err := os.WriteFile(source, []byte("existing demo data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, filepath.Join(preview, DatabaseFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := PreviewDir(local, ""); !errors.Is(err, ErrSeparateProfiles) {
		t.Fatalf("preview accepted a hard link to the shared database: %v", err)
	}
	if _, err := DemoDir(local, ""); !errors.Is(err, ErrSeparateProfiles) {
		t.Fatalf("desktop accepted a hard link to the preview database: %v", err)
	}
}
