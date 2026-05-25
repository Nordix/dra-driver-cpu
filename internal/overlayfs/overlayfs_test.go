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

package overlayfs

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestNewWithInvalidData(t *testing.T) {
	base := makeFakeBaseFS(t)
	tests := []struct {
		name    string
		files   map[string]string
		wantErr string
	}{
		{
			name:    "leading slash",
			files:   map[string]string{"/devices/system/cpu/online": "0-7"},
			wantErr: "invalid path",
		},
		{
			name:    "parent traversal",
			files:   map[string]string{"../escape": "x"},
			wantErr: "invalid path",
		},
		{
			name:    "missing in base",
			files:   map[string]string{"devices/system/cpu/online2": "0-7"},
			wantErr: "not present in base",
		},
		{
			name:    "not a regular file",
			files:   map[string]string{"devices/system/cpu": "x"},
			wantErr: "not a regular file",
		},
		{
			name: "non-obvious duplicate",
			files: map[string]string{
				"devices/system/cpu/online":     "a",
				"./devices/system/cpu/./online": "b",
			},
			wantErr: "duplicate path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(base, tt.files)
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestNewNilValues(t *testing.T) {
	if _, err := New(nil, nil); err == nil {
		t.Error("want error for nil base")
	}
	if _, err := New(nil, map[string]string{
		"devices/system/cpu/online": "0-5",
	}); err == nil {
		t.Error("want error for nil base")
	}
}

func TestEmptyOverlayIsPassthrough(t *testing.T) {
	base := makeFakeBaseFS(t)
	ofs, err := New(base, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	checkPathPassthrough(t, "devices/system/cpu/online", base, ofs)
}

func TestReadFileWithOverlay(t *testing.T) {
	base := makeFakeBaseFS(t)
	wantCPUs := "0-2"
	wantLocalCPUs := "1"
	ofs, err := New(base, map[string]string{
		"devices/system/cpu/online":                  wantCPUs,
		"bus/pci/devices/0000:00:1f.0/local_cpulist": wantLocalCPUs,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	online, err := fs.ReadFile(ofs, "devices/system/cpu/online")
	if err != nil {
		t.Errorf("ReadFile online: %v", err)
	}
	if string(online) != wantCPUs {
		t.Errorf("online: got %q want %q", online, "0-7")
	}

	cpulist, err := fs.ReadFile(ofs, "bus/pci/devices/0000:00:1f.0/local_cpulist")
	if err != nil {
		t.Fatalf("ReadFile cpulist: %v", err)
	}
	if string(cpulist) != wantLocalCPUs {
		t.Errorf("local_cpulist: got %q want %q", cpulist, "4-7")
	}

	checkPathPassthrough(t, "bus/pci/devices/0000:00:1f.0/numa_node", base, ofs)
}

func TestReadDirPassthrough(t *testing.T) {
	base := makeFakeBaseFS(t)
	ofs, err := New(base, map[string]string{
		"bus/pci/devices/0000:00:1f.0/local_cpulist": "4-7",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	entries, err := fs.ReadDir(ofs, "bus/pci/devices/0000:00:1f.0")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	names := sets.New[string]()
	for _, e := range entries {
		names.Insert(e.Name())
	}
	want := sets.New("local_cpulist", "numa_node", "subsystem")
	if missing := names.Difference(want); missing.Len() > 0 {
		wantString := strings.Join(sets.List(want), ",")
		namesString := strings.Join(sets.List(names), ",")
		t.Errorf("missing entries %q in ReadDir result %q", wantString, namesString)
	}
}

func TestReadLinkPassthrough(t *testing.T) {
	base := makeFakeBaseFS(t)
	ofs, err := New(base, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want, err := base.ReadLink("bus/pci/devices/0000:00:1f.0/subsystem")
	if err != nil {
		t.Fatalf("ReadLink (base): %v", err)
	}
	got, err := ofs.ReadLink("bus/pci/devices/0000:00:1f.0/subsystem")
	if err != nil {
		t.Fatalf("ReadLink (overlay): %v", err)
	}
	if got != want {
		t.Errorf("ReadLink: got %q want %q", got, want)
	}
}

func TestLstatOverlayReportSize(t *testing.T) {
	base := makeFakeBaseFS(t)
	wantCPUs := "0-7,8-15"
	ofs, err := New(base, map[string]string{
		"devices/system/cpu/online": wantCPUs,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	info, err := ofs.Lstat("devices/system/cpu/online")
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Size() != int64(len(wantCPUs)) {
		t.Errorf("Lstat.Size: got %d want %d", info.Size(), len(wantCPUs))
	}
	if !info.Mode().IsRegular() {
		t.Errorf("Lstat.Mode: got %s, want regular", info.Mode())
	}
}

func TestPathsAndGet(t *testing.T) {
	base := makeFakeBaseFS(t)
	wantCPUs := "0-7"
	ofs, err := New(base, map[string]string{
		"devices/system/cpu/online":                  wantCPUs,
		"bus/pci/devices/0000:00:1f.0/local_cpulist": "4-7",
		"bus/pci/devices/0000:00:1f.0/numa_node":     "1",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := sets.New(ofs.Paths()...)
	want := sets.New(
		"bus/pci/devices/0000:00:1f.0/local_cpulist",
		"bus/pci/devices/0000:00:1f.0/numa_node",
		"devices/system/cpu/online",
	)
	if !got.Equal(want) {
		wantString := strings.Join(sets.List(want), ",")
		gotString := strings.Join(sets.List(got), ",")
		t.Errorf("Paths: got %q, want %q", gotString, wantString)
	}
	gotCPUs, ok := ofs.Get("devices/system/cpu/online")
	if !ok || string(gotCPUs) != wantCPUs {
		t.Errorf("from overlayfs got (%q, %v), want (%q, true)", gotCPUs, ok, wantCPUs)
	}
	if _, ok := ofs.Get("does/not/exist"); ok {
		t.Errorf("overlayfs reported unexistent file")
	}
}

func TestFromYAML(t *testing.T) {
	base := makeFakeBaseFS(t)
	yamlText := `
files:
  devices/system/cpu/online:
    data: "0-7"
  bus/pci/devices/0000:00:1f.0/local_cpulist:
    data: |
      4-7
`
	ofs, err := FromYAML(base, strings.NewReader(yamlText))
	if err != nil {
		t.Fatalf("FromYAML: %v", err)
	}
	got, err := fs.ReadFile(ofs, "devices/system/cpu/online")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "0-7" {
		t.Errorf("got %q, want %q", got, "0-7")
	}
	got2, err := fs.ReadFile(ofs, "bus/pci/devices/0000:00:1f.0/local_cpulist")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got2) != "4-7\n" {
		t.Errorf("got %q, want %q", got2, "4-7\n")
	}
}

func TestFromMalformedYAML(t *testing.T) {
	base := makeFakeBaseFS(t)
	if _, err := FromYAML(base, strings.NewReader("not: [valid")); err == nil {
		t.Fatal("want parse error")
	}
}

func checkPathPassthrough(t *testing.T, name string, base, ofs device.SysFS) {
	t.Helper()
	want, err := fs.ReadFile(base, name)
	if err != nil {
		t.Fatalf("ReadFile (base): %v", err)
	}
	got, err := fs.ReadFile(ofs, name)
	if err != nil {
		t.Fatalf("ReadFile (overlay): %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("got %q, want base content %q", got, want)
	}
}

func makeFakeBaseFS(t *testing.T) fstest.MapFS {
	t.Helper()
	return fstest.MapFS{
		"devices/system/cpu/online": &fstest.MapFile{
			Data: []byte("0-3\n"),
		},
		"bus/pci/devices/0000:00:1f.0/local_cpulist": &fstest.MapFile{
			Data: []byte("0-1\n"),
		},
		"bus/pci/devices/0000:00:1f.0/numa_node": &fstest.MapFile{
			Data: []byte("0\n"),
		},
		"bus/pci/devices/0000:00:1f.0/subsystem": &fstest.MapFile{
			Mode: fs.ModeSymlink,
			Data: []byte("../../../../bus/pci"),
		},
	}
}
