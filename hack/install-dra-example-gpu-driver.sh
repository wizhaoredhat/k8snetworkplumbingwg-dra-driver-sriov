#!/usr/bin/env bash
# Install gpu.example.com (kubernetes-sigs/dra-example-driver) for resource-alignment demos.
#
# Source: DRA_EXAMPLE_DRIVER_REPO / DRA_EXAMPLE_DRIVER_REF (default upstream main).
# Alignment env: GPU_PUBLISH_PCIE_ROOT (default true), PCIE_ROOTS (auto from SR-IOV slices).
set -xeo pipefail

source hack/common.sh

DRA_EXAMPLE_DRIVER_REPO="${DRA_EXAMPLE_DRIVER_REPO:-https://github.com/kubernetes-sigs/dra-example-driver.git}"
DRA_EXAMPLE_DRIVER_REF="${DRA_EXAMPLE_DRIVER_REF:-main}"
DRA_EXAMPLE_DRIVER_RELEASE="${DRA_EXAMPLE_DRIVER_RELEASE:-dra-example-driver}"
DRA_EXAMPLE_DRIVER_NAMESPACE="${DRA_EXAMPLE_DRIVER_NAMESPACE:-dra-example-driver}"
GPU_DRIVER_NAME="${GPU_DRIVER_NAME:-gpu.example.com}"
SRIOV_DRIVER_NAME="${SRIOV_DRIVER_NAME:-sriovnetwork.k8snetworkplumbingwg.io}"
PCIE_ROOT_ATTR="${PCIE_ROOT_ATTR:-resource.kubernetes.io/pcieRoot}"
GPU_PUBLISH_PCIE_ROOT="${GPU_PUBLISH_PCIE_ROOT:-true}"

export GPU_DRIVER_NAME SRIOV_DRIVER_NAME PCIE_ROOT_ATTR

cache_dir="${root}/.cache/dra-example-driver"

# check_install_requirements verifies kubectl, podman, git, make, go, jq, and KUBECONFIG exist.
check_install_requirements() {
  local -a cmds=(kubectl podman git make go jq)
  for cmd in "${cmds[@]}"; do
    if ! command -v "$cmd" &> /dev/null; then
      echo "$cmd is not available"
      exit 1
    fi
  done
  if [[ ! -f "${KUBECONFIG}" ]]; then
    echo "KUBECONFIG=${KUBECONFIG} not found"
    exit 1
  fi
}

# fetch_example_driver shallow-clones or updates DRA_EXAMPLE_DRIVER_REPO at DRA_EXAMPLE_DRIVER_REF.
fetch_example_driver() {
  echo "## Fetching dra-example-driver (${DRA_EXAMPLE_DRIVER_REPO}@${DRA_EXAMPLE_DRIVER_REF})"
  if [[ -d "${cache_dir}/.git" ]]; then
    current_remote="$(git -C "${cache_dir}" remote get-url origin 2>/dev/null || true)"
    if [[ "${current_remote}" != "${DRA_EXAMPLE_DRIVER_REPO}" ]]; then
      rm -rf "${cache_dir}"
    fi
  fi
  if [[ -d "${cache_dir}/.git" ]]; then
    git -C "${cache_dir}" fetch --depth 1 origin "${DRA_EXAMPLE_DRIVER_REF}"
    git -C "${cache_dir}" checkout -f FETCH_HEAD
  else
    mkdir -p "$(dirname "${cache_dir}")"
    git clone --depth 1 --branch "${DRA_EXAMPLE_DRIVER_REF}" \
      "${DRA_EXAMPLE_DRIVER_REPO}" "${cache_dir}"
  fi
}

# discover_pcie_roots_from_sriov prints a sorted, comma-separated union of pcieRoot values
# from SR-IOV ResourceSlice devices (stdout); exits non-zero if none found.
# Uses jq because kubectl jsonpath cannot reliably read qualified attribute map keys.
discover_pcie_roots_from_sriov() {
  local joined
  joined="$(kubectl get resourceslices -o json | jq -r --arg driver "${SRIOV_DRIVER_NAME}" --arg attr "${PCIE_ROOT_ATTR}" '
    [.items[]
     | select(.spec.driver == $driver)
     | .spec.devices[]?
     | .attributes[$attr].string // empty
    ] | unique | sort | join(",")')"
  if [[ -z "${joined}" ]]; then
    return 1
  fi
  printf '%s' "${joined}"
}

