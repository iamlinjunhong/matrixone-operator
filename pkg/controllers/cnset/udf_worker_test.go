// Copyright 2025-2026 Matrix Origin
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

package cnset

import (
	"reflect"
	"strings"
	"testing"

	reconfake "github.com/matrixorigin/controller-runtime/pkg/fake"
	"github.com/matrixorigin/matrixone-operator/api/core/v1alpha1"
	"github.com/matrixorigin/matrixone-operator/pkg/controllers/common"
	kruisev1alpha1 "github.com/openkruise/kruise-api/apps/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func cnSetUDFWorkerPolicyForTest() *v1alpha1.UDFWorkerPolicy {
	return &v1alpha1.UDFWorkerPolicy{
		Enabled:  true,
		Topology: v1alpha1.UDFWorkerTopologyPaired,
		Launcher: v1alpha1.UDFWorkerLauncherPythonImage,
		Worker: v1alpha1.UDFWorkerSpec{
			Image: "registry.example/udf-worker@sha256:" + strings.Repeat("c", 64),
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
		},
		Client: v1alpha1.UDFClientConfig{AllowUnisolated: true},
	}
}

func TestSyncPodSpecRendersAuthoritativeUDFWorker(t *testing.T) {
	cn := &v1alpha1.CNSet{Spec: v1alpha1.CNSetSpec{
		PodSet:                 v1alpha1.PodSet{MainContainer: v1alpha1.MainContainer{Image: "matrixone@sha256:" + strings.Repeat("d", 64)}},
		ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{UDFWorker: cnSetUDFWorkerPolicyForTest()},
	}}
	cs := &kruisev1alpha1.CloneSet{}
	if err := syncPodSpec(cn, cs, v1alpha1.SharedStorageProvider{}); err != nil {
		t.Fatal(err)
	}
	if got := len(cs.Spec.Template.Spec.Containers); got != 2 {
		t.Fatalf("container count = %d, want main + worker", got)
	}
	worker := cs.Spec.Template.Spec.Containers[1]
	if worker.Name != v1alpha1.ContainerUDFWorker {
		t.Fatalf("worker name = %q", worker.Name)
	}
	if got := strings.Join(worker.Args, " "); got != "--address=grpc://127.0.0.1:50051" {
		t.Fatalf("worker args = %q, want loopback address", got)
	}
	if worker.Image != cn.Spec.UDFWorker.Worker.Image {
		t.Fatalf("worker image = %q, want %q", worker.Image, cn.Spec.UDFWorker.Worker.Image)
	}
	if worker.ReadinessProbe != nil || worker.LivenessProbe != nil || worker.StartupProbe != nil {
		t.Fatalf("same-Pod worker must not add kubelet probes: %#v", worker)
	}
	if worker.SecurityContext == nil || worker.SecurityContext.RunAsNonRoot == nil ||
		!*worker.SecurityContext.RunAsNonRoot || worker.SecurityContext.RunAsUser == nil ||
		*worker.SecurityContext.RunAsUser != v1alpha1.UDFWorkerRunAsUser ||
		worker.SecurityContext.RunAsGroup == nil ||
		*worker.SecurityContext.RunAsGroup != v1alpha1.UDFWorkerRunAsGroup {
		t.Fatalf("worker security context must pin the image's numeric unprivileged identity: %#v", worker.SecurityContext)
	}
	if worker.Ports[0].ContainerPort != v1alpha1.ContainerUDFWorkerDefaultPort {
		t.Fatalf("worker port = %d", worker.Ports[0].ContainerPort)
	}
}

