package cluster_test

import (
	"context"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/backend/cluster"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/danielfbm/tkn-act/internal/volumes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func volInvocation(vols ...tektontypes.Volume) backend.PipelineRunInvocation {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		Steps:   []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}},
		Volumes: vols,
	}}
	tk.Metadata.Name = "t"
	return backend.PipelineRunInvocation{RunID: "12345678", PipelineRunName: "p-12345678", Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk}}
}

// TestApplyVolumeSourcesOptionalMissingSkipped: an `optional: true`
// configMap / secret that the store can't resolve mounts empty in
// Kubernetes; the cluster backend must not fail the run before submit
// (docker already mounts it empty).
func TestApplyVolumeSourcesOptionalMissingSkipped(t *testing.T) {
	yes := true
	kube := kubefake.NewSimpleClientset()
	be := cluster.NewWithClientsAndStores(cluster.ClientBundle{
		Dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), Kube: kube,
	}, volumes.NewStore(""), volumes.NewStore(""))
	in := volInvocation(
		tektontypes.Volume{Name: "c", ConfigMap: &tektontypes.ConfigMapSource{Name: "absent-cm", Optional: &yes}},
		tektontypes.Volume{Name: "s", Secret: &tektontypes.SecretSource{SecretName: "absent-sec", Optional: &yes}},
	)
	if err := be.ApplyVolumeSourcesForTest(context.Background(), in, "ns"); err != nil {
		t.Fatalf("optional missing sources must not fail: %v", err)
	}
	if _, err := kube.CoreV1().ConfigMaps("ns").Get(context.Background(), "absent-cm", metav1.GetOptions{}); err == nil {
		t.Error("no ConfigMap should be created for a missing optional source")
	}

	// One required reference to the same name keeps it a hard error.
	no := false
	in = volInvocation(
		tektontypes.Volume{Name: "c1", ConfigMap: &tektontypes.ConfigMapSource{Name: "absent-cm", Optional: &yes}},
		tektontypes.Volume{Name: "c2", ConfigMap: &tektontypes.ConfigMapSource{Name: "absent-cm", Optional: &no}},
	)
	if err := be.ApplyVolumeSourcesForTest(context.Background(), in, "ns"); err == nil {
		t.Error("a required reference to a missing configMap must fail")
	}
}

// TestUpsertConfigMapBinaryData: non-UTF-8 bytes must go to BinaryData;
// putting them in Data corrupts them.
func TestUpsertConfigMapBinaryData(t *testing.T) {
	cm := volumes.NewStore("")
	cm.LoadBytes("bin", map[string][]byte{"blob": {0xff, 0xfe, 0x00, 0x01}, "text": []byte("hi")})
	kube := kubefake.NewSimpleClientset()
	be := cluster.NewWithClientsAndStores(cluster.ClientBundle{
		Dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), Kube: kube,
	}, cm, volumes.NewStore(""))
	in := volInvocation(tektontypes.Volume{Name: "b", ConfigMap: &tektontypes.ConfigMapSource{Name: "bin"}})
	if err := be.ApplyVolumeSourcesForTest(context.Background(), in, "ns"); err != nil {
		t.Fatal(err)
	}
	got, err := kube.CoreV1().ConfigMaps("ns").Get(context.Background(), "bin", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(got.BinaryData["blob"]) != string([]byte{0xff, 0xfe, 0x00, 0x01}) {
		t.Errorf("binaryData.blob = %v", got.BinaryData["blob"])
	}
	if _, inData := got.Data["blob"]; inData {
		t.Error("non-UTF-8 value leaked into Data")
	}
	if got.Data["text"] != "hi" {
		t.Errorf("data.text = %q", got.Data["text"])
	}
}
