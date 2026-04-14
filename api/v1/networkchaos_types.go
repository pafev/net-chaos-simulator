package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type PodSelector struct {
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	Labels *metav1.LabelSelector `json:"labels,omitempty"`
}

type NetworkChaosSpec struct {
	SourceSelector PodSelector `json:"sourceSelector"`
	TargetSelector PodSelector `json:"targetSelector"`
	Delay          string      `json:"delay"`
}

type NetworkChaosStatus struct {
	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	Applied bool `json:"applied"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

type NetworkChaos struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`
	// +required
	Spec NetworkChaosSpec `json:"spec"`
	// +optional
	Status NetworkChaosStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

type NetworkChaosList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []NetworkChaos `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetworkChaos{}, &NetworkChaosList{})
}
