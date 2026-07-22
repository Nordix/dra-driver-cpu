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
	"context"
	"time"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/ctxlog"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/utils/cpuset"
)

// healthResendInterval is how often the driver resends the full device
// health snapshot to the kubelet. It must be shorter than the kubelet'sq
// default defaultKubeletHealthTimeout(30s), otherwise the kubelet will
// treat devices as stale and report them as unknown between updates.
var healthResendInterval = 10 * time.Second

// setDeviceHealth records the health of a single device and if it changed,
// notifies any active WatchHealthStatus callers. deviceName must be a key
// already known from device enumeration. Unknown devices are ignored so a
// caller passing through a claim's allocation results cannot populate the
// health map with devices from a different driver/pool.
func (cp *CPUDriver) setDeviceHealth(logger logr.Logger, deviceName string, status kubeletplugin.HealthStatus, message string) {
	cp.healthMu.Lock()
	entry, known := cp.deviceHealth[deviceName]
	if !known {
		cp.healthMu.Unlock()
		return
	}
	changed := entry.status != status || entry.message != message
	entry.status = status
	entry.message = message
	cp.healthMu.Unlock()

	if changed {
		logger.V(2).Info("device health changed", "device", deviceName, "health", status, "message", message)
		cp.notifyHealthClients()
	}
}

// markCPUSetDevicesHealth marks the individual CPU devices backing cpus with
// the given health status. Only meaningful in ungrouped device mode, where
// each CPU maps to exactly one device (cpuIDToDeviceName). In grouped mode a
// single device can cover many CPUs shared across claims and thus attributing a
// container level failure to one specific device is not safe and is left as
// future work.
func (cp *CPUDriver) markCPUSetDevicesHealth(logger logr.Logger, cpus cpuset.CPUSet, status kubeletplugin.HealthStatus, message string) {
	if len(cp.cpuIDToDeviceName) == 0 {
		return
	}
	for _, cpuID := range cpus.List() {
		if name, ok := cp.cpuIDToDeviceName[cpuID]; ok {
			cp.setDeviceHealth(logger, name, status, message)
		}
	}
}

// markClaimDevicesHealth updates the health of every device backing claim
// that belongs to this driver. Used for failures which are not specific to a
// single device, such as a shared CDI spec write covering the whole claim.
func (cp *CPUDriver) markClaimDevicesHealth(logger logr.Logger, claim *resourceapi.ResourceClaim, status kubeletplugin.HealthStatus, message string) {
	if claim.Status.Allocation == nil {
		return
	}
	for _, allocResult := range claim.Status.Allocation.Devices.Results {
		if allocResult.Driver != cp.driverName {
			continue
		}
		cp.setDeviceHealth(logger, allocResult.Device, status, message)
	}
}

// buildHealthReport snapshots the health of every device this driver
// manages. All devices are published under a single pool named after this
// node (see PublishResources), so PoolName is always cp.nodeName.
func (cp *CPUDriver) buildHealthReport() kubeletplugin.DeviceHealthReport {
	cp.healthMu.RLock()
	defer cp.healthMu.RUnlock()

	devices := make([]kubeletplugin.DeviceHealth, 0, len(cp.deviceHealth))
	for name, entry := range cp.deviceHealth {
		devices = append(devices, kubeletplugin.DeviceHealth{
			PoolName:           cp.nodeName,
			DeviceName:         name,
			Health:             entry.status,
			LastUpdated:        time.Now(),
			HealthCheckTimeout: 2 * healthResendInterval,
			Message:            entry.message,
		})
	}
	return kubeletplugin.DeviceHealthReport{Devices: devices}
}

// notifyHealthClients pushes the current health snapshot to every active
// WatchHealthStatus subscriber. Sends are best-effort. A subscriber whose
// channel is full will simply pick up the next periodic resend instead of
// blocking the caller that triggered the change.
func (cp *CPUDriver) notifyHealthClients() {
	report := cp.buildHealthReport()

	cp.clientsMu.RLock()
	defer cp.clientsMu.RUnlock()
	for _, ch := range cp.healthClients {
		select {
		case ch <- report:
		default:
		}
	}
}

// healthResendLoop periodically resends the full health snapshot so that
// the kubelet's per-device lease (HealthCheckTimeout) never expires while
// the driver is healthy and simply has nothing new to report. WatchHealthStatus
// deliberately does not resend on its own: a wedged driver should decay to
// Unknown instead of being kept alive artificially.
func (cp *CPUDriver) healthResendLoop(ctx context.Context) {
	defer cp.healthWg.Done()

	ticker := time.NewTicker(healthResendInterval)
	defer ticker.Stop()

	for {
		select {
		case <-cp.stopHealthCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			cp.notifyHealthClients()
		}
	}
}

// WatchHealthStatus implements kubeletplugin.DRAPlugin. The kubeletplugin
// helper calls it whenever the kubelet subscribes to device health updates and
// takes care of translating the reports produced here into whatever DRAResourceHealth
// gRPC API version the kubelet supports.
func (cp *CPUDriver) WatchHealthStatus(ctx context.Context, reports chan<- kubeletplugin.DeviceHealthReport) error {
	_, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen))
	logger.V(2).Info("kubelet subscribed to device health updates")
	defer logger.V(2).Info("kubelet unsubscribed from device health updates")

	// Buffered so a burst of setDeviceHealth calls doesn't block on a slow
	// consumer. notifyHealthClients drops reports rather than blocking.
	clientCh := make(chan kubeletplugin.DeviceHealthReport, 4)
	cp.clientsMu.Lock()
	cp.healthClients = append(cp.healthClients, clientCh)
	cp.clientsMu.Unlock()

	defer func() {
		cp.clientsMu.Lock()
		for i, ch := range cp.healthClients {
			if ch == clientCh {
				cp.healthClients = append(cp.healthClients[:i], cp.healthClients[i+1:]...)
				break
			}
		}
		cp.clientsMu.Unlock()
	}()

	select {
	case <-ctx.Done():
		return nil
	case <-cp.stopHealthCh:
		return nil
	case reports <- cp.buildHealthReport():
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-cp.stopHealthCh:
			return nil
		case report := <-clientCh:
			select {
			case <-ctx.Done():
				return nil
			case <-cp.stopHealthCh:
				return nil
			case reports <- report:
			}
		}
	}
}
