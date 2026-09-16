package framework

import (
	"context"
	"fmt"

	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ResourceClaimNameForPodClaim returns the generated ResourceClaim name for a pod resource claim ref.
func ResourceClaimNameForPodClaim(pod *corev1.Pod, podClaim string) string {
	for _, st := range pod.Status.ResourceClaimStatuses {
		if st.Name == podClaim && st.ResourceClaimName != nil {
			return *st.ResourceClaimName
		}
	}
	return ""
}

// ExpectResourceClaimRequestsSharePCIeRoot asserts that each named device request in an
// allocated ResourceClaim resolved to devices with the same resource.kubernetes.io/pcieRoot
// on their ResourceSlices (cross-driver matchAttribute alignment).
func (c *Clients) ExpectResourceClaimRequestsSharePCIeRoot(ctx context.Context, namespace, claimName string, requestNames ...string) {
	claim := c.WaitForResourceClaimAllocated(ctx, namespace, claimName)

	want := make(map[string]struct{}, len(requestNames))
	for _, name := range requestNames {
		want[name] = struct{}{}
	}

	results, found, err := unstructured.NestedSlice(claim.Object, "status", "allocation", "devices", "results")
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue(), "claim %s/%s has no allocation device results", namespace, claimName)

	rootsByRequest := make(map[string]string, len(requestNames))
	for _, raw := range results {
		res, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		request, _ := res["request"].(string)
		if _, ok := want[request]; !ok {
			continue
		}
		driver, _ := res["driver"].(string)
		pool, _ := res["pool"].(string)
		device, _ := res["device"].(string)
		Expect(driver).NotTo(BeEmpty(), "allocation result for request %q missing driver", request)
		Expect(pool).NotTo(BeEmpty(), "allocation result for request %q missing pool", request)
		Expect(device).NotTo(BeEmpty(), "allocation result for request %q missing device", request)

		root, err := c.pcieRootFromResourceSliceDevice(ctx, driver, pool, device)
		Expect(err).NotTo(HaveOccurred(), "request %q allocated %s/%s/%s", request, driver, pool, device)
		rootsByRequest[request] = root
	}

	for _, request := range requestNames {
		Expect(rootsByRequest).To(HaveKey(request), "no allocation result for request %q in claim %s", request, claimName)
	}

	ref := rootsByRequest[requestNames[0]]
	Expect(ref).NotTo(BeEmpty(), "pcieRoot empty for request %q", requestNames[0])
	for _, request := range requestNames[1:] {
		Expect(rootsByRequest[request]).To(Equal(ref),
			"expected requests %q and %q to share pcieRoot %q", requestNames[0], request, ref)
	}
}

func (c *Clients) pcieRootFromResourceSliceDevice(ctx context.Context, driver, pool, deviceName string) (string, error) {
	list, err := c.Dynamic.Resource(schemaGroupVersionResource("resourceslices")).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}
	for _, item := range list.Items {
		sliceDriver, _, err := unstructured.NestedString(item.Object, "spec", "driver")
		if err != nil || sliceDriver != driver {
			continue
		}
		slicePool, _, err := unstructured.NestedString(item.Object, "spec", "pool", "name")
		if err != nil || slicePool != pool {
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
			name, _ := device["name"].(string)
			if name != deviceName {
				continue
			}
			root, ok, err := unstructured.NestedString(device, "attributes", PCIeRootAttributeKey, "string")
			if err != nil {
				return "", err
			}
			if !ok || root == "" {
				return "", fmt.Errorf("device %s/%s/%s has no %s attribute", driver, pool, deviceName, PCIeRootAttributeKey)
			}
			return root, nil
		}
	}
	return "", fmt.Errorf("device %s/%s/%s not found on any ResourceSlice", driver, pool, deviceName)
}
