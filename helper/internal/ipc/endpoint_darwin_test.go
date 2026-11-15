package ipc

import "testing"

func testUserRuntimeDir(t *testing.T) {
	for _, variant := range []Variant{Production, Dev, Test} {
		if _, err := UserEndpoint(variant); err != nil {
			t.Errorf("UserEndpoint(%s) = %v", variant, err)
		}
	}
}
