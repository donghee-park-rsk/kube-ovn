package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"strings"

	// "errors"
	"fmt"
	// "maps"
	// "reflect"
	// "slices"
	// "strconv"

	// nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	// appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	// "k8s.io/apimachinery/pkg/labels"
	// "k8s.io/apimachinery/pkg/util/intstr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	// "k8s.io/utils/set"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kubeovnv1 "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"

	// "github.com/kubeovn/kube-ovn/pkg/ovs"
	// "github.com/kubeovn/kube-ovn/pkg/ovsdb/ovnnb"
	"github.com/kubeovn/kube-ovn/pkg/util"
)

const (
	LastAppliedConfigAnnotation = "kubectl.kubernetes.io/last-applied-configuration"
)

type updateVerObject struct {
	key    string
	oldVer *kubeovnv1.VpcEdgeRouter
	newVer *kubeovnv1.VpcEdgeRouter
}

func (c *Controller) enqueueAddVpcEdgeRouter(obj any) {
	key := cache.MetaObjectToName(obj.(*kubeovnv1.VpcEdgeRouter)).String()
	klog.Infof("enqueue add vpc-edge-router %s", key)
	c.addVpcEdgeRouterQueue.Add(key)
}

// func (c *Controller) enqueueUpdateVpcEdgeRouter(_, newObj any) {
// 	key := cache.MetaObjectToName(newObj.(*kubeovnv1.VpcEdgeRouter)).String()
// 	klog.Infof("enqueue update vpc-edge-router %s", key)
// 	c.updateVpcEdgeRouterQueue.Add(key)
// }

func (c *Controller) enqueueUpdateVpcEdgeRouter(oldObj, newObj any) {
	key := cache.MetaObjectToName(newObj.(*kubeovnv1.VpcEdgeRouter)).String()
	klog.Infof("enqueue update vpc-edge-router %s", key)
	if oldObj == nil {
		klog.Warningf("enqueue update vpc-edge-router %s, but old object is nil", key)
		return
	}
	oldRouter := oldObj.(*kubeovnv1.VpcEdgeRouter)
	newRouter := newObj.(*kubeovnv1.VpcEdgeRouter)
	updateVer := &updateVerObject{
		key:    key,
		oldVer: oldRouter,
		newVer: newRouter,
	}
	c.updateVpcEdgeRouterQueue.Add(updateVer)
}

func (c *Controller) handleUpdateVpcEdgeRouter(updateVerObj *updateVerObject) error {
	// ---------------------------------------------------------------------
	// 1. Retrieve latest object state from informer cache
	// ---------------------------------------------------------------------
	key := updateVerObj.key
	ns, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return fmt.Errorf("invalid key %q: %w", key, err)
	}

	c.vpcEdgeRouterKeyMutex.LockKey(key)
	defer func() { _ = c.vpcEdgeRouterKeyMutex.UnlockKey(key) }()

	cachedRouter, err := c.vpcEdgeRouterLister.VpcEdgeRouters(ns).Get(name)
	if err != nil {
		// The object may have been deleted after being enqueued.
		if k8serrors.IsNotFound(err) {
			klog.Error(err)
			return nil
		}
		return err // transient error → retry
	}

	if !cachedRouter.DeletionTimestamp.IsZero() {
		c.delVpcEdgeRouterQueue.Add(key)
		return nil
	}
	klog.Infof("reconciling vpc-edge-router %s", key)
	// Deep copy because we might mutate Status below.
	curRouter := cachedRouter.DeepCopy()

	oldRouter := updateVerObj.oldVer
	if oldRouter == nil {
		klog.Warningf("old router is nil for vpc-edge-router %s, using new version for comparison", key)
		oldRouter = updateVerObj.newVer
	}

	// routerIDChanged := oldRouter.Spec.BGP.RouterID != curRouter.Spec.BGP.RouterID
	klog.Infof("old router advertised routes: %v", oldRouter.Spec.BGP.AdvertisedRoutes)
	klog.Infof("current router advertised routes: %v", curRouter.Spec.BGP.AdvertisedRoutes)
	// Check if advertised routes have changed
	routesChanged := !slices.Equal(
		oldRouter.Spec.BGP.AdvertisedRoutes,
		curRouter.Spec.BGP.AdvertisedRoutes,
	)

	// ---------------------------------------------------------------------
	// 4. Execute business logic based on the diff outcome
	// ---------------------------------------------------------------------
	// if routerIDChanged {
	// 	if err := c.reconfigureRouterID(curRouter); err != nil {
	// 		return err
	// 	}
	// }

	if routesChanged {
		if err := c.updateAdvertisedRoutes(curRouter, oldRouter); err != nil {
			return err
		}
	}

	klog.Infof("finished reconciling vpc-edge-router %s", key)
	return nil
}