func TestSyncPodSpecClearsInheritedHostNamespaces(t *testing.T) {
	cn := &v1alpha1.CNSet{Spec: v1alpha1.CNSetSpec{
		PodSet:                 v1alpha1.PodSet{MainContainer: v1alpha1.MainContainer{Image: "matrixone@sha256:" + strings.Repeat("d", 64)}},
		ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{UDFWorker: cnSetUDFWorkerPolicyForTest()},
	}}
	shared := true
	runtimeClass := "unapproved-runtime"
	cs := &kruisev1alpha1.CloneSet{Spec: kruisev1alpha1.CloneSetSpec{Template: corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			HostNetwork:           true,
			HostPID:               true,
			ShareProcessNamespace: &shared,
			InitContainers:        []corev1.Container{{Name: "stale-init"}},
			SecurityContext:       &corev1.PodSecurityContext{},
			ServiceAccountName:    "stale-service-account",
			RuntimeClassName:      &runtimeClass,
		},
	}}}
	if err := syncPodSpec(cn, cs, v1alpha1.SharedStorageProvider{}); err != nil {
		t.Fatal(err)
	}
	got := cs.Spec.Template.Spec
	if got.HostNetwork || got.HostPID || got.ShareProcessNamespace != nil || got.InitContainers != nil ||
		got.SecurityContext != nil || got.ServiceAccountName != "" || got.RuntimeClassName != nil {
		t.Fatalf("worker template retained inherited Pod boundary settings: %#v", got)
	}
}

