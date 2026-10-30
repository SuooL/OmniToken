package web

import (
	"os/exec"
	"testing"
)

func TestWebToolUIRegressionSuites(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required for Web UI regression suites")
	}
	for _, file := range []string{"tool-ui.test.cjs", "operational-ui.test.cjs", "workflow-ui.test.cjs", "analysis-ui.test.cjs"} {
		t.Run(file, func(t *testing.T) {
			output, err := exec.Command(node, "--test", file).CombinedOutput()
			if err != nil {
				t.Fatalf("%s: %v\n%s", file, err, output)
			}
		})
	}
}
