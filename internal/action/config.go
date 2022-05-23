package action

import (
	"context"

	"helm.sh/helm/v3/pkg/action"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/fluxcd/pkg/runtime/client"

	helmv2 "github.com/fluxcd/helm-controller/api/v2beta1"
	"github.com/fluxcd/helm-controller/internal/kube"
	"github.com/fluxcd/helm-controller/internal/runner"
	"github.com/fluxcd/helm-controller/internal/storage"
)

const (
	defaultHelmStorageDriver = "secret"
)

type Configuration struct {
	Getter           genericclioptions.RESTClientGetter
	StorageNamespace string
}

func (c *Configuration) newObservingConfig(ctx context.Context, observers ...storage.ObserveFunc) (*action.Configuration, *storage.Observer, error) {
	log := ctrl.LoggerFrom(ctx)
	bufLog := runner.NewLogBuffer(runner.NewDebugLog(log).Log, 5)

	config := new(action.Configuration)

	if err := config.Init(c.Getter, c.StorageNamespace, defaultHelmStorageDriver, bufLog.Log); err != nil {
		return nil, nil, err
	}

	observer := storage.NewObserver(config.Releases.Driver, observers...)
	config.Releases.Driver = observer

	return config, observer, nil
}

func GetConfig(rel *helmv2.HelmRelease, clientOpts client.Options, secret *corev1.Secret, kubeCfgOpts client.KubeConfigOptions) (*Configuration, error) {
	opts := []kube.ClientGetterOption{kube.WithClientOptions(clientOpts)}
	if rel.Spec.ServiceAccountName != "" {
		opts = append(opts, kube.WithImpersonate(rel.Spec.ServiceAccountName))
	}
	if secret != nil {
		kubeConfig, err := kube.ConfigFromSecret(secret, rel.Spec.KubeConfig.SecretRef.Key)
		if err != nil {
			return nil, err
		}
		opts = append(opts, kube.WithKubeConfig(kubeConfig, kubeCfgOpts))
	}
	getter, err := kube.BuildClientGetter(rel.GetReleaseNamespace(), opts...)
	if err != nil {
		return nil, err
	}
	return &Configuration{Getter: getter, StorageNamespace: rel.GetStorageNamespace()}, nil
}