func (c *Controller) enqueueDeleteVpcEdgeRouter(obj any) {
	key := cache.MetaObjectToName(obj.(*kubeovnv1.VpcEdgeRouter)).String()
	klog.Infof("enqueue delete vpc-edge-router %s", key)
	c.delVpcEdgeRouterQueue.Add(key)
}

func (c *Controller) handleAddVpcEdgeRouter(key string) error {
	ns, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	c.vpcEdgeRouterKeyMutex.LockKey(key)
	defer func() { _ = c.vpcEdgeRouterKeyMutex.UnlockKey(key) }()

	cachedRouter, err := c.vpcEdgeRouterLister.VpcEdgeRouters(ns).Get(name)
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			klog.Error(err)
			return err
		}
		return nil
	}

	if !cachedRouter.DeletionTimestamp.IsZero() {
		c.delVpcEdgeRouterQueue.Add(key)
		return nil
	}

	klog.Infof("reconciling vpc-edge-router %s", key)
	router := cachedRouter.DeepCopy()
	if router, err = c.initVpcEdgeRouterStatus(router); err != nil {
		return err
	}

	if controllerutil.AddFinalizer(router, util.KubeOVNControllerFinalizer) {
		updatedRouter, err := c.config.KubeOvnClient.KubeovnV1().VpcEdgeRouters(router.Namespace).
			Update(context.Background(), router, metav1.UpdateOptions{})
		if err != nil {
			err = fmt.Errorf("failed to add finalizer for vpc-edge-router %s/%s: %w", router.Namespace, router.Name, err)
			klog.Error(err)
			return err
		}
		router = updatedRouter
	}

	if err := c.validateParentRefs(router); err != nil {
		klog.Error(err)
		router.Status.Conditions.SetCondition(kubeovnv1.Validated, corev1.ConditionFalse, "ParentRefValidationFailed", err.Error(), router.Generation)
		_, _ = c.updateVpcEdgeRouterStatus(router)
		return err
	}

	if err := c.reconcileVpcEdgeRouterBGP(router); err != nil {
		klog.Error(err)
		router.Status.Conditions.SetCondition(kubeovnv1.Ready, corev1.ConditionFalse, "BGPReconcileFailed", err.Error(), router.Generation)
		_, _ = c.updateVpcEdgeRouterStatus(router)
		return err
	}

	// router.Status.Conditions.SetReady("ReconcileSuccess")
	if _, err = c.updateVpcEdgeRouterStatus(router); err != nil {
		return err
	}

	klog.Infof("finished reconciling vpc-edge-router %s", key)

	return nil
}

// func (c *Controller) handleUpdateVpcEdgeRouter(key string) error {
// 	// ---------------------------------------------------------------------
// 	// 1. Retrieve latest object state from informer cache
// 	// ---------------------------------------------------------------------
// 	ns, name, err := cache.SplitMetaNamespaceKey(key)
// 	if err != nil {
// 		return fmt.Errorf("invalid key %q: %w", key, err)
// 	}

// 	c.vpcEdgeRouterKeyMutex.LockKey(key)
// 	defer func() { _ = c.vpcEdgeRouterKeyMutex.UnlockKey(key) }()

// 	cachedRouter, err := c.vpcEdgeRouterLister.VpcEdgeRouters(ns).Get(name)
// 	if err != nil {
// 		// The object may have been deleted after being enqueued.
// 		if k8serrors.IsNotFound(err) {
// 			klog.Error(err)
// 			return nil
// 		}
// 		return err // transient error → retry
// 	}