# resolve_pcie_roots sets PCIE_ROOTS from the environment or discovers it from the cluster.
resolve_pcie_roots() {
  if [[ -n "${PCIE_ROOTS:-}" ]]; then
    echo "## Using PCIE_ROOTS from environment: ${PCIE_ROOTS}"
    return 0
  fi
  echo "## Discovering PCIE_ROOTS from ${SRIOV_DRIVER_NAME} ResourceSlices"
  if ! PCIE_ROOTS="$(discover_pcie_roots_from_sriov)"; then
    echo "## ERROR: could not discover ${PCIE_ROOT_ATTR} on ${SRIOV_DRIVER_NAME} ResourceSlices"
    echo "## Set PCIE_ROOTS explicitly (comma-separated) to match SR-IOV VF topology"
    kubectl get resourceslices -o wide || true
    exit 1
  fi
  export PCIE_ROOTS
  echo "## Discovered PCIE_ROOTS=${PCIE_ROOTS}"
}

# build_and_push_example_driver_image builds the example driver image and pushes it to the
# cluster internal registry at controller_ip:5000.
build_and_push_example_driver_image() {
  export EXAMPLE_DRIVER_IMAGE="${controller_ip}:5000/dra-example-driver"

  echo "## Building dra-example-driver image ${EXAMPLE_DRIVER_IMAGE}:latest"
  CONTAINER_TOOL=podman IMAGE_NAME="${EXAMPLE_DRIVER_IMAGE}" \
    make -C "${cache_dir}" -f deployments/container/Makefile ubuntu22.04
  podman push --tls-verify=false "${EXAMPLE_DRIVER_IMAGE}:latest"
  podman rmi -fi "${EXAMPLE_DRIVER_IMAGE}:latest" || true
}

# deploy_example_driver installs the local Helm chart with gpuPublishPCIeRoot and pcieRoots.
deploy_example_driver() {
  echo "## Installing dra-example-driver via Helm"
  export PATH="${root}/bin:${PATH}"
  if [[ ! -x "${root}/bin/helm" ]]; then
    make -C "${root}" helm
  fi
  local pcie_roots_set="${PCIE_ROOTS//,/\\,}"
  "${root}/bin/helm" upgrade -i "${DRA_EXAMPLE_DRIVER_RELEASE}" \
    "${cache_dir}/deployments/helm/dra-example-driver" \
    --namespace "${DRA_EXAMPLE_DRIVER_NAMESPACE}" \
    --create-namespace \
    --set "gpuPublishPCIeRoot=${GPU_PUBLISH_PCIE_ROOT}" \
    --set "pcieRoots=${pcie_roots_set}" \
    --set "image.repository=${EXAMPLE_DRIVER_IMAGE}" \
    --set image.tag=latest \
    --set image.pullPolicy=Always \
    --set webhook.enabled=false
}

# wait_for_example_driver waits until the kubeletplugin DaemonSet is fully rolled out.
wait_for_example_driver() {
  local ds="${DRA_EXAMPLE_DRIVER_RELEASE}-kubeletplugin"
  echo "## Waiting for ${DRA_EXAMPLE_DRIVER_NAMESPACE}/${ds}"
  kubectl -n "${DRA_EXAMPLE_DRIVER_NAMESPACE}" rollout status "ds/${ds}" --timeout=180s
}

# has_gpu_pcie_root_attribute returns 0 when gpu.example.com publishes pcieRoot on a device.
has_gpu_pcie_root_attribute() {
  kubectl get resourceslices -o json | jq -e --arg driver "${GPU_DRIVER_NAME}" --arg attr "${PCIE_ROOT_ATTR}" '
    any(.items[];
      .spec.driver == $driver and
      any(.spec.devices[]?; (.attributes[$attr].string // "") != ""))
  ' >/dev/null
}

# wait_for_gpu_pcie_root polls until the example driver ResourceSlice includes pcieRoot.
wait_for_gpu_pcie_root() {
  echo "## Waiting for ${GPU_DRIVER_NAME} ResourceSlice with ${PCIE_ROOT_ATTR}"
  local attempts=0
  local max_attempts=60
  while [[ $attempts -lt $max_attempts ]]; do
    if has_gpu_pcie_root_attribute; then
      echo "## Found ${GPU_DRIVER_NAME} device with ${PCIE_ROOT_ATTR}"
      return 0
    fi
    sleep 5
    attempts=$((attempts + 1))
  done
  echo "## ERROR: Timed out waiting for ${GPU_DRIVER_NAME} ResourceSlice with ${PCIE_ROOT_ATTR}"
  kubectl get resourceslices -o wide || true
  exit 1
}

check_install_requirements
fetch_example_driver
get_controller_ip
resolve_pcie_roots
build_and_push_example_driver_image
deploy_example_driver
wait_for_example_driver
wait_for_gpu_pcie_root

echo "## DRA example driver for (fake) GPUs installed successfully"
echo "## Image: ${EXAMPLE_DRIVER_IMAGE}:latest"
echo "## Release: ${DRA_EXAMPLE_DRIVER_RELEASE} (${DRA_EXAMPLE_DRIVER_NAMESPACE})"
echo "## GPU_PUBLISH_PCIE_ROOT=${GPU_PUBLISH_PCIE_ROOT} PCIE_ROOTS=${PCIE_ROOTS}"
