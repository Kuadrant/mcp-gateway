package controller

import (
	"context"
	"log/slog"
	"strings"

	semver "github.com/Masterminds/semver/v3"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	httpRouteCRDName        = "httproutes.gateway.networking.k8s.io"
	bundleVersionAnnotation = "gateway.networking.k8s.io/bundle-version"
)

var minRuleNameVersion = semver.MustParse("1.4.0")

func supportsHTTPRouteRuleNames(ctx context.Context, reader client.Reader, log *slog.Logger) bool {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := reader.Get(ctx, client.ObjectKey{Name: httpRouteCRDName}, crd); err != nil {
		log.Info("could not read HTTPRoute CRD, assuming rule names supported", "error", err)
		return true
	}

	raw, ok := crd.Annotations[bundleVersionAnnotation]
	if !ok {
		log.Info("HTTPRoute CRD missing bundle-version annotation, assuming rule names supported")
		return true
	}

	raw = strings.TrimPrefix(raw, "v")
	v, err := semver.NewVersion(raw)
	if err != nil {
		log.Info("could not parse bundle-version, assuming rule names supported", "version", raw, "error", err)
		return true
	}

	supported := !v.LessThan(minRuleNameVersion)
	if supported {
		log.Info("Gateway API supports HTTPRoute rule names", "version", v.String())
	} else {
		log.Info("Gateway API < v1.4.0, HTTPRoute rule names will be omitted", "version", v.String())
	}
	return supported
}
