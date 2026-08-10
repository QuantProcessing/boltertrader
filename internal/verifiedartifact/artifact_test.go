package verifiedartifact

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestReadSnapshotRejectsPermissiveModeAndPathSwap(t *testing.T) {
	for _, test := range []struct {
		name   string
		mode   os.FileMode
		before func(*testing.T, string)
	}{
		{name: "permissive mode", mode: 0o644},
		{name: "symlink swap", mode: 0o600, before: func(t *testing.T, path string) {
			replacement := filepath.Join(t.TempDir(), "replacement")
			writePrivateArtifact(t, replacement, "replacement")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(replacement, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "inode swap", mode: 0o600, before: func(t *testing.T, path string) {
			replacement := filepath.Join(filepath.Dir(path), "replacement")
			writePrivateArtifact(t, replacement, "replacement")
			if err := os.Rename(replacement, path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "artifact")
			if err := os.WriteFile(path, []byte("original"), test.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, test.mode); err != nil {
				t.Fatal(err)
			}
			var beforeOpen func()
			if test.before != nil {
				beforeOpen = func() { test.before(t, path) }
			}
			if _, _, err := readSnapshot(path, 1024, beforeOpen); err == nil {
				t.Fatal("unsafe artifact was accepted")
			}
		})
	}
}

func TestReadRepositorySnapshotAcceptsTrackedModeAndRejectsGroupWrites(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    os.FileMode
		wantErr bool
	}{
		{name: "private", mode: 0o600},
		{name: "tracked", mode: 0o644},
		{name: "group writable", mode: 0o664, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mapping.json")
			if err := os.WriteFile(path, []byte(`{"schemaVersion":1}`), tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			data, _, err := ReadRepositorySnapshot(path, 1024)
			if tc.wantErr {
				if err == nil {
					t.Fatal("unsafe repository artifact was accepted")
				}
				return
			}
			if err != nil || string(data) != `{"schemaVersion":1}` {
				t.Fatalf("data=%q err=%v", data, err)
			}
		})
	}
}

func TestOpenStreamRemainsBoundToVerifiedInodeAfterPathReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive")
	writePrivateArtifact(t, path, "original archive")
	artifact, err := openStream(path, 1024, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = artifact.Close() })

	replacement := filepath.Join(filepath.Dir(path), "replacement")
	writePrivateArtifact(t, replacement, "replacement archive")
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if err := artifact.Rewind(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(artifact.File())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original archive" {
		t.Fatalf("stream read %q", data)
	}
	if err := artifact.Revalidate(); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("revalidation error=%v", err)
	}
}

func TestCreatedStreamCannotSealReplacementPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive")
	artifact, err := CreateStream(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = artifact.Close() })
	if _, err := artifact.File().Write([]byte("original archive")); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(filepath.Dir(path), "replacement")
	writePrivateArtifact(t, replacement, "replacement archive")
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if err := artifact.Seal(); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("seal error=%v", err)
	}
}

func TestValidateInfoRejectsWrongOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact")
	writePrivateArtifact(t, path, "private")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateInfo(fileInfoWithOwner{FileInfo: info, uid: uint32(os.Geteuid() + 1)}, 1024); err == nil {
		t.Fatalf("error=%v", err)
	}
}

type fileInfoWithOwner struct {
	os.FileInfo
	uid uint32
}

func (info fileInfoWithOwner) Sys() any {
	return &syscall.Stat_t{Uid: info.uid}
}

func writePrivateArtifact(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}
