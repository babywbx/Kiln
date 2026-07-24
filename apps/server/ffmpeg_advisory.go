//go:build !lite

package main

import (
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/resources"
)

func shouldWarnFFmpegMemory(plan resources.Plan, available bool, cfg config.File, channels []config.Channel) bool {
	if !available || !memoryConstrainedProfile(plan.Profile) {
		return false
	}
	if cfg.Packager.Engine != config.EngineNative {
		return true
	}
	for _, channel := range channels {
		if cfg.EngineFor(channel) != config.EngineNative {
			return true
		}
	}
	return false
}

func memoryConstrainedProfile(profile resources.Profile) bool {
	switch profile {
	case resources.ProfileCompact, resources.ProfileBalanced, resources.ProfileStandard:
		return true
	default:
		return false
	}
}