func TestSyncPodSpecRejectsRetiredPythonConfiguration(t *testing.T) {
	tests := []struct {
		name string
		spec v1alpha1.CNSetSpec
		want string
	}{
		{
			name: "legacy sidecar",
			spec: v1alpha1.CNSetSpec{ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{
				PythonUdfSidecar: &v1alpha1.PythonUdfSidecar{},
			}},
			want: "LegacyPythonUdfSidecarUnsupported",
		},
		{
			name: "arbitrary client config",
			spec: v1alpha1.CNSetSpec{PodSet: v1alpha1.PodSet{Config: v1alpha1.NewTomlConfig(map[string]interface{}{
				"cn": map[string]interface{}{"python-udf-client": map[string]interface{}{"enabled": true}},
			})}},
			want: "PythonClientConfigManagedByUDFWorkerPolicy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cn := &v1alpha1.CNSet{Spec: tt.spec}
			err := syncPodSpec(cn, &kruisev1alpha1.CloneSet{}, v1alpha1.SharedStorageProvider{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("syncPodSpec error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSyncPodSpecRejectsDangerousOverlayEvenWithoutWebhook(t *testing.T) {
	v := true
	cn := &v1alpha1.CNSet{Spec: v1alpha1.CNSetSpec{
		PodSet:                 v1alpha1.PodSet{Overlay: &v1alpha1.Overlay{ShareProcessNamespace: &v}},
		ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{UDFWorker: cnSetUDFWorkerPolicyForTest()},
	}}
	if err := syncPodSpec(cn, &kruisev1alpha1.CloneSet{}, v1alpha1.SharedStorageProvider{}); err == nil || !strings.Contains(err.Error(), "SharedProcessNamespace") {
		t.Fatalf("syncPodSpec error = %v, want defensive overlay rejection", err)
	}
}

func TestSyncPodMetaRejectsControllerOwnedOverlayMetadata(t *testing.T) {
	cn := &v1alpha1.CNSet{Spec: v1alpha1.CNSetSpec{
		PodSet: v1alpha1.PodSet{Overlay: &v1alpha1.Overlay{
			PodLabels: map[string]string{common.ComponentLabelKey: "Other"},
		}},
		ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{UDFWorker: cnSetUDFWorkerPolicyForTest()},
	}}
	if err := syncPodMeta(cn, &kruisev1alpha1.CloneSet{}); err == nil || !strings.Contains(err.Error(), "ControllerOwnedPodLabel") {
		t.Fatalf("syncPodMeta error = %v, want controller-owned label rejection", err)
	}
}

func TestBuildCNSetConfigMapUsesTypedUDFWorkerRoute(t *testing.T) {
	cn := &v1alpha1.CNSet{
		ObjectMeta: metav1.ObjectMeta{Name: "cn", Namespace: "ns"},
		Spec: v1alpha1.CNSetSpec{
			ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{UDFWorker: cnSetUDFWorkerPolicyForTest()},
		},
	}
	ls := &v1alpha1.LogSet{
		ObjectMeta: metav1.ObjectMeta{Name: "log", Namespace: "ns"},
		Spec:       v1alpha1.LogSetSpec{SharedStorage: v1alpha1.SharedStorageProvider{FileSystem: &v1alpha1.FileSystemProvider{Path: "/shared"}}},
		Status:     v1alpha1.LogSetStatus{Discovery: &v1alpha1.LogSetDiscovery{Address: "log", Port: 6001}},
	}
	cm, _, err := buildCNSetConfigMap(cn, ls, nil)
	if err != nil {
		t.Fatal(err)
	}
	config := cm.Data["config.toml"]
	for _, want := range []string{
		"enabled = true",
		"allow-unisolated = true",
		"server-address = \"127.0.0.1:50051\"",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("config missing %q:\n%s", want, config)
		}
	}
	if strings.Contains(config, "localhost:50051") || strings.Contains(config, "0.0.0.0:50051") {
		t.Fatalf("config contains an unsafe worker address:\n%s", config)
	}
}

func TestBuildCNSetConfigMapRejectsStalePythonClient(t *testing.T) {
	cn := &v1alpha1.CNSet{
		ObjectMeta: metav1.ObjectMeta{Name: "cn", Namespace: "ns"},
		Spec: v1alpha1.CNSetSpec{PodSet: v1alpha1.PodSet{Config: v1alpha1.NewTomlConfig(map[string]interface{}{
			"cn": map[string]interface{}{"python-udf-client": map[string]interface{}{"enabled": true, "server-address": "old:50051"}},
		})}},
	}
	ls := &v1alpha1.LogSet{Spec: v1alpha1.LogSetSpec{SharedStorage: v1alpha1.SharedStorageProvider{FileSystem: &v1alpha1.FileSystemProvider{Path: "/shared"}}}, Status: v1alpha1.LogSetStatus{Discovery: &v1alpha1.LogSetDiscovery{Address: "log", Port: 6001}}}
	if _, _, err := buildCNSetConfigMap(cn, ls, nil); err == nil || !strings.Contains(err.Error(), "PythonClientConfigManagedByUDFWorkerPolicy") {
		t.Fatalf("buildCNSetConfigMap error = %v, want stale client rejection", err)
	}
}

func TestBuildUDFWorkerNetworkPolicyAllowsCNQueryButOmitsWorkerPort(t *testing.T) {
	cn := &v1alpha1.CNSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "CNSet"},
		ObjectMeta: metav1.ObjectMeta{Name: "cn", Namespace: "ns"},
		Spec:       v1alpha1.CNSetSpec{ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{UDFWorker: cnSetUDFWorkerPolicyForTest()}},
	}
	np := buildUDFWorkerNetworkPolicy(cn)
	if np == nil || len(np.Spec.Ingress) != 1 {
		t.Fatalf("network policy = %#v", np)
	}
	allowed := map[int]bool{}
	for _, port := range np.Spec.Ingress[0].Ports {
		if port.Port != nil {
			allowed[port.Port.IntValue()] = true
		}
		if port.Port != nil && port.Port.IntValue() == v1alpha1.ContainerUDFWorkerDefaultPort {
			t.Fatalf("worker port is present in ingress allowlist: %#v", np.Spec.Ingress[0].Ports)
		}
	}
	if !allowed[int(cnQueryPort)] {
		t.Fatalf("CN query service port %d is missing from ingress allowlist: %#v", cnQueryPort, np.Spec.Ingress[0].Ports)
	}
	for offset := int32(0); offset < v1alpha1.CNUDFWorkerReservedPortSlots; offset++ {
		port := int(v1alpha1.CNUDFWorkerReservedPortBase + offset)
		if !allowed[port] {
			t.Fatalf("CN internal service port %d is missing from ingress allowlist: %#v", port, np.Spec.Ingress[0].Ports)
		}
	}
	if np.Spec.PodSelector.MatchLabels[common.ComponentLabelKey] == "" {
		t.Fatalf("network policy selector is not tied to the CNSet Pod labels: %#v", np.Spec.PodSelector)
	}
}

func TestSyncPodMetaControllerOwnsUDFWorkerMarker(t *testing.T) {
	cn := &v1alpha1.CNSet{Spec: v1alpha1.CNSetSpec{ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{UDFWorker: cnSetUDFWorkerPolicyForTest()}}}
	cs := &kruisev1alpha1.CloneSet{}
	if err := syncPodMeta(cn, cs); err != nil {
		t.Fatal(err)
	}
	if got := cs.Spec.Template.Labels[v1alpha1.UDFWorkerEnabledLabel]; got != v1alpha1.UDFWorkerEnabledValue {
		t.Fatalf("marker = %q, want %q", got, v1alpha1.UDFWorkerEnabledValue)
	}
}

func TestSyncPodMetaProjectsMatrixOneClusterLabelAfterOverlay(t *testing.T) {
	cn := &v1alpha1.CNSet{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{common.MatrixoneClusterLabelKey: "cluster-a"},
		},
		Spec: v1alpha1.CNSetSpec{
			PodSet:                 v1alpha1.PodSet{Overlay: &v1alpha1.Overlay{PodLabels: map[string]string{"example.com/label": "kept"}}},
			ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{UDFWorker: cnSetUDFWorkerPolicyForTest()},
		},
	}
	cs := &kruisev1alpha1.CloneSet{}
	if err := syncPodMeta(cn, cs); err != nil {
		t.Fatal(err)
	}
	if got := cs.Spec.Template.Labels[common.MatrixoneClusterLabelKey]; got != "cluster-a" {
		t.Fatalf("cluster label = %q, want cluster-a", got)
	}
	if got := cs.Spec.Template.Labels["example.com/label"]; got != "kept" {
		t.Fatalf("user label = %q, want kept", got)
	}
}

func TestSyncPodMetaProjectsPoolLifecycleLabelsAfterOverlay(t *testing.T) {
	cn := &v1alpha1.CNSet{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{v1alpha1.PoolNameLabel: "pool-a"},
		},
		Spec: v1alpha1.CNSetSpec{
			PodSet: v1alpha1.PodSet{Overlay: &v1alpha1.Overlay{
				PodLabels: map[string]string{"example.com/label": "kept"},
			}},
			PodManagementPolicy:    func() *string { v := v1alpha1.PodManagementPolicyPooling; return &v }(),
			ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{UDFWorker: cnSetUDFWorkerPolicyForTest()},
		},
	}
	cs := &kruisev1alpha1.CloneSet{}
	if err := syncPodMeta(cn, cs); err != nil {
		t.Fatal(err)
	}
	if got := cs.Spec.Template.Labels[v1alpha1.PoolNameLabel]; got != "pool-a" {
		t.Fatalf("pool label = %q, want pool-a", got)
	}
	if got := cs.Spec.Template.Labels[v1alpha1.CNPodPhaseLabel]; got != v1alpha1.CNPodPhaseUnknown {
		t.Fatalf("phase label = %q, want %q", got, v1alpha1.CNPodPhaseUnknown)
	}
	if got := cs.Spec.Template.Labels["example.com/label"]; got != "kept" {
		t.Fatalf("user label = %q, want kept", got)
	}
}

