package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestDeploy_KubernetesManifestsSyntaxAndSchema verifies that all Kubernetes YAML manifests
// in deploy/k8s parse cleanly with valid apiVersion, kind, and metadata fields.
func TestDeploy_KubernetesManifestsSyntaxAndSchema(t *testing.T) {
	manifestDir := filepath.Join("..", "..", "deploy", "k8s")
	entries, err := os.ReadDir(manifestDir)
	if err != nil {
		t.Fatalf("failed reading deploy/k8s directory: %v", err)
	}

	var checkedCount int

	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml")) {
			continue
		}

		filePath := filepath.Join(manifestDir, entry.Name())
		content, readErr := os.ReadFile(filePath)
		if readErr != nil {
			t.Fatalf("failed reading %s: %v", entry.Name(), readErr)
		}

		var parsed map[string]interface{}
		if yamlErr := yaml.Unmarshal(content, &parsed); yamlErr != nil {
			t.Fatalf("SYNTAX ERROR in %s: %v", entry.Name(), yamlErr)
		}

		// Verify standard Kubernetes object structure
		apiVersion, ok := parsed["apiVersion"].(string)
		if !ok || apiVersion == "" {
			t.Errorf("%s missing or invalid 'apiVersion'", entry.Name())
		}

		kind, ok := parsed["kind"].(string)
		if !ok || kind == "" {
			t.Errorf("%s missing or invalid 'kind'", entry.Name())
		}

		meta, ok := parsed["metadata"].(map[string]interface{})
		if !ok || meta == nil {
			t.Errorf("%s missing or invalid 'metadata' block", entry.Name())
		} else {
			name, nameOk := meta["name"].(string)
			if !nameOk || name == "" {
				t.Errorf("%s missing 'metadata.name'", entry.Name())
			}
		}

		t.Logf("PASS: Validated Kubernetes Manifest: %-20s | apiVersion: %-22s | kind: %s", entry.Name(), apiVersion, kind)
		checkedCount++
	}

	t.Logf("=== KUBERNETES MANIFEST VALIDATION COMPLETE (%d files checked) ===", checkedCount)
	if checkedCount < 6 {
		t.Fatalf("expected at least 6 manifests validated, got %d", checkedCount)
	}
}

// TestDeploy_DockerComposeSyntax verifies that deploy/docker-compose.yml parses cleanly
// and defines all 5 required services (app, postgres, redis, prometheus, grafana).
func TestDeploy_DockerComposeSyntax(t *testing.T) {
	composePath := filepath.Join("..", "..", "deploy", "docker-compose.yml")
	content, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("failed reading docker-compose.yml: %v", err)
	}

	var composeMap struct {
		Services map[string]interface{} `yaml:"services"`
		Volumes  map[string]interface{} `yaml:"volumes"`
		Networks map[string]interface{} `yaml:"networks"`
	}

	if err := yaml.Unmarshal(content, &composeMap); err != nil {
		t.Fatalf("SYNTAX ERROR in docker-compose.yml: %v", err)
	}

	requiredServices := []string{"app", "postgres", "redis", "prometheus", "grafana"}
	for _, svc := range requiredServices {
		if _, exists := composeMap.Services[svc]; !exists {
			t.Fatalf("missing required service in docker-compose.yml: %s", svc)
		}
		t.Logf("PASS: Verified Docker Compose Service: %s", svc)
	}

	requiredVolumes := []string{"postgres_data", "redis_data", "prometheus_data", "grafana_data"}
	for _, vol := range requiredVolumes {
		if _, exists := composeMap.Volumes[vol]; !exists {
			t.Fatalf("missing required volume in docker-compose.yml: %s", vol)
		}
		t.Logf("PASS: Verified Docker Compose Volume: %s", vol)
	}
}
