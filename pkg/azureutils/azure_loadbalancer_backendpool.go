package azureutils

import (
	armnetwork "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v2"
	"github.com/Azure/azure-sdk-for-go/services/network/mgmt/2019-06-01/network"

	v1 "k8s.io/api/core/v1"
)

type backendPoolTypeNodeIPConfig struct {
	*Cloud
}

type backendPoolTypeNodeIP struct {
	*Cloud
}

type BackendPool interface {
	// EnsureHostsInPool ensures the nodes join the backend pool of the load balancer
	EnsureHostsInPool(service *v1.Service, nodes []*v1.Node, backendPoolID, vmSetName, clusterName, lbName string, backendPool armnetwork.BackendAddressPool) error

	// CleanupVMSetFromBackendPoolByCondition removes nodes of the unwanted vmSet from the lb backend pool.
	// This is needed in two scenarios:
	// 1. When migrating from single SLB to multiple SLBs, the existing
	// SLB's backend pool contains nodes from different agent pools, while we only want the
	// nodes from the primary agent pool to join the backend pool.
	// 2. When migrating from dedicated SLB to shared SLB (or vice versa), we should move the vmSet from
	// one SLB to another one.
	CleanupVMSetFromBackendPoolByCondition(slb *armnetwork.LoadBalancer, service *v1.Service, nodes []*v1.Node, clusterName string, shouldRemoveVMSetFromSLB func(string) bool) (*network.LoadBalancer, error)

	// ReconcileBackendPools creates the inbound backend pool if it is not existed, and removes nodes that are supposed to be
	// excluded from the load balancers.
	ReconcileBackendPools(clusterName string, service *v1.Service, lb *armnetwork.LoadBalancer) (bool, bool, bool, error)

	// GetBackendPrivateIPs returns the private IPs of LoadBalancer's backend pool
	GetBackendPrivateIPs(clusterName string, service *v1.Service, lb *armnetwork.LoadBalancer) ([]string, []string)
}

func newBackendPoolTypeNodeIPConfig(c *Cloud) BackendPool {
	return &backendPoolTypeNodeIPConfig{c}
}

func newBackendPoolTypeNodeIP(c *Cloud) BackendPool {
	return &backendPoolTypeNodeIP{c}
}

// PLACEHOLDER
// EnsureHostsInPool ensures the nodes join the backend pool of the load balancer
func (bc *backendPoolTypeNodeIPConfig) EnsureHostsInPool(service *v1.Service, nodes []*v1.Node, backendPoolID, vmSetName, clusterName, lbName string, backendPool armnetwork.BackendAddressPool) error {
	return nil
}

// CleanupVMSetFromBackendPoolByCondition removes nodes of the unwanted vmSet from the lb backend pool.
// This is needed in two scenarios:
// 1. When migrating from single SLB to multiple SLBs, the existing
// SLB's backend pool contains nodes from different agent pools, while we only want the
// nodes from the primary agent pool to join the backend pool.
// 2. When migrating from dedicated SLB to shared SLB (or vice versa), we should move the vmSet from
// one SLB to another one.
func (bc *backendPoolTypeNodeIPConfig) CleanupVMSetFromBackendPoolByCondition(slb *armnetwork.LoadBalancer, service *v1.Service, nodes []*v1.Node, clusterName string, shouldRemoveVMSetFromSLB func(string) bool) (*network.LoadBalancer, error) {
	return nil, nil
}

// ReconcileBackendPools creates the inbound backend pool if it is not existed, and removes nodes that are supposed to be
// excluded from the load balancers.
func (bc *backendPoolTypeNodeIPConfig) ReconcileBackendPools(clusterName string, service *v1.Service, lb *armnetwork.LoadBalancer) (bool, bool, bool, error) {
	return false, false, false, nil
}

// GetBackendPrivateIPs returns the private IPs of LoadBalancer's backend pool
func (bc *backendPoolTypeNodeIPConfig) GetBackendPrivateIPs(clusterName string, service *v1.Service, lb *armnetwork.LoadBalancer) ([]string, []string) {
	return nil, nil
}

// PLACEHOLDER
// EnsureHostsInPool ensures the nodes join the backend pool of the load balancer
func (bc *backendPoolTypeNodeIP) EnsureHostsInPool(service *v1.Service, nodes []*v1.Node, backendPoolID, vmSetName, clusterName, lbName string, backendPool armnetwork.BackendAddressPool) error {
	return nil
}

// CleanupVMSetFromBackendPoolByCondition removes nodes of the unwanted vmSet from the lb backend pool.
// This is needed in two scenarios:
// 1. When migrating from single SLB to multiple SLBs, the existing
// SLB's backend pool contains nodes from different agent pools, while we only want the
// nodes from the primary agent pool to join the backend pool.
// 2. When migrating from dedicated SLB to shared SLB (or vice versa), we should move the vmSet from
// one SLB to another one.
func (bc *backendPoolTypeNodeIP) CleanupVMSetFromBackendPoolByCondition(slb *armnetwork.LoadBalancer, service *v1.Service, nodes []*v1.Node, clusterName string, shouldRemoveVMSetFromSLB func(string) bool) (*network.LoadBalancer, error) {
	return nil, nil
}

// ReconcileBackendPools creates the inbound backend pool if it is not existed, and removes nodes that are supposed to be
// excluded from the load balancers.
func (bc *backendPoolTypeNodeIP) ReconcileBackendPools(clusterName string, service *v1.Service, lb *armnetwork.LoadBalancer) (bool, bool, bool, error) {
	return false, false, false, nil
}

// GetBackendPrivateIPs returns the private IPs of LoadBalancer's backend pool
func (bc *backendPoolTypeNodeIP) GetBackendPrivateIPs(clusterName string, service *v1.Service, lb *armnetwork.LoadBalancer) ([]string, []string) {
	return nil, nil
}
