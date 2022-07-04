/*
Copyright 2022 The Flux authors

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

package action

import (
	"context"

	helmaction "helm.sh/helm/v3/pkg/action"
	helmrelease "helm.sh/helm/v3/pkg/release"

	helmv2 "github.com/fluxcd/helm-controller/api/v2beta2"
)

// Uninstall runs the Helm uninstall action with the provided config, using the
// v2beta2.HelmReleaseSpec of the given object to determine the target release
// and uninstall configuration.
//
// It does not determine if there is a desire to perform the action, this is
// expected to be done by the caller. In addition, it does not take note of the
// action result. The caller is expected to listen on this using a
// storage.ObserveFunc, which provides superior access to Helm storage writes.
func Uninstall(ctx context.Context, config *helmaction.Configuration, obj *helmv2.HelmRelease) (*helmrelease.UninstallReleaseResponse, error) {
	uninstall := newUninstall(config, obj)
	return uninstall.Run(obj.GetReleaseName())
}

func newUninstall(config *helmaction.Configuration, obj *helmv2.HelmRelease) *helmaction.Uninstall {
	uninstall := helmaction.NewUninstall(config)

	uninstall.Timeout = obj.Spec.GetUninstall().GetTimeout(obj.GetTimeout()).Duration
	uninstall.DisableHooks = obj.Spec.GetUninstall().DisableHooks
	uninstall.KeepHistory = obj.Spec.GetUninstall().KeepHistory
	uninstall.Wait = !obj.Spec.GetUninstall().DisableWait

	return uninstall
}
