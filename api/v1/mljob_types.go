package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	schema "k8s.io/apimachinery/pkg/runtime/schema"
)

// MLJobSpec defines the desired state of MLJob
type MLJobSpec struct {
	Image          string `json:"image"`
	CPU            string `json:"cpu"`
//	GPU	       string `json:"gpu,omitempty"`

	CheckpointPVC  string `json:"checkpointPVC,omitempty"`
	CheckpointPath string `json:"checkpointPath,omitempty"`
	QueueName      string `json:"queueName,omitempty"`
}

// +kubebuilder:object:root=true
type MLJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	
	Spec MLJobSpec `json:"spec,omitempty"`
}

func (in *MLJob) DeepCopyObject() runtime.Object {
	out := new(MLJob)
	*out = *in
	return out
}

// +kubebuilder:object:root=true
type MLJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MLJob `json:"items"`
}

func (in *MLJobList) DeepCopyObject() runtime.Object {
	out := new(MLJobList)
	*out = *in
	return out
}

func AddToScheme(s *runtime.Scheme) error {
	s.AddKnownTypes(SchemeGroupVersion, &MLJob{}, &MLJobList{})
	metav1.AddToGroupVersion(s, SchemeGroupVersion)
	return nil
}

var (
	GroupName           = "ai.mljob-controller"
	SchemeGroupVersion  = schema.GroupVersion{Group: GroupName, Version: "v1"}
)

