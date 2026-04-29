//go:build e2e
// +build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gaurangkudale/rca-operator/test/utils"
)

const (
	phase2Namespace = "rca-phase2-system"
	phase2DemoNS    = "rca-phase2-demo"
	phase2TraceID   = "0102030405060708090a0b0c0d0e0f10"
)

var _ = Describe("Phase 2 Helm golden path", Ordered, func() {
	BeforeAll(func() {
		if os.Getenv("PHASE2_HELM_E2E") != "true" {
			Skip("set PHASE2_HELM_E2E=true to run the full Phase 2 Helm golden-path suite")
		}

		repo, tag := splitImage(managerImage)

		By("installing the full Helm chart")
		cmd := exec.Command("helm", "upgrade", "--install", "rca-operator", "./helm",
			"--namespace", phase2Namespace,
			"--create-namespace",
			"--set", "image.repository="+repo,
			"--set", "image.tag="+tag,
			"--set", "image.pullPolicy=Never",
			"--wait",
			"--timeout", "10m",
		)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "failed to install full Helm chart")

		By("creating a watched demo namespace")
		_, _ = utils.Run(exec.Command("kubectl", "create", "namespace", phase2DemoNS))

		By("creating an RCAAgent")
		agent := fmt.Sprintf(`apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCAAgent
metadata:
  name: phase2-agent
  namespace: %s
spec:
  watchNamespaces:
    - %s
  incidentRetention: 1d
`, phase2Namespace, phase2DemoNS)
		Expect(kubectlApply(agent)).To(Succeed())
	})

	AfterAll(func() {
		if os.Getenv("PHASE2_HELM_E2E") != "true" {
			return
		}
		_, _ = utils.Run(exec.Command("helm", "uninstall", "rca-operator", "-n", phase2Namespace))
		_, _ = utils.Run(exec.Command("kubectl", "delete", "namespace", phase2Namespace, "--ignore-not-found=true"))
		_, _ = utils.Run(exec.Command("kubectl", "delete", "namespace", phase2DemoNS, "--ignore-not-found=true"))
	})

	It("correlates OTLP signals into trace-aware IncidentReports and dashboard APIs", func() {
		By("verifying default correlation rules were installed")
		verifyDefaultRules := func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "get", "rcacorrelationrules", "-o", "jsonpath={.items[*].metadata.name}"))
			g.Expect(err).NotTo(HaveOccurred())
			for _, name := range []string{"node-plus-eviction", "crashloop-plus-oom", "crashloop-plus-deploy", "imagepull-no-history"} {
				g.Expect(out).To(ContainSubstring(name))
			}
		}
		Eventually(verifyDefaultRules, 3*time.Minute, 2*time.Second).Should(Succeed())

		By("sending an OTLP error span and error log through the collector")
		Expect(sendOTLPJSON("phase2-otlp-trace", "/v1/traces", otlpTracePayload())).To(Succeed())
		Expect(sendOTLPJSON("phase2-otlp-log", "/v1/logs", otlpLogPayload())).To(Succeed())

		By("verifying an IncidentReport captured the trace annotations and graph")
		var report incidentReportItem
		findTraceIncident := func(g Gomega) {
			list, err := listIncidentReports()
			g.Expect(err).NotTo(HaveOccurred())
			found := false
			for _, item := range list.Items {
				if item.Metadata.Annotations["rca.rca-operator.tech/trace-id"] == phase2TraceID {
					report = item
					found = true
					break
				}
			}
			g.Expect(found).To(BeTrue(), "expected IncidentReport with trace-id annotation")
			g.Expect(report.Metadata.Annotations["rca.rca-operator.tech/trace-ids"]).To(ContainSubstring(phase2TraceID))
			g.Expect(report.Status.IncidentGraph).NotTo(BeNil())
		}
		Eventually(findTraceIncident, 5*time.Minute, 3*time.Second).Should(Succeed())

		By("verifying the dashboard trace endpoint is reachable for the trace")
		verifyTraceEndpoint := func(g Gomega) {
			status, err := curlFromCluster("phase2-dashboard-trace",
				fmt.Sprintf("http://rca-operator-dashboard.%s.svc.cluster.local:9090/api/traces/%s", phase2Namespace, phase2TraceID))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(status).To(Or(Equal("200"), Equal("502")), "endpoint should be wired; 502 means Jaeger has not indexed the trace yet")
		}
		Eventually(verifyTraceEndpoint, 3*time.Minute, 5*time.Second).Should(Succeed())
	})
})

