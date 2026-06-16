package controller

import (
	. "github.com/onsi/ginkgo/v2"
)

var _ = Describe("IncidentReport Controller", func() {
	Context("When reconciling a resource", func() {

<<<<<<< HEAD
=======
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
>>>>>>> tmp-original-16-06-26-04-10
		It("should successfully reconcile the resource", func() {

			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})
})
