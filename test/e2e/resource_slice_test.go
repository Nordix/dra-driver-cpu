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

package e2e

import (
	"context"
	"strconv"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/driver"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
)

const (
	driverName = "dra.cpu"
)

var _ = ginkgo.Describe("Resource Attributes", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		fxt               *fixture.Fixture
		cpuDeviceMode     string
		forcePCIeRootList bool
		slices            []resourcev1.ResourceSlice
	)

	ginkgo.BeforeAll(func(ctx context.Context) {
		var err error
		fxt, err = fixture.ForGinkgo()
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create fixture")

		ginkgo.By("reading daemonset configuration")
		daemonSet, err := fxt.K8SClientset.AppsV1().DaemonSets(daemonSetNamespace).Get(ctx, "dracpu", metav1.GetOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get dracpu daemonset")
		gomega.Expect(daemonSet.Spec.Template.Spec.Containers).ToNot(gomega.BeEmpty())

		cnt := &daemonSet.Spec.Template.Spec.Containers[0]
		if val, ok := findArgInContainer(cnt, argCPUDeviceMode); ok {
			cpuDeviceMode = val
		}
		if envVar, ok := findEnvInContainer(cnt, driver.EnvVarForcePCIeRootList); ok {
			val, err := strconv.ParseBool(envVar)
			gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get dracpu pcie roots setting")
			forcePCIeRootList = val
		}
		fxt.Log.Info("daemonset configuration", "cpuDeviceMode", cpuDeviceMode, "forcePCIeRootList", forcePCIeRootList)

		ginkgo.By("listing ResourceSlices for driver " + driverName)
		sliceList, err := fxt.K8SClientset.ResourceV1().ResourceSlices().List(ctx, metav1.ListOptions{
			FieldSelector: "spec.driver=" + driverName,
		})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot list ResourceSlices")
		gomega.Expect(sliceList.Items).ToNot(gomega.BeEmpty(), "no ResourceSlices found for driver %s", driverName)
		slices = sliceList.Items
		fxt.Log.Info("found ResourceSlices", "count", len(slices))
	})

	ginkgo.It("should have devices in ResourceSlices", func() {
		totalDevices := 0
		for _, slice := range slices {
			totalDevices += len(slice.Spec.Devices)
		}
		gomega.Expect(totalDevices).To(gomega.BeNumerically(">", 0), "no devices found across all ResourceSlices")
	})

	ginkgo.It("should have correct base attributes on every device", func() {
		type attrCheck struct {
			name    resourcev1.QualifiedName
			checker func(resourcev1.DeviceAttribute) bool
		}

		isInt := func(a resourcev1.DeviceAttribute) bool { return a.IntValue != nil }
		isBool := func(a resourcev1.DeviceAttribute) bool { return a.BoolValue != nil }
		isString := func(a resourcev1.DeviceAttribute) bool { return a.StringValue != nil }

		var checks []attrCheck
		switch cpuDeviceMode {
		case driver.CPU_DEVICE_MODE_INDIVIDUAL:
			checks = []attrCheck{
				{driver.AttributeNUMANodeID, isInt},
				{driver.AttributeSocketID, isInt},
				{driver.AttributeSMTEnabled, isBool},
				{driver.AttributeCacheL3ID, isInt},
				{driver.AttributeCoreType, isString},
				{driver.AttributeCoreID, isInt},
				{driver.AttributeCPUID, isInt},
			}
		default:
			checks = []attrCheck{
				{driver.AttributeSocketID, isInt},
				{driver.AttributeSMTEnabled, isBool},
				{driver.AttributeNumCPUs, isInt},
			}
		}

		for _, slice := range slices {
			for _, dev := range slice.Spec.Devices {
				for _, check := range checks {
					attr, ok := dev.Attributes[check.name]
					gomega.Expect(ok).To(gomega.BeTrue(),
						"device %q in slice %q missing attribute %s", dev.Name, slice.Name, check.name)
					gomega.Expect(check.checker(attr)).To(gomega.BeTrue(),
						"device %q in slice %q attribute %s has wrong type", dev.Name, slice.Name, check.name)
				}
			}
		}
	})

	ginkgo.It("should have valid PCIe root attributes when present", func() {
		devicesWithPCIeRoots := 0

		for _, slice := range slices {
			for _, dev := range slice.Spec.Devices {
				_, hasPCIeRoots := dev.Attributes[driver.AttributePCIeRoots]
				if !hasPCIeRoots {
					continue
				}
				devicesWithPCIeRoots++
			}
		}

		if devicesWithPCIeRoots == 0 {
			// Skip is more explicit than passing the tests doing nothing.
			// It's the strongest signal we get until we find a way to inspect the system before
			// the suite runs and set expectations accordingly.
			ginkgo.Skip("No PCIe roots reported on this system")
		}

		for _, slice := range slices {
			for _, dev := range slice.Spec.Devices {
				pcieRoots, ok := dev.Attributes[driver.AttributePCIeRoots]
				if !ok {
					// still, not all devices expose this attribute, and that's correct
					continue
				}
				gomega.Expect(pcieRoots.StringValues).ToNot(gomega.BeEmpty(),
					"device %q in slice %q has dra.cpu/pcieRoots but StringValues is empty", dev.Name, slice.Name)

				if !forcePCIeRootList {
					continue
				}

				stdAttr, hasStd := dev.Attributes[deviceattribute.StandardDeviceAttributePCIeRoot]
				gomega.Expect(hasStd).To(gomega.BeTrue(),
					"device %q in slice %q has dra.cpu/pcieRoots but missing standard resource.kubernetes.io/pcieRoot (forcePCIeRootList is enabled)",
					dev.Name, slice.Name)
				gomega.Expect(stdAttr.StringValues).ToNot(gomega.BeEmpty(),
					"device %q in slice %q has resource.kubernetes.io/pcieRoot but StringValues is empty", dev.Name, slice.Name)
			}
		}
	})

	ginkgo.It("should have consistent PCIe root attributes across devices", func() {
		if !forcePCIeRootList {
			ginkgo.Skip("this tests requires PCIe root list (DRAListTypeAttributes=true)")
		}
		fixture.By("verifying that dra.cpu/pcieRoots and resource.kubernetes.io/pcieRoot are set on the same devices")
		for _, slice := range slices {
			for _, dev := range slice.Spec.Devices {
				_, hasCPU := dev.Attributes[driver.AttributePCIeRoots]
				_, hasStd := dev.Attributes[deviceattribute.StandardDeviceAttributePCIeRoot]
				gomega.Expect(hasCPU).To(gomega.Equal(hasStd),
					"device %q in slice %q: dra.cpu/pcieRoots present=%v but resource.kubernetes.io/pcieRoot present=%v",
					dev.Name, slice.Name, hasCPU, hasStd)
			}
		}
	})
})
