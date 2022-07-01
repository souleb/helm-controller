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

package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/fluxcd/helm-controller/api/v2beta1"
	sourcev1 "github.com/fluxcd/source-controller/api/v1beta2"
)

func TestHelmReleaseChartReconciler_Reconcile(t *testing.T) {
	g := NewWithT(t)

	scheme := runtime.NewScheme()
	g.Expect(sourcev1.AddToScheme(scheme)).To(Succeed())
	g.Expect(v2beta1.AddToScheme(scheme)).To(Succeed())

	t.Run("reconciles ChartTemplate", func(t *testing.T) {
		g := NewWithT(t)

		chartSpecTemplate := v2beta1.HelmChartTemplateSpec{
			Chart: "chart",
			SourceRef: v2beta1.CrossNamespaceObjectReference{
				Kind: sourcev1.HelmRepositoryKind,
				Name: "repository",
			},
		}
		obj := &v2beta1.HelmRelease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "release",
				Namespace: "default",
			},
			Spec: v2beta1.HelmReleaseSpec{
				Interval: metav1.Duration{Duration: 1 * time.Millisecond},
				Chart: v2beta1.HelmChartTemplate{
					Spec: chartSpecTemplate,
				},
			},
		}
		controllerutil.AddFinalizer(obj, v2beta1.ChartFinalizer)

		builder := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(obj)
		r := &HelmReleaseChartReconciler{
			Client:        builder.Build(),
			EventRecorder: record.NewFakeRecorder(32),
		}

		key := types.NamespacedName{
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
		}
		got, err := r.Reconcile(ctrl.LoggerInto(context.TODO(), logr.Discard()), reconcile.Request{NamespacedName: key})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(got).To(Equal(ctrl.Result{RequeueAfter: obj.Spec.Interval.Duration}))

		g.Expect(r.Client.Get(context.TODO(), key, obj)).To(Succeed())
		g.Expect(obj.Status.HelmChart).ToNot(BeEmpty())

		chartNs, chartName := obj.Status.GetHelmChart()
		var chartObj sourcev1.HelmChart
		g.Expect(r.Client.Get(context.TODO(), types.NamespacedName{Namespace: chartNs, Name: chartName}, &chartObj)).To(Succeed())
		g.Expect(chartObj.Spec.Chart).To(Equal(obj.Spec.Chart.Spec.Chart))
	})

	t.Run("HelmRelease NotFound", func(t *testing.T) {
		g := NewWithT(t)

		builder := fake.NewClientBuilder().
			WithScheme(scheme)
		r := &HelmReleaseChartReconciler{
			Client:        builder.Build(),
			EventRecorder: record.NewFakeRecorder(32),
		}

		key := types.NamespacedName{
			Name:      "not",
			Namespace: "found",
		}
		got, err := r.Reconcile(ctrl.LoggerInto(context.TODO(), logr.Discard()), reconcile.Request{NamespacedName: key})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(got).To(Equal(ctrl.Result{}))
	})

	t.Run("HelmRelease suspended", func(t *testing.T) {
		g := NewWithT(t)

		obj := &v2beta1.HelmRelease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "release",
				Namespace: "default",
			},
			Spec: v2beta1.HelmReleaseSpec{
				Suspend: true,
			},
		}

		builder := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(obj)
		r := &HelmReleaseChartReconciler{
			Client:        builder.Build(),
			EventRecorder: record.NewFakeRecorder(32),
		}

		key := types.NamespacedName{
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
		}
		got, err := r.Reconcile(ctrl.LoggerInto(context.TODO(), logr.Discard()), reconcile.Request{NamespacedName: key})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(got).To(Equal(ctrl.Result{}))
	})

	t.Run("finalizer set before start reconciliation", func(t *testing.T) {
		g := NewWithT(t)

		obj := &v2beta1.HelmRelease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "release",
				Namespace: "default",
			},
		}

		builder := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(obj)
		r := &HelmReleaseChartReconciler{
			Client:        builder.Build(),
			EventRecorder: record.NewFakeRecorder(32),
		}

		key := types.NamespacedName{
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
		}
		got, err := r.Reconcile(ctrl.LoggerInto(context.TODO(), logr.Discard()), reconcile.Request{NamespacedName: key})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(got).To(Equal(ctrl.Result{Requeue: true}))

		g.Expect(r.Client.Get(context.TODO(), key, obj)).To(Succeed())
		g.Expect(controllerutil.ContainsFinalizer(obj, v2beta1.ChartFinalizer)).To(BeTrue())
	})

	t.Run("DeletionTimestamp triggers delete", func(t *testing.T) {
		g := NewWithT(t)

		now := metav1.Now()

		obj := &v2beta1.HelmRelease{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "release",
				Namespace:         "default",
				DeletionTimestamp: &now,
			},
			Status: v2beta1.HelmReleaseStatus{
				HelmChart: "default/does-not-exist",
			},
		}
		controllerutil.AddFinalizer(obj, sourcev1.SourceFinalizer)
		controllerutil.AddFinalizer(obj, v2beta1.ChartFinalizer)

		builder := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(obj)
		r := &HelmReleaseChartReconciler{
			Client:        builder.Build(),
			EventRecorder: record.NewFakeRecorder(32),
		}

		key := types.NamespacedName{
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
		}
		got, err := r.Reconcile(ctrl.LoggerInto(context.TODO(), logr.Discard()), reconcile.Request{NamespacedName: key})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(got).To(Equal(ctrl.Result{}))

		g.Expect(r.Client.Get(context.TODO(), key, obj)).To(Succeed())
		g.Expect(obj.Status.HelmChart).To(BeEmpty())
		g.Expect(controllerutil.ContainsFinalizer(obj, v2beta1.ChartFinalizer)).To(BeFalse())
	})
}