// 	if !cachedRouter.DeletionTimestamp.IsZero() {
// 		c.delVpcEdgeRouterQueue.Add(key)
// 		return nil
// 	}
// 	klog.Infof("reconciling vpc-edge-router %s", key)
// 	// Deep copy because we might mutate Status below.
// 	curRouter := cachedRouter.DeepCopy()

// 	var oldRouter kubeovnv1.VpcEdgeRouter
// 	if err := json.Unmarshal([]byte(curRouter.GetAnnotations()[LastAppliedConfigAnnotation]), &oldRouter); err != nil {
// 		return fmt.Errorf("failed to unmarshal last-applied-configuration: %w", err)
// 	}
// 	// routerIDChanged := oldRouter.Spec.BGP.RouterID != curRouter.Spec.BGP.RouterID
// 	klog.Infof("old router advertised routes: %v", oldRouter.Spec.BGP.AdvertisedRoutes)
// 	klog.Infof("current router advertised routes: %v", curRouter.Spec.BGP.AdvertisedRoutes)
// 	// Check if advertised routes have changed
// 	routesChanged := !slices.Equal(
// 		oldRouter.Spec.BGP.AdvertisedRoutes,
// 		curRouter.Spec.BGP.AdvertisedRoutes,
// 	)

// 	// ---------------------------------------------------------------------
// 	// 4. Execute business logic based on the diff outcome
// 	// ---------------------------------------------------------------------
// 	// if routerIDChanged {
// 	// 	if err := c.reconfigureRouterID(curRouter); err != nil {
// 	// 		return err
// 	// 	}
// 	// }

// 	if routesChanged {
// 		if err := c.updateAdvertisedRoutes(curRouter, &oldRouter); err != nil {
// 			return err
// 		}
// 	}

// 	klog.Infof("finished reconciling vpc-edge-router %s", key)
// 	return nil
// }

// func (c *Controller) reconfigureRouterID(router *kubeovnv1.VpcEdgeRouter) error {
// 	// 1. Validate new RouterID (must be valid IP, clash-free, etc.)
// 	if net.ParseIP(router.Spec.BGP.RouterID) == nil {
// 		return fmt.Errorf("invalid routerId %q", router.Spec.BGP.RouterID)
// 	}

// 	// // 2. Push config to BGP speaker (e.g., FRR, GoBGP)
// 	// if err := c.bgpManager.SetRouterID(router.Namespace, router.Name, router.Spec.BGP.RouterID); err != nil {
// 	// 	return err
// 	// }

// 	// // 3. Emit event
// 	// c.recorder.Event(er, corev1.EventTypeNormal, "RouterIDUpdated",
// 	// 	fmt.Sprintf("Set router-id=%s", er.Spec.Bgp.RouterId))

// 	return nil
// }

func (c *Controller) updateAdvertisedRoutes(newRouter, oldRouter *kubeovnv1.VpcEdgeRouter) error {
	newCidrs := newRouter.Spec.BGP.AdvertisedRoutes
	oldCidrs := oldRouter.Spec.BGP.AdvertisedRoutes
	klog.Infof("new cidrs: %v", newCidrs)
	klog.Infof("old cidrs: %v", oldCidrs)

	// Validate each CIDR
	for _, cidr := range newCidrs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("malformed CIDR %q: %w", cidr, err)
		}
	}

	routerPods, routerErr := c.getRouterPods(newRouter)
	if routerErr != nil {
		return fmt.Errorf("failed to get router pods for vpc-edge-router %s/%s: %w", newRouter.Namespace, newRouter.Name, routerErr)
	}
	if len(routerPods) == 0 {
		return fmt.Errorf("no router pods found for vpc-edge-router %s/%s", newRouter.Namespace, newRouter.Name)
	}
	for _, routerPod := range routerPods {
		if err := c.execUpdateBgpRoute(routerPod, oldCidrs, newCidrs); err != nil {
			return err
		}
	}

	return nil
}

