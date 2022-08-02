package azureutils

import (
	"fmt"
	"reflect"
	"testing"
)

func TestNewFakeHostUtil(t *testing.T) {
	fakeHostUtil := NewFakeHostUtil()
	fakeHostUtil.SetPathIsDeviceResult("/home/", true, nil)

	tests := []struct {
		path         string
		expectedResp bool
		expectedErr  error
	}{
		{
			"/home/",
			true,
			nil,
		},
		{
			"/hame/",
			false,
			fmt.Errorf("path %q does not exist", "/hame/"),
		},
		{
			"/mnt/",
			false,
			nil,
		},
	}

	for _, test := range tests {
		resultResp, resultErr := fakeHostUtil.PathIsDevice(test.path)
		if !reflect.DeepEqual(resultResp, test.expectedResp) || (!reflect.DeepEqual(resultErr, test.expectedErr)) {
			t.Errorf("path: %v, resultResp: %v, expectedResp: %v, resultErr: %v, expectedErr: %v", test.path, resultResp, test.expectedResp, resultErr, test.expectedErr)
		}
	}
}