func TestUDFWorkerStatusRequiresAuthoritativeTemplate(t *testing.T) {
	cn := &v1alpha1.CNSet{
		Spec: v1alpha1.CNSetSpec{
			PodSet:                 v1alpha1.PodSet{Replicas: 1},
			ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{UDFWorker: cnSetUDFWorkerPolicyForTest()},
		},
	}
	cs := &kruisev1alpha1.CloneSet{}
	if err := syncPodSpec(cn, cs, v1alpha1.SharedStorageProvider{}); err != nil {
		t.Fatal(err)
	}
	cs.Status.ReadyReplicas = 1
	cs.Status.UpdatedReadyReplicas = 1
	syncUDFWorkerStatus(cn, cs, true)
	if condition := findUDFWorkerCondition(cn.Status.UDFWorker.Conditions, v1alpha1.UDFWorkerConditionProvisioned); condition == nil || condition.Status != metav1.ConditionTrue {
		t.Fatalf("provisioned condition = %#v, want true", condition)
	}
	if condition := findUDFWorkerCondition(cn.Status.UDFWorker.Conditions, v1alpha1.UDFWorkerConditionClientConfigReady); condition == nil || condition.Status != metav1.ConditionTrue {
		t.Fatalf("client config condition = %#v, want true", condition)
	}

	cs.Spec.Template.Spec.Containers[1].Args[0] = "--address=grpc://0.0.0.0:50051"
	syncUDFWorkerStatus(cn, cs, true)
	if condition := findUDFWorkerCondition(cn.Status.UDFWorker.Conditions, v1alpha1.UDFWorkerConditionProvisioned); condition == nil || condition.Status != metav1.ConditionFalse {
		t.Fatalf("tampered provisioned condition = %#v, want false", condition)
	}
}

