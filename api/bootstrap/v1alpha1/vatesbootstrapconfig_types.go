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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VatesBootstrapConfigSpec is the desired configuration of a Vates Kube OS
// machine. It is the CAPI-facing shape of the vates-node.yaml document the node
// reads at first boot: the provider fills in what it can derive from the owner
// Cluster and Machine (role, Kubernetes version, control plane endpoint) and
// renders the rest from here.
//
// The fields mirror github.com/vatesfr/vates-kube-os/vatescfg one for one. That
// package stays the single source of truth for the semantics and the validation:
// this spec is encoded into a vatescfg.Config and validated by it before a node
// ever sees the document.
type VatesBootstrapConfigSpec struct {
	// KubernetesVersion is the Kubernetes release to run, e.g. "v1.31.0".
	// Optional: when empty the owner Cluster's spec.topology.version is used.
	// +optional
	KubernetesVersion string `json:"kubernetesVersion,omitempty"`

	// Cluster carries the cluster-level settings that cannot be derived from
	// the CAPI objects.
	// +optional
	Cluster VatesCluster `json:"cluster,omitempty"`

	// Network is the node's own addressing.
	Network VatesNetwork `json:"network"`

	// CNI selects the pod network.
	CNI VatesCNI `json:"cni"`

	// Registry says where this node's container images come from.
	// +optional
	Registry *VatesRegistry `json:"registry,omitempty"`

	// Binaries says where the Kubernetes binaries come from.
	// +optional
	Binaries *VatesBinaries `json:"binaries,omitempty"`

	// Dashboard is what the machine shows on its console.
	// +optional
	Dashboard *VatesDashboard `json:"dashboard,omitempty"`

	// API configures the node's management API.
	// +optional
	API *VatesAPI `json:"api,omitempty"`

	// Cloud is the node's relationship with an external cloud provider.
	// +optional
	Cloud *VatesCloud `json:"cloud,omitempty"`

	// Time is how the node keeps its clock.
	// +optional
	Time *VatesTime `json:"time,omitempty"`
}

// VatesCluster carries the cluster-level settings, and the join material a
// joining machine needs.
type VatesCluster struct {
	// ControlPlaneEndpoint overrides the owner Cluster's
	// spec.controlPlaneEndpoint. Optional; normally left empty so the endpoint
	// comes from the Cluster.
	// +optional
	ControlPlaneEndpoint string `json:"controlPlaneEndpoint,omitempty"`

	// Token is the kubeadm bootstrap token, "abcdef.0123456789abcdef". Required
	// for a worker, which uses it to join. Optional on a master, which creates
	// the cluster instead of joining it.
	// +optional
	Token string `json:"token,omitempty"`

	// CertificateKey is kubeadm's --certificate-key, for a joining control
	// plane. It expires (kubeadm gives it two hours), so the provider must fetch
	// it when the machine is about to be created, never store it.
	// +optional
	CertificateKey string `json:"certificateKey,omitempty"`

	// CACertHash is the sha256 of the cluster's CA certificate. Required with
	// certificateKey.
	// +optional
	CACertHash string `json:"caCertHash,omitempty"`

	// VIP makes this control plane node participate in owning a virtual IP.
	// +optional
	VIP *VatesVIP `json:"vip,omitempty"`

	// ServiceCIDR is the range Service addresses are allocated from. Optional;
	// defaults to kubeadm's 10.96.0.0/12.
	// +optional
	ServiceCIDR string `json:"serviceCIDR,omitempty"`

	// DNSDomain is the cluster's DNS domain. Optional; defaults to
	// "cluster.local".
	// +optional
	DNSDomain string `json:"dnsDomain,omitempty"`

	// Proxy controls kube-proxy, which kubeadm installs with its addons.
	// +optional
	Proxy *VatesProxy `json:"proxy,omitempty"`
}

// VatesVIP is the floating control plane address, as provided by kube-vip.
// It is only valid on a control plane machine.
type VatesVIP struct {
	// Address is the virtual address to own, e.g. "192.168.1.10".
	// +optional
	Address string `json:"address,omitempty"`

	// Interface is the interface to announce on. Optional; defaults to the
	// node's network interface.
	// +optional
	Interface string `json:"interface,omitempty"`
}

// VatesProxy controls kube-proxy.
type VatesProxy struct {
	// Disabled omits kube-proxy, for a CNI that replaces it in eBPF. It is only
	// valid with cni.plugin: none: flannel and cilium, as the node installs
	// them, run alongside kube-proxy and do not replace it.
	// +optional
	Disabled bool `json:"disabled,omitempty"`
}

// VatesNetwork is the node's own addressing.
type VatesNetwork struct {
	// Iface is the network interface to configure, e.g. "eth0".
	Iface string `json:"iface"`

	// Mode is "dhcp" or "static".
	// +kubebuilder:validation:Enum=dhcp;static
	Mode string `json:"mode"`

	// CIDR is the node address in CIDR form. Required when mode is static.
	// +optional
	CIDR string `json:"cidr,omitempty"`

	// Gateway is the default route. Required when mode is static.
	// +optional
	Gateway string `json:"gateway,omitempty"`

	// DNS is an optional list of resolvers.
	// +optional
	DNS []string `json:"dns,omitempty"`
}

// VatesCNI selects the pod network.
type VatesCNI struct {
	// Plugin is the CNI implementation. Optional; defaults to "flannel". Both
	// "flannel" and "cilium" are installed by the node itself -- an embedded,
	// pinned manifest the node applies at first boot -- and both run alongside
	// kube-proxy. "none" leaves the CNI to the operator, which is how a CNI
	// that replaces kube-proxy is installed.
	// +optional
	// +kubebuilder:validation:Enum=flannel;cilium;none
	Plugin string `json:"plugin,omitempty"`

	// CIDR is the pod network, e.g. "10.244.0.0/16". Required.
	CIDR string `json:"cidr"`
}

