// Package testutil sets up the envtest environment the controller tests run against.
package testutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/helmetica-framework/ampulla/controllers"
)

// The CRDs of the two APIs ampulla writes to come out of the module cache: both are
// dependencies of this module already, so nothing has to be vendored into this repo or
// downloaded during the test run. ampulla's own CRD comes from config/crd.
const (
	k8upModule = "github.com/k8up-io/k8up/v2"
	k8upCRDDir = "config/crd/apiextensions.k8s.io/v1"
	cosiModule = "sigs.k8s.io/container-object-storage-interface/client"
	cosiCRDDir = "config/crd"
)

// SetupEnvtestEnv starts an API server with ampulla's, COSI's and k8up's CRDs installed
// and returns the scheme and config to talk to it. The environment is stopped when the
// test ends; any manager started against it has to be stopped first, by cancelling its
// context.
func SetupEnvtestEnv(t *testing.T) (*runtime.Scheme, *rest.Config) {
	t.Helper()

	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "config", "crd", "bases"),
			filepath.Join(moduleDir(t, k8upModule), k8upCRDDir),
			filepath.Join(moduleDir(t, cosiModule), cosiCRDDir),
		},
		ErrorIfCRDPathMissing: true,
		Scheme:                controllers.Scheme(),
		BinaryAssetsDirectory: getFirstFoundEnvTestBinaryDir(t),
	}

	cfg, err := testEnv.Start()
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, testEnv.Stop())
	})

	return testEnv.Scheme, cfg
}

// TmpNamespace creates a namespace with a generated name and deletes it when the test ends.
func TmpNamespace(t *testing.T, c client.Client) string {
	t.Helper()

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "ampulla-test-",
			Annotations: map[string]string{
				"test.ampulla.helmetica.io/name": t.Name(),
			},
		},
	}
	require.NoError(t, c.Create(t.Context(), ns))

	t.Cleanup(func() {
		require.NoError(t, client.IgnoreNotFound(c.Delete(context.Background(), ns)))
	})
	return ns.Name
}

// moduleDir resolves a dependency's directory in the module cache.
func moduleDir(t *testing.T, module string) string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}", module).Output()
	if err != nil {
		t.Fatalf("Failed to locate module %s: %s", module, err.Error())
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		t.Fatalf("Module %s has no directory: run 'go mod download' first", module)
	}
	return dir
}

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
// ENVTEST-based tests depend on specific binaries, usually located in paths set by
// controller-runtime. When running tests directly (e.g., via an IDE) without using
// the Justfile recipes, the 'BinaryAssetsDirectory' must be explicitly configured.
//
// This function streamlines the process by finding the required binaries, similar to
// setting the 'KUBEBUILDER_ASSETS' environment variable. To ensure the binaries are
// properly set up, run 'just test' once beforehand.
func getFirstFoundEnvTestBinaryDir(t *testing.T) string {
	out, err := exec.CommandContext(t.Context(), "go", "env", "GOMOD").CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to get GOMOD: %s", err.Error())
	}
	if len(out) == 0 {
		t.Fatal("GOMOD is empty, ensure you are running tests from a Go module")
	}
	root := filepath.Dir(string(out))

	basePath := filepath.Join(root, "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		t.Logf("Failed to read directory %q: %s", basePath, err.Error())
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}
