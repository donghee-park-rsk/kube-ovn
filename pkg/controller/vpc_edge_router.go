package controller

import (
	"context"
	"encoding/json"
	"errors"
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

func (c *Controller) enqueueAddVpcEdgeRouter(obj any) {
	key := cache.MetaObjectToName(obj.(*kubeovnv1.VpcEdgeRouter)).String()
	klog.V(3).Infof("enqueue add vpc-edge-router %s", key)
	c.addOrUpdateVpcEdgeRouterQueue.Add(key)
}

func (c *Controller) enqueueUpdateVpcEdgeRouter(_, newObj any) {
	key := cache.MetaObjectToName(newObj.(*kubeovnv1.VpcEdgeRouter)).String()
	klog.V(3).Infof("enqueue update vpc-edge-router %s", key)
	c.addOrUpdateVpcEdgeRouterQueue.Add(key)
}

func (c *Controller) enqueueDeleteVpcEdgeRouter(obj any) {
	key := cache.MetaObjectToName(obj.(*kubeovnv1.VpcEdgeRouter)).String()
	klog.V(3).Infof("enqueue delete vpc-edge-router %s", key)
	c.delVpcEdgeRouterQueue.Add(key)
}

func (c *Controller) handleAddOrUpdateVpcEdgeRouter(key string) error {
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
		klog.V(3).Infof("BGP is disabled for vpc-edge-router %s/%s", router.Namespace, router.Name)
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

		// Already validate vpc egress gateway in validateParentRefs
		// gw, err := c.vpcEgressGatewayLister.VpcEgressGateways(ns).Get(parent.Name)
		// if err != nil {
		// 	return fmt.Errorf("failed to get VpcEgressGateway %s/%s: %w", ns, parent.Name, err)
		// }

		// Build JSON patch operations
		// var patchOps []map[string]any

		// 1) Add volume for speaker config
		// patchOps = append(patchOps, map[string]any{
		// 	"op":   "add",
		// 	"path": "/spec/template/spec/volumes/-",
		// 	"value": corev1.Volume{
		// 		Name: "bgp-speaker-config",
		// 		VolumeSource: corev1.VolumeSource{
		// 			ConfigMap: &corev1.ConfigMapVolumeSource{
		// 				LocalObjectReference: corev1.LocalObjectReference{
		// 					Name: gw.Name + "-bgp-speaker-config",
		// 				},
		// 			},
		// 		},
		// 	},
		// })

		// ADD LOGIC
		// bgpSpeakerContainer := &corev1.Container{
		// 	Name:            "vpc-egress-gw-speaker",
		// 	Image:           router.Spec.BGP.Image,
		// 	Command:         []string{"/kube-ovn/kube-ovn-speaker"},
		// 	ImagePullPolicy: corev1.PullIfNotPresent,
		// 	Env: []corev1.EnvVar{
		// 		{
		// 			Name:  "EGRESS_GATEWAY_NAME",
		// 			Value: gw.Name,
		// 		},
		// 		{
		// 			Name: "POD_IP",
		// 			ValueFrom: &corev1.EnvVarSource{
		// 				FieldRef: &corev1.ObjectFieldSelector{
		// 					FieldPath: "status.podIP",
		// 				},
		// 			},
		// 		},
		// 		{
		// 			Name: "MULTI_NET_STATUS",
		// 			ValueFrom: &corev1.EnvVarSource{
		// 				FieldRef: &corev1.ObjectFieldSelector{
		// 					FieldPath: "metadata.annotations['k8s.v1.cni.cncf.io/networks-status']",
		// 				},
		// 			},
		// 		},
		// 	},
		// 	Args: args,
		// 	// bgp need to add/remove fib, it needs root user
		// 	SecurityContext: &corev1.SecurityContext{
		// 		Privileged: ptr.To(false),
		// 		RunAsUser:  ptr.To[int64](0),
		// 		Capabilities: &corev1.Capabilities{
		// 			Add:  []corev1.Capability{"NET_ADMIN", "NET_BIND_SERVICE", "NET_RAW"},
		// 			Drop: []corev1.Capability{"ALL"},
		// 		},
		// 	},
		// }
		// // 2) Add the sidecar container with env and securityContext
		// patchOps = append(patchOps, map[string]any{
		// 	"op":    "add",
		// 	"path":  "/spec/template/spec/containers/-",
		// 	"value": bgpSpeakerContainer,
		// })

		// // Marshal and apply patch
		// patchBytes, err := json.Marshal(patchOps)
		// if err != nil {
		// 	return fmt.Errorf("failed to marshal patch: %w", err)
		// }
		// if _, err := c.config.KubeClient.AppsV1().Deployments(ns).
		// 	Patch(context.Background(),
		// 		gw.Status.Workload.Name,
		// 		types.JSONPatchType,
		// 		patchBytes,
		// 		metav1.PatchOptions{}); err != nil {
		// 	return fmt.Errorf("failed to patch deployment %s/%s: %w", ns, gw.Status.Workload.Name, err)
		// }
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
