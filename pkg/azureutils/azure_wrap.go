package azureutils

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	armnetwork "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v2"

	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
)

const (
	// ID string used to create a not existing PLS placehold in plsCache to avoid redundant
	PrivateLinkServiceNotExistID = "PrivateLinkServiceNotExistID"
)

var (
	vmCacheTTLDefaultInSeconds           = 60
	loadBalancerCacheTTLDefaultInSeconds = 120
	nsgCacheTTLDefaultInSeconds          = 120
	routeTableCacheTTLDefaultInSeconds   = 120
	publicIPCacheTTLDefaultInSeconds     = 120
	plsCacheTTLDefaultInSeconds          = 120
)

func (az *Cloud) newVMCache() (*TimedCache, error) {
	getter := func(key string) (interface{}, error) {
		// Currently InstanceView request are used by azure_zones, while the calls come after non-InstanceView
		// request. If we first send an InstanceView request and then a non InstanceView request, the second
		// request will still hit throttling. This is what happens now for cloud controller manager: In this
		// case we do get instance view every time to fulfill the azure_zones requirement without hitting
		// throttling.
		// Consider adding separate parameter for controlling 'InstanceView' once node update issue #56276 is fixed
		ctx, cancel := getContextWithCancel()
		defer cancel()

		resourceGroup, err := az.GetNodeResourceGroup(key)
		if err != nil {
			return nil, err
		}

		res, verr := az.VMClient.Get(ctx, resourceGroup, key, nil)
		vm := res.VirtualMachine
		exists, rerr := checkResourceExistsFromError(&Error{
			RawError: verr,
		})
		if rerr != nil {
			return nil, rerr.Error()
		}

		if !exists {
			klog.V(2).Infof("Virtual machine %q not found", key)
			return nil, nil
		}

		if vm.Properties != nil &&
			strings.EqualFold(pointer.StringDeref(vm.Properties.ProvisioningState, ""), string(ProvisioningStateDeleting)) {
			klog.V(2).Infof("Virtual machine %q is under deleting", key)
			return nil, nil
		}

		return &vm, nil
	}

	if az.VMCacheTTLInSeconds == 0 {
		az.VMCacheTTLInSeconds = vmCacheTTLDefaultInSeconds
	}
	return NewTimedcache(time.Duration(az.VMCacheTTLInSeconds)*time.Second, getter)
}

// checkExistsFromError inspects an error and returns a true if err is nil,
// false if error is an autorest.Error with StatusCode=404 and will return the
// error back if error is another status code or another type of error.
func checkResourceExistsFromError(err *Error) (bool, *Error) {
	if err == nil {
		return true, nil
	}

	if err.HTTPStatusCode == http.StatusNotFound {
		return false, nil
	}

	return false, err
}

func getContextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

func (az *Cloud) newLBCache() (*TimedCache, error) {
	getter := func(key string) (interface{}, error) {
		ctx, cancel := getContextWithCancel()
		defer cancel()

		var resourceGroup string
		if az.LoadBalancerResourceGroup != "" {
			resourceGroup = az.LoadBalancerResourceGroup
		} else {
			resourceGroup = az.ResourceGroup
		}

		res, err := az.LoadBalancerClient.Get(ctx, resourceGroup, key, nil)
		lb := res.LoadBalancer
		exists, rerr := checkResourceExistsFromError(&Error{
			RawError: err,
		})
		if rerr != nil {
			return nil, rerr.Error()
		}

		if !exists {
			klog.V(2).Infof("Load balancer %q not found", key)
			return nil, nil
		}

		return &lb, nil
	}

	if az.LoadBalancerCacheTTLInSeconds == 0 {
		az.LoadBalancerCacheTTLInSeconds = loadBalancerCacheTTLDefaultInSeconds
	}
	return NewTimedcache(time.Duration(az.LoadBalancerCacheTTLInSeconds)*time.Second, getter)
}

func (az *Cloud) newNSGCache() (*TimedCache, error) {
	getter := func(key string) (interface{}, error) {
		ctx, cancel := getContextWithCancel()
		defer cancel()
		res, err := az.SecurityGroupsClient.Get(ctx, az.SecurityGroupResourceGroup, key, nil)
		nsg := res.SecurityGroup
		exists, rerr := checkResourceExistsFromError(&Error{
			RawError: err,
		})
		if rerr != nil {
			return nil, rerr.Error()
		}

		if !exists {
			klog.V(2).Infof("Security group %q not found", key)
			return nil, nil
		}

		return &nsg, nil
	}

	if az.NsgCacheTTLInSeconds == 0 {
		az.NsgCacheTTLInSeconds = nsgCacheTTLDefaultInSeconds
	}
	return NewTimedcache(time.Duration(az.NsgCacheTTLInSeconds)*time.Second, getter)
}

