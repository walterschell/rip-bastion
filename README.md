# rip-bastion
Raspberry Pi Based VPN Endpoint

## Example Display

![Example render of the rip-bastion display](docs/example-render.png)

## Built-in Proxy

`rip-bastion` includes a built-in reverse proxy with:

- YAML configuration
- File-watch hot reload
- Host/SNI-based routing on HTTPS
- HTTP to HTTPS redirect

### Generate placeholder config

Write a documented starter config to the default location:

`rip-bastion --write-proxy-config`

Use a custom location:

`rip-bastion --write-proxy-config --proxy-config /path/to/proxy.yaml`

Overwrite an existing file:

`rip-bastion --write-proxy-config --overwrite-proxy-config`

The command writes the file and prints the resulting config to stdout.

### Default locations

- Proxy config: `/etc/rip-bastion/proxy.yaml`
- Self-signed cert store: `/var/lib/rip-bastion/proxy-certs`

### Hot reload

The proxy watches the config file and applies route/TLS changes automatically
without restarting the service.

### Stable self-signed certificates

When `tls.mode: self-signed`, certificates are generated per hostname and
persisted in `tls.cert_dir` so they remain stable across service restarts and
system reboots.

### Services section on display

Configured proxy routes are shown in the display Services section alongside
other service status entries.
