package azureutils

import "github.com/golang/mock/gomock"

func GetTestCloud(ctrl *gomock.Controller) *Cloud {
	return &Cloud{}
}