func TestHelmReleaseChartReconciler_reconcile(t *testing.T) {
	g := NewWithT(t)

	scheme := runtime.NewScheme()
	g.Expect(sourcev1.AddToScheme(scheme)).To(Succeed())

	t.Run("Status.HelmChart divergence triggers delete and requeue", func(t *testing.T) {
		g := NewWithT(t)

		builder := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(&sourcev1.HelmChart{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "chart",
				},
			})

		r := &HelmReleaseChartReconciler{
			Client:        builder.Build(),
			EventRecorder: record.NewFakeRecorder(32),
		}

		obj := &v2beta1.HelmRelease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "release",
				Namespace: "default",
			},
			Status: v2beta1.HelmReleaseStatus{
				HelmChart: "default/chart",
			},
		}
		got, err := r.reconcile(context.TODO(), obj)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(got).To(Equal(ctrl.Result{Requeue: true}))
		g.Expect(obj.Status.HelmChart).To(BeEmpty())
	})

	t.Run("HelmChart NotFound creates HelmChart", func(t *testing.T) {
		g := NewWithT(t)

		builder := fake.NewClientBuilder().
			WithScheme(scheme)

		recorder := record.NewFakeRecorder(32)
		r := &HelmReleaseChartReconciler{
			Client:        builder.Build(),
			EventRecorder: recorder,
		}

		obj := &v2beta1.HelmRelease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "release",
				Namespace: "default",
			},
			Spec: v2beta1.HelmReleaseSpec{
				Chart: v2beta1.HelmChartTemplate{
					Spec: v2beta1.HelmChartTemplateSpec{
						SourceRef: v2beta1.CrossNamespaceObjectReference{
							Name: "foo",
						},
					},
				},
			},
			Status: v2beta1.HelmReleaseStatus{
				HelmChart: "default/default-release",
			},
		}
		got, err := r.reconcile(context.TODO(), obj)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(got).To(Equal(ctrl.Result{RequeueAfter: obj.GetRequeueAfter()}))
		g.Expect(obj.Status.HelmChart).ToNot(BeEmpty())

		g.Expect(r.Client.Get(context.TODO(), types.NamespacedName{
			Namespace: obj.Spec.Chart.GetNamespace(obj.Namespace),
			Name:      obj.GetHelmChartName()},
			&sourcev1.HelmChart{},
		)).To(Succeed())
	})

	t.Run("Spec divergence updates HelmChart", func(t *testing.T) {
		g := NewWithT(t)

		builder := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(&sourcev1.HelmChart{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default-release",
					Namespace: "default",
				},
				Spec: sourcev1.HelmChartSpec{
					Chart: "foo",
					SourceRef: sourcev1.LocalHelmChartSourceReference{
						Kind: sourcev1.HelmRepositoryKind,
						Name: "foo-repository",
					},
				}})

		recorder := record.NewFakeRecorder(32)
		r := &HelmReleaseChartReconciler{
			Client:        builder.Build(),
			EventRecorder: recorder,
		}

		obj := &v2beta1.HelmRelease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "release",
				Namespace: "default",
			},
			Spec: v2beta1.HelmReleaseSpec{
				Chart: v2beta1.HelmChartTemplate{
					Spec: v2beta1.HelmChartTemplateSpec{
						Chart: "./foo",
						SourceRef: v2beta1.CrossNamespaceObjectReference{
							Kind: sourcev1.HelmRepositoryKind,
							Name: "foo-repository",
						},
					},
				},
			},
			Status: v2beta1.HelmReleaseStatus{
				HelmChart: "default/default-release",
			},
		}
		got, err := r.reconcile(context.TODO(), obj)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(got).To(Equal(ctrl.Result{RequeueAfter: obj.GetRequeueAfter()}))
		g.Expect(obj.Status.HelmChart).ToNot(BeEmpty())

		chart := sourcev1.HelmChart{}
		g.Expect(r.Client.Get(context.TODO(), types.NamespacedName{
			Namespace: obj.Spec.Chart.GetNamespace(obj.Namespace),
			Name:      obj.GetHelmChartName()}, &chart)).To(Succeed())
		g.Expect(chart.Spec.Chart).To(Equal(obj.Spec.Chart.Spec.Chart))
		g.Expect(chart.Spec.SourceRef.Name).To(Equal(obj.Spec.Chart.Spec.SourceRef.Name))
		g.Expect(chart.Spec.SourceRef.Kind).To(Equal(obj.Spec.Chart.Spec.SourceRef.Kind))
	})

	t.Run("no HelmChart divergence", func(t *testing.T) {
		g := NewWithT(t)

		chart := &sourcev1.HelmChart{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "default-release",
				Namespace: "default",
			},
			Spec: sourcev1.HelmChartSpec{
				Chart: "foo",
				SourceRef: sourcev1.LocalHelmChartSourceReference{
					Kind: sourcev1.HelmRepositoryKind,
					Name: "foo-repository",
				},
			},
		}
		builder := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(chart)

		recorder := record.NewFakeRecorder(32)
		r := &HelmReleaseChartReconciler{
			Client:        builder.Build(),
			EventRecorder: recorder,
		}

		obj := &v2beta1.HelmRelease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "release",
				Namespace: "default",
			},
			Spec: v2beta1.HelmReleaseSpec{
				Chart: v2beta1.HelmChartTemplate{
					Spec: v2beta1.HelmChartTemplateSpec{
						Chart: chart.Spec.Chart,
						SourceRef: v2beta1.CrossNamespaceObjectReference{
							Kind: chart.Spec.SourceRef.Kind,
							Name: chart.Spec.SourceRef.Name,
						},
					},
				},
			},
			Status: v2beta1.HelmReleaseStatus{
				HelmChart: "default/default-release",
			},
		}
		got, err := r.reconcile(context.TODO(), obj)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(got).To(Equal(ctrl.Result{RequeueAfter: obj.GetRequeueAfter()}))
		g.Expect(obj.Status.HelmChart).ToNot(BeEmpty())
	})

	t.Run("disallow cross namespace access", func(t *testing.T) {
		g := NewWithT(t)

		r := &HelmReleaseChartReconciler{
			Client:              fake.NewClientBuilder().WithScheme(scheme).Build(),
			NoCrossNamespaceRef: true,
		}

		obj := &v2beta1.HelmRelease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "release",
				Namespace: "default",
			},
			Spec: v2beta1.HelmReleaseSpec{
				Chart: v2beta1.HelmChartTemplate{
					Spec: v2beta1.HelmChartTemplateSpec{
						SourceRef: v2beta1.CrossNamespaceObjectReference{
							Name:      "chart",
							Namespace: "other",
						},
					},
				},
			},
			Status: v2beta1.HelmReleaseStatus{},
		}
		got, err := r.reconcile(context.TODO(), obj)
		g.Expect(err).To(HaveOccurred())
		g.Expect(got).To(Equal(ctrl.Result{}))
		g.Expect(obj.Status.HelmChart).To(BeEmpty())

		err = r.Client.Get(context.TODO(), types.NamespacedName{Namespace: "other", Name: "chart"}, &sourcev1.HelmChart{})
		g.Expect(err).To(HaveOccurred())
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})
}

