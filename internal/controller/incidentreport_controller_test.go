/*
Copyright 2026.

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

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
)

var _ = Describe("IncidentReport Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}
		incidentreport := &rcav1alpha1.IncidentReport{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind IncidentReport")
			err := k8sClient.Get(ctx, typeNamespacedName, incidentreport)
			if err != nil && errors.IsNotFound(err) {
				resource := &rcav1alpha1.IncidentReport{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: rcav1alpha1.IncidentReportSpec{
						AgentRef:     "test-agent",
						Fingerprint:  "test|incident|resource",
						IncidentType: "CrashLoop",
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &rcav1alpha1.IncidentReport{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err != nil {
				// Already deleted or never created — nothing to clean up
				return
			}

			By("Cleanup the specific resource instance IncidentReport")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &IncidentReportReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})
})
