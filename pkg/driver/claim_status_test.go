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
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/cpuset"
)

const (
	testPublisherDriverName = "dra.cpu"
	testClaimNamespace      = "default"
	testClaimName           = "my-claim"
	testClaimUID            = types.UID("claim-uid-1")
	testPool                = "test-node"
	testDevice              = "cpudev000"
)

func makeClaim(uid types.UID, ns, name string, driverName string, pool, device string) *resourceapi.ResourceClaim {
	return &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			UID:             uid,
			Namespace:       ns,
			Name:            name,
			ResourceVersion: "1",
		},
		Status: resourceapi.ResourceClaimStatus{
			Allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Results: []resourceapi.DeviceRequestAllocationResult{
						{Driver: driverName, Pool: pool, Device: device},
					},
				},
			},
		},
	}
}

// TestSetReady_WritesConditionAndData verifies that SetReady writes Ready=True
// and the CPU data into ResourceClaim.status.devices.
func TestSetReady_WritesConditionAndData(t *testing.T) {
	claim := makeClaim(testClaimUID, testClaimNamespace, testClaimName, testPublisherDriverName, testPool, testDevice)
	fakeClient := k8sfake.NewClientset(claim)
	p := newClaimStatusPublisher(testPublisherDriverName, fakeClient)

	cpus := cpuset.New(0, 1, 2, 3)
	err := p.SetReady(context.Background(), claim, cpus)
	require.NoError(t, err)

	updated, err := fakeClient.ResourceV1().ResourceClaims(testClaimNamespace).Get(context.Background(), testClaimName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, updated.Status.Devices, 1)

	ds := updated.Status.Devices[0]
	assert.Equal(t, testPublisherDriverName, ds.Driver)
	assert.Equal(t, testPool, ds.Pool)
	assert.Equal(t, testDevice, ds.Device)

	require.Len(t, ds.Conditions, 1)
	cond := ds.Conditions[0]
	assert.Equal(t, conditionTypeReady, cond.Type)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, reasonCPUsPinned, cond.Reason)
	assert.Contains(t, cond.Message, cpus.String())

	require.NotNil(t, ds.Data)
	var payload assignedCPUData
	require.NoError(t, json.Unmarshal(ds.Data.Raw, &payload))
	assert.Equal(t, cpus.String(), payload.AssignedCPUs)
}

// TestClearReady_WritesReadyFalse verifies that ClearReady writes Ready=False.
func TestClearReady_WritesReadyFalse(t *testing.T) {
	claim := makeClaim(testClaimUID, testClaimNamespace, testClaimName, testPublisherDriverName, testPool, testDevice)
	fakeClient := k8sfake.NewClientset(claim)
	p := newClaimStatusPublisher(testPublisherDriverName, fakeClient)

	err := p.ClearReady(context.Background(), testClaimNamespace, testClaimName, testClaimUID)
	require.NoError(t, err)

	updated, err := fakeClient.ResourceV1().ResourceClaims(testClaimNamespace).Get(context.Background(), testClaimName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, updated.Status.Devices, 1)

	cond := updated.Status.Devices[0].Conditions[0]
	assert.Equal(t, conditionTypeReady, cond.Type)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, reasonCPUsReleased, cond.Reason)
}

