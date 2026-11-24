package controller

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CacheOptions returns the cache configuration for the controller manager.
func CacheOptions() cache.Options {
	managedBy := labels.SelectorFromSet(labels.Set{labelManagedBy: labelManagedByValue})
	return cache.Options{
		ByObject: map[client.Object]cache.ByObject{
			&appsv1.Deployment{}:        {Label: managedBy},
			&corev1.Service{}:           {Label: managedBy},
			&corev1.ServiceAccount{}:    {Label: managedBy},
			&networking.NetworkPolicy{}: {Label: managedBy},
			&apiextensions.CustomResourceDefinition{}: {
				Field: fields.OneTermEqualSelector("metadata.name", envoyFilterCRDName),
			},
			&corev1.Secret{}: {
				Label: labels.SelectorFromSet(labels.Set{
					ManagedSecretLabel: ManagedSecretValue,
				}),
			},
		},
	}
}
