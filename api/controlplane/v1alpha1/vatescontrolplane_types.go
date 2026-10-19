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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// VatesControlPlaneSpec is the desired state of a Vates Kube OS control plane.
//
// It owns the cluster's certificate authority: this provider generates it,
// stores it in a Secret, and injects it into every control plane machine's
// document, so no machine has to create one and no provider has to ask a live
// node for it.
type VatesControlPlaneSpec struct {
	// Replicas is the number of control plane machines. Optional; defaults to 1.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Replicas *int32 `json:"replicas,omitempty"`

	// Version is the Kubernetes release every control plane machine runs,
	// e.g. "v1.31.4". Required.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Version string `json:"version"`

	// MachineTemplate holds the templates each control plane Machine is stamped
	// from.
	// +required
	MachineTemplate VatesControlPlaneMachineTemplate `json:"machineTemplate"`
}

// VatesControlPlaneMachineTemplate names the templates the provider clones when
// it creates a control plane Machine and the objects it references.
type VatesControlPlaneMachineTemplate struct {
	// InfrastructureRef is the XOMachineTemplate the VM is stamped from. It is
	// part of the Cluster API control plane contract.
	// +required
	InfrastructureRef corev1.ObjectReference `json:"infrastructureRef"`

	// BootstrapConfigTemplateRef is the VatesConfigTemplate the
	// bootstrap document is stamped from. The provider adds the cluster CA to
	// the document it produces.
	// +required
	BootstrapConfigTemplateRef corev1.ObjectReference `json:"bootstrapConfigTemplateRef"`

	// Metadata is added to every object generated from these templates (the
	// Machines, and the bootstrap/infrastructure objects derived from them).
	// +optional
	Metadata clusterv1.ObjectMeta `json:"metadata,omitempty,omitzero"`
}

// VatesControlPlaneInitializationStatus is part of the Cluster API control
// plane contract: it tells the Cluster controller the control plane is
// functional enough to accept requests, after which worker machines may join.
type VatesControlPlaneInitializationStatus struct {
	// ControlPlaneInitialized is true once the control plane can accept
	// requests, no matter whether it is fully provisioned yet.
	// +optional
	ControlPlaneInitialized *bool `json:"controlPlaneInitialized,omitempty"`
}

// VatesControlPlaneStatus is the observed state, in the shape the Cluster API
// v1beta2 control plane contract reads.
type VatesControlPlaneStatus struct {
	// Conditions describe the current state of the control plane. Known types
	// are Available and MachinesReady.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=32
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Initialization is the contract field the Cluster controller reads.
	// +optional
	Initialization VatesControlPlaneInitializationStatus `json:"initialization,omitempty,omitzero"`

	// Selector is the label selector in string form, used for the scale
	// subresource and by clients such as kubectl describe.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=4096
	Selector string `json:"selector,omitempty"`

	// Replicas is the number of non-terminated machines this control plane owns.
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// ReadyReplicas is the number of machines whose Ready condition is true.
	// +optional
	ReadyReplicas *int32 `json:"readyReplicas,omitempty"`

	// AvailableReplicas is the number of machines whose Available condition is
	// true.
	// +optional
	AvailableReplicas *int32 `json:"availableReplicas,omitempty"`

	// UpToDateReplicas is the number of machines matching the desired version.
	// +optional
	UpToDateReplicas *int32 `json:"upToDateReplicas,omitempty"`

	// Version is the Kubernetes version the control plane runs.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Version string `json:"version,omitempty"`

	// ObservedGeneration is the latest generation observed by the controller.
	// +optional
	// +kubebuilder:validation:Minimum=1
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=vcp,categories=cluster-api
// +kubebuilder:printcolumn:name="Initialized",type="boolean",JSONPath=".status.initialization.controlPlaneInitialized"
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".status.replicas"
// +kubebuilder:printcolumn:name="Ready",type="integer",JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".spec.version"

// VatesControlPlane is the Schema for the vatescontrolplanes API. One is created
// per Cluster, and the provider turns it into the cluster's control plane: it
// owns the certificate authority and stamps out the control plane Machines.
type VatesControlPlane struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of VatesControlPlane
	// +required
	Spec VatesControlPlaneSpec `json:"spec"`

	// status defines the observed state of VatesControlPlane
	// +optional
	Status VatesControlPlaneStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// VatesControlPlaneList contains a list of VatesControlPlane.
type VatesControlPlaneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []VatesControlPlane `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VatesControlPlane{}, &VatesControlPlaneList{})
}