// TestSetReady_PreservesLastTransitionTime verifies that LastTransitionTime is not
// bumped when the condition status hasn't changed (Kubernetes convention).
func TestSetReady_PreservesLastTransitionTime(t *testing.T) {
	originalTime := metav1.NewTime(time.Now().Add(-1 * time.Hour))
	claim := makeClaim(testClaimUID, testClaimNamespace, testClaimName, testPublisherDriverName, testPool, testDevice)
	// Pre-populate the claim with a Ready=True condition at originalTime.
	claim.Status.Devices = []resourceapi.AllocatedDeviceStatus{
		{
			Driver: testPublisherDriverName,
			Pool:   testPool,
			Device: testDevice,
			Conditions: []metav1.Condition{
				{
					Type:               conditionTypeReady,
					Status:             metav1.ConditionTrue,
					Reason:             reasonCPUsPinned,
					LastTransitionTime: originalTime,
				},
			},
		},
	}

	fakeClient := k8sfake.NewClientset(claim)
	p := newClaimStatusPublisher(testPublisherDriverName, fakeClient)

	err := p.SetReady(context.Background(), claim, cpuset.New(0, 1))
	require.NoError(t, err)

	updated, err := fakeClient.ResourceV1().ResourceClaims(testClaimNamespace).Get(context.Background(), testClaimName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, updated.Status.Devices, 1)

	cond := updated.Status.Devices[0].Conditions[0]
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, originalTime.UTC(), cond.LastTransitionTime.UTC(),
		"LastTransitionTime should not change when status is unchanged")
}

// TestSetReady_UpdatesLastTransitionTime verifies that LastTransitionTime IS bumped
// when the condition status changes (e.g. False → True).
func TestSetReady_UpdatesLastTransitionTime(t *testing.T) {
	originalTime := metav1.NewTime(time.Now().Add(-1 * time.Hour))
	claim := makeClaim(testClaimUID, testClaimNamespace, testClaimName, testPublisherDriverName, testPool, testDevice)
	claim.Status.Devices = []resourceapi.AllocatedDeviceStatus{
		{
			Driver: testPublisherDriverName,
			Pool:   testPool,
			Device: testDevice,
			Conditions: []metav1.Condition{
				{
					Type:               conditionTypeReady,
					Status:             metav1.ConditionFalse, // was False
					Reason:             reasonCPUsReleased,
					LastTransitionTime: originalTime,
				},
			},
		},
	}

	fakeClient := k8sfake.NewClientset(claim)
	p := newClaimStatusPublisher(testPublisherDriverName, fakeClient)

	err := p.SetReady(context.Background(), claim, cpuset.New(0, 1))
	require.NoError(t, err)

	updated, err := fakeClient.ResourceV1().ResourceClaims(testClaimNamespace).Get(context.Background(), testClaimName, metav1.GetOptions{})
	require.NoError(t, err)
	cond := updated.Status.Devices[0].Conditions[0]
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.True(t, cond.LastTransitionTime.After(originalTime.Time),
		"LastTransitionTime should advance when status changes from False to True")
}

// TestSetReady_NoDevicesForDriver verifies that SetReady is a no-op when the claim
// has no devices belonging to this driver.
func TestSetReady_NoDevicesForDriver(t *testing.T) {
	claim := makeClaim(testClaimUID, testClaimNamespace, testClaimName, "other-driver", testPool, testDevice)
	fakeClient := k8sfake.NewClientset(claim)
	p := newClaimStatusPublisher(testPublisherDriverName, fakeClient)

	err := p.SetReady(context.Background(), claim, cpuset.New(0, 1))
	require.NoError(t, err)

	updated, err := fakeClient.ResourceV1().ResourceClaims(testClaimNamespace).Get(context.Background(), testClaimName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, updated.Status.Devices, "status.devices should be empty when driver has no devices in the claim")
}

// TestClearReady_ClaimNotFound verifies that ClearReady is a no-op when the claim
// has already been deleted.
func TestClearReady_ClaimNotFound(t *testing.T) {
	fakeClient := k8sfake.NewClientset() // no claims
	p := newClaimStatusPublisher(testPublisherDriverName, fakeClient)

	err := p.ClearReady(context.Background(), testClaimNamespace, testClaimName, testClaimUID)
	require.NoError(t, err, "ClearReady should not error when claim is not found")
}

