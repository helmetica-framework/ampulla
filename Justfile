import "Justfile.vars.just"

_default:
    @just --list

# Build the manager binary, generators and checks included
build: generate manifests fmt vet binary

# CGO is disabled here only, not globally: the image is distroless and needs a static
# binary, while `just test` runs with -race, which requires cgo.
#
# Build the binary without running the generators
binary:
    @echo "GOOS=$(go env GOOS) GOARCH=$(go env GOARCH)"
    CGO_ENABLED=0 go build -o {{ bin_filename }}

# Run tests, including the envtest integration tests
test: manifests generate
    #!/usr/bin/env bash
    set -euo pipefail
    export KUBEBUILDER_ASSETS="$({{ SETUP_ENVTEST }} use {{ ENVTEST_K8S_VERSION }} --bin-dir {{ localbin }} -p path)"
    go test ./... -race -coverprofile cover.tmp.out
    grep -v "zz_generated.deepcopy.go" cover.tmp.out > cover.out

# Generate ClusterRole and CustomResourceDefinition objects
manifests:
    {{ CONTROLLER_GEN }} rbac:roleName=manager-role crd:generateEmbeddedObjectMeta=true paths="./..." output:crd:artifacts:config=config/crd/bases

# Generate deepcopy functions and manifests
generate: manifests
    go generate ./...
    {{ CONTROLLER_GEN }} object paths="./..."
    {{ CONTROLLER_GEN }} applyconfiguration paths="./api/..."

# Generate documentation
docs:
    @echo "Nothing to do yet"

# Run go fmt against code
fmt:
    go fmt ./...

# Run go vet against code
vet:
    go vet ./...

# All-in-one linting
lint: fmt vet generate manifests docs
    @echo 'Checking kustomize build ...'
    {{ KUSTOMIZE }} build config/crd -o /dev/null
    {{ KUSTOMIZE }} build config/default -o /dev/null
    @echo 'Check for uncommitted changes ...'
    git diff --exit-code

# Build the docker image
build-docker tag=IMG_TAG: binary
    docker build . --tag {{ image_repo }}:{{ tag }}

# Apply the controller and its RBAC to the current cluster
install:
    {{ KUSTOMIZE }} build config/default | kubectl apply --server-side -f -

# Build, side-load into the athanor kind cluster and (re)deploy
deploy-kind: (build-docker "dev")
    {{ KIND_CMD }} load docker-image {{ image_repo }}:dev --name {{ KIND_CLUSTER }}
    {{ KUSTOMIZE }} build config/dev | kubectl apply --server-side -f -
    kubectl -n {{ NAMESPACE }} rollout restart deployment/{{ bin_filename }}-controller-manager
    kubectl -n {{ NAMESPACE }} rollout status deployment/{{ bin_filename }}-controller-manager --timeout=120s

# Run the controller from your host
run: manifests generate fmt vet
    go run main.go controller

# Clean up the generated resources
clean:
    rm -rf dist/ cover.out cover.tmp.out {{ bin_filename }} || true
