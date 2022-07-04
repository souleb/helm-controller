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

package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/gomega"
	helmrelease "helm.sh/helm/v3/pkg/release"
	helmreleaseutil "helm.sh/helm/v3/pkg/releaseutil"
	helmstorage "helm.sh/helm/v3/pkg/storage"
	helmdriver "helm.sh/helm/v3/pkg/storage/driver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fluxcd/pkg/runtime/conditions"

	helmv2 "github.com/fluxcd/helm-controller/api/v2beta2"
	"github.com/fluxcd/helm-controller/internal/action"
	"github.com/fluxcd/helm-controller/internal/release"
	"github.com/fluxcd/helm-controller/internal/testutil"
)

func TestRollback_Reconcile(t *testing.T) {
	tests := []struct {
		name string
		// driver allows for modifying the Helm storage driver.
		driver func(driver helmdriver.Driver) helmdriver.Driver
		// releases is the list of releases that are stored in the driver
		// before rollback.
		releases func(namespace string) []*helmrelease.Release
		// spec modifies the HelmRelease object's spec before rollback.
		spec func(spec *helmv2.HelmReleaseSpec)
		// status to configure on the HelmRelease before rollback.
		status func(releases []*helmrelease.Release) helmv2.HelmReleaseStatus
		// wantErr is the error that is expected to be returned.
		wantErr error
		// expectedConditions are the conditions that are expected to be set on
		// the HelmRelease after rolling back.
		expectConditions []metav1.Condition
		// expectCurrent is the expected Current release information on the
		// HelmRelease after rolling back.
		expectCurrent func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo
		// expectPrevious returns the expected Previous release information of
		// the HelmRelease after rolling back.
		expectPrevious func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo
		// expectFailures is the expected Failures count on the HelmRelease.
		expectFailures int64
		// expectInstallFailures is the expected InstallFailures count on the
		// HelmRelease.
		expectInstallFailures int64
		// expectUpgradeFailures is the expected UpgradeFailures count on the
		// HelmRelease.
		expectUpgradeFailures int64
	}{
		{
			name: "rollback",
			releases: func(namespace string) []*helmrelease.Release {
				return []*helmrelease.Release{
					testutil.BuildRelease(&helmrelease.MockReleaseOptions{
						Name:      mockReleaseName,
						Version:   1,
						Chart:     testutil.BuildChart(),
						Status:    helmrelease.StatusSuperseded,
						Namespace: namespace,
					}),
					testutil.BuildRelease(&helmrelease.MockReleaseOptions{
						Name:      mockReleaseName,
						Version:   2,
						Chart:     testutil.BuildChart(),
						Status:    helmrelease.StatusFailed,
						Namespace: namespace,
					}),
				}
			},
			status: func(releases []*helmrelease.Release) helmv2.HelmReleaseStatus {
				return helmv2.HelmReleaseStatus{
					Current:  release.ObservedToInfo(release.ObserveRelease(releases[1])),
					Previous: release.ObservedToInfo(release.ObserveRelease(releases[0])),
				}
			},
			expectConditions: []metav1.Condition{
				*conditions.TrueCondition(helmv2.RemediatedCondition, helmv2.RollbackSucceededReason,
					"Rolled back to version 1"),
			},
			expectCurrent: func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo {
				return release.ObservedToInfo(release.ObserveRelease(releases[2]))
			},
			expectPrevious: func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo {
				return release.ObservedToInfo(release.ObserveRelease(releases[0]))
			},
		},
		{
			name: "rollback without previous",
			releases: func(namespace string) []*helmrelease.Release {
				return []*helmrelease.Release{
					testutil.BuildRelease(&helmrelease.MockReleaseOptions{
						Name:      mockReleaseName,
						Version:   1,
						Chart:     testutil.BuildChart(),
						Status:    helmrelease.StatusSuperseded,
						Namespace: namespace,
					}),
					testutil.BuildRelease(&helmrelease.MockReleaseOptions{
						Name:      mockReleaseName,
						Version:   2,
						Chart:     testutil.BuildChart(),
						Status:    helmrelease.StatusFailed,
						Namespace: namespace,
					}),
				}
			},
			status: func(releases []*helmrelease.Release) helmv2.HelmReleaseStatus {
				return helmv2.HelmReleaseStatus{
					Current: release.ObservedToInfo(release.ObserveRelease(releases[1])),
				}
			},
			wantErr: ErrNoPrevious,
			expectCurrent: func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo {
				return release.ObservedToInfo(release.ObserveRelease(releases[1]))
			},
		},
		{
			name: "rollback failure",
			releases: func(namespace string) []*helmrelease.Release {
				return []*helmrelease.Release{
					testutil.BuildRelease(&helmrelease.MockReleaseOptions{
						Name:      mockReleaseName,
						Version:   1,
						Chart:     testutil.BuildChart(testutil.ChartWithFailingHook()),
						Status:    helmrelease.StatusSuperseded,
						Namespace: namespace,
					}, testutil.ReleaseWithFailingHook()),
					testutil.BuildRelease(&helmrelease.MockReleaseOptions{
						Name:      mockReleaseName,
						Version:   2,
						Chart:     testutil.BuildChart(),
						Status:    helmrelease.StatusFailed,
						Namespace: namespace,
					}),
				}
			},
			status: func(releases []*helmrelease.Release) helmv2.HelmReleaseStatus {
				return helmv2.HelmReleaseStatus{
					Current:  release.ObservedToInfo(release.ObserveRelease(releases[1])),
					Previous: release.ObservedToInfo(release.ObserveRelease(releases[0])),
				}
			},
			expectConditions: []metav1.Condition{
				*conditions.FalseCondition(helmv2.RemediatedCondition, helmv2.RollbackFailedReason,
					"timed out waiting for the condition"),
			},
			expectCurrent: func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo {
				return release.ObservedToInfo(release.ObserveRelease(releases[2]))
			},
			expectPrevious: func(releases []*helmrelease.Release) *helmv2.HelmReleaseInfo {
				return release.ObservedToInfo(release.ObserveRelease(releases[0]))
			},
			expectFailures: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			namedNS, err := testEnv.CreateNamespace(context.TODO(), mockReleaseNamespace)
			g.Expect(err).NotTo(HaveOccurred())
			t.Cleanup(func() {
				_ = testEnv.Delete(context.TODO(), namedNS)
			})
			releaseNamespace := namedNS.Name

			var releases []*helmrelease.Release
			if tt.releases != nil {
				releases = tt.releases(releaseNamespace)
				helmreleaseutil.SortByRevision(releases)
			}

			obj := &helmv2.HelmRelease{
				Spec: helmv2.HelmReleaseSpec{
					ReleaseName:      mockReleaseName,
					TargetNamespace:  releaseNamespace,
					StorageNamespace: releaseNamespace,
					Timeout:          &metav1.Duration{Duration: 100 * time.Millisecond},
				},
			}
			if tt.status != nil {
				obj.Status = tt.status(releases)
			}

			getter, err := RESTClientGetterFromManager(testEnv.Manager, obj.GetReleaseNamespace())
			g.Expect(err).ToNot(HaveOccurred())

			cfg, err := action.NewConfigFactory(getter,
				action.WithStorage(action.DefaultStorageDriver, obj.GetStorageNamespace()),
				action.WithDebugLog(logr.Discard()),
			)
			g.Expect(err).ToNot(HaveOccurred())

			store := helmstorage.Init(cfg.Driver)
			for _, r := range releases {
				g.Expect(store.Create(r)).To(Succeed())
			}

			if tt.driver != nil {
				cfg.Driver = tt.driver(cfg.Driver)
			}

			got := (&Rollback{configFactory: cfg}).Reconcile(context.TODO(), &Request{
				Object: obj,
			})
			if tt.wantErr != nil {
				g.Expect(errors.Is(got, tt.wantErr)).To(BeTrue())
			} else {
				g.Expect(got).ToNot(HaveOccurred())
			}

			g.Expect(obj.Status.Conditions).To(conditions.MatchConditions(tt.expectConditions))

			releases, _ = store.History(mockReleaseName)
			helmreleaseutil.SortByRevision(releases)

			if tt.expectCurrent != nil {
				g.Expect(obj.Status.Current).To(testutil.Equal(tt.expectCurrent(releases)))
			} else {
				g.Expect(obj.Status.Current).To(BeNil(), "expected current to be nil")
			}

			if tt.expectPrevious != nil {
				g.Expect(obj.Status.Previous).To(testutil.Equal(tt.expectPrevious(releases)))
			} else {
				g.Expect(obj.Status.Previous).To(BeNil(), "expected previous to be nil")
			}

			g.Expect(obj.Status.Failures).To(Equal(tt.expectFailures))
			g.Expect(obj.Status.InstallFailures).To(Equal(tt.expectInstallFailures))
			g.Expect(obj.Status.UpgradeFailures).To(Equal(tt.expectUpgradeFailures))
		})
	}
}

