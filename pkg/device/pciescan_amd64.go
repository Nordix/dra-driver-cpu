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
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/utils/cpuset"
)

var (
	pciAddrRegex *regexp.Regexp
)

func init() {
	// same regex as k8s' deviceattribute package, borrowed here
	pciAddrRegex = regexp.MustCompile(`^([0-9a-f]{4}):([0-9a-f]{2}):([0-9a-f]{2})\.([0-9a-f]{1})$`)
}

func ScanPCIeDevices(logger logr.Logger, sysfs SysFS, processDevice func(PCIeDevice) error) error {
	pciDevicesDir := filepath.Join("bus", "pci", "devices")
	entries, err := fs.ReadDir(sysfs, pciDevicesDir)
	if err != nil {
		return err
	}
	logger.V(6).Info("begin: processing PCIe devices", "devices", len(entries))
	defer logger.V(6).Info("end: processing PCIe devices", "devices", len(entries))

	for _, entry := range entries {
		pciAddress := entry.Name()
		if !isValidPCIAddress(pciAddress) {
			continue
		}

		classData, err := fs.ReadFile(sysfs, filepath.Join(pciDevicesDir, pciAddress, "class"))
		if err != nil {
			return err
		}

		pciClassInfo := strings.TrimSpace(string(classData))
		if len(pciClassInfo) != 8 { // format: "0xCCSSpp"
			return fmt.Errorf("invalid PCI Class data: %q", pciClassInfo)
		}

		pciDev := PCIeDevice{
			Address:    pciAddress,
			ClassID:    pciClassInfo[2:4],
			SubclassID: pciClassInfo[4:6],
		}

		err = processDevice(pciDev)
		if err != nil {
			return err
		}
	}
	return nil
}

func PCIeDomainsFromFS(logger logr.Logger, sysfs SysFS) ([]PCIeDomain, error) {
	// We use a map (not a slice) to deduplicate PCIeRoot domains.
	// We scan all PCI devices and use GetPCIeRootAttributeByPCIBusID to walk
	// up the sysfs tree to find the root complex for each device. Multiple
	// devices under the same root complex produce the same key, so the map
	// deduplication is harmless.
	//
	// We intentionally do NOT restrict to PCI-to-PCI bridges (class 06:04)
	// because some root complexes have devices connected directly on the root
	// bus with no intermediate bridge (e.g. Intel QAT/DSA accelerators on
	// certain NUMA nodes). Restricting to bridges would silently miss those
	// root complexes.
	domains := make(map[string]PCIeDomain)

	err := ScanPCIeDevices(logger, sysfs, func(pciDev PCIeDevice) error {
		plogger := logger.WithValues("device", pciDev.String())

		plogger.V(6).Info("PCIe: candidate device found")

		pcieRootAttr, err := deviceattribute.GetPCIeRootAttributeByPCIBusID(pciDev.Address, deviceattribute.WithFS(sysfs))
		if err != nil {
			return err
		}

		// Skip if we already have this root complex; all devices under the
		// same root report identical local_cpulist and numa_node.
		if _, exists := domains[*pcieRootAttr.Value.StringValue]; exists {
			return nil
		}

		cpuData, err := fs.ReadFile(sysfs, filepath.Join(pciDev.SysfsPath(), "local_cpulist"))
		if err != nil {
			return err
		}
		localCPUs, err := cpuset.Parse(strings.TrimSpace(string(cpuData)))
		if err != nil {
			return err
		}
		plogger.V(4).Info("PCIe: candidate device", "localCPUs", localCPUs.String())

		numaData, err := fs.ReadFile(sysfs, filepath.Join(pciDev.SysfsPath(), "numa_node"))
		if err != nil {
			return err
		}
		numaNode, err := strconv.Atoi(strings.TrimSpace(string(numaData)))
		if err != nil {
			return err
		}
		plogger.V(4).Info("PCIe: candidate device", "numaNode", numaNode)

		pcd := PCIeDomain{
			PCIeRootAttr: pcieRootAttr,
			LocalCPUs:    localCPUs,
			NUMANode:     numaNode,
		}
		domains[pcd.Root()] = pcd
		plogger.V(2).Info("PCIe: device mapped to domain", "domain", pcd.String())

		return nil
	})

	if err != nil {
		return nil, err
	}
	doms := slices.Collect(maps.Values(domains))
	slices.SortFunc(doms, func(a, b PCIeDomain) int {
		return strings.Compare(a.Root(), b.Root())
	})
	return doms, nil
}

// isValidPCIAddress checks if s matches the format
// DDDD:BB:SS.F (domain:bus:slot.function)
// where each letter is a hex digit.
func isValidPCIAddress(addr string) bool {
	return pciAddrRegex.MatchString(addr)
}