// TestClearReady_StaleUID verifies that ClearReady is a no-op when the claim UID
// doesn't match (a new claim reused the same namespace/name).
func TestClearReady_StaleUID(t *testing.T) {
	claim := makeClaim("different-uid", testClaimNamespace, testClaimName, testPublisherDriverName, testPool, testDevice)
	fakeClient := k8sfake.NewClientset(claim)
	p := newClaimStatusPublisher(testPublisherDriverName, fakeClient)

	err := p.ClearReady(context.Background(), testClaimNamespace, testClaimName, testClaimUID /* stale UID */)
	require.NoError(t, err)

	updated, err := fakeClient.ResourceV1().ResourceClaims(testClaimNamespace).Get(context.Background(), testClaimName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, updated.Status.Devices, "status.devices should not be modified for stale UID")
}

// TestSetReady_NilKubeClient verifies that a publisher with nil kubeClient is a no-op.
func TestSetReady_NilKubeClient(t *testing.T) {
	p := newClaimStatusPublisher(testPublisherDriverName, nil)
	claim := makeClaim(testClaimUID, testClaimNamespace, testClaimName, testPublisherDriverName, testPool, testDevice)
	require.NoError(t, p.SetReady(context.Background(), claim, cpuset.New(0, 1)))
}

// TestClearReady_NilKubeClient verifies that a publisher with nil kubeClient is a no-op.
func TestClearReady_NilKubeClient(t *testing.T) {
	p := newClaimStatusPublisher(testPublisherDriverName, nil)
	require.NoError(t, p.ClearReady(context.Background(), testClaimNamespace, testClaimName, testClaimUID))
}

// TestSetReady_RetriesOnConflict verifies that SetReady retries when the API server
// returns a Conflict error (concurrent update).
func TestSetReady_RetriesOnConflict(t *testing.T) {
	claim := makeClaim(testClaimUID, testClaimNamespace, testClaimName, testPublisherDriverName, testPool, testDevice)
	fakeClient := k8sfake.NewClientset(claim)

	conflictCount := 0
	fakeClient.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if conflictCount < 2 {
			conflictCount++
			return true, nil, apierrors.NewConflict(
				resourceapi.Resource("resourceclaims"), testClaimName, fmt.Errorf("the object has been modified"),
			)
		}
		return false, nil, nil // let the default reactor handle it
	})

	p := newClaimStatusPublisher(testPublisherDriverName, fakeClient)
	err := p.SetReady(context.Background(), claim, cpuset.New(0, 1))
	require.NoError(t, err)
	assert.Equal(t, 2, conflictCount, "should have retried exactly twice before succeeding")

	updated, err := fakeClient.ResourceV1().ResourceClaims(testClaimNamespace).Get(context.Background(), testClaimName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, updated.Status.Devices, 1)
	assert.Equal(t, metav1.ConditionTrue, updated.Status.Devices[0].Conditions[0].Status)
}

// TestSetDriverDeviceStatus_PreservesOtherDrivers verifies that setDriverDeviceStatus
// does not touch entries owned by other drivers.
func TestSetDriverDeviceStatus_PreservesOtherDrivers(t *testing.T) {
	claim := &resourceapi.ResourceClaim{
		Status: resourceapi.ResourceClaimStatus{
			Devices: []resourceapi.AllocatedDeviceStatus{
				{Driver: "other-driver", Pool: "p", Device: "d", Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}},
				{Driver: testPublisherDriverName, Pool: testPool, Device: testDevice},
			},
		},
	}

	newEntries := []resourceapi.AllocatedDeviceStatus{
		{Driver: testPublisherDriverName, Pool: testPool, Device: testDevice, Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse}}},
	}
	setDriverDeviceStatus(claim, testPublisherDriverName, newEntries)

	require.Len(t, claim.Status.Devices, 2)
	var otherEntry, ownEntry resourceapi.AllocatedDeviceStatus
	for _, ds := range claim.Status.Devices {
		if ds.Driver == "other-driver" {
			otherEntry = ds
		} else {
			ownEntry = ds
		}
	}
	assert.Equal(t, metav1.ConditionTrue, otherEntry.Conditions[0].Status, "other driver entry should be untouched")
	assert.Equal(t, metav1.ConditionFalse, ownEntry.Conditions[0].Status, "own entry should be updated")
}
