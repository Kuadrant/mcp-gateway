package controller

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CacheByObject limits the informer cache to objects the controller manages,
// so unrelated secrets and network policies in the cluster aren't cached.
// an object whose label may be missing or edited won't be in the cache, so
// read it with DirectAPIReader.
func CacheByObject() map[client.Object]cache.ByObject {
	return map[client.Object]cache.ByObject{
		&corev1.Secret{}: {
			Label: labels.SelectorFromSet(labels.Set{ManagedSecretLabel: ManagedSecretValue}),
		},
		&networkingv1.NetworkPolicy{}: {
			Label: labels.SelectorFromSet(labels.Set{labelManagedBy: labelManagedByValue}),
		},
	}
}
