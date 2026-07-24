package main

import (
	"os"
	"runtime/debug"
	"strconv"

	"github.com/babywbx/kiln/modules/filecache"
	"github.com/babywbx/kiln/modules/resources"
)

func configureFileCache(plan resources.Plan) {
	filecache.SetEnabled(plan.DropFileCache)
}

func configureGC(plan resources.Plan) string {
	value := os.Getenv("GOGC")
	if value == "" && plan.GCPercent > 0 {
		debug.SetGCPercent(plan.GCPercent)
		return strconv.Itoa(plan.GCPercent)
	}
	return value
}
