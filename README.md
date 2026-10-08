# vates-kube-os-capi-providers

The Cluster API providers for **[Vates Kube OS](https://github.com/vatesfr/vates-kube-os)**:
a **bootstrap** provider and a **control plane** provider, in one repository —
the model upstream [Cluster API](https://cluster-api.sigs.k8s.io/) uses for its
kubeadm providers (`bootstrap/kubeadm` + `controlplane/kubeadm`).

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

## Build

```bash
make build    # bin/bootstrap-manager, bin/controlplane-manager
make test
make manifests generate
```

## License

Copyright 2026. Licensed under the Apache License, Version 2.0.
