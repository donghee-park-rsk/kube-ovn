package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VPCEdgeRouterList VPC Edge Router list
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type VpcEdgeRouterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []VpcEdgeRouter `json:"items"`
}

// +genclient
// +genclient:method=GetScale,verb=get,subresource=scale,result=k8s.io/api/autoscaling/v1.Scale
// +genclient:method=UpdateScale,verb=update,subresource=scale,input=k8s.io/api/autoscaling/v1.Scale,result=k8s.io/api/autoscaling/v1.Scale
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resourceName=vpc-edge-routers
// VpcEdgeRouter BGP CRD
type VpcEdgeRouter struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	Spec   VpcEdgeRouterSpec   `json:"spec"`
	Status VpcEdgeRouterStatus `json:"status"`
}

// VpcEdgeRouterSpec BGP spec
type VpcEdgeRouterSpec struct {
	// ParentRefs refer VPC Egress Gateway
	ParentRefs []ParentReference `json:"parentRefs,omitempty"`

	// BGP settings
	BGP BGPConfig `json:"bgp"`
}

// ParentReference Gateway API
type ParentReference struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// BGPConfig BGP
type BGPConfig struct {
	EdgeRouterMode        bool            `json:"edgeRouterMode,omitempty"`
	Enabled               bool            `json:"enabled,omitempty"`
	Image                 string          `json:"image,omitempty"`
	ASN                   int32           `json:"asn,omitempty"`
	RemoteASN             int32           `json:"remoteAsn,omitempty"`
	Neighbors             []string        `json:"neighbors,omitempty"`
	HoldTime              metav1.Duration `json:"holdTime"`
	RouterID              string          `json:"routerId,omitempty"`
	Password              string          `json:"password,omitempty"`
	EnableGracefulRestart bool            `json:"enableGracefulRestart,omitempty"`
	ExtraArgs             []string        `json:"extraArgs,omitempty"`
	AdvertisedRoutes      []string        `json:"advertisedRoutes,omitempty"`
}

// VPCEdgeRouterStatus BGP status
type VpcEdgeRouterStatus struct {
	Conditions Conditions `json:"conditions,omitempty"`
	BGPStatus  *BGPStatus `json:"bgpStatus,omitempty"`
}

// BGPStatus BGP session status
type BGPStatus struct {
	SessionState string       `json:"sessionState,omitempty"`
	PeersStatus  []PeerStatus `json:"peersStatus,omitempty"`
}

// PeerStatus BGP peer status
type PeerStatus struct {
	Address          string `json:"address,omitempty"`
	State            string `json:"state,omitempty"`
	Uptime           string `json:"uptime,omitempty"`
	ReceivedRoutes   int    `json:"receivedRoutes,omitempty"`
	AdvertisedRoutes int    `json:"advertisedRoutes,omitempty"`
}
