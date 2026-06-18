/*
Copyright 2025 The Kubernetes Authors.

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
package driver

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-logr/logr"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdiSpec "tags.cncf.io/container-device-interface/specs-go"
)

const (
	cdiSpecVersion  = "0.8.0"
	cdiVendor       = "dra.k8s.io"
	cdiClass        = "cpu"
	cdiEnvVarPrefix = "DRA_CPUSET"
	// This mirrors the semantics of the assigned.cpuset introduced in k/k KEP #6122.
	cdiEnvVarAssigned     = "DRA_CPUSET_ASSIGNED"
	cdiSpecDir            = "/var/run/cdi"
	cdiMountBaseDir       = "/var/run/dra-cpu"
	cdiContainerMountPath = "/etc/dra-cpu/assigned-cpuset"
)

// CdiManager handles the lifecycle of CDI allocations for the driver.
type CdiManager struct {
	cache        *cdiapi.Cache
	cdiKind      string
	driverName   string
	mountBaseDir string
}

// NewCdiManager creates a manager for the driver's CDI allocations.
func NewCdiManager(logger logr.Logger, driverName string, cdiDir string, mountBaseDir string) (*CdiManager, error) {
	cache, err := cdiapi.NewCache(
		cdiapi.WithSpecDirs(cdiDir),
		// Disabled because we manage state entirely via the filesystem
		// and write individual stateless files per device.
		cdiapi.WithAutoRefresh(false),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create CDI cache: %w", err)
	}

	c := &CdiManager{
		cache:        cache,
		cdiKind:      fmt.Sprintf("%s/%s", cdiVendor, cdiClass),
		driverName:   driverName,
		mountBaseDir: mountBaseDir,
	}

	logger.Info("Initialized CDI manager", "driverName", driverName, "cdiDir", cdiDir, "mountBaseDir", mountBaseDir)
	return c, nil
}

// getSpecName generates a unique, sanitized filename for a specific device allocation.
func (c *CdiManager) getSpecName(deviceName string) string {
	return cdiapi.GenerateTransientSpecName(cdiVendor, cdiClass, deviceName) + ".json"
}

// cpusetHostPath returns the host path of the cpuset file for a given device.
func (c *CdiManager) cpusetHostPath(deviceName string) string {
	return filepath.Join(c.mountBaseDir, deviceName, "assigned-cpuset")
}

func (c *CdiManager) AddDevice(logger logr.Logger, deviceName string, envVars []string, cpusetStr string) error {
	hostPath := c.cpusetHostPath(deviceName)
	if err := os.MkdirAll(filepath.Dir(hostPath), 0750); err != nil {
		return fmt.Errorf("failed to create cpuset dir %q: %w", filepath.Dir(hostPath), err)
	}
	if err := os.WriteFile(hostPath, []byte(cpusetStr), 0640); err != nil {
		return fmt.Errorf("failed to write cpuset file %q: %w", hostPath, err)
	}

	specName := c.getSpecName(deviceName)
	spec := &cdiSpec.Spec{
		Version: cdiSpecVersion,
		Kind:    c.cdiKind,
		Devices: []cdiSpec.Device{
			{
				Name: deviceName,
				ContainerEdits: cdiSpec.ContainerEdits{
					Env: envVars,
					Mounts: []*cdiSpec.Mount{
						{
							HostPath:      hostPath,
							ContainerPath: cdiContainerMountPath,
							Options:       []string{"ro", "bind"},
						},
					},
				},
			},
		},
	}

	if err := c.cache.WriteSpec(spec, specName); err != nil {
		return fmt.Errorf("failed to write CDI spec %q: %w", specName, err)
	}

	logger.V(4).Info("Added CDI device", "deviceName", deviceName, "specName", specName,
		"envVars", envVars, "hostPath", hostPath, "containerPath", cdiContainerMountPath)
	return nil
}

// RemoveDevice deletes the CDI spec file and the host-side cpuset file for a device allocation.
func (c *CdiManager) RemoveDevice(logger logr.Logger, deviceName string) error {
	specName := c.getSpecName(deviceName)
	if err := c.cache.RemoveSpec(specName); err != nil {
		return fmt.Errorf("failed to remove CDI spec %q: %w", specName, err)
	}

	hostDir := filepath.Dir(c.cpusetHostPath(deviceName))
	if err := os.RemoveAll(hostDir); err != nil {
		return fmt.Errorf("failed to remove cpuset dir %q: %w", hostDir, err)
	}

	logger.V(4).Info("Removed CDI device", "deviceName", deviceName, "specName", specName, "hostDir", hostDir)
	return nil
}
