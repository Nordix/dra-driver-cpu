/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sysfs

import (
	"io"
	"io/fs"
	"reflect"
	"testing"
	"testing/fstest"
)

func TestParseOverlay(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    map[string]string
		wantErr bool
	}{
		{
			name: "valid",
			data: `
/sys/devices/system/cpu/online: "0-1\n"
/sys/devices/system/cpu/smt/control: |
  off
`,
			want: map[string]string{
				"/sys/devices/system/cpu/online":      "0-1\n",
				"/sys/devices/system/cpu/smt/control": "off\n",
			},
		},
		{
			name:    "non-string value",
			data:    "/sys/devices/system/cpu/online: 1\n",
			wantErr: true,
		},
		{
			name:    "not an object",
			data:    "- value\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseOverlay([]byte(tt.data))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseOverlay() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseOverlay() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestNewOverlayFromYAML(t *testing.T) {
	base := fstest.MapFS{
		"devices/system/cpu/base-only": {
			Data: []byte("base"),
		},
		"devices/system/cpu/online": {
			Data: []byte("0-3\n"),
		},
		"devices/system/cpu/overridden": {
			Data: []byte("base"),
		},
		"links/online": {
			Data: []byte("../devices/system/cpu/online"),
			Mode: fs.ModeSymlink,
		},
	}
	overlayData := []byte(`
/sys/devices/system/cpu/online: "0-1\n"
/sys/devices/system/cpu/overridden: overlay
/sys/devices/system/cpu/virtual/value: virtual
`)

	overlayFS, err := NewOverlayFromYAML(base, overlayData)
	if err != nil {
		t.Fatalf("NewOverlayFromYAML() error = %v", err)
	}

	// The overlay is an immutable startup snapshot.
	clear(overlayData)

	assertFileContents(t, overlayFS, "devices/system/cpu/online", "0-1\n")
	assertFileContents(t, overlayFS, "devices/system/cpu/overridden", "overlay")
	assertFileContents(t, overlayFS, "devices/system/cpu/base-only", "base")
	assertFileContents(t, overlayFS, "devices/system/cpu/virtual/value", "virtual")

	file, err := overlayFS.Open("devices/system/cpu/overridden")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer file.Close()
	contents, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if got, want := string(contents), "overlay"; got != want {
		t.Fatalf("Open() contents = %q, want %q", got, want)
	}

	entries, err := fs.ReadDir(overlayFS, "devices/system/cpu")
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	wantEntries := []string{"base-only", "online", "overridden", "virtual"}
	gotEntries := make([]string, 0, len(entries))
	for _, entry := range entries {
		gotEntries = append(gotEntries, entry.Name())
		if entry.Name() == "virtual" && !entry.IsDir() {
			t.Fatal("virtual overlay parent is not a directory")
		}
	}
	if !reflect.DeepEqual(gotEntries, wantEntries) {
		t.Fatalf("ReadDir() entries = %v, want %v", gotEntries, wantEntries)
	}

	info, err := fs.Stat(overlayFS, "devices/system/cpu/virtual")
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if !info.IsDir() {
		t.Fatal("overlay parent Stat() did not report a directory")
	}

	target, err := overlayFS.ReadLink("links/online")
	if err != nil {
		t.Fatalf("ReadLink() error = %v", err)
	}
	if got, want := target, "../devices/system/cpu/online"; got != want {
		t.Fatalf("ReadLink() = %q, want %q", got, want)
	}

	if err := fstest.TestFS(overlayFS,
		"devices/system/cpu/base-only",
		"devices/system/cpu/online",
		"devices/system/cpu/overridden",
		"devices/system/cpu/virtual/value",
		"links/online",
	); err != nil {
		t.Fatalf("overlay does not satisfy fs.FS: %v", err)
	}
}

func TestNewOverlayFromYAMLValidation(t *testing.T) {
	base := fstest.MapFS{}
	tests := []struct {
		name string
		base FS
		data string
	}{
		{
			name: "nil base",
			data: "/sys/value: value\n",
		},
		{
			name: "relative path",
			base: base,
			data: "devices/value: value\n",
		},
		{
			name: "outside sysfs",
			base: base,
			data: "/proc/value: value\n",
		},
		{
			name: "sysfs root",
			base: base,
			data: "/sys: value\n",
		},
		{
			name: "unclean path",
			base: base,
			data: "/sys/devices/../value: value\n",
		},
		{
			name: "file is also parent",
			base: base,
			data: "/sys/devices: file\n/sys/devices/value: value\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewOverlayFromYAML(tt.base, []byte(tt.data)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func assertFileContents(t *testing.T, sysfs fs.FS, name, want string) {
	t.Helper()
	contents, err := fs.ReadFile(sysfs, name)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", name, err)
	}
	if got := string(contents); got != want {
		t.Fatalf("ReadFile(%q) = %q, want %q", name, got, want)
	}
}