func TestHelmReleaseChartReconciler_reconcileDelete(t *testing.T) {
	g := NewWithT(t)

	scheme := runtime.NewScheme()
	g.Expect(sourcev1.AddToScheme(scheme)).To(Succeed())
	now := metav1.Now()

	t.Run("Status.HelmChart is deleted", func(t *testing.T) {
		g := NewWithT(t)

		builder := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(&sourcev1.HelmChart{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "chart",
				},
			})

		recorder := record.NewFakeRecorder(32)
		r := &HelmReleaseChartReconciler{
			Client:        builder.Build(),
			EventRecorder: recorder,
		}

		obj := &v2beta1.HelmRelease{
			Status: v2beta1.HelmReleaseStatus{
				HelmChart: "default/chart",
			},
		}
		got, err := r.reconcileDelete(context.TODO(), obj)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(got).To(Equal(ctrl.Result{Requeue: true}))
		g.Expect(obj.Status.HelmChart).To(BeEmpty())

		err = r.Client.Get(context.TODO(), types.NamespacedName{Namespace: "default", Name: "chart"}, &sourcev1.HelmChart{})
		g.Expect(err).To(HaveOccurred())
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	t.Run("Status.HelmChart already deleted", func(t *testing.T) {
		g := NewWithT(t)

		r := &HelmReleaseChartReconciler{
			Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		}
		obj := &v2beta1.HelmRelease{
			ObjectMeta: metav1.ObjectMeta{
				Finalizers: []string{v2beta1.ChartFinalizer},
			},
			Status: v2beta1.HelmReleaseStatus{
				HelmChart: "default/chart",
			},
		}
		got, err := r.reconcileDelete(context.TODO(), obj)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(got).To(Equal(ctrl.Result{Requeue: true}))
		g.Expect(obj.Status.HelmChart).To(BeEmpty())
		g.Expect(obj.Finalizers).To(ContainElement(v2beta1.ChartFinalizer))
	})

	t.Run("DeletionTimestamp removes finalizer", func(t *testing.T) {
		g := NewWithT(t)

		r := &HelmReleaseChartReconciler{
			Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		}
		obj := &v2beta1.HelmRelease{
			ObjectMeta: metav1.ObjectMeta{
				DeletionTimestamp: &now,
				Finalizers:        []string{v2beta1.ChartFinalizer},
			},
			Status: v2beta1.HelmReleaseStatus{
				HelmChart: "default/chart",
			},
		}
		got, err := r.reconcileDelete(context.TODO(), obj)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(got).To(Equal(ctrl.Result{}))
		g.Expect(obj.Status.HelmChart).To(BeEmpty())
		g.Expect(obj.Finalizers).ToNot(ContainElement(v2beta1.ChartFinalizer))
	})

	t.Run("disallow cross namespace access", func(t *testing.T) {
		g := NewWithT(t)

		chart := &sourcev1.HelmChart{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "other",
				Name:      "chart",
			},
		}
		builder := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(chart)

		r := &HelmReleaseChartReconciler{
			Client:              builder.Build(),
			NoCrossNamespaceRef: true,
		}

		obj := &v2beta1.HelmRelease{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
			},
			Status: v2beta1.HelmReleaseStatus{
				HelmChart: "other/chart",
			},
		}
		got, err := r.reconcileDelete(context.TODO(), obj)
		g.Expect(err).To(HaveOccurred())
		g.Expect(got).To(Equal(ctrl.Result{}))
		g.Expect(obj.Status.HelmChart).ToNot(BeEmpty())
		g.Expect(r.Client.Get(context.TODO(), types.NamespacedName{Namespace: chart.Namespace, Name: chart.Name}, &sourcev1.HelmChart{})).To(Succeed())
	})

	t.Run("empty Status.HelmChart", func(t *testing.T) {
		g := NewWithT(t)

		r := &HelmReleaseChartReconciler{
			Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		}
		obj := &v2beta1.HelmRelease{
			Status: v2beta1.HelmReleaseStatus{},
		}
		got, err := r.reconcileDelete(context.TODO(), obj)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(got).To(Equal(ctrl.Result{Requeue: true}))
	})
}

