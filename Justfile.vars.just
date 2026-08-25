bin_filename := "ampulla"

# Container image
image_repo := "ghcr.io/helmetica-framework/ampulla"
IMG_TAG := "latest"
GHCR_IMG := image_repo + ":" + IMG_TAG

# Namespace the controller is deployed into
NAMESPACE := "ampulla-system"

# Tools, pinned by the tool directives in go.mod
CONTROLLER_GEN := "go tool sigs.k8s.io/controller-tools/cmd/controller-gen"
KUSTOMIZE := "go tool sigs.k8s.io/kustomize/kustomize/v5"
KIND_CMD := "go tool sigs.k8s.io/kind"

# Local test cluster (athanor's kind cluster)
KIND_CLUSTER := "athanor"
