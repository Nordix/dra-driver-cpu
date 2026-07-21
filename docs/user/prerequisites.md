# Prerequisites

The driver relies on [NRI (Node Resource Interface)](https://github.com/containerd/nri) to pin containers to their
allocated CPUs, and on [CDI (Container Device Interface)](https://github.com/cncf-tags/container-device-interface) to
inject the allocated cpuset into the container environment.

## Minimum Runtime Requirements

Both NRI and CDI are enabled by default in modern container runtimes:

| Runtime    | NRI enabled by default | CDI enabled by default |
| ---------- | ---------------------- | ---------------------- |
| containerd | 2.0+                   | 2.0+                   |
| CRI-O      | 1.30+                  | always                 |

Both runtimes also ship with the following CDI spec directories configured by default:

```toml
cdi_spec_dirs = ["/etc/cdi", "/var/run/cdi"]
```

No manual runtime configuration is needed if you are running one of the versions above or newer.

## Manual Configuration for Older Runtimes

If you are running an older version of containerd (pre-2.0), you need to manually enable CDI and NRI in the containerd
configuration (typically `/etc/containerd/config.toml`) and restart containerd.

### Enable CDI

```toml
[plugins."io.containerd.grpc.v1.cri"]
  enable_cdi = true
  cdi_spec_dirs = ["/etc/cdi", "/var/run/cdi"]
```

### Enable NRI

```toml
[plugins."io.containerd.nri.v1.nri"]
  disable = false
  disable_connections = false
  plugin_config_path = "/etc/nri/conf.d"
  plugin_path = "/opt/nri/plugins"
  plugin_registration_timeout = "5s"
  plugin_request_timeout = "5s"
  socket_path = "/var/run/nri/nri.sock"
```

After editing the config, restart containerd:

```bash
systemctl restart containerd
```
