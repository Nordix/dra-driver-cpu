/*
Copyright 2026 The Kubernetes Authors.

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
	"context"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/utils/cpuset"
)

// prepareClaimWithStatus builds a driver backed by a fake kube client so that
// PrepareResourceClaims can also publish claim device status.
func makeDriverWithFakeClient(t *testing.T, claim *resourceapi.ResourceClaim) (*CPUDriver, *k8sfake.Clientset) {
	t.Helper()
	fakeClient := k8sfake.NewClientset(claim)
	prov := Providers{
		CPUInfo: &cpuinfo.MockCPUInfoProvider{
			CPUInfos: mockCPUInfos_SingleSocket_4CPUS_HT,
		},
		SysFS:     testSysFS(mockCPUInfos_SingleSocket_4CPUS_HT),
		K8SClient: fakeClient,
	}
	conf := Config{
		DriverName: testDriverName,
		NodeName:   testNodeName,
	}
	driver, err := New(testr.New(t), prov, &conf)
	require.NoError(t, err)
	driver.cdiMgr = newMockCdiMgr()
	return driver, fakeClient
}

// TestPrepareResourceClaims_PublishesClaimStatus verifies that a successful
// PrepareResourceClaims call writes Ready=True into ResourceClaim.status.devices.
func TestPrepareResourceClaims_PublishesClaimStatus(t *testing.T) {
	claimUID := types.UID("claim-status-test")
	claim := &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			UID:             claimUID,
			Name:            "my-claim",
			Namespace:       "default",
			ResourceVersion: "1",
		},
		Status: resourceapi.ResourceClaimStatus{
			Allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Results: []resourceapi.DeviceRequestAllocationResult{
						{Driver: testDriverName, Pool: testNodeName, Device: "cpudev000"},
						{Driver: testDriverName, Pool: testNodeName, Device: "cpudev002"},
					},
				},
			},
		},
	}

	driver, fakeClient := makeDriverWithFakeClient(t, claim)

	result, err := driver.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.NoError(t, result[claimUID].Err)

	// Verify claim status was published.
	updated, err := fakeClient.ResourceV1().ResourceClaims("default").Get(context.Background(), "my-claim", metav1.GetOptions{})
	require.NoError(t, err)

	require.Len(t, updated.Status.Devices, 2, "expected one AllocatedDeviceStatus per allocated device")
	for _, ds := range updated.Status.Devices {
		assert.Equal(t, testDriverName, ds.Driver)
		require.Len(t, ds.Conditions, 1)
		assert.Equal(t, conditionTypeReady, ds.Conditions[0].Type)
		assert.Equal(t, metav1.ConditionTrue, ds.Conditions[0].Status)
		assert.Equal(t, reasonCPUsPinned, ds.Conditions[0].Reason)
		assert.NotNil(t, ds.Data, "expected Data to contain assigned CPUs")
	}
}

// TestUnprepareResourceClaims_ClearsClaimStatus verifies that a successful
// UnprepareResourceClaims call writes Ready=False into ResourceClaim.status.devices.
func TestUnprepareResourceClaims_ClearsClaimStatus(t *testing.T) {
	claimUID := types.UID("claim-unprepare-test")
	claim := &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			UID:             claimUID,
			Name:            "my-claim",
			Namespace:       "default",
			ResourceVersion: "1",
		},
		Status: resourceapi.ResourceClaimStatus{
			Allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Results: []resourceapi.DeviceRequestAllocationResult{
						{Driver: testDriverName, Pool: testNodeName, Device: "cpudev000"},
					},
				},
			},
		},
	}

	driver, fakeClient := makeDriverWithFakeClient(t, claim)

	// Prepare first so the allocation is tracked.
	result, err := driver.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.NoError(t, result[claimUID].Err)

	// Now unprepare.
	unprepareResult, err := driver.UnprepareResourceClaims(context.Background(), []kubeletplugin.NamespacedObject{
		{UID: claimUID, NamespacedName: types.NamespacedName{Namespace: "default", Name: "my-claim"}},
	})
	require.NoError(t, err)
	require.NoError(t, unprepareResult[claimUID])

	updated, err := fakeClient.ResourceV1().ResourceClaims("default").Get(context.Background(), "my-claim", metav1.GetOptions{})
	require.NoError(t, err)

	require.Len(t, updated.Status.Devices, 1)
	cond := updated.Status.Devices[0].Conditions[0]
	assert.Equal(t, conditionTypeReady, cond.Type)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, reasonCPUsReleased, cond.Reason)
}

// TestPrepareResourceClaims_StatusPublishFailureDoesNotFailPrepare verifies that
// a failure to write claim status does not cause PrepareResourceClaims to fail,
// since status publication is best-effort.
func TestPrepareResourceClaims_StatusPublishFailureDoesNotFailPrepare(t *testing.T) {
	claimUID := types.UID("claim-status-fail")
	// Claim exists in the driver allocation logic but NOT in the fake client —
	// so the status Get will return NotFound, causing SetReady to error internally.
	// The error should be logged but not propagated.
	claim := &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			UID:       claimUID,
			Name:      "missing-from-api",
			Namespace: "default",
		},
		Status: resourceapi.ResourceClaimStatus{
			Allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Results: []resourceapi.DeviceRequestAllocationResult{
						{Driver: testDriverName, Pool: testNodeName, Device: "cpudev000"},
					},
				},
			},
		},
	}

	// Use an empty fake client — the claim is not registered, so Get will fail.
	fakeClient := k8sfake.NewClientset()
	prov := Providers{
		CPUInfo: &cpuinfo.MockCPUInfoProvider{
			CPUInfos: mockCPUInfos_SingleSocket_4CPUS_HT,
		},
		SysFS:     testSysFS(mockCPUInfos_SingleSocket_4CPUS_HT),
		K8SClient: fakeClient,
	}
	driver, err := New(testr.New(t), prov, &Config{DriverName: testDriverName, NodeName: testNodeName})
	require.NoError(t, err)
	driver.cdiMgr = newMockCdiMgr()

	result, err := driver.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err, "PrepareResourceClaims should succeed even if status publish fails")
	assert.NoError(t, result[claimUID].Err)

	// Confirm the allocation is in the store (prepare succeeded).
	_, allocated := driver.cpuAllocationStore.GetResourceClaimAllocation(claimUID)
	assert.True(t, allocated)
}

// TestPrepareResourceClaims_PreservesLastTransitionTime verifies that calling
// Prepare twice does not advance LastTransitionTime when the condition is already True.
func TestPrepareResourceClaims_PreservesLastTransitionTime(t *testing.T) {
	claimUID := types.UID("claim-ltt-test")
	claim := &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			UID:             claimUID,
			Name:            "my-claim",
			Namespace:       "default",
			ResourceVersion: "1",
		},
		Status: resourceapi.ResourceClaimStatus{
			Allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Results: []resourceapi.DeviceRequestAllocationResult{
						{Driver: testDriverName, Pool: testNodeName, Device: "cpudev000"},
					},
				},
			},
		},
	}

	driver, fakeClient := makeDriverWithFakeClient(t, claim)

	// First prepare.
	result, err := driver.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.NoError(t, result[claimUID].Err)

	first, err := fakeClient.ResourceV1().ResourceClaims("default").Get(context.Background(), "my-claim", metav1.GetOptions{})
	require.NoError(t, err)
	firstTime := first.Status.Devices[0].Conditions[0].LastTransitionTime

	// Second prepare (idempotent, CDI already written).
	result2, err := driver.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{first})
	require.NoError(t, err)
	require.NoError(t, result2[claimUID].Err)

	second, err := fakeClient.ResourceV1().ResourceClaims("default").Get(context.Background(), "my-claim", metav1.GetOptions{})
	require.NoError(t, err)
	secondTime := second.Status.Devices[0].Conditions[0].LastTransitionTime

	assert.Equal(t, firstTime, secondTime,
		"LastTransitionTime should not advance on a repeated Prepare with same status")
}

// TestGetOnlineCPUs is a helper used in makeDriverWithFakeClient tests.
var _ = cpuset.New // ensure cpuset import is used
