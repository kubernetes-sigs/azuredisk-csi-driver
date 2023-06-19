package azureutils

import (
	"strings"
	"sync"
	"time"

	armnetwork "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v2"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/cloud-provider-azure/pkg/consts"
)

var (
	// routeUpdateInterval defines the route reconciling interval.
	routeUpdateInterval = 30 * time.Second
)

// routeOperation defines the allowed operations for route updating.
type routeOperation string

// copied to minimize the number of cross reference
// and exceptions in publishing and allowed imports.
const (
	// Route operations.
	routeOperationAdd             routeOperation = "add"
	routeOperationDelete          routeOperation = "delete"
	routeTableOperationUpdateTags routeOperation = "updateRouteTableTags"
)

// delayedRouteOperation defines a delayed route operation which is used in delayedRouteUpdater.
type delayedRouteOperation struct {
	route          armnetwork.Route
	routeTableTags map[string]*string
	operation      routeOperation
	result         chan error
}

// delayedRouteUpdater defines a delayed route updater, which batches all the
// route updating operations within "interval" period.
// Example usage:
// op, err := updater.addRouteOperation(routeOperationAdd, route)
// err = op.wait()
type delayedRouteUpdater struct {
	az       *Cloud
	interval time.Duration

	lock           sync.Mutex
	routesToUpdate []*delayedRouteOperation
}

// newDelayedRouteUpdater creates a new delayedRouteUpdater.
func newDelayedRouteUpdater(az *Cloud, interval time.Duration) *delayedRouteUpdater {
	return &delayedRouteUpdater{
		az:             az,
		interval:       interval,
		routesToUpdate: make([]*delayedRouteOperation, 0),
	}
}

// run starts the updater reconciling loop.
func (d *delayedRouteUpdater) run() {
	err := wait.PollImmediateInfinite(d.interval, func() (bool, error) {
		d.updateRoutes()
		return false, nil
	})
	if err != nil { // this should never happen, if it does, panic
		panic(err)
	}
}

// updateRoutes invokes route table client to update all routes.
func (d *delayedRouteUpdater) updateRoutes() {
	d.lock.Lock()
	defer d.lock.Unlock()

	ctx, cancel := getContextWithCancel()
	defer cancel()

	// No need to do any updating.
	if len(d.routesToUpdate) == 0 {
		klog.V(4).Info("updateRoutes: nothing to update, returning")
		return
	}

	var err error
	defer func() {
		// Notify all the goroutines.
		for _, rt := range d.routesToUpdate {
			rt.result <- err
		}
		// Clear all the jobs.
		d.routesToUpdate = make([]*delayedRouteOperation, 0)
	}()

	var (
		routeTable       armnetwork.RouteTable
		existsRouteTable bool
	)
	routeTable, existsRouteTable, err = d.az.getRouteTable(CacheReadTypeDefault)
	if err != nil {
		klog.Errorf("getRouteTable() failed with error: %v", err)
		return
	}

	// create route table if it doesn't exists yet.
	if !existsRouteTable {
		err = d.az.createRouteTable()
		if err != nil {
			klog.Errorf("createRouteTable() failed with error: %v", err)
			return
		}

		routeTable, _, err = d.az.getRouteTable(CacheReadTypeDefault)
		if err != nil {
			klog.Errorf("getRouteTable() failed with error: %v", err)
			return
		}
	}

	// reconcile routes.
	dirty, onlyUpdateTags := false, true
	routes := []armnetwork.Route{}
	var rts []*armnetwork.Route
	if routeTable.Properties != nil && routeTable.Properties.Routes != nil {
		rts = routeTable.Properties.Routes
	}

	for _, route := range rts {
		routes = append(routes, *route)
	}

	routes, dirty = d.cleanupOutdatedRoutes(routes)
	if dirty {
		onlyUpdateTags = false
	}

	for _, rt := range d.routesToUpdate {
		if rt.operation == routeTableOperationUpdateTags {
			routeTable.Tags = rt.routeTableTags
			dirty = true
			continue
		}

		routeMatch := false
		onlyUpdateTags = false
		for i, existingRoute := range routes {
			if strings.EqualFold(pointer.StringDeref(existingRoute.Name, ""), pointer.StringDeref(rt.route.Name, "")) {
				// delete the name-matched routes here (missing routes would be added later if the operation is add).
				routes = append(routes[:i], routes[i+1:]...)
				if existingRoute.Properties != nil &&
					rt.route.Properties != nil &&
					strings.EqualFold(pointer.StringDeref(existingRoute.Properties.AddressPrefix, ""), pointer.StringDeref(rt.route.Properties.AddressPrefix, "")) &&
					strings.EqualFold(pointer.StringDeref(existingRoute.Properties.NextHopIPAddress, ""), pointer.StringDeref(rt.route.Properties.NextHopIPAddress, "")) {
					routeMatch = true
				}
				if rt.operation == routeOperationDelete {
					dirty = true
				}
				break
			}
		}
		if rt.operation == routeOperationDelete && !dirty {
			klog.Warningf("updateRoutes: route to be deleted %s does not match any of the existing route", pointer.StringDeref(rt.route.Name, ""))
		}

		// Add missing routes if the operation is add.
		if rt.operation == routeOperationAdd {
			routes = append(routes, rt.route)
			if !routeMatch {
				dirty = true
			}
			continue
		}
	}

	rts = []*armnetwork.Route{}
	for _, route := range routes {
		rts = append(rts, &route)
	}

	if dirty {
		if !onlyUpdateTags {
			klog.V(2).Infof("updateRoutes: updating routes")
			routeTable.Properties.Routes = rts
		}
		poller, err := d.az.RouteTablesClient.BeginCreateOrUpdate(ctx, d.az.RouteTableResourceGroup, d.az.RouteTableName, routeTable, nil)
		if err != nil {
			klog.Fatalf("failed to create request: %v", err)
		}
		_, err = poller.PollUntilDone(ctx, nil)
		if err != nil {
			klog.Errorf("CreateOrUpdateRouteTable() failed with error: %v", err)
			return
		}

		// wait a while for route updates to take effect.
		time.Sleep(time.Duration(d.az.Config.RouteUpdateWaitingInSeconds) * time.Second)
	}
}

