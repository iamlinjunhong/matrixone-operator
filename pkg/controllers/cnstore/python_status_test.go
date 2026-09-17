// Copyright 2026 Matrix Origin
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cnstore

import (
	"context"
	"testing"

	"github.com/matrixorigin/matrixone-operator/api/core/v1alpha1"
	"github.com/matrixorigin/matrixone-operator/pkg/querycli"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestQueryPythonUDFStatusRequiresCurrentRuntimePolicy(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "cn-0", Namespace: "ns", UID: "pod-uid"},
		Spec:       corev1.PodSpec{Subdomain: "cn-headless"},
	}
	cn := &v1alpha1.CNSet{Spec: v1alpha1.CNSetSpec{
		ConfigThatChangeCNSpec: v1alpha1.ConfigThatChangeCNSpec{
			UDFWorker: &v1alpha1.UDFWorkerPolicy{Enabled: true},
		},
	}}
	expectedUID := v1alpha1.GetCNPodUUID(pod)
	tests := []struct {
		name       string
		status     *querycli.PythonUDFStatus
		wantReady  bool
		wantError  string
		wantReason string
	}{
		{
			name: "enabled and unisolated",
			status: &querycli.PythonUDFStatus{
				CNUUID: expectedUID, Language: "python", Enabled: true,
				AllowUnisolated: true, Ready: true, LeaseEpoch: 1,
			},
			wantReady: true,
		},
		{
			name: "runtime disabled",
			status: &querycli.PythonUDFStatus{
				CNUUID: expectedUID, Language: "python", Enabled: false,
				AllowUnisolated: true, Ready: true, LeaseEpoch: 1,
			},
			wantError:  v1alpha1.UDFWorkerStatusErrorPolicyMismatch,
			wantReason: v1alpha1.UDFWorkerStatusReasonPolicyMismatch,
		},
		{
			name: "unisolated gate denied",
			status: &querycli.PythonUDFStatus{
				CNUUID: expectedUID, Language: "python", Enabled: true,
				AllowUnisolated: false, Ready: true, LeaseEpoch: 1,
			},
			wantError:  v1alpha1.UDFWorkerStatusErrorPolicyMismatch,
			wantReason: v1alpha1.UDFWorkerStatusReasonPolicyMismatch,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &withCNSet{
				Controller: &Controller{queryCli: &fakeQueryClient{pythonStatus: tt.status}},
				cn:         cn,
			}
			got := c.queryPythonUDFStatus(context.Background(), pod, "cn-0:6002")
			if got.Ready != tt.wantReady {
				t.Fatalf("ready = %v, want %v; status = %#v", got.Ready, tt.wantReady, got)
			}
			if got.ErrorClass != tt.wantError {
				t.Fatalf("error class = %q, want %q; status = %#v", got.ErrorClass, tt.wantError, got)
			}
			if got.Reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q; status = %#v", got.Reason, tt.wantReason, got)
			}
		})
	}
}