func (c *Controller) getRouterPods(router *kubeovnv1.VpcEdgeRouter) ([]*corev1.Pod, error) {
	if len(router.Spec.ParentRefs) == 0 {
		return nil, fmt.Errorf("no parent references found for VpcEdgeRouter %s/%s",
			router.Namespace, router.Name)
	}

	var allPods []*corev1.Pod
	var lastError error

	for _, parentRef := range router.Spec.ParentRefs {
		namespace := parentRef.Namespace
		if namespace == "" {
			namespace = router.Namespace
		}

		// List VpcEgressGateway
		gateway, err := c.vpcEgressGatewayLister.VpcEgressGateways(namespace).Get(parentRef.Name)
		if err != nil {
			lastError = err
			if k8serrors.IsNotFound(err) {
				klog.Errorf("Referenced VPC Egress Gateway %s/%s not found for router %s/%s",
					namespace, parentRef.Name, router.Namespace, router.Name)
			} else {
				klog.Errorf("Failed to get VPC Egress Gateway %s/%s for router %s/%s: %v",
					namespace, parentRef.Name, router.Namespace, router.Name, err)
			}
			continue
		}

		// List Pods from Gateway
		pods, err := c.getPodsFromGateway(gateway)
		if err != nil {
			lastError = err
			klog.Errorf("Failed to get pods from gateway %s/%s: %v",
				gateway.Namespace, gateway.Name, err)
			continue
		}

		// Validate pod, status runnuing
		for _, pod := range pods {
			if pod.Status.Phase == corev1.PodRunning {
				allPods = append(allPods, pod)
			}
		}
	}

	if len(allPods) == 0 {
		if lastError != nil {
			return nil, fmt.Errorf("no running pods found for VpcEdgeRouter %s/%s, last error: %w",
				router.Namespace, router.Name, lastError)
		}
		return nil, fmt.Errorf("no running pods found for any parent references of VpcEdgeRouter %s/%s",
			router.Namespace, router.Name)
	}

	klog.Infof("Found %d running pods for VpcEdgeRouter %s/%s",
		len(allPods), router.Namespace, router.Name)

	return allPods, nil
}

func (c *Controller) getPodsFromGateway(gateway *kubeovnv1.VpcEgressGateway) ([]*corev1.Pod, error) {
	var pods []*corev1.Pod

	if gateway.Status.Workload.Name != "" {
		workloadPods, err := c.getPodsFromWorkload(gateway)
		if err != nil {
			klog.Warningf("Failed to get pods from workload for gateway %s/%s: %v",
				gateway.Namespace, gateway.Name, err)
		} else {
			pods = append(pods, workloadPods...)
		}
	}

	if len(pods) == 0 {
		labelPods, err := c.getPodsFromLabelSelector(gateway)
		if err != nil {
			return nil, fmt.Errorf("failed to get pods using label selector: %w", err)
		}
		pods = append(pods, labelPods...)
	}

	return pods, nil
}

func (c *Controller) getPodsFromWorkload(gateway *kubeovnv1.VpcEgressGateway) ([]*corev1.Pod, error) {
	workload := gateway.Status.Workload
	return c.getPodsFromDeployment(gateway.Namespace, workload.Name)
}

func (c *Controller) getPodsFromDeployment(namespace, name string) ([]*corev1.Pod, error) {
	deployment, err := c.deploymentsLister.Deployments(namespace).Get(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment %s/%s: %w", namespace, name, err)
	}

	// Use the deployment's label selector to find pods
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return nil, fmt.Errorf("failed to convert label selector: %w", err)
	}

	pods, err := c.podsLister.Pods(namespace).List(selector)
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	return pods, nil
}

func (c *Controller) getPodsFromLabelSelector(gateway *kubeovnv1.VpcEgressGateway) ([]*corev1.Pod, error) {
	// VPC Egress Gateway label selector
	labelSelector := labels.SelectorFromSet(labels.Set{
		"app":                      "vpc-egress-gateway",
		util.VpcEgressGatewayLabel: gateway.Name,
	})

	pods, err := c.podsLister.Pods(gateway.Namespace).List(labelSelector)
	if err != nil {
		return nil, fmt.Errorf("failed to list pods with label selector: %w", err)
	}

	return pods, nil
}

