package controller

import (
<<<<<<< HEAD
	. "github.com/onsi/ginkgo/v2"
=======
	"context"
	"sync"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/collectors"
>>>>>>> tmp-original-16-06-26-04-10
)

var _ = Describe("RCAAgent Controller", func() {
	Context("When reconciling a resource", func() {

<<<<<<< HEAD
=======
		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}
		rcaagent := &rcav1alpha1.RCAAgent{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind RCAAgent")
			err := k8sClient.Get(ctx, typeNamespacedName, rcaagent)
			if err != nil && errors.IsNotFound(err) {
				resource := &rcav1alpha1.RCAAgent{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: rcav1alpha1.RCAAgentSpec{
						WatchNamespaces: []string{"default"},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &rcav1alpha1.RCAAgent{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance RCAAgent")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
>>>>>>> tmp-original-16-06-26-04-10
		It("should successfully reconcile the resource", func() {

			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})
})

type noopEmitter struct{}

func (n noopEmitter) Emit(_ collectors.Signal) {}

type fakePodCollector struct {
	mu        sync.Mutex
	startErr  error
	startCtxs []context.Context
}

func (f *fakePodCollector) Start(ctx context.Context) error {
	f.mu.Lock()
	f.startCtxs = append(f.startCtxs, ctx)
	f.mu.Unlock()
	return f.startErr
}

func (f *fakePodCollector) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.startCtxs)
}

func (f *fakePodCollector) firstCtx() context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.startCtxs) == 0 {
		return nil
	}
	return f.startCtxs[0]
}

type fakeEventCollector struct {
	mu        sync.Mutex
	startErr  error
	startCtxs []context.Context
}

func (f *fakeEventCollector) Start(ctx context.Context) error {
	f.mu.Lock()
	f.startCtxs = append(f.startCtxs, ctx)
	f.mu.Unlock()
	return f.startErr
}

var _ = Describe("RCAAgent collector registry", func() {
	It("starts collectors once and restarts on watch namespace changes", func() {
		ctx := context.Background()
		reconciler := &RCAAgentReconciler{
			SignalEmitter:  noopEmitter{},
			ManagerContext: context.Background(),
		}
		reconciler.initCollectorRegistry()

		startedCollectors := make([]*fakePodCollector, 0, 2)
		reconciler.newPodCollector = func(_ ctrlcache.Cache, _ collectors.SignalEmitter, _ logr.Logger, _ collectors.PodCollectorConfig) podCollector {
			c := &fakePodCollector{}
			startedCollectors = append(startedCollectors, c)
			return c
		}
		reconciler.newEventCollector = func(_ ctrlcache.Cache, _ collectors.SignalEmitter, _ logr.Logger, _ collectors.EventCollectorConfig) eventCollector {
			return &fakeEventCollector{}
		}
		reconciler.newWorkloadCollector = func(_ ctrlcache.Cache, _ collectors.SignalEmitter, _ logr.Logger, _ collectors.WorkloadCollectorConfig) workloadCollector {
			return &fakeEventCollector{}
		}
		reconciler.newNodeCollector = func(_ ctrlcache.Cache, _ collectors.SignalEmitter, _ logr.Logger, _ collectors.NodeCollectorConfig) nodeCollector {
			return &fakeEventCollector{}
		}

		agent := &rcav1alpha1.RCAAgent{ObjectMeta: metav1.ObjectMeta{Name: "agent-a", Namespace: "default"}, Spec: rcav1alpha1.RCAAgentSpec{WatchNamespaces: []string{"default"}}}
		Expect(reconciler.ensureCollectorsRunning(ctx, agent)).To(Succeed())
		Expect(startedCollectors).To(HaveLen(1))
		Expect(startedCollectors[0].startCount()).To(Equal(1))

		// Same namespace set should not trigger restart.
		agent.Spec.WatchNamespaces = []string{"default", "default"}
		Expect(reconciler.ensureCollectorsRunning(ctx, agent)).To(Succeed())
		Expect(startedCollectors).To(HaveLen(1))

		firstCtx := startedCollectors[0].firstCtx()
		Expect(firstCtx).NotTo(BeNil())

		// Different namespace set should trigger restart.
		agent.Spec.WatchNamespaces = []string{"production"}
		Expect(reconciler.ensureCollectorsRunning(ctx, agent)).To(Succeed())
		Expect(startedCollectors).To(HaveLen(2))
		Expect(startedCollectors[1].startCount()).To(Equal(1))
		Eventually(firstCtx.Done()).Should(BeClosed())
	})

	It("stops collectors and removes registry entry", func() {
		reconciler := &RCAAgentReconciler{}
		reconciler.initCollectorRegistry()

		key := types.NamespacedName{Name: "agent-a", Namespace: "default"}
		ctx, cancel := context.WithCancel(context.Background())
		reconciler.collectorRegistry[key] = collectorEntry{cancel: cancel, watchNamespaces: []string{"default"}}

		reconciler.stopCollectors(key)

		_, exists := reconciler.collectorRegistry[key]
		Expect(exists).To(BeFalse())
		Eventually(ctx.Done()).Should(BeClosed())
	})
})
