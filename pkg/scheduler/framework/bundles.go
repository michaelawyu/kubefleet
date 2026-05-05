/*
Copyright 2026 The KubeFleet Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package framework

import (
	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
)

type ObservedBindingState string

const (
	ObservedBindingStateBound       ObservedBindingState = "bound"
	ObservedBindingStateScheduled   ObservedBindingState = "scheduled"
	ObservedBindingStateUnscheduled ObservedBindingState = "unscheduled"
	ObservedBindingStateObsolete    ObservedBindingState = "obsolete"
	ObservedBindingStateDangling    ObservedBindingState = "dangling"
	ObservedBindingStateDeleting    ObservedBindingState = "deleting"
	ObservedBindingStatePlaceholder ObservedBindingState = "placeholder"
	ObservedBindingStateUnknown     ObservedBindingState = "unknown"
)

type BindingActionType string

const (
	BindingActionTypeCreate          BindingActionType = "create"
	BindingActionTypeRefresh         BindingActionType = "refresh"
	BindingActionTypeUnschedule      BindingActionType = "unschedule"
	BindingActionTypeNoOpNeeded      BindingActionType = "noOpNeeded"
	BindingActionTypeRemoveFinalizer BindingActionType = "removeFinalizer"
)

type BindingProcessingBundle struct {
	ClusterName string

	Binding      placementv1beta1.BindingObj
	CurrentState ObservedBindingState

	PatchedBinding placementv1beta1.BindingObj
	Action         BindingActionType
}

type BindingProcessingBundleReaderWriter interface {
	GetBinding() placementv1beta1.BindingObj
	GetCurrentState() ObservedBindingState

	GetPatchedBinding() placementv1beta1.BindingObj
	GetAction() BindingActionType

	SetPatchedBinding(binding placementv1beta1.BindingObj)
	SetAction(action BindingActionType)
}

func (b *BindingProcessingBundle) GetBinding() placementv1beta1.BindingObj {
	if b.Binding == nil {
		return nil
	}
	return b.Binding.DeepCopyObject().(placementv1beta1.BindingObj)
}

func (b *BindingProcessingBundle) GetCurrentState() ObservedBindingState {
	return b.CurrentState
}

func (b *BindingProcessingBundle) GetPatchedBinding() placementv1beta1.BindingObj {
	if b.PatchedBinding == nil {
		return nil
	}
	return b.PatchedBinding.DeepCopyObject().(placementv1beta1.BindingObj)
}

func (b *BindingProcessingBundle) GetAction() BindingActionType {
	return b.Action
}

func (b *BindingProcessingBundle) SetPatchedBinding(binding placementv1beta1.BindingObj) {
	b.PatchedBinding = binding.DeepCopyObject().(placementv1beta1.BindingObj)
}

func (b *BindingProcessingBundle) SetAction(action BindingActionType) {
	b.Action = action
}

type AllBindingProcessingBundles map[string]*BindingProcessingBundle