func TestHelmReleaseChartReconciler_aclAllowAccessTo(t *testing.T) {
	tests := []struct {
		name           string
		obj            *v2beta1.HelmRelease
		namespacedName types.NamespacedName
		allowCrossNS   bool
		wantErr        bool
	}{
		{
			name: "disallow cross namespace",
			obj: &v2beta1.HelmRelease{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "a",
				},
			},
			namespacedName: types.NamespacedName{
				Namespace: "b",
				Name:      "foo",
			},
			allowCrossNS: false,
			wantErr:      true,
		},
		{
			name: "allow cross namespace",
			obj: &v2beta1.HelmRelease{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "a",
				},
			},
			namespacedName: types.NamespacedName{
				Namespace: "b",
				Name:      "foo",
			},
			allowCrossNS: true,
		},
		{
			name: "same namespace disallow cross namespace",
			obj: &v2beta1.HelmRelease{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "a",
				},
			},
			namespacedName: types.NamespacedName{
				Namespace: "a",
				Name:      "foo",
			},
			allowCrossNS: false,
			wantErr:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			r := &HelmReleaseChartReconciler{
				NoCrossNamespaceRef: !tt.allowCrossNS,
			}
			err := r.aclAllowAccessTo(tt.obj, tt.namespacedName)
			g.Expect(err != nil).To(Equal(tt.wantErr), err)
		})
	}
}

