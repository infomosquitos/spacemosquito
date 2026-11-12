package embed

import (
	"os/exec"
	"strings"
	"testing"
)

// The main binary must not link the native-loading code: libffi's package
// init panics when the library is missing (ADR-019). Only
// cmd/spacemosquito-embedder may depend on it.
func TestMainBinaryDoesNotLinkLlama(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/vkh/spacemosquito/cmd/spacemosquito").CombinedOutput()
	if err != nil {
		t.Skipf("go list unavailable: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		for _, banned := range []string{"github.com/hybridgroup/yzma", "github.com/jupiterrider/ffi", "github.com/ebitengine/purego", "/internal/embed/llamacpp"} {
			if strings.Contains(pkg, banned) {
				t.Errorf("cmd/spacemosquito depends on %s", pkg)
			}
		}
	}
}
