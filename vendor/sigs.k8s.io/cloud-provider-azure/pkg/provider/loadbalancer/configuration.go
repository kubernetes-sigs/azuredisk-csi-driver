/*
Copyright 2023 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package loadbalancer

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"

	v1 "k8s.io/api/core/v1"

	"sigs.k8s.io/cloud-provider-azure/pkg/consts"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider/loadbalancer/iputil"
)

// IsInternal returns true if the given service is internal load balancer.
func IsInternal(svc *v1.Service) bool {
	value, found := svc.Annotations[consts.ServiceAnnotationLoadBalancerInternal]
	return found && strings.ToLower(value) == "true"
}

// AllowedServiceTags returns the allowed service tags configured by user through AKS custom annotation.
func AllowedServiceTags(svc *v1.Service) ([]string, error) {
	const (
		Sep = ","
		Key = consts.ServiceAnnotationAllowedServiceTags
	)

	value, found := svc.Annotations[Key]
	if !found {
		return nil, nil
	}

	return strings.Split(strings.TrimSpace(value), Sep), nil
}

// AllowedIPRanges returns the allowed IP ranges configured by user through AKS custom annotation.
func AllowedIPRanges(svc *v1.Service) ([]netip.Prefix, []string, error) {
	const (
		Sep = ","
		Key = consts.ServiceAnnotationAllowedIPRanges
	)
	var (
		errs          []error
		validRanges   []netip.Prefix
		invalidRanges []string
	)

	value, found := svc.Annotations[Key]
	if !found {
		return nil, nil, nil
	}

	for _, p := range strings.Split(strings.TrimSpace(value), Sep) {
		prefix, err := iputil.ParsePrefix(p)
		if err != nil {
			errs = append(errs, err)
			invalidRanges = append(invalidRanges, p)
		} else {
			validRanges = append(validRanges, prefix)
		}
	}
	if len(errs) > 0 {
		return validRanges, invalidRanges, NewErrAnnotationValue(Key, value, errors.Join(errs...))
	}
	return validRanges, invalidRanges, nil
}

// SourceRanges returns the allowed IP ranges configured by user through `spec.LoadBalancerSourceRanges` and standard annotation.
// If `spec.LoadBalancerSourceRanges` is not set, it will try to parse the annotation.
func SourceRanges(svc *v1.Service) ([]netip.Prefix, []string, error) {
	var (
		errs          []error
		validRanges   []netip.Prefix
		invalidRanges []string
	)
	// Read from spec
	if len(svc.Spec.LoadBalancerSourceRanges) > 0 {
		for _, p := range svc.Spec.LoadBalancerSourceRanges {
			prefix, err := iputil.ParsePrefix(p)
			if err != nil {
				errs = append(errs, err)
				invalidRanges = append(invalidRanges, p)
			} else {
				validRanges = append(validRanges, prefix)
			}
		}
		if len(errs) > 0 {
			return validRanges, invalidRanges, fmt.Errorf("invalid service.Spec.LoadBalancerSourceRanges [%v]: %w", svc.Spec.LoadBalancerSourceRanges, errors.Join(errs...))
		}
		return validRanges, invalidRanges, nil
	}

	// Read from annotation
	const (
		Sep = ","
		Key = v1.AnnotationLoadBalancerSourceRangesKey
	)
	value, found := svc.Annotations[Key]
	if !found {
		return nil, nil, nil
	}
	for _, p := range strings.Split(strings.TrimSpace(value), Sep) {
		prefix, err := iputil.ParsePrefix(p)
		if err != nil {
			errs = append(errs, err)
			invalidRanges = append(invalidRanges, p)
		} else {
			validRanges = append(validRanges, prefix)
		}
	}
	if len(errs) > 0 {
		return validRanges, invalidRanges, NewErrAnnotationValue(Key, value, errors.Join(errs...))
	}
	return validRanges, invalidRanges, nil
}

func AdditionalPublicIPs(svc *v1.Service) ([]netip.Addr, error) {
	const (
		Sep = ","
		Key = consts.ServiceAnnotationAdditionalPublicIPs
	)

	value, found := svc.Annotations[Key]
	if !found {
		return nil, nil
	}

	rv, err := iputil.ParseAddresses(strings.Split(strings.TrimSpace(value), Sep))
	if err != nil {
		return nil, NewErrAnnotationValue(Key, value, err)
	}

	return rv, nil
}

type ErrAnnotationValue struct {
	AnnotationKey   string
	AnnotationValue string
	Inner           error
}

func NewErrAnnotationValue(key, value string, inner error) *ErrAnnotationValue {
	return &ErrAnnotationValue{
		AnnotationKey:   key,
		AnnotationValue: value,
		Inner:           inner,
	}
}

func (err *ErrAnnotationValue) Error() string {
	return fmt.Sprintf("invalid service annotation %s:%s: %s", err.AnnotationKey, err.AnnotationValue, err.Inner)
}
