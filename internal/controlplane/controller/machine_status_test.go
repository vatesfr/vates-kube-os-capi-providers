package controller

import (
	"context"
	"testing"

	controlplanev1 "github.com/vatesfr/vates-kube-os-capi-providers/api/controlplane/v1alpha1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMachineUpToDateConditions(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	m := &clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{Name: "cp-0", Namespace: "default", Generation: 3}, Spec: clusterv1.MachineSpec{Version: "v1.37.1"}}
	apimeta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{Type: clusterv1.MachineReadyCondition, Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 3})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(m).WithObjects(m).Build()
	r := &VatesControlPlaneReconciler{Client: c, Scheme: scheme}
	vcp := &controlplanev1.VatesControlPlane{}
	vcp.Spec.Replicas = ptr.To(int32(1))
	cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}}
	for _, tt := range []struct {
		version string
		status  metav1.ConditionStatus
		count   int32
	}{
		{"v1.37.1", metav1.ConditionTrue, 1},
		{"v1.38.0", metav1.ConditionFalse, 0},
		{"v1.37.1", metav1.ConditionTrue, 1},
	} {
		vcp.Spec.Version = tt.version
		current := &clusterv1.Machine{}
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(m), current); err != nil {
			t.Fatal(err)
		}
		machines := []clusterv1.Machine{*current}
		if err := r.reconcileMachineUpToDateConditions(context.Background(), vcp, machines); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(m), current); err != nil {
			t.Fatal(err)
		}
		cond := apimeta.FindStatusCondition(current.Status.Conditions, clusterv1.MachineUpToDateCondition)
		if cond == nil || cond.Status != tt.status || cond.ObservedGeneration != 3 {
			t.Fatalf("unexpected condition: %+v", cond)
		}
		if tt.status == metav1.ConditionFalse && cond.Message == "" {
			t.Fatal("missing mismatch explanation")
		}
		if !apimeta.IsStatusConditionTrue(current.Status.Conditions, clusterv1.MachineReadyCondition) {
			t.Fatal("Ready condition lost")
		}
		r.updateStatus(vcp, cluster, machines, true)
		if *vcp.Status.UpToDateReplicas != tt.count {
			t.Fatalf("replica count: got %d, want %d", *vcp.Status.UpToDateReplicas, tt.count)
		}
	}
}