func Test_observeRollback(t *testing.T) {
	t.Run("rollback", func(t *testing.T) {
		g := NewWithT(t)

		obj := &helmv2.HelmRelease{}
		rls := helmrelease.Mock(&helmrelease.MockReleaseOptions{
			Name:      mockReleaseName,
			Namespace: mockReleaseNamespace,
			Version:   2,
			Status:    helmrelease.StatusPendingRollback,
		})
		observeRollback(obj)(rls)
		expect := release.ObservedToInfo(release.ObserveRelease(rls))

		g.Expect(obj.Status.Previous).To(BeNil())
		g.Expect(obj.Status.Current).To(Equal(expect))
	})

	t.Run("rollback with current", func(t *testing.T) {
		g := NewWithT(t)

		current := &helmv2.HelmReleaseInfo{
			Name:      mockReleaseName,
			Namespace: mockReleaseNamespace,
			Version:   2,
			Status:    helmrelease.StatusFailed.String(),
		}
		obj := &helmv2.HelmRelease{
			Status: helmv2.HelmReleaseStatus{
				Current: current,
			},
		}
		rls := helmrelease.Mock(&helmrelease.MockReleaseOptions{
			Name:      current.Name,
			Namespace: current.Namespace,
			Version:   current.Version + 1,
			Status:    helmrelease.StatusPendingRollback,
		})
		expect := release.ObservedToInfo(release.ObserveRelease(rls))

		observeRollback(obj)(rls)
		g.Expect(obj.Status.Current).ToNot(BeNil())
		g.Expect(obj.Status.Current).To(Equal(expect))
		g.Expect(obj.Status.Previous).To(BeNil())
	})

	t.Run("rollback with current with higher version", func(t *testing.T) {
		g := NewWithT(t)

		current := &helmv2.HelmReleaseInfo{
			Name:      mockReleaseName,
			Namespace: mockReleaseNamespace,
			Version:   2,
			Status:    helmrelease.StatusPendingRollback.String(),
		}
		obj := &helmv2.HelmRelease{
			Status: helmv2.HelmReleaseStatus{
				Current: current,
			},
		}
		rls := helmrelease.Mock(&helmrelease.MockReleaseOptions{
			Name:      current.Name,
			Namespace: current.Namespace,
			Version:   current.Version - 1,
			Status:    helmrelease.StatusSuperseded,
		})

		observeRollback(obj)(rls)
		g.Expect(obj.Status.Previous).To(BeNil())
		g.Expect(obj.Status.Current).To(Equal(current))
	})

	t.Run("rollback with current with different name", func(t *testing.T) {
		g := NewWithT(t)

		current := &helmv2.HelmReleaseInfo{
			Name:      mockReleaseName + "-other",
			Namespace: mockReleaseNamespace,
			Version:   2,
			Status:    helmrelease.StatusFailed.String(),
		}
		obj := &helmv2.HelmRelease{
			Status: helmv2.HelmReleaseStatus{
				Current: current,
			},
		}
		rls := helmrelease.Mock(&helmrelease.MockReleaseOptions{
			Name:      mockReleaseName,
			Namespace: mockReleaseNamespace,
			Version:   1,
			Status:    helmrelease.StatusPendingRollback,
		})
		expect := release.ObservedToInfo(release.ObserveRelease(rls))

		observeRollback(obj)(rls)
		g.Expect(obj.Status.Previous).To(BeNil())
		g.Expect(obj.Status.Current).To(Equal(expect))
	})
}
