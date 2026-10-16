# vates-kube-os-capi-providers

The Cluster API providers for **[Vates Kube OS](https://github.com/vatesfr/vates-kube-os)**:
a **bootstrap** provider and a **control plane** provider, in one repository —
the model upstream [Cluster API](https://cluster-api.sigs.k8s.io/) uses for its
kubeadm providers (`bootstrap/kubeadm` + `controlplane/kubeadm`).

> [!WARNING]
> **Experimental — no support, not for production.**
> These providers are a work in progress: they are **not supported** by Vates,
> and they are **not recommended for production use**. Expect breaking changes
> and data loss; evaluate them at your own risk.

They are two **providers** to Cluster API (and `clusterctl`), shipped from one
module:

| provider | CRDs | owns |
|---|---|---|
| **CAVBP** (bootstrap) | `VatesBootstrapConfig` (+ `Template`) | turns a Machine into the `vates-node.yaml` user-data the node reads at first boot (role, Kubernetes version, endpoint, node name, PKI, join material) |
| **CAVCPP** (control plane) | `VatesControlPlane` (+ `Template`) | the cluster certificate authority, and the control plane Machines |

The infrastructure half lives in
[`cluster-api-provider-vates`](https://github.com/vatesfr/cluster-api-provider-vates)
(Xen Orchestra): it makes the VM and attaches the config drive.

## Layout

```
api/bootstrap/v1alpha1/       VatesBootstrapConfig types
api/controlplane/v1alpha1/    VatesControlPlane types
internal/bootstrap/           the bootstrap reconciler + document builder + pki + management-API client
internal/controlplane/        the control plane reconciler + cluster CA
cmd/bootstrap/                the bootstrap manager
cmd/controlplane/             the control plane manager
config/bootstrap/             bootstrap CRDs, RBAC, manager, clusterctl metadata
config/controlplane/          control plane CRDs, RBAC, manager, clusterctl metadata
```

## The split

The control plane provider **designates** the machine that creates the cluster
(annotation `vates.io/bootstrap`); the bootstrap provider obeys it. So all
control plane replicas can be created at once, as KCP does. The cluster CA is
owned by the control plane provider and injected into the bootstrapping node;
joining machines fetch their credentials from a live control plane's management
API.

## Install with clusterctl

`clusterctl` sees **two** providers here. Build the artifacts and register both
with a `file://` URL (a release repository does the same over https):

```bash
make release-manifests IMG=ghcr.io/vatesfr/vates-kube-os-capi-providers:v0.1.0
# clusterctl reads each provider's metadata from its own override directory:
for t in bootstrap control-plane; do
  d=~/.config/cluster-api/overrides/$t-vates/v0.1.0
  mkdir -p "$d" && cp dist/metadata.yaml "$d/"
done
```

Then, in `~/.config/cluster-api/clusterctl.yaml`:

```yaml
providers:
  - name: vates
    type: BootstrapProvider
    url: file:///ABS/PATH/CAVP/main/dist/bootstrap-components.yaml
  - name: vates
    type: ControlPlaneProvider
    url: file:///ABS/PATH/CAVP/main/dist/control-plane-components.yaml
  - name: vates
    type: InfrastructureProvider
    url: file:///ABS/PATH/CAPI/dist/infrastructure-components.yaml
```

```bash
clusterctl init --bootstrap vates:v0.1.0 --control-plane vates:v0.1.0 --infrastructure vates:v0.1.0
```

The two managers run the **same image** with different `command`s
(`/bootstrap-manager`, `/controlplane-manager`); both land in `cavp-system`.

## Build

```bash
make build    # bin/bootstrap-manager, bin/controlplane-manager
make test
make manifests generate
```

## License

Copyright 2026. Licensed under the Apache License, Version 2.0.
