package controller

import (
	"context"
	"log/slog"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSupportsHTTPRouteRuleNames(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	log := slog.Default()

	tests := []struct {
		name string
		crd  *apiextensionsv1.CustomResourceDefinition
		want bool
	}{
		{
			name: "v1.4.0 supports rule names",
			crd: &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "httproutes.gateway.networking.k8s.io",
					Annotations: map[string]string{
						"gateway.networking.k8s.io/bundle-version": "v1.4.0",
					},
				},
			},
			want: true,
		},
		{
			name: "v1.4.1 supports rule names",
			crd: &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "httproutes.gateway.networking.k8s.io",
					Annotations: map[string]string{
						"gateway.networking.k8s.io/bundle-version": "v1.4.1",
					},
				},
			},
			want: true,
		},
		{
			name: "v1.6.1 supports rule names",
			crd: &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "httproutes.gateway.networking.k8s.io",
					Annotations: map[string]string{
						"gateway.networking.k8s.io/bundle-version": "v1.6.1",
					},
				},
			},
			want: true,
		},
		{
			name: "v1.3.0 does not support rule names",
			crd: &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "httproutes.gateway.networking.k8s.io",
					Annotations: map[string]string{
						"gateway.networking.k8s.io/bundle-version": "v1.3.0",
					},
				},
			},
			want: false,
		},
		{
			name: "v1.2.1 does not support rule names",
			crd: &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "httproutes.gateway.networking.k8s.io",
					Annotations: map[string]string{
						"gateway.networking.k8s.io/bundle-version": "v1.2.1",
					},
				},
			},
			want: false,
		},
		{
			name: "missing annotation defaults to supported",
			crd: &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "httproutes.gateway.networking.k8s.io",
				},
			},
			want: true,
		},
		{
			name: "missing CRD defaults to supported",
			crd:  nil,
			want: true,
		},
		{
			name: "malformed version defaults to supported",
			crd: &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "httproutes.gateway.networking.k8s.io",
					Annotations: map[string]string{
						"gateway.networking.k8s.io/bundle-version": "not-a-version",
					},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.crd != nil {
				builder = builder.WithObjects(tt.crd)
			}
			reader := builder.Build().(client.Reader)

			got := supportsHTTPRouteRuleNames(context.Background(), reader, log)
			if got != tt.want {
				t.Errorf("supportsHTTPRouteRuleNames() = %v, want %v", got, tt.want)
			}
		})
	}
}