func (c *Controller) execUpdateBgpRoute(pod *corev1.Pod, oldCidrs, newCidrs []string) error {
	// add_announced_route
	cmdArs := []string{}
	if len(oldCidrs) > 0 {
		cmdArs = append(cmdArs, "del_announced_route="+strings.Join(oldCidrs, ","))
	}
	if len(newCidrs) > 0 {
		cmdArs = append(cmdArs, "add_announced_route="+strings.Join(newCidrs, ","))
	}
	cmdArs = append(cmdArs, "list_announced_route")
	cmd := fmt.Sprintf("bash /kube-ovn/update-bgp-route.sh %s", strings.Join(cmdArs, " "))

	klog.Infof("exec command : %s", cmd)
	stdOutput, errOutput, err := util.ExecuteCommandInContainer(c.config.KubeClient, c.config.KubeRestConfig, pod.Namespace, pod.Name, "vpc-edge-router-speaker", []string{"/bin/bash", "-c", cmd}...)
	if err != nil {
		if len(errOutput) > 0 {
			klog.Errorf("failed to ExecuteCommandInContainer, errOutput: %v", errOutput)
		}
		if len(stdOutput) > 0 {
			klog.Infof("failed to ExecuteCommandInContainer, stdOutput: %v", stdOutput)
		}
		klog.Error(err)
		return err
	}

	if len(stdOutput) > 0 {
		klog.Infof("ExecuteCommandInContainer stdOutput: %v", stdOutput)
	}

	if len(errOutput) > 0 {
		klog.Errorf("failed to ExecuteCommandInContainer errOutput: %v", errOutput)
		return errors.New(errOutput)
	}

	// list the current rule and check if the routes are updated

	return nil
}

func (c *Controller) initVpcEdgeRouterStatus(router *kubeovnv1.VpcEdgeRouter) (*kubeovnv1.VpcEdgeRouter, error) {
	var err error
	if len(router.Status.Conditions) == 0 {
		router.Status.Conditions.SetCondition(kubeovnv1.Init, corev1.ConditionUnknown, "Processing", "", router.Generation)
		router, err = c.updateVpcEdgeRouterStatus(router)
	}
	return router, err
}

func (c *Controller) updateVpcEdgeRouterStatus(router *kubeovnv1.VpcEdgeRouter) (*kubeovnv1.VpcEdgeRouter, error) {
	updateRouter, err := c.config.KubeOvnClient.KubeovnV1().VpcEdgeRouters(router.Namespace).
		UpdateStatus(context.Background(), router, metav1.UpdateOptions{})
	if err != nil {
		err = fmt.Errorf("failed to update status of vpc-edge-router %s/%s: %w", router.Namespace, router.Name, err)
		klog.Error(err)
		return nil, err
	}
	return updateRouter, nil
}

func (c *Controller) validateParentRefs(router *kubeovnv1.VpcEdgeRouter) error {
	for _, parentRef := range router.Spec.ParentRefs {
		// set default group and kind if not specified
		group := parentRef.Group
		if group == "" {
			group = "kubeovn.io"
		}
		kind := parentRef.Kind
		if kind == "" {
			kind = "VpcEgressGateway"
		}

		// VPC Egress Gateway is the only supported parent reference for VPC Edge Router
		if group == "kubeovn.io" && kind == "VpcEgressGateway" {
			namespace := parentRef.Namespace
			if namespace == "" {
				namespace = router.Namespace
			}

			_, err := c.vpcEgressGatewayLister.VpcEgressGateways(namespace).Get(parentRef.Name)
			if err != nil {
				if k8serrors.IsNotFound(err) {
					return fmt.Errorf("referenced VPC Egress Gateway %s/%s not found", namespace, parentRef.Name)
				}
				return fmt.Errorf("failed to get VPC Egress Gateway %s/%s: %w", namespace, parentRef.Name, err)
			}
		}
	}
	return nil
}

