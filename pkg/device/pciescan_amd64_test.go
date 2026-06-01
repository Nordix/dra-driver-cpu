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

package device

import (
	"io/fs"
	"sort"
	"testing"
	"testing/fstest"

	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/utils/ptr"
)

func TestPCIeDomainsFromFS(t *testing.T) {
	logger := testr.New(t)
	tests := []struct {
		name    string
		fs      fstest.MapFS
		want    []PCIeDomain
		wantErr bool
	}{
		{
			name: "empty FS",
			fs:   mapFSFromDevices([]PCIeDevice{}),
		},
		{
			name: "laptop single PCIe root",
			fs:   makeBasePCSysFSFixture(),
			want: []PCIeDomain{
				{
					PCIeRootAttr: deviceattribute.DeviceAttribute{
						Name:  deviceattribute.StandardDeviceAttributePCIeRoot,
						Value: resourceapi.DeviceAttribute{StringValue: ptr.To("pci0000:00")},
					},
					LocalCPUs: mustParseCPUSet(t, "0-7"),
					NUMANode:  -1, // dev laptop has no real affinity reported
				},
			},
		},
		{
			// Regression test: a root complex whose only device is directly on
			// the root bus with no intermediate PCI-to-PCI bridge must still be
			// discovered. This mirrors Intel QAT/DSA accelerators on certain
			// NUMA nodes (e.g. pci0000:f3 on a 2-socket Intel Xeon server).
			name: "device directly on root bus, no bridge",
			fs:   makeDirectDeviceOnRootBusFixture(),
			want: []PCIeDomain{
				{
					PCIeRootAttr: deviceattribute.DeviceAttribute{
						Name:  deviceattribute.StandardDeviceAttributePCIeRoot,
						Value: resourceapi.DeviceAttribute{StringValue: ptr.To("pci0000:f3")},
					},
					LocalCPUs: mustParseCPUSet(t, "1,3,5,7"),
					NUMANode:  1,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PCIeDomainsFromFS(logger, tt.fs)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			sort.Slice(got, func(i, j int) bool {
				return *got[i].PCIeRootAttr.Value.StringValue < *got[j].PCIeRootAttr.Value.StringValue
			})
			if diff := cmp.Diff(tt.want, got, cpuSetComparer); diff != "" {
				t.Errorf("PCIeDomain mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// makeDirectDeviceOnRootBusFixture simulates a root complex (pci0000:f3) with
// a co-processor (Intel QAT, class 0b:40) sitting directly on the root bus —
// no intermediate PCI-to-PCI bridge. This mirrors the topology seen on Intel
// Xeon servers where some NUMA nodes expose accelerators without a Root Port.
func makeDirectDeviceOnRootBusFixture() fstest.MapFS {
	return fstest.MapFS{
		"bus/pci/devices/0000:f3:00.0": &fstest.MapFile{
			Data: []byte("../../../devices/pci0000:f3/0000:f3:00.0"),
			Mode: fs.ModeSymlink,
		},
		"devices/pci0000:f3/0000:f3:00.0/class": &fstest.MapFile{
			Data: []byte("0x0b4000\n"), // Co-processor: class 0b, subclass 40
		},
		"devices/pci0000:f3/0000:f3:00.0/local_cpulist": &fstest.MapFile{
			Data: []byte("1,3,5,7\n"),
		},
		"devices/pci0000:f3/0000:f3:00.0/numa_node": &fstest.MapFile{
			Data: []byte("1\n"),
		},
		"devices/system/cpu/online": &fstest.MapFile{
			Data: []byte("0-7\n"),
		},
	}
}
