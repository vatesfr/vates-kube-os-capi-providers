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

// VatesControlPlaneTemplateSpec is the desired state of a
// VatesControlPlaneTemplate.
type VatesControlPlaneTemplateSpec struct {
	// Template is the template that describes the VatesControlPlane created
	// from it.
	// +required
	Template VatesControlPlaneTemplateResource `json:"template"`
}

// VatesControlPlaneTemplateResource is the resource created from a
// VatesControlPlaneTemplate. It is the shape the Cluster API topology
// controller clones.
type VatesControlPlaneTemplateResource struct {
	// metadata is added to the VatesControlPlane created from this template.
	// +optional
	ObjectMeta VatesControlPlaneTemplateResourceMeta `json:"metadata,omitempty,omitzero"`

	// spec is the spec of the VatesControlPlane created from this template.
	// +required
	Spec VatesControlPlaneSpec `json:"spec"`
}

// VatesControlPlaneTemplateResourceMeta is the metadata the topology controller
// propagates from a control plane template onto the object it creates.
type VatesControlPlaneTemplateResourceMeta struct {
	// labels is a set of labels to apply to the VatesControlPlane.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// annotations is a set of annotations to apply to the VatesControlPlane.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=vcpt,categories=cluster-api

// VatesControlPlaneTemplate is the Schema for the vatescontrolplanetemplates
// API, so a ClusterClass can stamp out control planes.
type VatesControlPlaneTemplate struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of VatesControlPlaneTemplate
	// +required
	Spec VatesControlPlaneTemplateSpec `json:"spec"`

	// status defines the observed state of VatesControlPlaneTemplate
	// +optional
	Status VatesControlPlaneTemplateStatus `json:"status,omitzero"`
}

// VatesControlPlaneTemplateStatus defines the observed state of
// VatesControlPlaneTemplate.
type VatesControlPlaneTemplateStatus struct{}

// +kubebuilder:object:root=true

// VatesControlPlaneTemplateList contains a list of VatesControlPlaneTemplate.
type VatesControlPlaneTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []VatesControlPlaneTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VatesControlPlaneTemplate{}, &VatesControlPlaneTemplateList{})
}
