package firecracker

import (
	"fmt"
	"hash/fnv"
)

// CIDInUseFn returns whether a candidate CID is already reserved/in use.
type CIDInUseFn func(cid uint32) (bool, error)

// AllocateDeterministicCID allocates a guest CID in [minCID, maxCID] using a
// deterministic VM-ID hash and linear probing for collisions.
func AllocateDeterministicCID(vmID string, minCID, maxCID uint32, inUse CIDInUseFn) (uint32, error) {
	if vmID == "" {
		return 0, fmt.Errorf("vm id is required")
	}
	if minCID < 3 {
		return 0, fmt.Errorf("min cid must be >= 3")
	}
	if maxCID < minCID {
		return 0, fmt.Errorf("max cid must be >= min cid")
	}
	if inUse == nil {
		return 0, fmt.Errorf("inUse callback is required")
	}

	rangeSize := maxCID - minCID + 1
	startCID := minCID + (hashVMID(vmID) % rangeSize)

	for i := uint32(0); i < rangeSize; i++ {
		candidate := minCID + ((startCID - minCID + i) % rangeSize)
		used, err := inUse(candidate)
		if err != nil {
			return 0, fmt.Errorf("check cid %d: %w", candidate, err)
		}
		if !used {
			return candidate, nil
		}
	}

	return 0, fmt.Errorf("no available cid in range [%d, %d]", minCID, maxCID)
}

func hashVMID(vmID string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(vmID))
	return h.Sum32()
}