func Test_buildHelmChartFromTemplate(t *testing.T) {
	hrWithChartTemplate := v2beta1.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-release",
			Namespace: "default",
		},
		Spec: v2beta1.HelmReleaseSpec{
			Interval: metav1.Duration{Duration: time.Minute},
			Chart: v2beta1.HelmChartTemplate{
				Spec: v2beta1.HelmChartTemplateSpec{
					Chart:   "chart",
					Version: "1.0.0",
					SourceRef: v2beta1.CrossNamespaceObjectReference{
						Name: "test-repository",
						Kind: "HelmRepository",
					},
					Interval:    &metav1.Duration{Duration: 2 * time.Minute},
					ValuesFiles: []string{"values.yaml"},
				},
			},
		},
	}

	tests := []struct {
		name   string
		modify func(release *v2beta1.HelmRelease)
		want   *sourcev1.HelmChart
	}{
		{
			name:   "builds HelmChart from HelmChartTemplate",
			modify: func(*v2beta1.HelmRelease) {},
			want: &sourcev1.HelmChart{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default-test-release",
					Namespace: "default",
				},
				Spec: sourcev1.HelmChartSpec{
					Chart:   "chart",
					Version: "1.0.0",
					SourceRef: sourcev1.LocalHelmChartSourceReference{
						Name: "test-repository",
						Kind: "HelmRepository",
					},
					Interval:    metav1.Duration{Duration: 2 * time.Minute},
					ValuesFiles: []string{"values.yaml"},
				},
			},
		},
		{
			name: "takes SourceRef namespace into account",
			modify: func(hr *v2beta1.HelmRelease) {
				hr.Spec.Chart.Spec.SourceRef.Namespace = "cross"
			},
			want: &sourcev1.HelmChart{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default-test-release",
					Namespace: "cross",
				},
				Spec: sourcev1.HelmChartSpec{
					Chart:   "chart",
					Version: "1.0.0",
					SourceRef: sourcev1.LocalHelmChartSourceReference{
						Name: "test-repository",
						Kind: "HelmRepository",
					},
					Interval:    metav1.Duration{Duration: 2 * time.Minute},
					ValuesFiles: []string{"values.yaml"},
				},
			},
		},
		{
			name: "falls back to HelmRelease interval",
			modify: func(hr *v2beta1.HelmRelease) {
				hr.Spec.Chart.Spec.Interval = nil
			},
			want: &sourcev1.HelmChart{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default-test-release",
					Namespace: "default",
				},
				Spec: sourcev1.HelmChartSpec{
					Chart:   "chart",
					Version: "1.0.0",
					SourceRef: sourcev1.LocalHelmChartSourceReference{
						Name: "test-repository",
						Kind: "HelmRepository",
					},
					Interval:    metav1.Duration{Duration: time.Minute},
					ValuesFiles: []string{"values.yaml"},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			hr := hrWithChartTemplate.DeepCopy()
			tt.modify(hr)
			g.Expect(buildHelmChartFromTemplate(hr)).To(Equal(tt.want))
		})
	}
}

