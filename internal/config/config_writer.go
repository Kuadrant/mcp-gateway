// Package config provides configuration management for MCP Gateway.
//
// This package handles reading and writing the broker configuration that is shared
// between multiple controllers. The configuration is stored in a Kubernetes Secret
// and contains both MCP server registrations and virtual server definitions.
//
// # Concurrent Access
//
// Multiple controllers (MCPServerRegistration, MCPVirtualServer) may update the
// configuration simultaneously. To handle this safely, the SecretReaderWriter uses
// a read-modify-write pattern with automatic retry on conflict:
//
//  1. Read the existing Secret (or create if missing)
//  2. Parse the existing BrokerConfig from the Secret's data
//  3. Update only the relevant section (servers OR virtualServers)
//  4. Write the updated config back to the Secret
//  5. If a conflict occurs (another controller modified it), retry from step 1
//
// This ensures that each controller only modifies its own section while preserving
// changes made by other controllers.
//
// # Secret Data vs StringData
//
// When reading a Kubernetes Secret, the actual content is in the Data field (as []byte).
// The StringData field is write-only and always empty when reading. This package handles
// this by copying Data to StringData before modifications.
package config

import (
	"context"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// SecretReaderWriter provides methods for reading and writing MCP Gateway configuration
// to a Kubernetes Secret. It supports concurrent access from multiple controllers by
// using optimistic locking with automatic retry on conflicts.
type SecretReaderWriter struct {
	Client client.Client
	Scheme *runtime.Scheme
	Logger *slog.Logger
}

// DefaultNamespaceName is the default location for the MCP Gateway config secret.
var DefaultNamespaceName = types.NamespacedName{Namespace: "mcp-system", Name: "mcp-gateway-config"}

// NamespaceName returns the NamespacedName for the config secret in the given namespace.
// The secret name is always "mcp-gateway-config".
func NamespaceName(ns string) types.NamespacedName {
	return types.NamespacedName{Namespace: ns, Name: "mcp-gateway-config"}
}

const (
	// configFileName is the key in the Secret's data map containing the YAML config.
	configFileName = "config.yaml"
	// emptyConfigFile is the initial content for a newly created config secret.
	emptyConfigFile = "servers: []\nvirtualServers: []\n"
)

// WriteVirtualServerConfig updates the virtualServers section of the config secret.
// It uses a read-modify-write pattern to preserve the servers section while updating
// virtualServers. Automatically retries on conflict errors caused by concurrent updates.
func (srw *SecretReaderWriter) WriteVirtualServerConfig(ctx context.Context, virtualServers []VirtualServerConfig, namespaceName types.NamespacedName) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		existingConfig, backingSecret, err := srw.readOrCreateConfigSecret(ctx, namespaceName)
		if err != nil {
			return fmt.Errorf("mcpvirtualserver failed to read config secret: %w", err)
		}

		existingConfig.VirtualServers = virtualServers
		updated, err := yaml.Marshal(existingConfig)
		if err != nil {
			return fmt.Errorf("mcpvirtualserver failed to marshal config: %w", err)
		}

		backingSecret.StringData[configFileName] = string(updated)
		return srw.Client.Update(ctx, backingSecret)
	})
}

// readOrCreateConfigSecret reads the config secret or creates it if it doesn't exist.
// It returns the parsed BrokerConfig and the Secret object (for subsequent updates).
//
// This method handles a Kubernetes Secret quirk: when reading a Secret, the actual
// content is in Data ([]byte), not StringData (which is write-only). We copy Data
// to StringData so the caller can modify StringData and call Update().
//
// If the secret doesn't exist, an empty one is created. If creation fails with
// AlreadyExists (race condition), the existing secret is fetched instead.
func (srw *SecretReaderWriter) readOrCreateConfigSecret(ctx context.Context, namespaceName types.NamespacedName) (*BrokerConfig, *corev1.Secret, error) {
	srw.Logger.Info("SecretReaderWriter readOrCreateConfigSecret")
	configSecret := &corev1.Secret{}
	err := srw.Client.Get(ctx, namespaceName, configSecret)
	if err != nil {
		if !errors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("failed to read config secret: %w", err)
		}
		// create empty secret
		configSecret = &corev1.Secret{
			ObjectMeta: v1.ObjectMeta{
				Name:      namespaceName.Name,
				Namespace: namespaceName.Namespace,
				Labels: map[string]string{
					"app":                        "mcp-gateway",
					"mcp.kuadrant.io/aggregated": "true",
				},
			},
			StringData: map[string]string{
				configFileName: emptyConfigFile,
			},
		}
		if err := srw.Client.Create(ctx, configSecret); err != nil {
			if !errors.IsAlreadyExists(err) {
				return nil, nil, fmt.Errorf("failed to create config secret: %w", err)
			}
			// re-fetch if already exists
			if err := srw.Client.Get(ctx, namespaceName, configSecret); err != nil {
				return nil, nil, fmt.Errorf("failed to get config secret after create: %w", err)
			}
		}
	}

	if configSecret.StringData == nil {
		configSecret.StringData = map[string]string{}
	}
	// copy Data to StringData for update
	if configSecret.Data != nil {
		if _, ok := configSecret.StringData[configFileName]; !ok {
			if data, ok := configSecret.Data[configFileName]; ok {
				configSecret.StringData[configFileName] = string(data)
			}
		}
	}

	existingConfig := &BrokerConfig{}
	if configYAML := configSecret.StringData[configFileName]; configYAML != "" {
		if err := yaml.Unmarshal([]byte(configYAML), existingConfig); err != nil {
			return nil, nil, fmt.Errorf("failed to unmarshal broker config: %w", err)
		}
	}

	return existingConfig, configSecret, nil
}

