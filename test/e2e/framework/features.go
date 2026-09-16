package framework

import (
	"context"
	"os"
	"strings"

	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	LabelMultus           = "Multus"
	LabelStandalone       = "Standalone"
	LabelVfio             = "Vfio"
	LabelExtendedResource = "ExtendedResource"
	LabelAlignment        = "Alignment"
	LabelResourcePolicy   = "ResourcePolicy"

	SriovCapableNodeLabelKey   = "feature.node.kubernetes.io/network-sriov.capable"
	SriovCapableNodeLabelValue = "true"

	AlignmentGPUDriverName = "gpu.example.com"
	PCIeRootAttributeKey   = "resource.kubernetes.io/pcieRoot"
)

// SkipUnlessMultus skips when Multus is not installed or the driver is not in MULTUS mode.
func (c *Clients) SkipUnlessMultus(ctx context.Context) {
	if os.Getenv("E2E_SKIP_MULTUS") == "1" {
		skipTestf("E2E_SKIP_MULTUS=1")
	}
	if !c.hasMultus(ctx) {
		skipTestf("Multus not detected in cluster")
	}
	if mode := c.detectDriverMode(ctx); mode != "" && mode != "MULTUS" {
		skipTestf("driver configurationMode=%s (need MULTUS)", mode)
	}
}

// SkipUnlessStandalone skips demos that rely on the NRI attach path (ifName /
// netAttachDefName) when the driver is in MULTUS mode. In MULTUS mode NRI is
// disabled and secondary interfaces only appear via Multus pod annotations.
func (c *Clients) SkipUnlessStandalone(ctx context.Context) {
	if mode := c.detectDriverMode(ctx); mode == "MULTUS" {
		skipTestf("driver configurationMode=MULTUS (need STANDALONE / NRI)")
	}
}

// SkipUnlessAlignment skips when the cluster has no gpu.example.com ResourceSlice with
// resource.kubernetes.io/pcieRoot. Call SkipUnlessStandalone or SkipUnlessMultus when the
// fixture depends on driver mode.
func (c *Clients) SkipUnlessAlignment(ctx context.Context) {
	if !c.hasResourceSliceDeviceAttribute(ctx, AlignmentGPUDriverName, PCIeRootAttributeKey) {
		skipTestf("no %s ResourceSlice device with %s (run make install-fake-gpu-driver or DEPLOY_FAKE_GPU_DRIVER=1)",
			AlignmentGPUDriverName, PCIeRootAttributeKey)
	}
}

// SkipUnlessSriovCapableNode skips when no node has the SR-IOV capable label.
func (c *Clients) SkipUnlessSriovCapableNode(ctx context.Context) {
	nodes, err := c.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: SriovCapableNodeLabelKey + "=" + SriovCapableNodeLabelValue,
	})
	Expect(err).NotTo(HaveOccurred())
	if len(nodes.Items) == 0 {
		skipTestf("no nodes with label %s=%s", SriovCapableNodeLabelKey, SriovCapableNodeLabelValue)
	}
}

// SkipIfNodeMissing skips when the node is absent.
func (c *Clients) SkipIfNodeMissing(ctx context.Context, wantNode string) {
	nodes, err := c.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	Expect(err).NotTo(HaveOccurred())
	for _, n := range nodes.Items {
		if n.Name == wantNode {
			return
		}
	}
	skipTestf("node %s not found", wantNode)
}

func (c *Clients) hasMultus(ctx context.Context) bool {
	nss := []string{"kube-system", "multus", "default"}
	for _, ns := range nss {
		pods, err := c.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
			LabelSelector: "name=multus",
		})
		if err == nil && len(pods.Items) > 0 {
			return true
		}
		dss, err := c.Clientset.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue
		}
		for _, ds := range dss.Items {
			if strings.Contains(strings.ToLower(ds.Name), "multus") {
				return true
			}
		}
	}
	return false
}

func (c *Clients) detectDriverMode(ctx context.Context) string {
	pods, err := c.Clientset.CoreV1().Pods(DriverNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: DriverLabel,
	})
	if err != nil || len(pods.Items) == 0 {
		return ""
	}
	for _, p := range pods.Items {
		for _, ctn := range p.Spec.Containers {
			joined := strings.ToUpper(strings.Join(ctn.Args, " "))
			if strings.Contains(joined, "MULTUS") {
				return "MULTUS"
			}
			if strings.Contains(joined, "STANDALONE") {
				return "STANDALONE"
			}
			for _, env := range ctn.Env {
				if strings.EqualFold(env.Name, "CONFIGURATION_MODE") || strings.EqualFold(env.Name, "DRA_DRIVER_MODE") {
					return strings.ToUpper(env.Value)
				}
			}
		}
	}
	return "STANDALONE"
}

func (c *Clients) hasResourceSliceDeviceAttribute(ctx context.Context, driver, attrKey string) bool {
	list, err := c.Dynamic.Resource(schemaGroupVersionResource("resourceslices")).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false
	}
	for _, item := range list.Items {
		sliceDriver, _, err := unstructured.NestedString(item.Object, "spec", "driver")
		if err != nil || sliceDriver != driver {
			continue
		}
		devices, ok, err := unstructured.NestedSlice(item.Object, "spec", "devices")
		if err != nil || !ok {
			continue
		}
		for _, raw := range devices {
			device, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			val, ok, err := unstructured.NestedString(device, "attributes", attrKey, "string")
			if err == nil && ok && val != "" {
				return true
			}
		}
	}
	return false
}