func TestUDFWorkerStatusRejectsExtraContainer(t *testing.T) {
	cn := &v1alpha1.CNSet{
		Spec: v1alpha1.CNSetSpec{
			PodSet:                 v1alpha1.PodSet{Replicas: 1},
			ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{UDFWorker: cnSetUDFWorkerPolicyForTest()},
		},
	}
	cs := &kruisev1alpha1.CloneSet{}
	if err := syncPodSpec(cn, cs, v1alpha1.SharedStorageProvider{}); err != nil {
		t.Fatal(err)
	}
	cs.Spec.Template.Spec.Containers = append(cs.Spec.Template.Spec.Containers, corev1.Container{Name: "unexpected"})
	syncUDFWorkerStatus(cn, cs, true)
	condition := findUDFWorkerCondition(cn.Status.UDFWorker.Conditions, v1alpha1.UDFWorkerConditionProvisioned)
	if condition == nil || condition.Status != metav1.ConditionFalse {
		t.Fatalf("provisioned condition = %#v, want false for an extra container", condition)
	}
}

func TestUDFWorkerStatusRejectsHostNamespaceAndHostPort(t *testing.T) {
	cn := &v1alpha1.CNSet{
		Spec: v1alpha1.CNSetSpec{
			PodSet:                 v1alpha1.PodSet{Replicas: 1},
			ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{UDFWorker: cnSetUDFWorkerPolicyForTest()},
		},
	}
	cs := &kruisev1alpha1.CloneSet{}
	if err := syncPodSpec(cn, cs, v1alpha1.SharedStorageProvider{}); err != nil {
		t.Fatal(err)
	}
	cs.Status.ReadyReplicas = 1
	cs.Status.UpdatedReadyReplicas = 1

	cs.Spec.Template.Spec.HostNetwork = true
	syncUDFWorkerStatus(cn, cs, true)
	if condition := findUDFWorkerCondition(cn.Status.UDFWorker.Conditions, v1alpha1.UDFWorkerConditionProvisioned); condition == nil || condition.Status != metav1.ConditionFalse {
		t.Fatalf("host-network provisioned condition = %#v, want false", condition)
	}

	cs.Spec.Template.Spec.HostNetwork = false
	cs.Spec.Template.Spec.Containers[0].Ports = []corev1.ContainerPort{{HostPort: 5001}}
	syncUDFWorkerStatus(cn, cs, true)
	if condition := findUDFWorkerCondition(cn.Status.UDFWorker.Conditions, v1alpha1.UDFWorkerConditionProvisioned); condition == nil || condition.Status != metav1.ConditionFalse {
		t.Fatalf("host-port provisioned condition = %#v, want false", condition)
	}
}