// UpsertMCPServer updates or inserts a single MCPServer in the config secret.
// If a server with the same Name already exists, it is replaced. Otherwise, the
// server is appended to the list. This uses a read-modify-write pattern with
// automatic retry on conflict errors.
func (srw *SecretReaderWriter) UpsertMCPServer(ctx context.Context, server MCPServer, namespaceName types.NamespacedName) error {
	srw.Logger.Info("SecretReaderWriter UpsertMCPServer", "secret", namespaceName, "name", server.Name)
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		existingConfig, backingSecret, err := srw.readOrCreateConfigSecret(ctx, namespaceName)
		if err != nil {
			return fmt.Errorf("upsert mcpserver failed to read config secret: %w", err)
		}

		// find and replace existing server, or append if not found
		found := false
		for i, existing := range existingConfig.Servers {

			if existing.Name == server.Name {
				existingConfig.Servers[i] = server
				found = true
				break
			}
		}
		if !found {
			existingConfig.Servers = append(existingConfig.Servers, server)
		}

		updated, err := yaml.Marshal(existingConfig)
		if err != nil {
			return fmt.Errorf("upsert mcpserver failed to marshal config: %w", err)
		}
		srw.Logger.Info("SecretReaderWriter total servers now", "total", len(existingConfig.Servers))
		backingSecret.StringData[configFileName] = string(updated)
		return srw.Client.Update(ctx, backingSecret)
	})
}

// RemoveMCPServer removes a single MCPServer by name from all config secrets cluster-wide.
// It finds all secrets with the "mcp.kuadrant.io/aggregated": "true" label and removes
// the server from each. If the server doesn't exist in a secret, that secret is skipped.
// This uses a read-modify-write pattern with automatic retry on conflict errors.
func (srw *SecretReaderWriter) RemoveMCPServer(ctx context.Context, serverName string) error {
	// list all aggregated config
	srw.Logger.Info("SecretReaderWriter RemoveMCPServer")
	secretList := &corev1.SecretList{}
	if err := srw.Client.List(ctx, secretList, client.MatchingLabels{
		"mcp.kuadrant.io/aggregated": "true",
	}); err != nil {
		return fmt.Errorf("remove mcpserver failed to list config secrets: %w", err)
	}

	var lastErr error
	for _, secret := range secretList.Items {
		namespaceName := types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			existingConfig, backingSecret, err := srw.readOrCreateConfigSecret(ctx, namespaceName)
			if err != nil {
				return fmt.Errorf("remove mcpserver failed to read config secret: %w", err)
			}

			// check if server exists in this config
			found := false
			filtered := make([]MCPServer, 0, len(existingConfig.Servers))
			for _, existing := range existingConfig.Servers {
				if existing.Name == serverName {
					found = true
				} else {
					filtered = append(filtered, existing)
				}
			}

			// skip update if server wasn't in this config
			if !found {
				return nil
			}

			existingConfig.Servers = filtered
			updated, err := yaml.Marshal(existingConfig)
			if err != nil {
				return fmt.Errorf("remove mcpserver failed to marshal config: %w", err)
			}

			backingSecret.StringData[configFileName] = string(updated)
			return srw.Client.Update(ctx, backingSecret)
		})
		if err != nil {
			lastErr = err
			srw.Logger.Error("failed to remove server from config secret",
				"error", err, "serverName", serverName, "namespace", secret.Namespace)
		}
	}

	return lastErr
}

// DeleteConfig deletes the entire config secret. If the secret doesn't exist,
// this is a no-op and returns nil.
func (srw *SecretReaderWriter) DeleteConfig(ctx context.Context, namespaceName types.NamespacedName) error {
	srw.Logger.Debug("deleting config", "namespacename", namespaceName)
	configSecret := &corev1.Secret{}
	err := srw.Client.Get(ctx, namespaceName, configSecret)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get config secret for deletion: %w", err)
	}
	if err := srw.Client.Delete(ctx, configSecret); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to delete config secret: %w", err)
	}
	return nil
}

// EnsureConfigExists creates the config secret if it doesn't exist.
// If the secret already exists, this is a no-op.
func (srw *SecretReaderWriter) EnsureConfigExists(ctx context.Context, namespaceName types.NamespacedName) error {
	_, _, err := srw.readOrCreateConfigSecret(ctx, namespaceName)
	return err
}

// WriteEmptyConfig overwrites the config secret with an empty configuration.
// This clears all servers and virtual servers from the config.
// Uses a read-modify-write pattern with automatic retry on conflict errors.
func (srw *SecretReaderWriter) WriteEmptyConfig(ctx context.Context, namespaceName types.NamespacedName) error {
	srw.Logger.Info("SecretReaderWriter WriteEmptyConfig", "secret", namespaceName)
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		_, backingSecret, err := srw.readOrCreateConfigSecret(ctx, namespaceName)
		if err != nil {
			return fmt.Errorf("write empty config failed to read config secret: %w", err)
		}

		backingSecret.StringData[configFileName] = emptyConfigFile
		return srw.Client.Update(ctx, backingSecret)
	})
}
