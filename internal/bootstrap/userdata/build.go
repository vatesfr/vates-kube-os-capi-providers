/*
Copyright 2026.

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

// Package userdata turns a VatesBootstrapConfig plus its owner Cluster and
// Machine into the vates-node.yaml user-data document a Vates Kube OS node
// reads at first boot.
//
// The document is built through github.com/vatesfr/vates-kube-os/vatescfg, the
// schema package the OS itself decodes, so the provider cannot emit a document
// the node would reject. Everything the provider can derive from the CAPI
// objects (the role, the Kubernetes version, the control plane endpoint) is
// derived; the rest comes from the VatesBootstrapConfig.
package userdata

import (
	"fmt"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/vatesfr/vates-kube-os/vatescfg"

	bootstrapv1 "github.com/vatesfr/vates-kube-os-capi-providers/api/bootstrap/v1alpha1"
)

// Assemble builds the vatescfg.Config for a Machine without validating it. The
// caller fills in what only it can know -- the PKI, and the join material a
// joining machine fetches from a live control plane -- before encoding, which is
// where Validate runs. Use Build when nothing more will be added.
func Assemble(cfg *bootstrapv1.VatesBootstrapConfig, cluster *clusterv1.Cluster, machine *clusterv1.Machine) (*vatescfg.Config, error) {
	if cfg == nil {
		return nil, fmt.Errorf("VatesBootstrapConfig is required")
	}
	if cluster == nil {
		return nil, fmt.Errorf("owner Cluster is required")
	}

	spec := cfg.Spec

	out := &vatescfg.Config{
		Role:       Role(machine),
		Kubernetes: vatescfg.Kubernetes{Version: kubernetesVersion(spec, machine, cluster)},
		Cluster: vatescfg.Cluster{
			ControlPlaneEndpoint: controlPlaneEndpoint(spec, cluster),
			Token:                spec.Cluster.Token,
			CertificateKey:       spec.Cluster.CertificateKey,
			CACertHash:           spec.Cluster.CACertHash,
			ServiceCIDR:          spec.Cluster.ServiceCIDR,
			DNSDomain:            spec.Cluster.DNSDomain,
			Proxy:                vatescfg.Proxy{Disabled: proxyDisabled(spec.Cluster.Proxy)},
		},
		Network: vatescfg.Network{
			Iface:   spec.Network.Iface,
			Mode:    vatescfg.NetworkMode(spec.Network.Mode),
			CIDR:    spec.Network.CIDR,
			Gateway: spec.Network.Gateway,
			DNS:     spec.Network.DNS,
		},
		CNI: vatescfg.CNI{
			Plugin: spec.CNI.Plugin,
			CIDR:   spec.CNI.CIDR,
		},
	}

	if vip := spec.Cluster.VIP; vip != nil {
		out.Cluster.VIP = vatescfg.VIP{Address: vip.Address, Interface: vip.Interface}
	}
	// The node name is the Machine's name: the bootstrap provider knows it, and
	// stating it here is what lets a node configure on a hypervisor whose
	// config-drive meta-data carries no local-hostname (Xen Orchestra).
	if machine != nil {
		out.Node = vatescfg.Node{Name: machine.Name}
	}
	if registry := spec.Registry; registry != nil {
		out.Registry = vatescfg.Registry{Kubernetes: registry.Kubernetes}
		for _, m := range registry.Mirrors {
			out.Registry.Mirrors = append(out.Registry.Mirrors, vatescfg.RegistryMirror{Host: m.Host, Replace: m.Replace})
		}
	}
	if binaries := spec.Binaries; binaries != nil {
		out.Binaries = vatescfg.Binaries{Base: binaries.Base}
	}
	if dashboard := spec.Dashboard; dashboard != nil {
		out.Dashboard = vatescfg.Dashboard{Mode: vatescfg.DashboardMode(dashboard.Mode)}
	}
	if api := spec.API; api != nil {
		out.API = vatescfg.API{Port: api.Port}
	}
	if cloud := spec.Cloud; cloud != nil {
		out.Cloud = vatescfg.Cloud{Provider: cloud.Provider, Manifests: cloud.Manifests}
	}
	if t := spec.Time; t != nil {
		out.Time = vatescfg.Time{Servers: t.Servers}
	}

	return out, nil
}

// Build is Assemble followed by validation. It is the entry point for a document
// that is complete as built; a caller that still has to add the PKI or join
// material uses Assemble and lets the encode validate.
func Build(cfg *bootstrapv1.VatesBootstrapConfig, cluster *clusterv1.Cluster, machine *clusterv1.Machine) (*vatescfg.Config, error) {
	out, err := Assemble(cfg, cluster, machine)
	if err != nil {
		return nil, err
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

// UserData is Build followed by encoding the document as the user-data payload.
func UserData(cfg *bootstrapv1.VatesBootstrapConfig, cluster *clusterv1.Cluster, machine *clusterv1.Machine) ([]byte, error) {
	out, err := Build(cfg, cluster, machine)
	if err != nil {
		return nil, err
	}
	data, err := out.UserData()
	if err != nil {
		return nil, fmt.Errorf("encoding the configuration as user-data: %w", err)
	}
	return data, nil
}

// Role reports whether the Machine is a control plane or a worker: a control
// plane Machine carries the cluster.x-k8s.io/control-plane label.
func Role(machine *clusterv1.Machine) vatescfg.Role {
	if machine != nil {
		if _, ok := machine.Labels[clusterv1.MachineControlPlaneLabel]; ok {
			return vatescfg.RoleMaster
		}
	}
	return vatescfg.RoleWorker
}

// kubernetesVersion prefers the explicit spec field, then the owner Machine's
// version -- which the CAPI control plane provider sets from its own spec -- and
// finally the owner Cluster's topology version. It deliberately does not
// default: an empty version is a document nothing can run, and Validate names it.
func kubernetesVersion(spec bootstrapv1.VatesBootstrapConfigSpec, machine *clusterv1.Machine, cluster *clusterv1.Cluster) string {
	if spec.KubernetesVersion != "" {
		return spec.KubernetesVersion
	}
	if machine != nil && machine.Spec.Version != "" {
		return machine.Spec.Version
	}
	return cluster.Spec.Topology.Version
}

// controlPlaneEndpoint is the spec override when set, otherwise the owner
// Cluster's endpoint as host:port.
func controlPlaneEndpoint(spec bootstrapv1.VatesBootstrapConfigSpec, cluster *clusterv1.Cluster) string {
	if spec.Cluster.ControlPlaneEndpoint != "" {
		return spec.Cluster.ControlPlaneEndpoint
	}
	ep := cluster.Spec.ControlPlaneEndpoint
	if ep.Host == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", ep.Host, ep.Port)
}

func proxyDisabled(proxy *bootstrapv1.VatesProxy) bool {
	return proxy != nil && proxy.Disabled
}