func TestUDFWorkerStatusIsStableAcrossReconcile(t *testing.T) {
	cn := &v1alpha1.CNSet{
		Spec: v1alpha1.CNSetSpec{
			PodSet:                 v1alpha1.PodSet{Replicas: 1},
			ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{UDFWorker: cnSetUDFWorkerPolicyForTest()},
		},
	}
	cs := &kruisev1alpha1.CloneSet{}
	if err := syncPodSpec(cn, cs, v1alpha1.SharedStorageProvider{}); err != nil {
		t.Fatal(err)
	}
	cs.Status.ReadyReplicas = 1
	cs.Status.UpdatedReadyReplicas = 1
	syncUDFWorkerStatus(cn, cs, true)
	want := append([]metav1.Condition(nil), cn.Status.UDFWorker.Conditions...)
	syncUDFWorkerStatus(cn, cs, true)
	if !reflect.DeepEqual(cn.Status.UDFWorker.Conditions, want) {
		t.Fatalf("conditions changed during an identical reconcile: before=%#v after=%#v", want, cn.Status.UDFWorker.Conditions)
	}
}

func TestUDFWorkerStatusGenerationIncludesWorkerPolicy(t *testing.T) {
	cn := &v1alpha1.CNSet{
		Spec: v1alpha1.CNSetSpec{
			PodSet:                 v1alpha1.PodSet{Replicas: 1},
			ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{UDFWorker: cnSetUDFWorkerPolicyForTest()},
		},
	}
	cs := &kruisev1alpha1.CloneSet{}
	if err := syncPodSpec(cn, cs, v1alpha1.SharedStorageProvider{}); err != nil {
		t.Fatal(err)
	}
	syncUDFWorkerStatus(cn, cs, false)
	first := cn.Status.UDFWorker.Generation
	if first == "" {
		t.Fatal("enabled UDF worker must publish a policy generation")
	}

	cn.Spec.UDFWorker.Worker.Image = "registry.example/udf-worker@sha256:" + strings.Repeat("e", 64)
	syncUDFWorkerStatus(cn, cs, false)
	second := cn.Status.UDFWorker.Generation
	if second == "" || second == first {
		t.Fatalf("worker policy change did not change generation: first=%q second=%q", first, second)
	}

	syncUDFWorkerStatus(cn, cs, false)
	if cn.Status.UDFWorker.Generation != second {
		t.Fatalf("identical policy changed generation: got=%q want=%q", cn.Status.UDFWorker.Generation, second)
	}
}