// VatesRegistry says where this node's container images come from.
type VatesRegistry struct {
	// Kubernetes is the base for the cluster's own images. Optional; empty
	// means registry.k8s.io.
	// +optional
	Kubernetes string `json:"kubernetes,omitempty"`

	// Mirrors rewrite the other images.
	// +optional
	Mirrors []VatesRegistryMirror `json:"mirrors,omitempty"`
}

// VatesRegistryMirror redirects one upstream host to a mirror.
type VatesRegistryMirror struct {
	// Host is the upstream registry, e.g. "ghcr.io".
	Host string `json:"host"`

	// Replace is where that host's images are found instead, without a scheme.
	Replace string `json:"replace"`
}

// VatesBinaries says where the Kubernetes binaries come from.
type VatesBinaries struct {
	// Base is the prefix the Kubernetes layout hangs under. Optional; empty
	// means https://dl.k8s.io/release.
	// +optional
	Base string `json:"base,omitempty"`
}

// VatesDashboard is what the machine shows on its console.
type VatesDashboard struct {
	// Mode is "tui" or "gui". Optional; defaults to "gui".
	// +kubebuilder:validation:Enum=tui;gui
	// +optional
	Mode string `json:"mode,omitempty"`
}

// VatesAPI configures the node's management API.
type VatesAPI struct {
	// Port is the TCP port the management API listens on. Optional; defaults
	// to 50000.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int `json:"port,omitempty"`
}

// VatesCloud is the node's relationship with an external cloud provider.
type VatesCloud struct {
	// Provider is the external provider the kubelet defers to. Only "external"
	// is accepted.
	// +kubebuilder:validation:Enum=external
	// +optional
	Provider string `json:"provider,omitempty"`

	// Manifests are applied by the bootstrap once the kubelet is up. Each entry
	// is an http(s) URL.
	// +optional
	Manifests []string `json:"manifests,omitempty"`
}

// VatesTime is how the node keeps its clock.
type VatesTime struct {
	// Servers are the NTP servers to synchronize from. Optional; falls back to
	// the public pool.
	// +optional
	Servers []string `json:"servers,omitempty"`
}

// VatesBootstrapConfigStatus is the state of the bootstrap, as observed by the
// provider. The provider sets DataSecretName once the document is written; under
// the Cluster API contract the Machine controller reads status.dataSecretName
// and status.initialization.dataSecretCreated.
type VatesBootstrapConfigStatus struct {
	// Ready is true once the bootstrap data Secret exists. Kept for the
	// v1beta1 bootstrap contract; v1beta2 clients read Initialization instead.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// Initialization tracks whether the bootstrap data Secret has been created.
	// +optional
	Initialization *VatesBootstrapConfigInitialization `json:"initialization,omitempty"`

	// DataSecretName is the name of the Secret that holds the bootstrap data.
	// +optional
	DataSecretName *string `json:"dataSecretName,omitempty"`

	// FailureReason is a one-word CamelCase reason for the last failure.
	// +optional
	FailureReason *string `json:"failureReason,omitempty"`

	// FailureMessage is the last failure's human-readable detail.
	// +optional
	FailureMessage *string `json:"failureMessage,omitempty"`

	// Conditions describe the current state of the bootstrap configuration.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// VatesBootstrapConfigInitialization carries the initialization checkpoint of
// the Cluster API bootstrap contract.
type VatesBootstrapConfigInitialization struct {
	// DataSecretCreated is true when the bootstrap data Secret has been
	// created. It is never unset once true.
	// +optional
	DataSecretCreated *bool `json:"dataSecretCreated,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=vbc,categories=cluster-api
// +kubebuilder:printcolumn:name="Ready",type="boolean",JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="DataSecret",type="string",JSONPath=".status.dataSecretName"

// VatesBootstrapConfig is the Schema for the vatesbootstrapconfigs API. One
// object is created per Machine, and the provider turns it into the
// vates-node.yaml user-data the machine reads at first boot.
type VatesBootstrapConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VatesBootstrapConfigSpec   `json:"spec,omitempty"`
	Status VatesBootstrapConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// VatesBootstrapConfigList contains a list of VatesBootstrapConfig.
type VatesBootstrapConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VatesBootstrapConfig `json:"items"`
}

// VatesBootstrapConfigTemplateSpec is the desired state of a
// VatesBootstrapConfigTemplate.
type VatesBootstrapConfigTemplateSpec struct {
	// Template is the template that describes the VatesBootstrapConfig created
	// from it.
	Template VatesBootstrapConfigTemplateResource `json:"template"`
}

// VatesBootstrapConfigTemplateResource is the resource created from a
// VatesBootstrapConfigTemplate.
type VatesBootstrapConfigTemplateResource struct {
	// Spec is the spec of the VatesBootstrapConfig created from this template.
	Spec VatesBootstrapConfigSpec `json:"spec"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=vbct,categories=cluster-api

// VatesBootstrapConfigTemplate is the Schema for the
// vatesbootstrapconfigtemplates API. It exists so a MachineDeployment or a
// ClusterClass can stamp out per-machine VatesBootstrapConfigs.
type VatesBootstrapConfigTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec VatesBootstrapConfigTemplateSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// VatesBootstrapConfigTemplateList contains a list of
// VatesBootstrapConfigTemplate.
type VatesBootstrapConfigTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VatesBootstrapConfigTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VatesBootstrapConfig{}, &VatesBootstrapConfigList{})
	SchemeBuilder.Register(&VatesBootstrapConfigTemplate{}, &VatesBootstrapConfigTemplateList{})
}
