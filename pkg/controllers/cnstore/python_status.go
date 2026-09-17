// Copyright 2026 Matrix Origin
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cnstore

import (
	"context"
	"encoding/json"

	"github.com/go-errors/errors"
	recon "github.com/matrixorigin/controller-runtime/pkg/reconciler"
	"github.com/matrixorigin/matrixone-operator/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const pythonLanguage = "python"

func (c *withCNSet) queryPythonUDFStatus(ctx context.Context, pod *corev1.Pod, address string) v1alpha1.UDFWorkerPodStatus {
	status := c.unavailablePythonUDFStatus(pod)
	if c.cn == nil || !c.cn.Spec.UDFWorker.IsEnabled() {
		return status
	}
	if ctx == nil {
		ctx = context.Background()
	}
	observed, err := c.queryCli.GetPythonUdfStatus(ctx, address)
	if err != nil {
		return status
	}
	status.CNUUID = observed.CNUUID
	status.Ready = observed.Ready
	status.ErrorClass = observed.ErrorClass
	status.Reason = observed.Reason
	status.ProtocolVersion = observed.ProtocolVersion
	status.ABIContract = observed.ABIContract
	status.AdapterVersion = observed.AdapterVersion
	status.SDKVersion = observed.SDKVersion
	status.DefinitionSchemaVersion = observed.DefinitionSchemaVersion
	status.PlanContractVersion = observed.PlanContractVersion
	status.TypeDescriptorContract = observed.TypeDescriptorContract
	status.TimezoneDatabaseVersion = observed.TimezoneDatabaseVersion
	status.WindowBatches = observed.WindowBatches
	status.CumulativeAck = observed.CumulativeAck
	status.MaxExecutionFrameBytes = observed.MaxExecutionFrameBytes
	status.MaxHandlerProcesses = observed.MaxHandlerProcesses
	status.MaxAccountHandlerProcesses = observed.MaxAccountHandlerProcesses
	status.MaxOwnerHandlerProcesses = observed.MaxOwnerHandlerProcesses
	status.LeaseEpoch = observed.LeaseEpoch
	status.Modes = append([]string(nil), observed.Modes...)
	status.NullPolicies = append([]string(nil), observed.NullPolicies...)
	status.ObservedAt = metav1.Now()

	expectedUID := v1alpha1.GetCNPodUUID(pod)
	if observed.CNUUID != expectedUID || observed.Language != pythonLanguage ||
		(observed.Ready && observed.LeaseEpoch == 0) ||
		(observed.Ready && observed.ErrorClass != "") {
		status.Ready = false
		status.ErrorClass = v1alpha1.UDFWorkerStatusErrorInvalid
		status.Reason = v1alpha1.UDFWorkerStatusReasonInvalid
		if observed.CNUUID != expectedUID {
			status.ErrorClass = v1alpha1.UDFWorkerStatusErrorIdentityMismatch
			status.Reason = v1alpha1.UDFWorkerStatusReasonIdentityMismatch
		}
	}
	return status
}

func (c *withCNSet) unavailablePythonUDFStatus(pod *corev1.Pod) v1alpha1.UDFWorkerPodStatus {
	status := v1alpha1.UDFWorkerPodStatus{
		PodUID:     string(pod.UID),
		CNUUID:     v1alpha1.GetCNPodUUID(pod),
		Ready:      false,
		ErrorClass: v1alpha1.UDFWorkerStatusErrorQueryUnavailable,
		Reason:     v1alpha1.UDFWorkerStatusReasonQueryUnavailable,
		ObservedAt: metav1.Now(),
	}
	if c.cn != nil {
		status.Generation = v1alpha1.UDFWorkerPolicyGeneration(c.cn.Spec.UDFWorker)
	}
	if c.cn == nil || !c.cn.Spec.UDFWorker.IsEnabled() {
		status.ErrorClass = ""
		status.Reason = "Disabled"
	}
	return status
}

func (c *withCNSet) patchPythonUDFStatus(ctx *recon.Context[*corev1.Pod], status v1alpha1.UDFWorkerPodStatus) error {
	if c.cn == nil || !c.cn.Spec.UDFWorker.IsEnabled() {
		if _, ok := ctx.Obj.Annotations[v1alpha1.UDFWorkerStatusAnno]; !ok {
			return nil
		}
		return ctx.Patch(ctx.Obj, func() error {
			delete(ctx.Obj.Annotations, v1alpha1.UDFWorkerStatusAnno)
			return nil
		})
	}
	payload, err := json.Marshal(status)
	if err != nil {
		return errors.WrapPrefix(err, "marshal Python UDF status", 0)
	}
	return ctx.Patch(ctx.Obj, func() error {
		if ctx.Obj.Annotations == nil {
			ctx.Obj.Annotations = map[string]string{}
		}
		ctx.Obj.Annotations[v1alpha1.UDFWorkerStatusAnno] = string(payload)
		return nil
	})
}
