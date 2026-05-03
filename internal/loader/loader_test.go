package loader_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/loader"
)

func TestLoadFiles(t *testing.T) {
	bundle, err := loader.LoadFiles([]string{filepath.Join("testdata", "multi.yaml")})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(bundle.Tasks) != 1 {
		t.Errorf("tasks = %d, want 1", len(bundle.Tasks))
	}
	if _, ok := bundle.Tasks["greet"]; !ok {
		t.Errorf("missing greet task; got %v", bundle.Tasks)
	}
	if _, ok := bundle.Pipelines["chain"]; !ok {
		t.Errorf("missing chain pipeline")
	}
}

func TestRejectsUnknownKind(t *testing.T) {
	yaml := []byte(`
apiVersion: tekton.dev/v1
kind: NotARealKind
metadata: {name: x}
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestRejectsWrongAPIVersion(t *testing.T) {
	yaml := []byte(`
apiVersion: tekton.dev/v1beta1
kind: Task
metadata: {name: old}
spec:
  steps: []
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil {
		t.Fatal("expected error for v1beta1")
	}
}

func TestLoadConfigMap(t *testing.T) {
	bundle, err := loader.LoadFiles([]string{filepath.Join("testdata", "cm-and-secret.yaml")})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cm, ok := bundle.ConfigMaps["app-config"]
	if !ok {
		t.Fatalf("missing app-config ConfigMap; got %v", bundle.ConfigMaps)
	}
	if got := cm["greeting"]; string(got) != "hello-from-yaml" {
		t.Errorf("greeting = %q, want hello-from-yaml", string(got))
	}
	if got := cm["lang"]; string(got) != "en" {
		t.Errorf("lang = %q, want en", string(got))
	}
}

func TestRejectsConfigMapBinaryData(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: ConfigMap
metadata: {name: c}
binaryData:
  k: aGVsbG8=
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil || !strings.Contains(err.Error(), "binaryData") {
		t.Fatalf("err = %v, want binaryData error", err)
	}
}

func TestRejectsDuplicateConfigMap(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: ConfigMap
metadata: {name: c}
data: {k: v1}
---
apiVersion: v1
kind: ConfigMap
metadata: {name: c}
data: {k: v2}
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil || !strings.Contains(err.Error(), "duplicate ConfigMap") {
		t.Fatalf("err = %v, want duplicate-ConfigMap error", err)
	}
}

func TestRejectsConfigMapMissingName(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: ConfigMap
data: {k: v}
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil || !strings.Contains(err.Error(), "metadata.name") {
		t.Fatalf("err = %v, want metadata.name error", err)
	}
}

func TestLoadSecretData(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: Secret
metadata: {name: s}
type: Opaque
data:
  token: aGVsbG8=
`)
	b, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sec, ok := b.Secrets["s"]
	if !ok {
		t.Fatalf("missing secret; got %v", b.Secrets)
	}
	if got := sec["token"]; string(got) != "hello" {
		t.Errorf("token = %q, want hello (base64-decoded)", string(got))
	}
}

func TestLoadSecretStringData(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: Secret
metadata: {name: s}
stringData:
  raw: hello-plain
`)
	b, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sec, ok := b.Secrets["s"]
	if !ok {
		t.Fatalf("missing secret; got %v", b.Secrets)
	}
	if got := sec["raw"]; string(got) != "hello-plain" {
		t.Errorf("raw = %q, want hello-plain", string(got))
	}
}

func TestLoadSecretStringDataWinsOverData(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: Secret
metadata: {name: s}
data:
  k: dmFsLWZyb20tZGF0YQ==
stringData:
  k: val-from-stringData
`)
	b, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := b.Secrets["s"]["k"]; string(got) != "val-from-stringData" {
		t.Errorf("k = %q, want val-from-stringData (stringData wins)", string(got))
	}
}

func TestRejectsSecretMalformedBase64(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: Secret
metadata: {name: s}
data:
  k: "!!!not-base64!!!"
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil || !strings.Contains(err.Error(), "base64") {
		t.Fatalf("err = %v, want base64 decode error", err)
	}
}

func TestRejectsDuplicateSecret(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: Secret
metadata: {name: s}
stringData: {k: a}
---
apiVersion: v1
kind: Secret
metadata: {name: s}
stringData: {k: b}
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil || !strings.Contains(err.Error(), "duplicate Secret") {
		t.Fatalf("err = %v, want duplicate-Secret error", err)
	}
}

func TestLoadConfigMapAndSecretFromFile(t *testing.T) {
	bundle, err := loader.LoadFiles([]string{filepath.Join("testdata", "cm-and-secret.yaml")})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := bundle.ConfigMaps["app-config"]; !ok {
		t.Errorf("missing app-config CM; got %v", bundle.ConfigMaps)
	}
	sec, ok := bundle.Secrets["app-secret"]
	if !ok {
		t.Fatalf("missing app-secret; got %v", bundle.Secrets)
	}
	if string(sec["token"]) != "hello" {
		t.Errorf("token = %q, want hello (base64-decoded)", sec["token"])
	}
	if string(sec["raw-token"]) != "hello-plain" {
		t.Errorf("raw-token = %q, want hello-plain", sec["raw-token"])
	}
}
