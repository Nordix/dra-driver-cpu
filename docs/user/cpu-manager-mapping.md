# Matching CPU Manager Options

The kubelet cpumanager supports [options](https://kubernetes.io/docs/tasks/administer-cluster/cpu-management-policies/#cpu-policy-static--options) to fine-tune the CPU allocation behavior.
This DRA driver aims to implement feature parity with the kubelet cpumanager. The following table summarizes how you can achieve a cpumanager functionality controlled by a cpumanager policy option.
Reference: [kubernetes 1.35.0](https://github.com/kubernetes/kubernetes/blob/v1.35.0/pkg/kubelet/cm/cpumanager/policy_options.go).

| CPU Manager Option        | Maturity | Kubelet development status | Driver equivalent functionality                                        | notes                 |
| ------------------------- | -------- | -------------------------- | ---------------------------------------------------------------------- | --------------------- |
| AlignBySocket             | alpha    | inactive                   | `--grouped-mode` driver option                                         |                       |
| DistributeCPUsAcrossCores | alpha    | inactive                   | none yet; postponed till k8s feature graduates to beta                 |                       |
| DistributeCPUsAcrossNUMA  | beta     | active                     | see issue: https://github.com/kubernetes-sigs/dra-driver-cpu/issues/46 | see below for details |
| PreferAlignByUnCoreCache  | beta     | active                     | builtin; enabled by default                                            |                       |
| FullPCPUsOnly             | GA       | N/A                        | see issue: https://github.com/kubernetes-sigs/dra-driver-cpu/issues/45 |                       |
| StrictCPUReservation      | GA       | N/A                        | builtin; enabled by default                                            |                       |

## Distributing CPUs across NUMA nodes

It is currently possible to encode a split of CPUs in such a way the allocator picks them from different NUMA nodes. Example:

```
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: claim-cpu-capacity-20
spec:
  devices:
    requests:
    - name: numa0-cpus
      exactly:
        deviceClassName: dra.cpu
        capacity:
          requests:
            dra.cpu/cpu: "10"
        selectors:
        - cel:
            expression: device.attributes["dra.cpu"].numaNodeID == 0
    - name: numa1-cpus
      exactly:
        deviceClassName: dra.cpu
        capacity:
          requests:
            dra.cpu/cpu: "10"
        selectors:
        - cel:
            expression: device.attributes["dra.cpu"].numaNodeID ==1
```

However, this is only a partial replacement of the corresponding CPU Manager option. The main problem of this approach is that it leaks assumptions about machine properties.
We hardcode the NUMA split and, unlike the cpumanager feature, it won't automatically adapt if the same claim is handled by a 1-NUMA, 2-NUMA or 4-NUMA machine;
the claim would need to be updated or recreated manually.
