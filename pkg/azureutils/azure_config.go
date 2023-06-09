package azureutils

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

type cloudConfigType string

const (
	cloudConfigTypeFile   cloudConfigType = "file"
	cloudConfigTypeSecret cloudConfigType = "secret"
	cloudConfigTypeMerge  cloudConfigType = "merge"
)

func (az *Cloud) GetConfigFromSecret() (*Config, error) {
	// Read config from file and no override, return nil.
	if az.Config.CloudConfigType == cloudConfigTypeFile {
		return nil, nil
	}

	secret, err := az.KubeClient.CoreV1().Secrets(az.SecretNamespace).Get(context.TODO(), az.SecretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get secret %s/%s: %w", az.SecretNamespace, az.SecretName, err)
	}

	cloudConfigData, ok := secret.Data[az.CloudConfigKey]
	if !ok {
		return nil, fmt.Errorf("cloud-config is not set in the secret (%s/%s)", az.SecretNamespace, az.SecretName)
	}

	config := Config{}
	if az.Config.CloudConfigType == "" || az.Config.CloudConfigType == cloudConfigTypeMerge {
		// Merge cloud config, set default value to existing config.
		config = az.Config
	}

	err = yaml.Unmarshal(cloudConfigData, &config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Azure cloud-config: %w", err)
	}

	return &config, nil
}