func splitImage(image string) (string, string) {
	idx := strings.LastIndex(image, ":")
	if idx <= 0 || idx == len(image)-1 {
		return image, "latest"
	}
	return image[:idx], image[idx+1:]
}

func kubectlApply(yaml string) error {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	_, err := utils.Run(cmd)
	return err
}

func sendOTLPJSON(name, path, payload string) error {
	_, _ = utils.Run(exec.Command("kubectl", "delete", "pod", name, "-n", phase2Namespace, "--ignore-not-found=true"))
	endpoint := fmt.Sprintf("http://rca-operator-otel-collector.%s.svc.cluster.local:4318%s", phase2Namespace, path)
	script := fmt.Sprintf("curl -sS -o /tmp/out -w %%{http_code} -H 'Content-Type: application/json' --data '%s' %s | grep -E '^(200|202)$'", payload, endpoint)
	cmd := exec.Command("kubectl", "run", name,
		"--restart=Never",
		"--namespace", phase2Namespace,
		"--image=curlimages/curl:latest",
		"--command", "--", "sh", "-c", script)
	if _, err := utils.Run(cmd); err != nil {
		return err
	}
	Eventually(func(g Gomega) {
		out, err := utils.Run(exec.Command("kubectl", "get", "pod", name, "-n", phase2Namespace, "-o", "jsonpath={.status.phase}"))
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(out).To(Equal("Succeeded"))
	}, 2*time.Minute, 2*time.Second).Should(Succeed())
	return nil
}

func curlFromCluster(name, url string) (string, error) {
	_, _ = utils.Run(exec.Command("kubectl", "delete", "pod", name, "-n", phase2Namespace, "--ignore-not-found=true"))
	script := fmt.Sprintf("curl -sS -o /tmp/out -w %%{http_code} %s", url)
	cmd := exec.Command("kubectl", "run", name,
		"--restart=Never",
		"--namespace", phase2Namespace,
		"--image=curlimages/curl:latest",
		"--command", "--", "sh", "-c", script)
	if _, err := utils.Run(cmd); err != nil {
		return "", err
	}
	Eventually(func(g Gomega) {
		out, err := utils.Run(exec.Command("kubectl", "get", "pod", name, "-n", phase2Namespace, "-o", "jsonpath={.status.phase}"))
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(out).To(Equal("Succeeded"))
	}, 2*time.Minute, 2*time.Second).Should(Succeed())
	return utils.Run(exec.Command("kubectl", "logs", name, "-n", phase2Namespace))
}

func listIncidentReports() (incidentReportList, error) {
	out, err := utils.Run(exec.Command("kubectl", "get", "incidentreports", "-A", "-o", "json"))
	if err != nil {
		return incidentReportList{}, err
	}
	var list incidentReportList
	err = json.Unmarshal([]byte(out), &list)
	return list, err
}

func otlpTracePayload() string {
	return `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"checkout"}},{"key":"k8s.namespace.name","value":{"stringValue":"` + phase2DemoNS + `"}},{"key":"k8s.pod.name","value":{"stringValue":"checkout-0"}},{"key":"k8s.node.name","value":{"stringValue":"kind-worker"}}]},"scopeSpans":[{"spans":[{"traceId":"` + phase2TraceID + `","spanId":"1112131415161718","name":"GET /checkout","kind":2,"startTimeUnixNano":"1710000000000000000","endTimeUnixNano":"1710000000100000000","status":{"code":2},"attributes":[{"key":"http.status_code","value":{"intValue":"500"}}]}]}]}]}`
}

func otlpLogPayload() string {
	return `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"checkout"}},{"key":"k8s.namespace.name","value":{"stringValue":"` + phase2DemoNS + `"}},{"key":"k8s.pod.name","value":{"stringValue":"checkout-0"}}]},"scopeLogs":[{"logRecords":[{"timeUnixNano":"1710000000200000000","traceId":"` + phase2TraceID + `","spanId":"1112131415161718","severityNumber":17,"severityText":"ERROR","body":{"stringValue":"checkout failed with synthetic error"}}]}]}]}`
}

type incidentReportList struct {
	Items []incidentReportItem `json:"items"`
}

type incidentReportItem struct {
	Metadata struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Status struct {
		IncidentGraph map[string]any `json:"incidentGraph"`
	} `json:"status"`
}