func (c *Controller) reconcileVpcEdgeRouterBGP(router *kubeovnv1.VpcEdgeRouter) error {
	if !router.Spec.BGP.Enabled {
		klog.Infof("BGP is disabled for vpc-edge-router %s/%s", router.Namespace, router.Name)
		return nil
	}

	if router.Spec.BGP.ASN == 0 || router.Spec.BGP.RemoteASN == 0 {
		return errors.New("BGP ASN and RemoteASN must be specified")
	}

	if len(router.Spec.BGP.Neighbors) == 0 {
		return errors.New("BGP neighbors must be specified")
	}
	if router.Spec.BGP.Image == "" {
		return errors.New("BGP speaker image must be specified")
	}

	args := []string{}
	if router.Spec.BGP.EdgeRouterMode {
		args = append(args, "--edge-router-mode=true")
	}
	if router.Spec.BGP.RouterID != "" {
		args = append(args, "--router-id="+router.Spec.BGP.RouterID)
	}
	if router.Spec.BGP.Password != "" {
		args = append(args, "--auth-password="+router.Spec.BGP.Password)
	}
	if router.Spec.BGP.EnableGracefulRestart {
		args = append(args, "--graceful-restart")
	}
	if router.Spec.BGP.HoldTime != (metav1.Duration{}) {
		args = append(args, "--holdtime="+router.Spec.BGP.HoldTime.Duration.String())
	}

	args = append(args, fmt.Sprintf("--cluster-as=%d", router.Spec.BGP.ASN))
	args = append(args, fmt.Sprintf("--neighbor-as=%d", router.Spec.BGP.RemoteASN))

	var neighIPv4, neighIPv6 []string
	for _, neighbor := range router.Spec.BGP.Neighbors {
		switch util.CheckProtocol(neighbor) {
		case kubeovnv1.ProtocolIPv4:
			neighIPv4 = append(neighIPv4, neighbor)
		case kubeovnv1.ProtocolIPv6:
			neighIPv6 = append(neighIPv6, neighbor)
		default:
			return fmt.Errorf("unsupported protocol for peer %s", neighbor)
		}
	}
	if len(neighIPv4) > 0 {
		args = append(args, "--neighbor-address="+strings.Join(neighIPv4, ","))
	}
	if len(neighIPv6) > 0 {
		args = append(args, "--neighbor-ipv6-address="+strings.Join(neighIPv6, ","))
	}

	var advertiseIPv4, advertiseIPv6 []string
	for _, advertisedRoutes := range router.Spec.BGP.AdvertisedRoutes {
		switch util.CheckProtocol(advertisedRoutes) {
		case kubeovnv1.ProtocolIPv4:
			advertiseIPv4 = append(advertiseIPv4, advertisedRoutes)
		case kubeovnv1.ProtocolIPv6:
			advertiseIPv6 = append(advertiseIPv6, advertisedRoutes)
		default:
			return fmt.Errorf("unsupported protocol for peer %s", advertisedRoutes)
		}
	}
	if len(advertiseIPv4) > 0 {
		args = append(args, "--advertised-routes="+strings.Join(advertiseIPv4, ","))
	}
	if len(advertiseIPv6) > 0 {
		args = append(args, "--advertised-ipv6-routes="+strings.Join(advertiseIPv6, ","))
	}

	for _, parent := range router.Spec.ParentRefs {
		ns := parent.Namespace
		if ns == "" {
			ns = router.Namespace
		}
		gwName := parent.Name

		// Strategic Merge Patch payload 구성
		patchData := map[string]any{
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"volumes": []map[string]any{
							{
								"name": "bgp-speaker-config",
								"configMap": map[string]any{
									"name": gwName + "-bgp-speaker-config",
								},
							},
						},
						"containers": []map[string]any{
							{
								"name":            "vpc-edge-router-speaker",
								"image":           router.Spec.BGP.Image,
								"command":         []string{"/kube-ovn/kube-ovn-speaker"},
								"imagePullPolicy": corev1.PullIfNotPresent,
								"env": []map[string]any{
									{"name": "EGRESS_GATEWAY_NAME", "value": gwName},
									{"name": "POD_IP", "valueFrom": map[string]any{
										"fieldRef": map[string]any{"fieldPath": "status.podIP"},
									}},
									{"name": "MULTI_NET_STATUS", "valueFrom": map[string]any{
										"fieldRef": map[string]any{
											"fieldPath": "metadata.annotations['k8s.v1.cni.cncf.io/networks-status']",
										},
									}},
								},
								"args": args,
								"securityContext": map[string]any{
									"runAsUser":  0,
									"privileged": false,
									"capabilities": map[string]any{
										"add":  []string{"NET_ADMIN", "NET_BIND_SERVICE", "NET_RAW"},
										"drop": []string{"ALL"},
									},
								},
							},
						},
					},
				},
			},
		}

		patchBytes, err := json.Marshal(patchData)
		if err != nil {
			return fmt.Errorf("failed to marshal strategic merge patch: %w", err)
		}

		// StrategicMergePatchType 으로 요청
		if _, err := c.config.KubeClient.AppsV1().
			Deployments(ns).
			Patch(context.Background(),
				gwName,
				types.StrategicMergePatchType,
				patchBytes,
				metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("failed to strategic-merge-patch Deployment %s/%s: %w", ns, gwName, err)
		}
	}

	// Update VPCEdgeRouter status
	// doesn't work
	router.Status.BGPStatus = &kubeovnv1.BGPStatus{
		SessionState: "Established",
		PeersStatus:  make([]kubeovnv1.PeerStatus, len(router.Spec.BGP.Neighbors)),
	}
	for i, neighbor := range router.Spec.BGP.Neighbors {
		router.Status.BGPStatus.PeersStatus[i] = kubeovnv1.PeerStatus{
			Address:          neighbor,
			State:            "Established",
			Uptime:           "00:01:00",
			ReceivedRoutes:   0,
			AdvertisedRoutes: 0,
		}
	}
	if _, err := c.updateVpcEdgeRouterStatus(router); err != nil {
		return err
	}

	klog.Infof("Patched speaker sidecar into VpcEgressGateway and reconciled vpc-edge-router %s/%s",
		router.Namespace, router.Name)
	return nil
}