func TestUDFWorkerNetworkPolicyDeletionWaitsForOldWorkerPods(t *testing.T) {
	cn := &v1alpha1.CNSet{
		TypeMeta: metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "CNSet"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      "cn",
			UID:       "cn-uid",
		},
	}
	labels := common.SubResourceLabels(cn)
	labels[v1alpha1.UDFWorkerEnabledLabel] = v1alpha1.UDFWorkerEnabledValue
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       cn.Namespace,
			Name:            udfWorkerNetworkPolicyName(cn),
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(cn, v1alpha1.GroupVersion.WithKind("CNSet"))},
		},
	}
	oldPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: cn.Namespace,
		Name:      "old-worker-pod",
		Labels:    labels,
	}}
	cli := reconfake.KubeClientBuilder().WithScheme(newScheme()).WithObjects(np, oldPod).Build()
	ctx := reconfake.NewContext(cn, cli, nil)

	if err := (&Actor{}).syncUDFWorkerNetworkPolicy(ctx); err == nil {
		t.Fatal("disabling Python must wait while the old Worker Pod is live")
	}
	current := &networkingv1.NetworkPolicy{}
	if err := cli.Get(ctx, client.ObjectKeyFromObject(np), current); err != nil {
		t.Fatalf("NetworkPolicy was removed while old Worker Pod was live: %v", err)
	}

	delete(oldPod.Labels, v1alpha1.UDFWorkerEnabledLabel)
	oldPod.Spec.Containers = []corev1.Container{{Name: v1alpha1.ContainerUDFWorker}}
	if err := cli.Update(ctx, oldPod); err != nil {
		t.Fatal(err)
	}
	if err := (&Actor{}).syncUDFWorkerNetworkPolicy(ctx); err == nil {
		t.Fatal("cleanup must also detect a Worker Pod whose marker was removed")
	}

	if err := cli.Delete(ctx, oldPod); err != nil {
		t.Fatal(err)
	}
	if err := (&Actor{}).syncUDFWorkerNetworkPolicy(ctx); err != nil {
		t.Fatalf("remove NetworkPolicy after Worker Pod termination: %v", err)
	}
	if err := cli.Get(ctx, client.ObjectKeyFromObject(np), current); !apierrors.IsNotFound(err) {
		t.Fatalf("NetworkPolicy lookup after cleanup = %v, want NotFound", err)
	}
}

func TestHasExpectedUDFWorkerConfigRejectsStaleRoute(t *testing.T) {
	policy := cnSetUDFWorkerPolicyForTest()
	cn := &v1alpha1.CNSet{
		ObjectMeta: metav1.ObjectMeta{Name: "cn", Namespace: "ns"},
		Spec:       v1alpha1.CNSetSpec{ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{UDFWorker: policy}},
	}
	ls := &v1alpha1.LogSet{
		Spec:   v1alpha1.LogSetSpec{SharedStorage: v1alpha1.SharedStorageProvider{FileSystem: &v1alpha1.FileSystemProvider{Path: "/shared"}}},
		Status: v1alpha1.LogSetStatus{Discovery: &v1alpha1.LogSetDiscovery{Address: "log", Port: 6001}},
	}
	cm, suffix, err := buildCNSetConfigMap(cn, ls, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := common.ConfigFile
	if suffix != "" {
		key += "-" + suffix
	}
	if !hasExpectedUDFWorkerConfig(cm, key, policy) {
		t.Fatalf("controller-rendered typed UDF config was not accepted: %#v", cm.Data)
	}

	tampered := cm.DeepCopy()
	tampered.Data[key] = strings.Replace(tampered.Data[key], "127.0.0.1:50051", "0.0.0.0:50051", 1)
	if hasExpectedUDFWorkerConfig(tampered, key, policy) {
		t.Fatal("tampered worker route must not report client config ready")
	}

	unknown := cm.DeepCopy()
	unknown.Data[key] = strings.Replace(unknown.Data[key], "server-address = \"127.0.0.1:50051\"", "server-address = \"127.0.0.1:50051\"\ncustom-option = true", 1)
	if hasExpectedUDFWorkerConfig(unknown, key, policy) {
		t.Fatal("unknown typed client option must not report client config ready")
	}
	if hasExpectedUDFWorkerConfig(cm, common.ConfigFile+"-wrong", policy) {
		t.Fatal("wrong config key must not report client config ready")
	}
}

func TestUDFWorkerConfigReadyRequiresCNSetOwnedConfigMap(t *testing.T) {
	policy := cnSetUDFWorkerPolicyForTest()
	currentOperatorVersion := v1alpha1.LatestOpVersion.String()
	cn := &v1alpha1.CNSet{
		ObjectMeta: metav1.ObjectMeta{Name: "cn", Namespace: "ns", UID: "cn-uid"},
		Spec: v1alpha1.CNSetSpec{
			PodSet:                 v1alpha1.PodSet{Replicas: 1, OperatorVersion: &currentOperatorVersion},
			ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{UDFWorker: policy},
		},
	}
	ls := &v1alpha1.LogSet{
		Spec:   v1alpha1.LogSetSpec{SharedStorage: v1alpha1.SharedStorageProvider{FileSystem: &v1alpha1.FileSystemProvider{Path: "/shared"}}},
		Status: v1alpha1.LogSetStatus{Discovery: &v1alpha1.LogSetDiscovery{Address: "log", Port: 6001}},
	}
	cm, suffix, err := buildCNSetConfigMap(cn, ls, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs := &kruisev1alpha1.CloneSet{
		Spec: kruisev1alpha1.CloneSetSpec{Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}},
			Spec: corev1.PodSpec{Volumes: []corev1.Volume{{
				Name:         common.ConfigVolume,
				VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: cm.Name}}},
			}}},
		}},
		Status: kruisev1alpha1.CloneSetStatus{ReadyReplicas: 1, UpdatedReadyReplicas: 1},
	}
	if suffix != "" {
		cs.Spec.Template.Annotations[common.ConfigSuffixAnno] = suffix
	}
	foreign := &v1alpha1.CNSet{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: cn.Namespace, UID: "other-uid"}}
	cm.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(foreign, v1alpha1.GroupVersion.WithKind("CNSet"))}
	cli := reconfake.KubeClientBuilder().WithScheme(newScheme()).WithObjects(cm).Build()
	ctx := reconfake.NewContext(cn, cli, nil)
	ready, err := (&Actor{}).udfWorkerConfigReady(ctx, cs)
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Fatal("a foreign ConfigMap must not satisfy the Python client readiness gate")
	}
	if err := ensureUDFWorkerConfigOwnership(ctx, cm); err == nil || !strings.Contains(err.Error(), "UDFWorkerConfigMapNameConflict") {
		t.Fatalf("foreign ConfigMap ownership check = %v, want stable conflict", err)
	}
}

