package azureutils

import (
	"github.com/golang/mock/gomock"
)

func GetTestCloud(ctrl *gomock.Controller) (*Cloud) {
	az := &Cloud{
		Config: Config{
			AzureAuthConfig: AzureAuthConfig{
				TenantID:       "tenant",
				SubscriptionID: "subscription",
			},
			ResourceGroup:                            "rg",
			Location:                                 "westus",
			PrimaryAvailabilitySetName:               "as",
			PrimaryScaleSetName:                      "vmss",
			VMType:                                   VMTypeStandard,
		},
		VMSSVMCache: NewCache(),
	}

	return az
}
