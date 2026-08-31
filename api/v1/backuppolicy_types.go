package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type BackupPolicyPhase string

const (
	BackupPolicyPhasePending BackupPolicyPhase = "Pending"
	BackupPolicyPhaseReady   BackupPolicyPhase = "Ready"
	BackupPolicyPhaseFailed  BackupPolicyPhase = "Failed"
)

// Mode selects who takes the backups.
// +kubebuilder:validation:Enum=Schedule;BucketOnly
type Mode string

const (
	// ModeSchedule is the default: ampulla provisions a bucket and drives k8up, which
	// backs up every persistent volume in the namespace.
	ModeSchedule Mode = "Schedule"

	// ModeBucketOnly provisions the bucket and its credentials and stops there, for a
	// service whose own operator writes backups to object storage. Those operators are
	// pointed at the bucket through the status.
	ModeBucketOnly Mode = "BucketOnly"
)

// BackupPolicySpec describes how the service next to it is backed up.
//
// Every field is optional: an empty spec takes the controller's defaults, which is what a
// chart renders when its values say no more than that backups are enabled.
type BackupPolicySpec struct {
	// Mode selects who takes the backups.
	// +kubebuilder:default=Schedule
	// +optional
	Mode Mode `json:"mode,omitempty"`

	// Schedule is when each of the jobs runs. Mode Schedule only.
	// +optional
	Schedule ScheduleSpec `json:"schedule,omitempty"`

	// BucketClassName is the object storage the backups are written to. Empty takes the
	// controller's default; without either, the policy fails rather than being backed up
	// somewhere arbitrary.
	// +optional
	BucketClassName string `json:"bucketClassName,omitempty"`

	// BucketAccessClassName is the class the bucket credentials are minted from. Empty
	// takes the controller's default.
	// +optional
	BucketAccessClassName string `json:"bucketAccessClassName,omitempty"`

	// Retention is how many backups survive a prune. All zero takes the controller's
	// default retention. Mode Schedule only.
	// +optional
	Retention Retention `json:"retention,omitempty"`
}

// ScheduleSpec is when the k8up jobs run. Every field is a cron expression or one of
// k8up's shortcuts such as `@daily-random`.
//
// An empty field falls back to the controller's default for that job, and removing one
// from a policy that had it set restores that fallback - and drops the job entirely if the
// controller has no default for it either.
type ScheduleSpec struct {
	// Backup is when the backup itself runs. Falls back to the controller's default.
	// +optional
	Backup string `json:"backup,omitempty"`

	// Prune is when old snapshots are forgotten and pruned. Falls back to the controller's
	// default; with no default configured there, no prune job is scheduled.
	// +optional
	Prune string `json:"prune,omitempty"`

	// Check is when the backup repository is verified. Falls back to the controller's
	// default; with no default configured there, no check job is scheduled.
	// +optional
	Check string `json:"check,omitempty"`
}

// IsZero reports whether no schedule at all was configured.
func (s ScheduleSpec) IsZero() bool {
	return s == ScheduleSpec{}
}

// Retention mirrors restic's forget policy, minus the tag and hostname filters: ampulla
// owns one repository per policy, so there is nothing to filter within it.
type Retention struct {
	// +kubebuilder:validation:Minimum=0
	// +optional
	KeepLast int `json:"keepLast,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	KeepHourly int `json:"keepHourly,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	KeepDaily int `json:"keepDaily,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	KeepWeekly int `json:"keepWeekly,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	KeepMonthly int `json:"keepMonthly,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	KeepYearly int `json:"keepYearly,omitempty"`
}

// IsZero reports whether no retention at all was configured.
func (r Retention) IsZero() bool {
	return r == Retention{}
}

// BackupPolicyStatus is where the bucket's coordinates are published. In ModeBucketOnly
// they are the whole deliverable: they are how a service's own backup resource is pointed
// at the storage ampulla provisioned.
type BackupPolicyStatus struct {
	// +optional
	Phase BackupPolicyPhase `json:"phase,omitempty"`
	// ObservedGeneration is the spec generation the phase was computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Message explains a Pending or Failed phase.
	// +optional
	Message string `json:"message,omitempty"`

	// Bucket is the bucket as clients address it.
	// +optional
	Bucket string `json:"bucket,omitempty"`
	// Endpoint is the object storage endpoint the bucket lives behind.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// CredentialsSecret is the secret in this namespace holding the bucket's key pair,
	// one value per key. A service that backs itself up reads its S3 credentials there.
	// +optional
	CredentialsSecret string `json:"credentialsSecret,omitempty"`
	// Schedule is the schedule the backups run on, once defaults are resolved.
	// +optional
	Schedule string `json:"schedule,omitempty"`
}

// BackupPolicy asks ampulla to back up the service it sits next to. The policy's own
// namespace is the whole scope: the bucket is provisioned for it, the credentials land
// beside it, and in ModeSchedule every persistent volume in the namespace is backed up.
// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Bucket",type=string,JSONPath=`.status.bucket`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`
type BackupPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupPolicySpec   `json:"spec,omitempty"`
	Status BackupPolicyStatus `json:"status,omitempty"`
}

// BackupPolicyList contains a list of BackupPolicy.
// +kubebuilder:object:root=true
type BackupPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []BackupPolicy `json:"items"`
}

func init() { SchemeBuilder.Register(&BackupPolicy{}, &BackupPolicyList{}) }
