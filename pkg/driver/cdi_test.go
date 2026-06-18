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

package driver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	cdiSpec "tags.cncf.io/container-device-interface/specs-go"
)

// getSpecFromCache is a test helper to verify WriteSpec succeeded.
// It forces a cache refresh and searches for the specific dynamically generated filename.
func getSpecFromCache(mgr *CdiManager, targetSpecName string) *cdiSpec.Spec {
	_ = mgr.cache.Refresh()
	specs := mgr.cache.GetVendorSpecs(cdiVendor)
	for _, spec := range specs {
		if spec.GetClass() == cdiClass && filepath.Base(spec.GetPath()) == targetSpecName {
			return spec.Spec
		}
	}
	return nil
}

func TestAddDevice(t *testing.T) {
	testcases := []struct {
		name          string
		deviceName    string
		envVar        string
		cpusetStr     string
		simulateErr   bool
		expectedError string
	}{
		{
			name:        "successfully writes a new device spec to disk",
			deviceName:  "claim-cpu-add-success",
			envVar:      "CPU=2,3",
			cpusetStr:   "2,3",
			simulateErr: false,
		},
		{
			name:          "fails to write spec to disk",
			deviceName:    "claim-cpu-add-error",
			envVar:        "CPU=2,3",
			cpusetStr:     "2,3",
			simulateErr:   true,
			expectedError: "failed to write CDI spec",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			logger := testr.New(t)
			tempCDIDir := t.TempDir()
			tempMountDir := t.TempDir()

			if tc.simulateErr {
				// Make cdiDir a file so the CDI spec write fails.
				tempFile := filepath.Join(tempCDIDir, "invalid-dir-file")
				err := os.WriteFile(tempFile, []byte(""), 0600)
				require.NoError(t, err)
				tempCDIDir = tempFile
			}

			mgr, err := NewCdiManager(logger, testDriverName, tempCDIDir, tempMountDir)
			require.NoError(t, err)

			expectedSpecName := mgr.getSpecName(tc.deviceName)
			expectedSpecFilePath := filepath.Join(tempCDIDir, expectedSpecName)
			expectedHostCPUSetPath := mgr.cpusetHostPath(tc.deviceName)

			err = mgr.AddDevice(logger, tc.deviceName, []string{tc.envVar}, tc.cpusetStr)

			if tc.expectedError != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedError)
				return
			}

			require.NoError(t, err)

			_, err = os.Stat(expectedSpecFilePath)
			require.NoError(t, err, "expected CDI spec file to be created on disk")

			// Verify the cpuset file was written with correct content.
			cpusetContent, err := os.ReadFile(expectedHostCPUSetPath)
			require.NoError(t, err, "expected cpuset file to be created on disk")
			require.Equal(t, tc.cpusetStr, string(cpusetContent))

			expectedSpec := &cdiSpec.Spec{
				Version: cdiSpecVersion,
				Kind:    cdiVendor + "/" + cdiClass,
				Devices: []cdiSpec.Device{
					{
						Name: tc.deviceName,
						ContainerEdits: cdiSpec.ContainerEdits{
							Env: []string{tc.envVar},
							Mounts: []*cdiSpec.Mount{
								{
									HostPath:      expectedHostCPUSetPath,
									ContainerPath: cdiContainerMountPath,
									Options:       []string{"ro", "bind"},
								},
							},
						},
					},
				},
			}

			got := getSpecFromCache(mgr, expectedSpecName)
			if diff := cmp.Diff(expectedSpec, got); diff != "" {
				t.Errorf("unexpected spec diff: %v", diff)
			}
		})
	}
}

func TestRemoveDevice(t *testing.T) {
	testcases := []struct {
		name          string
		deviceName    string
		envVar        string
		cpusetStr     string
		simulateErr   bool
		expectedError string
	}{
		{
			name:        "successfully removes an existing device spec from disk",
			deviceName:  "claim-cpu-remove-success",
			envVar:      "CPU=4,5",
			cpusetStr:   "4,5",
			simulateErr: false,
		},
		{
			name:          "fails to remove spec when directory is actually a file",
			deviceName:    "claim-cpu-remove-error",
			envVar:        "CPU=4,5",
			cpusetStr:     "4,5",
			simulateErr:   true,
			expectedError: "failed to remove CDI spec",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			logger := testr.New(t)
			tempCDIDir := t.TempDir()
			tempMountDir := t.TempDir()

			if tc.simulateErr {
				tempFile := filepath.Join(tempCDIDir, "invalid-dir-file")
				err := os.WriteFile(tempFile, []byte(""), 0600)
				require.NoError(t, err)
				tempCDIDir = tempFile
			}

			mgr, err := NewCdiManager(logger, testDriverName, tempCDIDir, tempMountDir)
			require.NoError(t, err)

			expectedSpecName := mgr.getSpecName(tc.deviceName)
			expectedSpecFilePath := filepath.Join(tempCDIDir, expectedSpecName)
			expectedHostCPUSetPath := mgr.cpusetHostPath(tc.deviceName)

			if !tc.simulateErr {
				err = mgr.AddDevice(logger, tc.deviceName, []string{tc.envVar}, tc.cpusetStr)
				require.NoError(t, err)
				// Verify both files exist before removal.
				_, err = os.Stat(expectedHostCPUSetPath)
				require.NoError(t, err, "expected cpuset file to exist before removal")
			}

			err = mgr.RemoveDevice(logger, tc.deviceName)

			if tc.expectedError != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedError)
				return
			}

			require.NoError(t, err)

			_, err = os.Stat(expectedSpecFilePath)
			require.Error(t, err, "expected an error when stating a deleted CDI spec file")
			require.True(t, os.IsNotExist(err), "expected CDI spec file to not exist on disk, but got: %v", err)

			_, err = os.Stat(expectedHostCPUSetPath)
			require.Error(t, err, "expected an error when stating a deleted cpuset file")
			require.True(t, os.IsNotExist(err), "expected cpuset file to not exist on disk, but got: %v", err)

			gotAfterRemove := getSpecFromCache(mgr, expectedSpecName)
			require.Nil(t, gotAfterRemove, "expected spec to be nil in cache after removal")
		})
	}
}

func TestAddDeviceOverwrite(t *testing.T) {
	logger := testr.New(t)
	tempCDIDir := t.TempDir()
	tempMountDir := t.TempDir()

	mgr, err := NewCdiManager(logger, testDriverName, tempCDIDir, tempMountDir)
	require.NoError(t, err)

	deviceName := "claim-cpu-overwrite"
	expectedSpecName := mgr.getSpecName(deviceName)

	assertCDIFileCount := func(expected int) {
		files, err := os.ReadDir(tempCDIDir)
		require.NoError(t, err)
		require.Len(t, files, expected)
	}

	err = mgr.AddDevice(logger, deviceName, []string{"CPU=0,1"}, "0,1")
	require.NoError(t, err)
	assertCDIFileCount(1)

	// Verify the cache has the initial spec
	spec1 := getSpecFromCache(mgr, expectedSpecName)
	require.NotNil(t, spec1)
	require.Equal(t, []string{"CPU=0,1"}, spec1.Devices[0].ContainerEdits.Env)

	// Verify cpuset file content
	content, err := os.ReadFile(mgr.cpusetHostPath(deviceName))
	require.NoError(t, err)
	require.Equal(t, "0,1", string(content))

	// Call AddDevice again with the same deviceName and same data
	err = mgr.AddDevice(logger, deviceName, []string{"CPU=0,1"}, "0,1")
	require.NoError(t, err)
	// Verify that we do not create a new CDI spec file
	assertCDIFileCount(1)
}
