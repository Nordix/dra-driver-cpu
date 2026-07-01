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

	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/cpuset"
)

const (
	// conditionTypeReady is the condition type used to signal that the driver
	// has successfully pinned CPUs for an allocated claim.
	conditionTypeReady = "Ready"

	// reasonCPUsPinned is set when CPUs have been successfully pinned after Prepare.
	reasonCPUsPinned = "CPUsPinned"
	// reasonCPUsReleased is set when the claim has been unprepared and CPUs released.
	reasonCPUsReleased = "CPUsReleased"
)

// assignedCPUData is the driver-specific payload written to AllocatedDeviceStatus.Data.
type assignedCPUData struct {
	AssignedCPUs string `json:"assignedCPUs"`
}

// claimStatusPublisher writes AllocatedDeviceStatus entries into ResourceClaim.status.devices
// after prepare/unprepare lifecycle events.
//
// This gives operators durable, queryable visibility into which CPUs have been pinned
// to each container — filling the observability gap that Kubernetes Events cannot address
// due to their transient nature.
//
// Requires feature gate DRAResourceClaimDeviceStatus (beta in k8s v1.36).
type claimStatusPublisher struct {
	driverName string
	kubeClient kubernetes.Interface
}

// newClaimStatusPublisher creates a publisher that writes device status for driverName.
func newClaimStatusPublisher(driverName string, kubeClient kubernetes.Interface) *claimStatusPublisher {
	return &claimStatusPublisher{
		driverName: driverName,
		kubeClient: kubeClient,
	}
}

// SetReady writes Ready=True with the assigned cpuset into ResourceClaim.status.devices
// for all devices owned by this driver in the claim. Called after a successful Prepare.
//
// Returns nil without doing anything if the publisher has no kube client (e.g. in tests).
func (p *claimStatusPublisher) SetReady(ctx context.Context, claim *resourceapi.ResourceClaim, cpus cpuset.CPUSet) error {
	if p == nil || p.kubeClient == nil {
		return nil
	}
	data, err := marshalCPUData(cpus)
	if err != nil {
		return err
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Re-fetch on each retry to get the latest resourceVersion.
		current, err := p.kubeClient.ResourceV1().ResourceClaims(claim.Namespace).Get(ctx, claim.Name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get claim: %w", err)
		}

		results := allocResultsForDriver(current, p.driverName)
		if len(results) == 0 {
			// This driver has no devices in this claim; nothing to publish.
			return nil
		}

		updated := current.DeepCopy()
		now := metav1.Now()

		var newEntries []resourceapi.AllocatedDeviceStatus
		for _, result := range results {
			cond := metav1.Condition{
				Type:               conditionTypeReady,
				Status:             metav1.ConditionTrue,
				Reason:             reasonCPUsPinned,
				Message:            fmt.Sprintf("CPUs %s pinned to container", cpus.String()),
				LastTransitionTime: preserveTransitionTime(current, result.Pool, result.Device, conditionTypeReady, metav1.ConditionTrue, now),
				ObservedGeneration: current.Generation,
			}
			newEntries = append(newEntries, resourceapi.AllocatedDeviceStatus{
				Driver:     p.driverName,
				Pool:       result.Pool,
				Device:     result.Device,
				Conditions: []metav1.Condition{cond},
				Data:       &runtime.RawExtension{Raw: data},
			})
		}

		setDriverDeviceStatus(updated, p.driverName, newEntries)

		_, err = p.kubeClient.ResourceV1().ResourceClaims(updated.Namespace).UpdateStatus(ctx, updated, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) {
			return err // triggers retry
		}
		return err
	})
}

// ClearReady writes Ready=False into ResourceClaim.status.devices for all devices
// owned by this driver. Called after a successful Unprepare.
//
// Returns nil without doing anything if the publisher has no kube client (e.g. in tests).
func (p *claimStatusPublisher) ClearReady(ctx context.Context, ns, name string, uid types.UID) error {
	if p == nil || p.kubeClient == nil {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := p.kubeClient.ResourceV1().ResourceClaims(ns).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil // claim already gone, nothing to do
		}
		if err != nil {
			return fmt.Errorf("get claim: %w", err)
		}
		if current.UID != uid {
			return nil // stale reference, a different claim reused the same name
		}

		results := allocResultsForDriver(current, p.driverName)
		if len(results) == 0 {
			return nil
		}

		updated := current.DeepCopy()
		now := metav1.Now()

		var clearedEntries []resourceapi.AllocatedDeviceStatus
		for _, result := range results {
			cond := metav1.Condition{
				Type:               conditionTypeReady,
				Status:             metav1.ConditionFalse,
				Reason:             reasonCPUsReleased,
				Message:            "CPUs released, claim unprepared",
				LastTransitionTime: preserveTransitionTime(current, result.Pool, result.Device, conditionTypeReady, metav1.ConditionFalse, now),
				ObservedGeneration: current.Generation,
			}
			clearedEntries = append(clearedEntries, resourceapi.AllocatedDeviceStatus{
				Driver:     p.driverName,
				Pool:       result.Pool,
				Device:     result.Device,
				Conditions: []metav1.Condition{cond},
			})
		}

		setDriverDeviceStatus(updated, p.driverName, clearedEntries)

		_, err = p.kubeClient.ResourceV1().ResourceClaims(updated.Namespace).UpdateStatus(ctx, updated, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) {
			return err
		}
		return err
	})
}

// allocResultsForDriver returns the allocation results in the claim that belong to driverName.
func allocResultsForDriver(claim *resourceapi.ResourceClaim, driverName string) []resourceapi.DeviceRequestAllocationResult {
	if claim.Status.Allocation == nil {
		return nil
	}
	var out []resourceapi.DeviceRequestAllocationResult
	for _, r := range claim.Status.Allocation.Devices.Results {
		if r.Driver == driverName {
			out = append(out, r)
		}
	}
	return out
}

// setDriverDeviceStatus replaces all status.devices entries owned by driverName with newEntries,
// leaving entries owned by other drivers untouched.
func setDriverDeviceStatus(claim *resourceapi.ResourceClaim, driverName string, newEntries []resourceapi.AllocatedDeviceStatus) {
	var kept []resourceapi.AllocatedDeviceStatus
	for _, ds := range claim.Status.Devices {
		if ds.Driver != driverName {
			kept = append(kept, ds)
		}
	}
	claim.Status.Devices = append(kept, newEntries...)
}

// preserveTransitionTime returns the existing LastTransitionTime for the given condition on a device
// if the condition status is unchanged, otherwise returns now. This follows the Kubernetes convention
// of only updating LastTransitionTime when the Status actually changes.
func preserveTransitionTime(
	claim *resourceapi.ResourceClaim,
	pool, device, condType string,
	newStatus metav1.ConditionStatus,
	now metav1.Time,
) metav1.Time {
	for _, ds := range claim.Status.Devices {
		if ds.Pool != pool || ds.Device != device {
			continue
		}
		for _, c := range ds.Conditions {
			if c.Type == condType && c.Status == newStatus {
				return c.LastTransitionTime
			}
		}
	}
	return now
}

func marshalCPUData(cpus cpuset.CPUSet) ([]byte, error) {
	return json.Marshal(assignedCPUData{AssignedCPUs: cpus.String()})
}