func TestUDFWorkerConfigOwnershipChecksLegacyDigestName(t *testing.T) {
	policy := cnSetUDFWorkerPolicyForTest()
	oldOperatorVersion := "1.2.0"
	cn := &v1alpha1.CNSet{
		ObjectMeta: metav1.ObjectMeta{Name: "cn", Namespace: "ns", UID: "cn-uid"},
		Spec: v1alpha1.CNSetSpec{
			PodSet:                 v1alpha1.PodSet{OperatorVersion: &oldOperatorVersion},
			ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{UDFWorker: policy},
		},
	}
	ls := &v1alpha1.LogSet{
		Spec:   v1alpha1.LogSetSpec{SharedStorage: v1alpha1.SharedStorageProvider{FileSystem: &v1alpha1.FileSystemProvider{Path: "/shared"}}},
		Status: v1alpha1.LogSetStatus{Discovery: &v1alpha1.LogSetDiscovery{Address: "log", Port: 6001}},
	}
	desired, _, err := buildCNSetConfigMap(cn, ls, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacyName, err := common.LegacyConfigMapName(desired)
	if err != nil {
		t.Fatal(err)
	}
	foreign := desired.DeepCopy()
	foreign.Name = legacyName
	foreign.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(
		&v1alpha1.CNSet{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: cn.Namespace, UID: "other-uid"}},
		v1alpha1.GroupVersion.WithKind("CNSet"),
	)}
	cli := reconfake.KubeClientBuilder().WithScheme(newScheme()).WithObjects(foreign).Build()
	ctx := reconfake.NewContext(cn, cli, nil)
	if err := ensureUDFWorkerConfigOwnership(ctx, desired); err == nil || !strings.Contains(err.Error(), "UDFWorkerConfigMapNameConflict") {
		t.Fatalf("legacy foreign ConfigMap ownership check = %v, want stable conflict", err)
	}
}

func findUDFWorkerCondition(conditions []metav1.Condition, typ string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == typ {
			return &conditions[i]
		}
	}
	return nil
}
