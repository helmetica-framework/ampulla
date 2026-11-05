package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type BackupTemplatePhase string

const (
	BackupTemplatePhasePending BackupTemplatePhase = "Pending"
	BackupTemplatePhaseReady   BackupTemplatePhase = "Ready"
	BackupTemplatePhaseFailed  BackupTemplatePhase = "Failed"
)

// BackupTemplateSpec is the backup resource of a service whose own operator takes the
// backups, rendered with the bucket of a BackupPolicy.
type BackupTemplateSpec struct {
	// PolicyName is the BackupPolicy in this namespace whose bucket the template is
	// rendered with.
	// +kubebuilder:validation:MinLength=1
	PolicyName string `json:"policyName"`

	// Template is the object to write, always into this namespace and owned by the
	// template, so deleting the template deletes it as well. Placeholders in it are
	// filled from the policy's status. ${s3.bucket}, ${s3.url}, ${s3.hostPort} and
	// ${s3.region} may be part of any string. ${s3.tls} (a boolean) and ${s3.accessKeyRef}
	// and ${s3.secretKeyRef} (each a `{name, key}` secret reference) only as a whole value.
	// +kubebuilder:validation:EmbeddedResource
	// +kubebuilder:pruning:PreserveUnknownFields
	Template runtime.RawExtension `json:"template"`
}

type BackupTemplateStatus struct {
	// +optional
	Phase BackupTemplatePhase `json:"phase,omitempty"`
	// ObservedGeneration is the spec generation the phase was computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Message explains a Pending or Failed phase.
	// +optional
	Message string `json:"message,omitempty"`
}

// BackupTemplate renders a backup resource for an operator that backs its service up
// itself, pointed at the bucket a BackupPolicy provisioned.
// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Policy",type=string,JSONPath=`.spec.policyName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`
type BackupTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupTemplateSpec   `json:"spec,omitempty"`
	Status BackupTemplateStatus `json:"status,omitempty"`
}

// BackupTemplateList contains a list of BackupTemplate.
// +kubebuilder:object:root=true
type BackupTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []BackupTemplate `json:"items"`
}

func init() { SchemeBuilder.Register(&BackupTemplate{}, &BackupTemplateList{}) }
