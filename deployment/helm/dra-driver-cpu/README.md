# dra-driver-cpu Helm Chart

Deploys the [dra-driver-cpu](https://github.com/kubernetes-sigs/dra-driver-cpu) DaemonSet — a Kubernetes Dynamic Resource Allocation (DRA) driver for managing CPU resources.

## Installation

From a local checkout:

```bash
helm install dra-driver-cpu ./deployment/helm/dra-driver-cpu -n kube-system
```

To override values at install time:

```bash
helm install dra-driver-cpu ./deployment/helm/dra-driver-cpu -n kube-system \
  --set args.cpuDeviceMode=individual \
  --set args.reservedCPUs="0-1"
```

## Configuration

The following table lists the configurable parameters and their default values:

| Parameter | Description | Default |
|---|---|---|
| `nameOverride` | Override the chart name | `""` |
| `fullnameOverride` | Override the full release name | `""` |
| `image.repository` | Container image repository | `us-central1-docker.pkg.dev/k8s-staging-images/dra-driver-cpu/dra-driver-cpu` |
| `image.tag` | Image tag (falls back to `Chart.AppVersion` if empty) | `latest` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `imagePullSecrets` | List of image pull secrets | `[]` |
| `rbac.create` | Create RBAC resources (ClusterRole and ClusterRoleBinding) | `true` |
| `serviceAccount.annotations` | Annotations to add to the ServiceAccount | `{}` |
| `podAnnotations` | Annotations to add to pods | `{}` |
| `podLabels` | Extra labels to add to pods | `{}` |
| `resources.requests.cpu` | CPU resource request | `100m` |
| `resources.requests.memory` | Memory resource request | `50Mi` |
| `resources.limits` | Resource limits (unset by default) | `{}` |
| `tolerations` | Node tolerations | control-plane NoSchedule tolerated |
| `args.logLevel` | Log verbosity (`--v`) | `4` |
| `args.cpuDeviceMode` | CPU exposure mode: `grouped` or `individual` | `grouped` |
| `args.reservedCPUs` | CPUs reserved for system/kubelet (e.g. `0-1`). Omitted if empty. | `""` |
| `livenessProbe` | Liveness probe configuration | httpGet /healthz, initialDelaySeconds 10 |
| `readinessProbe` | Readiness probe configuration | httpGet /healthz, initialDelaySeconds 5 |

Parameters can be set at install time using `--set` or a custom values file:

```bash
helm install dra-driver-cpu ./deployment/helm/dra-driver-cpu -n kube-system --set args.logLevel=6
helm install dra-driver-cpu ./deployment/helm/dra-driver-cpu -n kube-system -f my-values.yaml
```

## CPU Device Modes

**`grouped` (default)** — exposes CPUs as consumable capacity within a group (NUMA node or socket). Better scalability; suited for most workloads.

**`individual`** — exposes each CPU as a separate device. Fine-grained control; suited for HPC or performance-critical workloads.

## Reserving CPUs

Use `args.reservedCPUs` to exclude CPUs from DRA allocation, matching the kubelet's `reservedSystemCPUs` setting. The value should equal the sum of the kubelet's `kubeReserved` and `systemReserved` CPU counts.

```bash
helm install dra-driver-cpu ./deployment/helm/dra-driver-cpu -n kube-system --set args.reservedCPUs="0-1"
```

## Uninstallation

```bash
helm uninstall dra-driver-cpu -n kube-system
```
