package main

import (
	"log"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// A container's memory limit is enforced by killing the process, and Go does not know
// about it. Told a soft limit below it, the runtime collects garbage harder as it nears
// it instead. A GOMEMLIMIT someone set wins, and the runtime has already applied it.

// memoryLimitShare is the part of a container's limit left to Go's heap, with the rest for
// what the limit counts and the heap does not: stacks, the binary, the kernel's buffers.
const memoryLimitShare = 0.7

// A cgroup's limit file holds "max" when there is none.
var cgroupLimitFiles = []string{
	"/sys/fs/cgroup/memory.max",                   // cgroup v2
	"/sys/fs/cgroup/memory/memory.limit_in_bytes", // cgroup v1
}

// noCgroupLimit is what cgroup v1 reports for no limit: near the largest number it holds.
const noCgroupLimit = int64(1) << 60

func applyMemoryLimit() {
	if limit := softMemoryLimit(os.Getenv("GOMEMLIMIT"), cgroupLimitFiles); limit > 0 {
		debug.SetMemoryLimit(limit)
		log.Printf("memory:       soft limit %d MiB, from the container's limit", limit>>20)
	}
}

// softMemoryLimit is the limit to set, 0 for none.
func softMemoryLimit(userLimit string, files []string) int64 {
	if strings.TrimSpace(userLimit) != "" {
		return 0
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		limit, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil || limit <= 0 || limit >= noCgroupLimit {
			continue
		}
		return int64(float64(limit) * memoryLimitShare)
	}
	return 0
}
