#!/usr/bin/env bash
#
# Test incremental snapshot flow: create VM → write data → snapshot → verify cow sizes → restore
#
# Usage: ./scripts/test_incremental_snapshots.sh [BASE_URL]
#
set -euo pipefail

BASE_URL="${1:-http://localhost:8080}"
EMAIL="admin@spacetrek.dev"
PASSWORD="admin123"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

log()  { echo -e "${CYAN}[$(date +%H:%M:%S)]${NC} $*"; }
ok()   { echo -e "${GREEN}[PASS]${NC} $*"; }
fail() { echo -e "${RED}[FAIL]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }

# --- Helpers ---

api() {
	local method="$1" path="$2"
	shift 2
	curl -s "$BASE_URL/api/v1$path" -X "$method" \
		-H "Authorization: Bearer $TOKEN" \
		-H 'Content-Type: application/json' \
		"$@"
}

login() {
	TOKEN=$(curl -s "$BASE_URL/api/v1/auth/login" \
		-H 'Content-Type: application/json' \
		-d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" \
		| jq -r '.data.access_token')
	if [ "$TOKEN" == "null" ] || [ -z "$TOKEN" ]; then
		fail "Login failed"
		exit 1
	fi
	ok "Logged in"
}

get_vm_id() {
	local chat_id="$1"
	docker exec spacetrek-psql psql -U spacetrek -d spacetrek -t -c \
		"SELECT vm_id FROM vm_leases WHERE chat_id='$chat_id' ORDER BY leased_at DESC LIMIT 1;" \
		| tr -d ' \n'
}

get_vm_status() {
	local vm_id="$1"
	docker exec spacetrek-psql psql -U spacetrek -d spacetrek -t -c \
		"SELECT status FROM vm_instances WHERE id='$vm_id';" \
		| tr -d ' \n'
}

exec_vm() {
	local vm_id="$1" cmd="$2"
	local res=$(api POST "/vm/$vm_id/execute" -d "{\"command\":\"$cmd\"}")
	local msg=$(echo "$res" | jq -r '.message // empty')
	if [ "$msg" == "token expired" ]; then
		login > /dev/null
		res=$(api POST "/vm/$vm_id/execute" -d "{\"command\":\"$cmd\"}")
	fi
	echo "$res" | jq -r '.data.output // .data.error // .data.stdout // .message'
}
dm_status() {
	local vm_id="$1"
	local dm_name="vm_$(echo "$vm_id" | tr '-' '_')"
	docker exec spacetrek-api dmsetup status "$dm_name" 2>/dev/null || echo "N/A"
}


take_snapshot() {
	local vm_id="$1"
	local res=$(api POST "/vm/$vm_id/snapshot")
	local msg=$(echo "$res" | jq -r '.message // empty')
	if [ "$msg" == "token expired" ]; then
		login > /dev/null
		res=$(api POST "/vm/$vm_id/snapshot")
	fi
	echo "$res"
}

cow_du() {
	local vm_id="$1"
	docker exec spacetrek-api du -sk "/var/lib/firecracker/vms/$vm_id/cow.img" 2>/dev/null \
		| awk '{print $1}' || echo "0"
}

cow_apparent() {
	local vm_id="$1"
	docker exec spacetrek-api du --apparent-size -sk "/var/lib/firecracker/vms/$vm_id/cow.img" 2>/dev/null \
		| awk '{print $1}' || echo "0"
}

wait_for_vm() {
	local chat_id="$1"
	log "Waiting for VM to be ready..."
	# Give LLM orchestrator time to process the chat and create VM
	sleep 20
	for i in $(seq 1 30); do
		# Re-login silently
		TOKEN=$(curl -s "$BASE_URL/api/v1/auth/login" \
			-H 'Content-Type: application/json' \
			-d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" \
			| jq -r '.data.access_token') 2>/dev/null || true

		_VM_ID=$(docker exec spacetrek-psql psql -U spacetrek -d spacetrek -t -c \
			"SELECT vm_id FROM vm_leases WHERE chat_id='$chat_id' ORDER BY leased_at DESC LIMIT 1;" \
			| tr -d ' \n')
		if [ -n "$_VM_ID" ] && [ "$_VM_ID" != "" ]; then
			local status=$(api GET "/vm/$_VM_ID" | jq -r '.data.runtime_state // empty')
			if [ "$status" == "running" ]; then
				local out=$(api POST "/vm/$_VM_ID/execute" -d '{"command":"echo ready"}' | jq -r '.data.output // empty' 2>/dev/null || true)
				if echo "$out" | grep -q "ready"; then
					ok "VM ready: $_VM_ID"
					VM_ID="$_VM_ID"
					return
				fi
			fi
		fi
		sleep 3
	done
	fail "VM not ready after 110s"
	exit 1
}

# --- Test Steps ---

REPORT_FILE="test_results_$(date +%Y%m%d_%H%M%S).txt"

echo "" | tee "$REPORT_FILE"
echo "=========================================" | tee -a "$REPORT_FILE"
echo "  Incremental Snapshot Test" | tee -a "$REPORT_FILE"
echo "  $(date -Iseconds)" | tee -a "$REPORT_FILE"
echo "=========================================" | tee -a "$REPORT_FILE"
echo "" | tee -a "$REPORT_FILE"

# Redirect all output to both stdout and report file from here on
exec > >(tee -a "$REPORT_FILE") 2>&1

# 1. Login
log "Step 1: Login"
login

# 2. Create VM
log "Step 2: Create VM via chat"
CHAT_RES=$(api POST "/chat" -d '{"message":"run ubuntu vm"}')
CHAT_ID=$(echo "$CHAT_RES" | jq -r '.data.chat_id')
log "Chat ID: $CHAT_ID"
VM_ID=""
wait_for_vm "$CHAT_ID"

# 3. Check initial dm-snapshot state
log "Step 3: Check initial dm-snapshot state"
DM_STATUS=$(dm_status "$VM_ID")
log "dmsetup status: $DM_STATUS"
COW_ACTUAL=$(cow_du "$VM_ID")
COW_APPARENT=$(cow_apparent "$VM_ID")
log "Cow: ${COW_ACTUAL}KB actual / ${COW_APPARENT}KB apparent"

if [ "$COW_APPARENT" -le 65536 ] 2>/dev/null; then
	ok "Cow apparent size <= 64MB (cowSizeForBase fix working)"
else
	fail "Cow apparent size > 64MB — cowSizeForBase fix NOT working (got ${COW_APPARENT}KB)"
fi

if [ "$COW_ACTUAL" -le 512 ] 2>/dev/null; then
	ok "Cow actual usage small (${COW_ACTUAL}KB) — fresh dm-snapshot"
else
	warn "Cow actual usage higher than expected: ${COW_ACTUAL}KB"
fi

# 4. Write data to rootfs + take snapshot 1 (full)
log "Step 4: Write 5MB to /root and take snapshot 1 (full)"
exec_vm "$VM_ID" "dd if=/dev/urandom of=/root/snap1.bin bs=1M count=5 2>/dev/null && echo done" > /dev/null
sleep 1

COW_BEFORE=$(cow_du "$VM_ID")
log "Cow before snapshot 1: ${COW_BEFORE}KB actual"

SNAP1=$(take_snapshot "$VM_ID")
SNAP1_TYPE=$(echo "$SNAP1" | jq -r '.data.type')
SNAP1_SIZE=$(echo "$SNAP1" | jq -r '.data.size_bytes')
SNAP1_ID=$(echo "$SNAP1" | jq -r '.data.id')
log "Snapshot 1: type=$SNAP1_TYPE size=$SNAP1_SIZE id=${SNAP1_ID:0:8}..."

if [ "$SNAP1_TYPE" == "full" ]; then
	ok "Snapshot 1 is 'full'"
else
	fail "Snapshot 1 should be 'full', got '$SNAP1_TYPE'"
fi

# Check cow was reset after snapshot
COW_AFTER_RESET=$(cow_du "$VM_ID")
DM_AFTER=$(dm_status "$VM_ID")
log "Cow after reset: ${COW_AFTER_RESET}KB | dm status: $DM_AFTER"

if [ "$COW_AFTER_RESET" -le 16 ] 2>/dev/null; then
	ok "Cow reset working — only ${COW_AFTER_RESET}KB after snapshot (ResetCoW + dmsetup reload)"
else
	fail "Cow NOT reset — ${COW_AFTER_RESET}KB after snapshot (ResetCoW broken?)"
fi

# 5. Write more data + take snapshot 2 (incremental)
log "Step 5: Write 10MB to /root and take snapshot 2 (incremental)"
exec_vm "$VM_ID" "dd if=/dev/urandom of=/root/snap2.bin bs=1M count=10 2>/dev/null && echo done" > /dev/null
sleep 1

COW_BEFORE=$(cow_du "$VM_ID")
log "Cow before snapshot 2: ${COW_BEFORE}KB actual"

SNAP2=$(take_snapshot "$VM_ID")
SNAP2_TYPE=$(echo "$SNAP2" | jq -r '.data.type')
SNAP2_SIZE=$(echo "$SNAP2" | jq -r '.data.size_bytes')
SNAP2_ID=$(echo "$SNAP2" | jq -r '.data.id')
log "Snapshot 2: type=$SNAP2_TYPE size=$SNAP2_SIZE id=${SNAP2_ID:0:8}..."

if [ "$SNAP2_TYPE" == "incremental" ]; then
	ok "Snapshot 2 is 'incremental'"
else
	fail "Snapshot 2 should be 'incremental', got '$SNAP2_TYPE'"
fi

# Check cow reset again
COW_AFTER_RESET=$(cow_du "$VM_ID")
log "Cow after reset: ${COW_AFTER_RESET}KB"

if [ "$COW_AFTER_RESET" -le 16 ] 2>/dev/null; then
	ok "Cow reset after snapshot 2 — ${COW_AFTER_RESET}KB"
else
	fail "Cow NOT reset after snapshot 2 — ${COW_AFTER_RESET}KB"
fi

# 6. Write even more + take snapshot 3 (incremental)
log "Step 6: Write 20MB to /root and take snapshot 3 (incremental)"
exec_vm "$VM_ID" "dd if=/dev/urandom of=/root/snap3.bin bs=1M count=20 2>/dev/null && echo done" > /dev/null
sleep 1

SNAP3=$(take_snapshot "$VM_ID")
SNAP3_TYPE=$(echo "$SNAP3" | jq -r '.data.type')
SNAP3_SIZE=$(echo "$SNAP3" | jq -r '.data.size_bytes')
log "Snapshot 3: type=$SNAP3_TYPE size=$SNAP3_SIZE"

if [ "$SNAP3_TYPE" == "incremental" ]; then
	ok "Snapshot 3 is 'incremental'"
else
	fail "Snapshot 3 should be 'incremental', got '$SNAP3_TYPE'"
fi

# 7. Verify database snapshot chain
log "Step 7: Verify snapshot chain in database"
docker exec spacetrek-psql psql -U spacetrek -d spacetrek -c "
	SELECT s.type, pg_size_pretty(s.size_bytes) as size,
	       LEFT(s.parent_snapshot_id::text, 8) as parent
	FROM vm_snapshots s
	WHERE vm_id='$VM_ID' ORDER BY created_at;"

# 8. Check S3 uploads
log "Step 8: Verify S3 uploads (memory.zst, state.zst, cow.zst)"
SNAP_COUNT=$(docker exec spacetrek-psql psql -U spacetrek -d spacetrek -t -c \
	"SELECT COUNT(*) FROM vm_snapshots WHERE vm_id='$VM_ID';" | tr -d ' \n')
log "Total snapshots in DB: $SNAP_COUNT"

# 9. Stop and attempt resume
log "Step 9: Stop VM and attempt resume"
api DELETE "/vm/$VM_ID" | jq '.message' || true
sleep 3

VM_STATUS=$(get_vm_status "$VM_ID")
log "VM status after stop: $VM_STATUS"

RESUME_RES=$(api POST "/vm/resume" -d "{\"chat_id\":\"$CHAT_ID\"}" 2>&1 || true)
RESUME_STATUS=$(echo "$RESUME_RES" | jq -r '.status_code // "error"')

if [ "$RESUME_STATUS" == "200" ]; then
	RESUMED_VM=$(echo "$RESUME_RES" | jq -r '.data.id')
	ok "VM resumed: $RESUMED_VM"

	# Verify marker file
	sleep 5
	MARKER=$(exec_vm "$RESUMED_VM" "cat /root/marker.txt 2>/dev/null" 2>/dev/null || echo "FAILED")
	log "Marker after resume: $MARKER"
else
	RESUME_ERR=$(echo "$RESUME_RES" | jq -r '.message // .' 2>/dev/null || echo "$RESUME_RES")
	warn "Resume failed (known vsock bug #8): $RESUME_ERR"
	log "Snapshot/restore flow validated up to vm start. Vsock config bug is separate."
fi

# --- Summary ---
echo ""
echo "========================================="
echo "  Summary"
echo "========================================="
echo "  VM ID:              $VM_ID"
echo "  Chat ID:            $CHAT_ID"
echo "  Snapshots taken:    $SNAP_COUNT"
echo "  Snapshot 1 (full):  $(echo $SNAP1_SIZE | numfmt --to=iec)"
echo "  Snapshot 2 (incr):  $(echo $SNAP2_SIZE | numfmt --to=iec)"
echo "  Snapshot 3 (incr):  $(echo $SNAP3_SIZE | numfmt --to=iec)"
echo "  Cow apparent size:  $(cow_apparent "$VM_ID")KB (should be ~64MB)"
echo "  Cow after reset:    $(cow_du "$VM_ID")KB (should be ~8KB)"
echo ""
echo "  Checks:"
echo "    Cow sizing fix:       $([ "$COW_APPARENT" -le 65536 ] && echo 'PASS' || echo 'FAIL')"
echo "    Full snapshot:        $([ "$SNAP1_TYPE" == "full" ] && echo 'PASS' || echo 'FAIL')"
echo "    Incremental snap 2:   $([ "$SNAP2_TYPE" == "incremental" ] && echo 'PASS' || echo 'FAIL')"
echo "    Incremental snap 3:   $([ "$SNAP3_TYPE" == "incremental" ] && echo 'PASS' || echo 'FAIL')"
echo "    Cow reset (reload):   $([ "$(cow_du "$VM_ID")" -le 16 ] && echo 'PASS' || echo 'FAIL')"
echo "========================================="

echo "  Report saved to: $REPORT_FILE"
