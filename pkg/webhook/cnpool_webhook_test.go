// Copyright 2025-2026 Matrix Origin
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

package webhook

import (
	"strings"
	"testing"

	"github.com/matrixorigin/matrixone-operator/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestCNPoolWebhookValidatesNestedUDFWorkerPolicy(t *testing.T) {
	p := &v1alpha1.CNPool{}
	p.Spec.Template.UDFWorker = webhookTestPolicy()
	p.Spec.Template.MainContainer.Image = "matrixone:1.2.0"
	p.Spec.Template.SemanticVersion = ptrString("1.2.0")
	p.Spec.Deps.ExternalLogSet = &v1alpha1.ExternalLogSet{HAKeeperEndpoint: "log:32001"}
	p.Spec.Template.Resources = corev1.ResourceRequirements{Limits: corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi"),
	}}
	// Remove the unrelated required PodSet failure so the assertion below
	// specifically exercises the nested policy path.
	if errs := (&cnPoolValidator{}).validate(p); len(errs) != 0 {
		t.Fatalf("valid nested policy rejected: %v", errs)
	}

	p.Spec.Template.UDFWorker.Launcher = v1alpha1.UDFWorkerLauncherMOService
	errs := (&cnPoolValidator{}).validate(p)
	if len(errs) == 0 || !strings.Contains(errs.ToAggregate().Error(), "UnsupportedSamePodMoServiceLauncher") {
		t.Fatalf("nested policy errors = %v", errs)
	}
}

func ptrString(v string) *string { return &v }