func (c *Controller) handleDelVpcEdgeRouter(key string) error {
	ns, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	c.vpcEdgeRouterKeyMutex.LockKey(key)
	defer func() { _ = c.vpcEdgeRouterKeyMutex.UnlockKey(key) }()

	cachedRouter, err := c.vpcEdgeRouterLister.VpcEdgeRouters(ns).Get(name)
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			err = fmt.Errorf("failed to get vpc-edge-router %s: %w", key, err)
			klog.Error(err)
			return err
		}
		return nil
	}

	for _, parent := range cachedRouter.Spec.ParentRefs {
		gwNS := parent.Namespace
		if gwNS == "" {
			gwNS = ns
		}
		gwName := parent.Name

		// 2) Strategic Merge Patch payload: containers
		patch := map[string]any{
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"containers": []map[string]any{
							{
								"name":   "vpc-edge-router-speaker",
								"$patch": "delete",
							},
						},
					},
				},
			},
		}

		patchBytes, err := json.Marshal(patch)
		if err != nil {
			klog.Errorf("failed to marshal delete patch for %s/%s: %v", gwNS, gwName, err)
			continue
		}

		// 3) patch to deployment
		// Use StrategicMergePatchType to remove the sidecar container
		_, err = c.config.KubeClient.AppsV1().
			Deployments(gwNS).
			Patch(context.Background(),
				gwName,
				types.StrategicMergePatchType,
				patchBytes,
				metav1.PatchOptions{})
		if err != nil {
			klog.Errorf("failed to remove BGP speaker from Deployment %s/%s: %v", gwNS, gwName, err)
			continue
		}
		klog.Infof("removed BGP speaker sidecar from %s/%s", gwNS, gwName)
	}

	router := cachedRouter.DeepCopy()
	if controllerutil.RemoveFinalizer(router, util.KubeOVNControllerFinalizer) {
		if _, err = c.config.KubeOvnClient.KubeovnV1().VpcEdgeRouters(router.Namespace).
			Update(context.Background(), router, metav1.UpdateOptions{}); err != nil {
			err = fmt.Errorf("failed to remove finalizer from vpc-edge-router %s: %w", key, err)
			klog.Error(err)
		}
	}

	return nil
}
