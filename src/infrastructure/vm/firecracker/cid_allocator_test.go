package firecracker

import "testing"

func TestAllocateDeterministicCIDStable(t *testing.T) {
	const vmID = "2062696c-dec8-478d-883f-2e4e19c3aa4f"
	inUse := func(uint32) (bool, error) { return false, nil }

	cid1, err := AllocateDeterministicCID(vmID, 1024, 65535, inUse)
	if err != nil {
		t.Fatalf("first allocation failed: %v", err)
	}
	cid2, err := AllocateDeterministicCID(vmID, 1024, 65535, inUse)
	if err != nil {
		t.Fatalf("second allocation failed: %v", err)
	}

	if cid1 != cid2 {
		t.Fatalf("expected stable cid, got %d and %d", cid1, cid2)
	}
}

func TestAllocateDeterministicCIDCollisionProbe(t *testing.T) {
	const vmID = "35593766-c4cf-43ae-8038-6e6471303e13"

	first, err := AllocateDeterministicCID(vmID, 1024, 1028, func(uint32) (bool, error) {
		return false, nil
	})
	if err != nil {
		t.Fatalf("baseline allocation failed: %v", err)
	}

	cid, err := AllocateDeterministicCID(vmID, 1024, 1028, func(candidate uint32) (bool, error) {
		return candidate == first, nil
	})
	if err != nil {
		t.Fatalf("collision probe allocation failed: %v", err)
	}

	if cid == first {
		t.Fatalf("expected probe to skip used cid %d", first)
	}
}

func TestAllocateDeterministicCIDExhaustedRange(t *testing.T) {
	_, err := AllocateDeterministicCID("vm-1", 1024, 1026, func(uint32) (bool, error) {
		return true, nil
	})
	if err == nil {
		t.Fatal("expected no-available-cid error")
	}
}
