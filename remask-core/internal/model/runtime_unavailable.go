package model

import "context"

type UnavailableRuntime struct{}

func (UnavailableRuntime) Name() string    { return "unavailable" }
func (UnavailableRuntime) Available() bool { return false }
func (UnavailableRuntime) Load(context.Context, string, Manifest) (Session, error) {
	return nil, ErrRuntimeUnavailable
}
