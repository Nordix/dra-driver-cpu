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

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/driver"
)

func TestSysFSOverlayFeedsCPUInfoProvider(t *testing.T) {
	overlayPath := filepath.Join(t.TempDir(), "sysfs-overlay.yaml")
	overlayData := []byte(`
/sys/devices/system/cpu/online: "999\n"
/sys/devices/system/cpu/smt/control: "on\n"
/sys/devices/system/node/node5/cpulist: "998-999\n"
/sys/devices/system/cpu/cpu999/node5: ""
/sys/devices/system/cpu/cpu999/topology/physical_package_id: "7\n"
/sys/devices/system/cpu/cpu999/topology/core_id: "3\n"
/sys/devices/system/cpu/cpu999/topology/cluster_id: "4\n"
/sys/devices/system/cpu/cpu999/cache/index3/level: "3\n"
/sys/devices/system/cpu/cpu999/cache/index3/id: "11\n"
/sys/devices/system/cpu/cpu999/cache/index3/shared_cpu_list: "999\n"
`)
	if err := os.WriteFile(overlayPath, overlayData, 0o600); err != nil {
		t.Fatalf("write sysfs overlay: %v", err)
	}

	logger := testr.New(t)
	sfs, err := newSysFS(logger, overlayPath)
	if err != nil {
		t.Fatalf("newSysFS() error = %v", err)
	}

	providers := driver.Providers{SysFS: sfs}
	topology, err := providers.EnsureCPUInfo().GetCPUTopology(logger)
	if err != nil {
		t.Fatalf("GetCPUTopology() error = %v", err)
	}

	if got, want := topology.NumCPUs, 1; got != want {
		t.Fatalf("NumCPUs = %d, want %d", got, want)
	}
	cpu, ok := topology.CPUDetails[999]
	if !ok {
		t.Fatal("CPUDetails does not contain overlaid CPU 999")
	}
	if got, want := cpu.SocketID, 7; got != want {
		t.Errorf("SocketID = %d, want %d", got, want)
	}
	if got, want := cpu.CoreID, 3; got != want {
		t.Errorf("CoreID = %d, want %d", got, want)
	}
	if got, want := cpu.ClusterID, 4; got != want {
		t.Errorf("ClusterID = %d, want %d", got, want)
	}
	if got, want := cpu.NUMANodeID, 5; got != want {
		t.Errorf("NUMANodeID = %d, want %d", got, want)
	}
	if got, want := cpu.NumaNodeCPUSet.String(), "998-999"; got != want {
		t.Errorf("NumaNodeCPUSet = %q, want %q", got, want)
	}
	if got, want := cpu.UncoreCacheID, 11; got != want {
		t.Errorf("UncoreCacheID = %d, want %d", got, want)
	}
	if !topology.SMTEnabled {
		t.Error("SMTEnabled = false, want true")
	}
}

func TestSysFSOverlayUsesHostRootAsBase(t *testing.T) {
	hostRoot := t.TempDir()
	hostRootFile := filepath.Join(hostRoot, "sys", "devices", "system", "cpu", "host-root-only")
	if err := os.MkdirAll(filepath.Dir(hostRootFile), 0o755); err != nil {
		t.Fatalf("create host sysfs directory: %v", err)
	}
	if err := os.WriteFile(hostRootFile, []byte("from host root\n"), 0o600); err != nil {
		t.Fatalf("write host sysfs file: %v", err)
	}
	t.Setenv("HOST_ROOT", hostRoot)

	overlayPath := filepath.Join(t.TempDir(), "sysfs-overlay.yaml")
	if err := os.WriteFile(overlayPath, []byte(`/sys/devices/system/cpu/online: "999\n"`), 0o600); err != nil {
		t.Fatalf("write sysfs overlay: %v", err)
	}

	sfs, err := newSysFS(testr.New(t), overlayPath)
	if err != nil {
		t.Fatalf("newSysFS() error = %v", err)
	}

	contents, err := fs.ReadFile(sfs, "devices/system/cpu/online")
	if err != nil {
		t.Fatalf("read overlaid sysfs file: %v", err)
	}
	if got, want := string(contents), "999\n"; got != want {
		t.Errorf("overlaid sysfs file = %q, want %q", got, want)
	}

	contents, err = fs.ReadFile(sfs, "devices/system/cpu/host-root-only")
	if err != nil {
		t.Fatalf("read host-root sysfs file: %v", err)
	}
	if got, want := string(contents), "from host root\n"; got != want {
		t.Errorf("host-root sysfs file = %q, want %q", got, want)
	}
}