func Test_helmChartDiff(t *testing.T) {
	tests := []struct {
		name string
		cur  sourcev1.HelmChartSpec
		new  sourcev1.HelmChartSpec
		diff string
		eq   bool
	}{
		{
			name: "simple divergence",
			cur: sourcev1.HelmChartSpec{
				Chart: "name",
			},
			new: sourcev1.HelmChartSpec{
				Chart: "other",
			},
			diff: "Chart:\n-name\n+other",
			eq:   false,
		},
		{
			name: "nested divergence",
			cur: sourcev1.HelmChartSpec{
				Chart: "./path.tgz",
				SourceRef: sourcev1.LocalHelmChartSourceReference{
					Kind: sourcev1.GitRepositoryKind,
					Name: "gitrepository",
				},
			},
			new: sourcev1.HelmChartSpec{
				Chart: "./path.tgz",
				SourceRef: sourcev1.LocalHelmChartSourceReference{
					Kind: sourcev1.BucketKind,
					Name: "bucket",
				},
			},
			diff: "SourceRef.Kind:\n-GitRepository\n+Bucket\n\nSourceRef.Name:\n-gitrepository\n+bucket",
			eq:   false,
		},
		{
			name: "removed field",
			cur: sourcev1.HelmChartSpec{
				Chart: "./path.tgz",
				SourceRef: sourcev1.LocalHelmChartSourceReference{
					Kind: sourcev1.GitRepositoryKind,
					Name: "gitrepository",
				},
			},
			new: sourcev1.HelmChartSpec{
				Chart: "./path.tgz",
			},
			diff: "SourceRef.Kind:\n-GitRepository\n\nSourceRef.Name:\n-gitrepository",
			eq:   false,
		},
		{
			name: "equal",
			cur: sourcev1.HelmChartSpec{
				Chart: "./path.tgz",
				SourceRef: sourcev1.LocalHelmChartSourceReference{
					Kind: sourcev1.GitRepositoryKind,
					Name: "gitrepository",
				},
			},
			new: sourcev1.HelmChartSpec{
				Chart: "./path.tgz",
				SourceRef: sourcev1.LocalHelmChartSourceReference{
					Kind: sourcev1.GitRepositoryKind,
					Name: "gitrepository",
				},
			},
			eq: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			diff, eq := helmChartSpecDiff(tt.cur, tt.new)
			g.Expect(diff).To(Equal(tt.diff), diff)
			g.Expect(eq).To(Equal(tt.eq))
		})
	}
}
