/*
Package main is that starting point for the mcp controller
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

This package contains the main of the mcp controller
*/
package main

import (
	"flag"
	"log/slog"
	"os"

	"github.com/go-logr/logr"

	goenv "github.com/caitlinelfring/go-env-default"
	istionetv1alpha3 "istio.io/client-go/pkg/apis/networking/v1alpha3"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/controller"
)

func init() {
	runtime.Must(v1alpha1.AddToScheme(scheme.Scheme))
	runtime.Must(gatewayv1.Install(scheme.Scheme))
	runtime.Must(gatewayv1beta1.Install(scheme.Scheme))
	runtime.Must(istionetv1alpha3.AddToScheme(scheme.Scheme))
}

func main() {
	var loglevel int
	var logFormat string
	flag.IntVar(&loglevel, "log-level", int(slog.LevelInfo), "log level: 0=info, 8=error, -4=debug")
	flag.StringVar(&logFormat, "log-format", "txt", "log format: txt or json")
	flag.Parse()

	loggerOpts := &slog.HandlerOptions{}
	switch loglevel {
	case 0:
		loggerOpts.Level = slog.LevelInfo
	case 8:
		loggerOpts.Level = slog.LevelError
	case -4:
		loggerOpts.Level = slog.LevelDebug
	default:
		loggerOpts.Level = slog.LevelDebug
	}

	var slogger *slog.Logger
	if logFormat == "json" {
		slogger = slog.New(slog.NewJSONHandler(os.Stdout, loggerOpts))
	} else {
		slogger = slog.New(slog.NewTextHandler(os.Stdout, loggerOpts))
	}

	ctrl.SetLogger(logr.FromSlogHandler(slogger.Handler()))
	slogger.Info("Controller starting (health: :8081, metrics: :8082)...")
	ctx := ctrl.SetupSignalHandler()
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme.Scheme,
		Metrics:                metricsserver.Options{BindAddress: ":8082"},
		LeaderElection:         false,
		HealthProbeBindAddress: ":8081",
		//TODO look at adding this type of filtering
		// Cache: cache.Options{
		// 	ByObject: map[client.Object]cache.ByObject{
		// 		&v1.Secret{}: {
		// 			Label: labels.SelectorFromSet(labels.Set{
		// 				"mcp.kagenti.com/credential": "true",
		// 			}),
		// 		},
		// 	},
		// },
	})
	if err != nil {
		panic("unable to start manager : " + err.Error())
	}

	configReaderWriter := config.SecretReaderWriter{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Logger: slogger,
	}

	mcpExtFinderValidator := &controller.MCPGatewayExtensionValidator{
		Client:          mgr.GetClient(),
		DirectAPIReader: mgr.GetAPIReader(),
	}

	if err = (&controller.MCPReconciler{
		Client:                mgr.GetClient(),
		Scheme:                mgr.GetScheme(),
		DirectAPIReader:       mgr.GetAPIReader(),
		ConfigReaderWriter:    &configReaderWriter,
		MCPExtFinderValidator: mcpExtFinderValidator,
	}).SetupWithManager(ctx, mgr); err != nil {
		panic("unable to start manager : " + err.Error())
	}

	brokerRouterImage := goenv.GetDefault("RELATED_IMAGE_ROUTER_BROKER", controller.DefaultBrokerRouterImage)
	brokerPollInterval := os.Getenv("BROKER_POLL_INTERVAL")

	if err = (&controller.MCPGatewayExtensionReconciler{
		Client:                mgr.GetClient(),
		Scheme:                mgr.GetScheme(),
		DirectAPIReader:       mgr.GetAPIReader(),
		ConfigWriterDeleter:   &configReaderWriter,
		MCPExtFinderValidator: mcpExtFinderValidator,
		BrokerRouterImage:     brokerRouterImage,
		BrokerPollInterval:    brokerPollInterval,
	}).SetupWithManager(ctx, mgr); err != nil {
		panic("unable to start manager : " + err.Error())
	}

	if err = (&controller.MCPVirtualServerReconciler{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		DirectAPIReader:    mgr.GetAPIReader(),
		ConfigReaderWriter: &configReaderWriter,
	}).SetupWithManager(ctx, mgr); err != nil {
		panic("unable to start manager : " + err.Error())
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		panic("unable to start manager : " + err.Error())
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		panic("unable to start manager : " + err.Error())
	}

	if err := mgr.Start(ctx); err != nil {
		panic("unable to start manager : " + err.Error())
	}
}