func (az *Cloud) createRouteTable() error {
	ctx, cancel := getContextWithCancel()
	defer cancel()

	routeTable := armnetwork.RouteTable{
		Name:       pointer.String(az.RouteTableName),
		Location:   pointer.String(az.Location),
		Properties: &armnetwork.RouteTablePropertiesFormat{},
	}

	klog.V(3).Infof("createRouteTableIfNotExists: creating routetable. routeTableName=%q", az.RouteTableName)
	poller, err := az.RouteTablesClient.BeginCreateOrUpdate(ctx, az.RouteTableResourceGroup, az.RouteTableName, routeTable, nil)
	if err != nil {
		klog.Fatalf("failed to create request: %v", err)
	}
	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return err
	}

	// Invalidate the cache right after updating
	_ = az.rtCache.Delete(az.RouteTableName)
	return nil
}

// cleanupOutdatedRoutes deletes all non-dualstack routes when dualstack is enabled,
// and deletes all dualstack routes when dualstack is not enabled.
func (d *delayedRouteUpdater) cleanupOutdatedRoutes(existingRoutes []armnetwork.Route) (routes []armnetwork.Route, changed bool) {
	for i := len(existingRoutes) - 1; i >= 0; i-- {
		existingRouteName := pointer.StringDeref(existingRoutes[i].Name, "")
		split := strings.Split(existingRouteName, consts.RouteNameSeparator)

		klog.V(4).Infof("cleanupOutdatedRoutes: checking route %s", existingRouteName)

		// filter out unmanaged routes
		deleteRoute := false
		if d.az.nodeNames.Has(split[0]) {
			if d.az.ipv6DualStackEnabled && len(split) == 1 {
				klog.V(2).Infof("cleanupOutdatedRoutes: deleting outdated non-dualstack route %s", existingRouteName)
				deleteRoute = true
			} else if !d.az.ipv6DualStackEnabled && len(split) == 2 {
				klog.V(2).Infof("cleanupOutdatedRoutes: deleting outdated dualstack route %s", existingRouteName)
				deleteRoute = true
			}

			if deleteRoute {
				existingRoutes = append(existingRoutes[:i], existingRoutes[i+1:]...)
				changed = true
			}
		}
	}

	return existingRoutes, changed
}
