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
	helmchart "helm.sh/helm/v3/pkg/chart"
	helmchartutil "helm.sh/helm/v3/pkg/chartutil"
	helmrelease "helm.sh/helm/v3/pkg/release"

	helmv2 "github.com/fluxcd/helm-controller/api/v2beta2"
	"github.com/fluxcd/helm-controller/internal/postrender"
)

// Install runs the Helm install action with the provided config, using the
// v2beta2.HelmReleaseSpec of the given object to determine the target release
// and rollback configuration.
//
// It performs the installation according to the spec, which includes installing
// the CRDs according to the defined policy.
//
// It does not determine if there is a desire to perform the action, this is
// expected to be done by the caller. In addition, it does not take note of the
// action result. The caller is expected to listen on this using a
// storage.ObserveFunc, which provides superior access to Helm storage writes.
func Install(ctx context.Context, config *helmaction.Configuration, obj *helmv2.HelmRelease,
	chrt *helmchart.Chart, vals helmchartutil.Values) (*helmrelease.Release, error) {

	install, err := newInstall(config, obj)
	if err != nil {
		return nil, err
	}

	if err := installCRDs(config, obj, chrt, install); err != nil {
		return nil, err
	}

	return install.RunWithContext(ctx, chrt, vals.AsMap())
}

func newInstall(config *helmaction.Configuration, obj *helmv2.HelmRelease) (*helmaction.Install, error) {
	install := helmaction.NewInstall(config)

	install.ReleaseName = obj.GetReleaseName()
	install.Namespace = obj.GetReleaseNamespace()
	install.Timeout = obj.Spec.GetInstall().GetTimeout(obj.GetTimeout()).Duration
	install.Wait = !obj.Spec.GetInstall().DisableWait
	install.WaitForJobs = !obj.Spec.GetInstall().DisableWaitForJobs
	install.DisableHooks = obj.Spec.GetInstall().DisableHooks
	install.DisableOpenAPIValidation = obj.Spec.GetInstall().DisableOpenAPIValidation
	install.Replace = obj.Spec.GetInstall().Replace
	install.Devel = true

	if obj.Spec.TargetNamespace != "" {
		install.CreateNamespace = obj.Spec.GetInstall().CreateNamespace
	}

	renderer, err := postrender.BuildPostRenderers(obj)
	if err != nil {
		return nil, err
	}
	install.PostRenderer = renderer

	return install, nil
}

func installCRDs(config *helmaction.Configuration, obj *helmv2.HelmRelease, chrt *helmchart.Chart, install *helmaction.Install) error {
	policy, err := crdPolicyOrDefault(obj.Spec.GetInstall().CRDs)
	if err != nil {
		return err
	}
	if policy == helmv2.Skip || policy == helmv2.CreateReplace {
		install.SkipCRDs = true
	}
	if policy == helmv2.CreateReplace {
		crds := chrt.CRDObjects()
		if len(crds) > 0 {
			if err := applyCRDs(config, policy, chrt); err != nil {
				return err
			}
		}
	}
	return nil
}
