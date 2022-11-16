/*
Copyright 2022 The Kubernetes Authors.

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

package azureutils

import (
	"context"

	crdInformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	azdiskinformers "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions"
)

type GenericInformer interface {
	Start(stopCh <-chan struct{})
	Informer() cache.SharedIndexInformer
}

type GenericAzInformerInfo struct {
	factory  azdiskinformers.SharedInformerFactory
	informer cache.SharedIndexInformer
}

func (i *GenericAzInformerInfo) Start(stopCh <-chan struct{}) {
	i.factory.Start(stopCh)
}

func (i *GenericAzInformerInfo) Informer() cache.SharedIndexInformer {
	return i.informer
}

func NewAzNodeInformer(factory azdiskinformers.SharedInformerFactory) GenericInformer {
	return &GenericAzInformerInfo{
		factory:  factory,
		informer: factory.Disk().V1beta2().AzDriverNodes().Informer(),
	}
}

func NewAzVolumeAttachmentInformer(factory azdiskinformers.SharedInformerFactory) GenericInformer {
	return &GenericAzInformerInfo{
		factory:  factory,
		informer: factory.Disk().V1beta2().AzVolumeAttachments().Informer(),
	}
}

func NewAzVolumeInformer(factory azdiskinformers.SharedInformerFactory) GenericInformer {
	return &GenericAzInformerInfo{
		factory:  factory,
		informer: factory.Disk().V1beta2().AzVolumes().Informer(),
	}
}

type GenericKubeInformer struct {
	factory  informers.SharedInformerFactory
	informer cache.SharedIndexInformer
}

func (i *GenericKubeInformer) Start(stopCh <-chan struct{}) {
	i.factory.Start(stopCh)
}

func (i *GenericKubeInformer) Informer() cache.SharedIndexInformer {
	return i.informer
}

func NewNodeInformer(factory informers.SharedInformerFactory) GenericInformer {
	return &GenericKubeInformer{
		factory:  factory,
		informer: factory.Core().V1().Nodes().Informer(),
	}
}

type GenericCrdInformer struct {
	factory  crdInformers.SharedInformerFactory
	informer cache.SharedIndexInformer
}

func NewCrdInformer(factory crdInformers.SharedInformerFactory) GenericInformer {
	return &GenericCrdInformer{
		factory:  factory,
		informer: factory.Apiextensions().V1().CustomResourceDefinitions().Informer(),
	}
}

func (i *GenericCrdInformer) Start(stopCh <-chan struct{}) {
	i.factory.Start(stopCh)
}

func (i *GenericCrdInformer) Informer() cache.SharedIndexInformer {
	return i.informer
}

func StartInformersAndWaitForCacheSync(ctx context.Context, factories ...GenericInformer) {
	informerSynced := make([]cache.InformerSynced, 0)
	for _, factory := range factories {
		factory.Start(ctx.Done())
		informerSynced = append(informerSynced, factory.Informer().HasSynced)
	}

	synced := cache.WaitForCacheSync(ctx.Done(), informerSynced...)
	if !synced {
		klog.Fatalf("Unable to sync caches")
	}
}
