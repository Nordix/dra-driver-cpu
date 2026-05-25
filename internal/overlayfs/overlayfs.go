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

// Package overlayfs provides a read-only filesystem that overlays in-memory
// file contents on top of the base sysfs. While the code tries to be generic,
// when it's cheap, it is designed exclusively for  pseudo-filesystems
// (sysfs, procfs) and leverage all the possible simplifications this assumption
// enables. Key highlights: file content is assumed tiny,  overrides are limited
// to leaf regular files that already exist in the base.
package overlayfs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path"
	"slices"
	"time"

	"sigs.k8s.io/yaml"
)

// Base is the underlying filesystem. In production this is os.DirFS.
type Base interface {
	fs.ReadLinkFS
	fs.ReadDirFS
}

// FS overlays in-memory file contents on top of a Base.
type FS struct {
	base  Base
	files map[string][]byte
}

// New builds an overlay. The `files` map is validated at creation time.
// Each key in `files` must:
// - be a valid fs.FS path (slash-separated, no leading "/", no "..").
// - be resolved in `base` to an existing regular file.
// Duplicate keys are rejected.
func New(base Base, files map[string]string) (*FS, error) {
	if base == nil {
		return nil, errors.New("overlayfs: base must not be nil")
	}
	out := &FS{
		base:  base,
		files: make(map[string][]byte, len(files)),
	}
	for k, v := range files {
		clean := path.Clean(k)
		if !fs.ValidPath(clean) {
			return nil, fmt.Errorf("overlayfs: invalid path %q", k)
		}
		if _, dup := out.files[clean]; dup {
			return nil, fmt.Errorf("overlayfs: duplicate path %q (cleaned: %q)", k, clean)
		}
		info, err := fs.Stat(base, clean)
		if err != nil {
			return nil, fmt.Errorf("overlayfs: %q not present in base: %w", clean, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("overlayfs: %q is not a regular file in base (mode=%s)", clean, info.Mode())
		}
		out.files[clean] = []byte(v)
	}
	return out, nil
}

type configEntry struct {
	Data string `json:"data"`
}

type config struct {
	Files map[string]configEntry `json:"files"`
}

// FromYAML parses an overlay configuration from `r` and constructs the FS.
//
// Example YAML:
// ```
//
//	files:
//	  devices/system/cpu/online:
//	    data: "0-7"
//	  bus/pci/devices/0000:00:1f.0/local_cpulist:
//	    data: "0-3,8-11"
//
// ```
func FromYAML(base Base, r io.Reader) (*FS, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("overlayfs: reading config: %w", err)
	}
	var cfg config
	if err := yaml.UnmarshalStrict(raw, &cfg); err != nil {
		return nil, fmt.Errorf("overlayfs: parsing config: %w", err)
	}
	files := make(map[string]string, len(cfg.Files))
	for k, v := range cfg.Files {
		files[k] = v.Data
	}
	return New(base, files)
}

func (f *FS) Open(name string) (fs.File, error) {
	data, ok := f.files[name]
	if !ok {
		return f.base.Open(name)
	}
	return &overlayFile{name: path.Base(name), r: bytes.NewReader(data), size: int64(len(data))}, nil
}

func (f *FS) Lstat(name string) (fs.FileInfo, error) {
	data, ok := f.files[name]
	if !ok {
		return f.base.Lstat(name)
	}
	return overlayFileInfo{name: path.Base(name), size: int64(len(data))}, nil
}

// Paths returns the sorted list of paths overridden by the overlay.
func (f *FS) Paths() []string {
	return slices.Sorted(maps.Keys(f.files))
}

// Get returns the overlaid bytes for the given path and whether such an
// overlay exists.
func (f *FS) Get(name string) ([]byte, bool) {
	data, ok := f.files[name]
	if !ok {
		return nil, false
	}
	return slices.Clone(data), true
}

// trivial passhthrough methods

func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) { return f.base.ReadDir(name) }
func (f *FS) ReadLink(name string) (string, error)       { return f.base.ReadLink(name) }

type overlayFile struct {
	name string
	r    *bytes.Reader
	size int64
}

func (b *overlayFile) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *overlayFile) Close() error               { return nil }
func (b *overlayFile) Stat() (fs.FileInfo, error) {
	return overlayFileInfo{name: b.name, size: b.size}, nil
}

type overlayFileInfo struct {
	name string
	size int64
}

func (i overlayFileInfo) Name() string       { return i.name }
func (i overlayFileInfo) Size() int64        { return i.size }
func (i overlayFileInfo) Mode() fs.FileMode  { return 0o444 }
func (i overlayFileInfo) ModTime() time.Time { return time.Time{} }
func (i overlayFileInfo) IsDir() bool        { return false }
func (i overlayFileInfo) Sys() any           { return nil }