func (az *Cloud) newRouteTableCache() (*TimedCache, error) {
	getter := func(key string) (interface{}, error) {
		ctx, cancel := getContextWithCancel()
		defer cancel()
		res, err := az.RouteTablesClient.Get(ctx, az.RouteTableResourceGroup, key, nil)
		rt := res.RouteTable
		exists, rerr := checkResourceExistsFromError(&Error{
			RawError: err,
		})
		if rerr != nil {
			return nil, rerr.Error()
		}

		if !exists {
			klog.V(2).Infof("Route table %q not found", key)
			return nil, nil
		}

		return &rt, nil
	}

	if az.RouteTableCacheTTLInSeconds == 0 {
		az.RouteTableCacheTTLInSeconds = routeTableCacheTTLDefaultInSeconds
	}
	return NewTimedcache(time.Duration(az.RouteTableCacheTTLInSeconds)*time.Second, getter)
}

func (az *Cloud) newPIPCache() (*TimedCache, error) {
	getter := func(key string) (interface{}, error) {
		_, cancel := getContextWithCancel()
		defer cancel()

		pipResourceGroup := key
		pager := az.PublicIPAddressesClient.NewListPager(pipResourceGroup, nil)
		var pipList []armnetwork.PublicIPAddress
		for pager.More() {
			page, err := pager.NextPage(context.Background())
			if err != nil {
				klog.Fatalf("failed to advance page: %v", err)
			}
			for _, pip := range page.Value {
				pipList = append(pipList, *pip)
			}
		}

		pipMap := &sync.Map{}
		for _, pip := range pipList {
			pip := pip
			pipMap.Store(pointer.StringDeref(pip.Name, ""), &pip)
		}
		return pipMap, nil
	}

	if az.PublicIPCacheTTLInSeconds == 0 {
		az.PublicIPCacheTTLInSeconds = publicIPCacheTTLDefaultInSeconds
	}
	return NewTimedcache(time.Duration(az.PublicIPCacheTTLInSeconds)*time.Second, getter)
}

func (az *Cloud) newPLSCache() (*TimedCache, error) {
	// for PLS cache, key is LBFrontendIPConfiguration ID
	getter := func(key string) (interface{}, error) {
		_, cancel := getContextWithCancel()
		defer cancel()
		pager := az.PrivateLinkServiceClient.NewListPager(az.PrivateLinkServiceResourceGroup, nil)
		var plsList []armnetwork.PrivateLinkService
		var err error
		for pager.More() {
			page, err := pager.NextPage(context.Background())
			if err != nil {
				klog.Fatalf("failed to advance page: %v", err)
			}
			for _, pls := range page.Value {
				plsList = append(plsList, *pls)
			}
		}
		exists, rerr := checkResourceExistsFromError(&Error{
			RawError: err,
		})
		if rerr != nil {
			return nil, rerr.Error()
		}

		if exists {
			for i := range plsList {
				pls := plsList[i]
				if pls.Properties == nil {
					continue
				}
				fipConfigs := pls.Properties.LoadBalancerFrontendIPConfigurations
				if fipConfigs == nil {
					continue
				}
				for _, fipConfig := range fipConfigs {
					if strings.EqualFold(*fipConfig.ID, key) {
						return &pls, nil
					}
				}

			}
		}

		klog.V(2).Infof("No privateLinkService found for frontendIPConfig %q", key)
		plsNotExistID := PrivateLinkServiceNotExistID
		return &armnetwork.PrivateLinkService{ID: &plsNotExistID}, nil
	}

	if az.PlsCacheTTLInSeconds == 0 {
		az.PlsCacheTTLInSeconds = plsCacheTTLDefaultInSeconds
	}
	return NewTimedcache(time.Duration(az.PlsCacheTTLInSeconds)*time.Second, getter)
}

func (az *Cloud) getRouteTable(crt AzureCacheReadType) (routeTable armnetwork.RouteTable, exists bool, err error) {
	if len(az.RouteTableName) == 0 {
		return routeTable, false, fmt.Errorf("Route table name is not configured")
	}

	cachedRt, err := az.rtCache.GetWithDeepCopy(az.RouteTableName, crt)
	if err != nil {
		return routeTable, false, err
	}

	if cachedRt == nil {
		return routeTable, false, nil
	}

	return *(cachedRt.(*armnetwork.RouteTable)), true, nil
}
