//go:build noonnxruntime

package model

import "errors"

func NewRuntime(libraryPath string) (Runtime, error) {
	if libraryPath != "" {
		return nil, errors.New("this remask-core binary was built with the noonnxruntime tag")
	}
	return UnavailableRuntime{}, nil
}
