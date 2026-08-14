//go:build noonnxruntime

package model

import "errors"

func NewRuntime(libraryPath string) (Runtime, error) {
	return NewRuntimeWithOptions(libraryPath, RuntimeOptions{})
}

func NewRuntimeWithOptions(libraryPath string, _ RuntimeOptions) (Runtime, error) {
	if libraryPath != "" {
		return nil, errors.New("this remask-core binary was built with the noonnxruntime tag")
	}
	return UnavailableRuntime{}, nil
}
