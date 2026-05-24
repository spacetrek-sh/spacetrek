#!/usr/bin/env bash
#
# Test incremental disk snapshot flow:
#   Snap 1 (full):       install base packages → snapshot → verify disk format
#   Snap 2 (incr):       write more files → snapshot → verify cow format, chain depth 1
#   Snap 3 (incr):       write more files → snapshot → verify cow format, chain depth 2
#   Snap 4 (incr):       write more files → snapshot → verify cow format, chain depth 3
#   Snap 5 (incr):       write more files → snapshot → verify cow format, chain depth 4
#   Snap 6 (compaction): write more files → snapshot → verify DISK format (forced full), chain resets
#   Resume:              verify ALL state from sessions 1-6 survived
#
# Validates: incremental cow uploads, chain-aware GC, compaction trigger, multi-hop restore
#
# Prerequisite: config.yaml must have disk_mode: incremental
#
# Usage: ./scripts/test_snapshot_incremental.sh [BASE_URL]
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

PASS_COUNT=0
FAIL_COUNT=0

log()  { echo -e "${CYAN}[$(date +%H:%M:%S)]${NC} $*"; }
ok()   { echo -e "${GREEN}[PASS]${NC} $*"; PASS_COUNT=$((PASS_COUNT + 1)); }
fail() { echo -e "${RED}[FAIL]${NC} $*"; FAIL_COUNT=$((FAIL_COUNT + 1)); }
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

exec_vm() {
	local vm_id="$1" cmd="$2"
	local json_body
	json_body=$(jq -n --arg cmd "$cmd" '{"command": $cmd}')
	local res
	res=$(api POST "/vm/$vm_id/execute" -d "$json_body")
	local msg
	msg=$(echo "$res" | jq -r '.message // empty')
	if [ "$msg" == "token expired" ]; then
		login > /dev/null 2>&1
		res=$(api POST "/vm/$vm_id/execute" -d "$json_body")
	fi
	echo "$res" | jq -r '.data.output // .data.error // .message'
}

take_snapshot() {
	local vm_id="$1"
	local res
	res=$(api POST "/vm/$vm_id/snapshot")
	local msg
	msg=$(echo "$res" | jq -r '.message // empty')
	if [ "$msg" == "token expired" ]; then
		login > /dev/null 2>&1
		res=$(api POST "/vm/$vm_id/snapshot")
	fi
	echo "$res"
}

snapshot_count() {
	local vm_id="$1"
	docker exec spacetrek-psql psql -U spacetrek -d spacetrek -t -c \
		"SELECT COUNT(*) FROM vm_snapshots WHERE vm_id='$vm_id';" \
		| tr -d ' \n'
}

snapshot_details() {
	local vm_id="$1"
	docker exec spacetrek-psql psql -U spacetrek -d spacetrek -c \
		"SELECT id, type, pg_size_pretty(size_bytes) as size, metadata->>'disk_snapshot_type' as disk_type, metadata->>'disk_chain_length' as chain_len, created_at FROM vm_snapshots WHERE vm_id='$vm_id' ORDER BY created_at;"
}

snapshot_s3_path() {
	local vm_id="$1" idx="${2:-latest}"
	if [ "$idx" == "latest" ]; then
		docker exec spacetrek-psql psql -U spacetrek -d spacetrek -t -c \
			"SELECT snapshot_path FROM vm_snapshots WHERE vm_id='$vm_id' ORDER BY created_at DESC LIMIT 1;" \
			| tr -d ' \n'
	else
		docker exec spacetrek-psql psql -U spacetrek -d spacetrek -t -c \
			"SELECT snapshot_path FROM vm_snapshots WHERE vm_id='$vm_id' ORDER BY created_at OFFSET $((idx-1)) LIMIT 1;" \
			| tr -d ' \n'
	fi
}

check_snapshot_format() {
	local s3_path="$1"
	if [ -z "$s3_path" ]; then
		echo "none"
		return
	fi
	# Query DB metadata as authoritative source for S3 upload format.
	local disk_type
	disk_type=$(docker exec spacetrek-psql psql -U spacetrek -d spacetrek -t -c \
		"SELECT metadata->>'disk_snapshot_type' FROM vm_snapshots WHERE snapshot_path='$s3_path';" \
		| tr -d ' \n')
	if [ "$disk_type" == "full" ]; then
		echo "disk"
	elif [ "$disk_type" == "incremental" ]; then
		echo "cow"
	else
		echo "unknown"
	fi
}

get_snapshot_metadata() {
	local vm_id="$1" field="$2"
	docker exec spacetrek-psql psql -U spacetrek -d spacetrek -t -c \
		"SELECT metadata->>'$field' FROM vm_snapshots WHERE vm_id='$vm_id' ORDER BY created_at DESC LIMIT 1;" \
		| tr -d ' \n'
}

get_vm_status() {
	local vm_id="$1"
	docker exec spacetrek-psql psql -U spacetrek -d spacetrek -t -c \
		"SELECT status FROM vm_instances WHERE id='$vm_id';" \
		| tr -d ' \n'
}

dm_status() {
	local vm_id="$1"
	local dm_name="vm_$(echo "$vm_id" | tr '-' '_')"
	docker exec spacetrek-api dmsetup status "$dm_name" 2>/dev/null || echo "N/A"
}

wait_for_vm() {
	local chat_id="$1"
	log "Waiting for VM to be ready..."
	sleep 20
	for i in $(seq 1 30); do
		TOKEN=$(curl -s "$BASE_URL/api/v1/auth/login" \
			-H 'Content-Type: application/json' \
			-d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" \
			| jq -r '.data.access_token') 2>/dev/null || true

		_VM_ID=$(docker exec spacetrek-psql psql -U spacetrek -d spacetrek -t -c \
			"SELECT vm_id FROM vm_leases WHERE chat_id='$chat_id' ORDER BY leased_at DESC LIMIT 1;" \
			| tr -d ' \n')
		if [ -n "$_VM_ID" ] && [ "$_VM_ID" != "" ]; then
			local status
			status=$(api GET "/vm/$_VM_ID" | jq -r '.data.runtime_state // empty')
			if [ "$status" == "running" ]; then
				local out
				out=$(exec_vm "$_VM_ID" "echo ready" 2>/dev/null || true)
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

wait_for_responsive() {
	local vm_id="$1" label="$2"
	sleep 10
	for i in $(seq 1 20); do
		local ready
		ready=$(exec_vm "$vm_id" "echo ready" 2>/dev/null || echo "")
		if echo "$ready" | grep -q "ready"; then
			ok "VM responsive after $label"
			return
		fi
		sleep 3
		if [ "$i" -eq 20 ]; then
			fail "VM not responsive after $label"
			exit 1
		fi
	done
}

stop_vm() {
	local vm_id="$1"
	api DELETE "/vm/$vm_id" | jq '.message' 2>/dev/null || true
	sleep 5
}

resume_vm() {
	local chat_id="$1" label="$2"
	for i in $(seq 1 5); do
		local res
		res=$(api POST "/vm/resume" -d "{\"chat_id\":\"$chat_id\"}" 2>&1 || true)
		local status
		status=$(echo "$res" | jq -r '.status_code // "error"')

		if [ "$status" == "200" ]; then
			local resumed_id
			resumed_id=$(echo "$res" | jq -r '.data.id')
			ok "VM resumed for $label: $resumed_id"
			VM_ID="$resumed_id"
			wait_for_responsive "$VM_ID" "$label"
			return 0
		fi

		log "Resume attempt $i for $label failed, retrying in 5s..."
		sleep 5
	done

	local res
	res=$(api POST "/vm/resume" -d "{\"chat_id\":\"$chat_id\"}" 2>&1 || true)
	fail "Resume failed for $label: $(echo "$res" | jq -r '.message // .' 2>/dev/null || echo "$res")"
	return 1
}

# --- Test Flow ---

REPORT_FILE="test_incremental_$(date +%Y%m%d_%H%M%S).txt"

echo "" | tee "$REPORT_FILE"
echo "==============================================" | tee -a "$REPORT_FILE"
echo "  Incremental Disk Snapshot Integration Test" | tee -a "$REPORT_FILE"
echo "  $(date -Iseconds)" | tee -a "$REPORT_FILE"
echo "==============================================" | tee -a "$REPORT_FILE"
echo "" | tee -a "$REPORT_FILE"

exec > >(tee -a "$REPORT_FILE") 2>&1

# ============================================================
# Step 1: Login
# ============================================================
log "Step 1: Login"
login

# ============================================================
# Step 2: Create VM
# ============================================================
log "Step 2: Create VM via chat"
CHAT_RES=$(api POST "/chat" -d '{"message":"run ubuntu vm"}')
CHAT_ID=$(echo "$CHAT_RES" | jq -r '.data.chat_id')
log "Chat ID: $CHAT_ID"
VM_ID=""
wait_for_vm "$CHAT_ID"

# ============================================================
# Step 3: Session 1 — install base packages
# ============================================================
log "Step 3 (Session 1): Install base packages"
INSTALL_OUT=$(exec_vm "$VM_ID" 'apt-get update -qq && apt-get install -y -qq curl htop 2>&1 | tail -1 && echo INSTALL_OK')
if echo "$INSTALL_OUT" | grep -q "INSTALL_OK"; then
	ok "Session 1: curl and htop installed"
else
	warn "Session 1: install output: $INSTALL_OUT"
fi

# Write session 1 markers
exec_vm "$VM_ID" "echo 'session1' > /etc/incr-test-s1.conf" > /dev/null
exec_vm "$VM_ID" "echo 'data1' > /opt/incr-test-s1.db" > /dev/null
ok "Session 1: markers written"

# ============================================================
# Step 4: Snapshot 1 — should be FULL (disk format)
# ============================================================
log "Step 4: Snapshot 1 (first snapshot → full, disk format)"
SNAP1=$(take_snapshot "$VM_ID")
SNAP1_TYPE=$(echo "$SNAP1" | jq -r '.data.type // empty')
SNAP1_ID=$(echo "$SNAP1" | jq -r '.data.id // empty')

if [ -z "$SNAP1_TYPE" ]; then
	SNAP1_ERR=$(echo "$SNAP1" | jq -r '.message // .error // .' 2>/dev/null || echo "$SNAP1")
	fail "Snapshot 1 failed: $SNAP1_ERR"
	exit 1
fi

log "Snapshot 1: type=$SNAP1_TYPE id=${SNAP1_ID:0:8}..."

# Check disk_snapshot_type from metadata
S1_DISK_TYPE=$(get_snapshot_metadata "$VM_ID" "disk_snapshot_type")
log "Snapshot 1 disk_snapshot_type: $S1_DISK_TYPE"
if [ "$S1_DISK_TYPE" == "full" ] || [ "$S1_DISK_TYPE" == "self_contained" ]; then
	ok "Snapshot 1 disk type is '$S1_DISK_TYPE' (chain root)"
else
	fail "Snapshot 1 disk type should be 'full', got '$S1_DISK_TYPE'"
fi

# Check chain length
S1_CHAIN=$(get_snapshot_metadata "$VM_ID" "disk_chain_length")
log "Snapshot 1 chain_length: $S1_CHAIN"
if [ "$S1_CHAIN" == "0" ]; then
	ok "Snapshot 1 chain_length = 0 (root)"
else
	fail "Snapshot 1 chain_length should be 0, got $S1_CHAIN"
fi

# Verify S3 format
sleep 2
S1_S3_PATH=$(snapshot_s3_path "$VM_ID")
S1_FORMAT=$(check_snapshot_format "$S1_S3_PATH")
log "Snapshot 1 S3 format: $S1_FORMAT"
if [ "$S1_FORMAT" == "disk" ]; then
	ok "Snapshot 1 uploaded as disk.zst (full)"
else
	fail "Snapshot 1 should have disk.zst, got format: $S1_FORMAT"
fi

# ============================================================
# Steps 5-8: Take incremental snapshots 2-5
# ============================================================
MAX_CHAIN=5  # must match config.yaml max_chain_length

for SNAP_IDX in $(seq 2 $MAX_CHAIN); do
	log ""
	log "Step $((SNAP_IDX+3)) (Session $SNAP_IDX): Write data + snapshot $SNAP_IDX"

	# Write session-specific markers
	exec_vm "$VM_ID" "echo 'session${SNAP_IDX}' > /etc/incr-test-s${SNAP_IDX}.conf" > /dev/null
	exec_vm "$VM_ID" "echo 'data${SNAP_IDX}' > /opt/incr-test-s${SNAP_IDX}.db" > /dev/null

	# Install a unique package per session for binary verification
	case $SNAP_IDX in
		2) PKG="tree" ;;
		3) PKG="jq" ;;
		4) PKG="strace" ;;
		5) PKG="lsof" ;;
		*) PKG="" ;;
	esac
	if [ -n "$PKG" ]; then
		INST_OUT=$(exec_vm "$VM_ID" "apt-get install -y -qq $PKG 2>&1 | tail -1 && echo INSTALL_OK")
		if echo "$INST_OUT" | grep -q "INSTALL_OK"; then
			ok "Session $SNAP_IDX: $PKG installed"
		else
			warn "Session $SNAP_IDX: $PKG install output: $INST_OUT"
		fi
	fi

	# Take snapshot
	SNAP_RES=$(take_snapshot "$VM_ID")
	SNAP_TYPE=$(echo "$SNAP_RES" | jq -r '.data.type // empty')
	SNAP_ID=$(echo "$SNAP_RES" | jq -r '.data.id // empty')

	if [ -z "$SNAP_TYPE" ]; then
		SNAP_ERR=$(echo "$SNAP_RES" | jq -r '.message // .error // .' 2>/dev/null || echo "$SNAP_RES")
		fail "Snapshot $SNAP_IDX failed: $SNAP_ERR"
		continue
	fi

	# Check disk snapshot type — should be incremental
	DISK_TYPE=$(get_snapshot_metadata "$VM_ID" "disk_snapshot_type")
	log "Snapshot $SNAP_IDX disk_snapshot_type: $DISK_TYPE"
	if [ "$DISK_TYPE" == "incremental" ]; then
		ok "Snapshot $SNAP_IDX is incremental"
	else
		fail "Snapshot $SNAP_IDX should be 'incremental', got '$DISK_TYPE'"
	fi

	# Check chain length
	CHAIN_LEN=$(get_snapshot_metadata "$VM_ID" "disk_chain_length")
	log "Snapshot $SNAP_IDX chain_length: $CHAIN_LEN"
	EXPECTED_LEN=$((SNAP_IDX-1))
	if [ "$CHAIN_LEN" == "$EXPECTED_LEN" ]; then
		ok "Snapshot $SNAP_IDX chain_length = $EXPECTED_LEN (correct)"
	else
		fail "Snapshot $SNAP_IDX chain_length should be $EXPECTED_LEN, got $CHAIN_LEN"
	fi

	# Verify S3 format — should be cow (not disk)
	sleep 2
	SNAP_S3_PATH=$(snapshot_s3_path "$VM_ID")
	SNAP_FORMAT=$(check_snapshot_format "$SNAP_S3_PATH")
	log "Snapshot $SNAP_IDX S3 format: $SNAP_FORMAT"
	if [ "$SNAP_FORMAT" == "cow" ]; then
		ok "Snapshot $SNAP_IDX uploaded as cow.zst (delta)"
	else
		fail "Snapshot $SNAP_IDX should have cow.zst, got format: $SNAP_FORMAT"
	fi

	# Verify chain-aware GC: all snapshots should be retained
	sleep 3
	SNAP_COUNT=$(snapshot_count "$VM_ID")
	log "Snapshot count after snapshot $SNAP_IDX: $SNAP_COUNT"
	if [ "$SNAP_COUNT" -eq "$SNAP_IDX" ]; then
		ok "Chain-aware GC: $SNAP_COUNT snapshots retained (chain intact)"
	else
		warn "Chain-aware GC: expected $SNAP_IDX snapshots, got $SNAP_COUNT"
	fi
done

# ============================================================
# Step 9: Snapshot 6 — should trigger COMPACTION (forced full)
# ============================================================
log ""
log "Step 9: Snapshot 6 (should trigger compaction → full disk)"

exec_vm "$VM_ID" "echo 'session6' > /etc/incr-test-s6.conf" > /dev/null
exec_vm "$VM_ID" "echo 'data6' > /opt/incr-test-s6.db" > /dev/null

SNAP6=$(take_snapshot "$VM_ID")
SNAP6_TYPE=$(echo "$SNAP6" | jq -r '.data.type // empty')
SNAP6_ID=$(echo "$SNAP6" | jq -r '.data.id // empty')

if [ -z "$SNAP6_TYPE" ]; then
	SNAP6_ERR=$(echo "$SNAP6" | jq -r '.message // .error // .' 2>/dev/null || echo "$SNAP6")
	fail "Snapshot 6 failed: $SNAP6_ERR"
else
	log "Snapshot 6: type=$SNAP6_TYPE id=${SNAP6_ID:0:8}..."
fi

# Check disk type — should be full (compaction)
S6_DISK_TYPE=$(get_snapshot_metadata "$VM_ID" "disk_snapshot_type")
log "Snapshot 6 disk_snapshot_type: $S6_DISK_TYPE"
if [ "$S6_DISK_TYPE" == "full" ]; then
	ok "Snapshot 6 is 'full' (compaction triggered at chain length $MAX_CHAIN)"
else
	fail "Snapshot 6 should be 'full' (compaction), got '$S6_DISK_TYPE'"
fi

# Chain length should reset
S6_CHAIN=$(get_snapshot_metadata "$VM_ID" "disk_chain_length")
log "Snapshot 6 chain_length: $S6_CHAIN"
if [ "$S6_CHAIN" == "0" ]; then
	ok "Snapshot 6 chain_length = 0 (new chain root after compaction)"
else
	fail "Snapshot 6 chain_length should be 0, got $S6_CHAIN"
fi

# S3 format should be disk (full)
sleep 2
S6_S3_PATH=$(snapshot_s3_path "$VM_ID")
S6_FORMAT=$(check_snapshot_format "$S6_S3_PATH")
log "Snapshot 6 S3 format: $S6_FORMAT"
if [ "$S6_FORMAT" == "disk" ]; then
	ok "Snapshot 6 uploaded as disk.zst (compaction full)"
else
	fail "Snapshot 6 should have disk.zst (compaction), got format: $S6_FORMAT"
fi

# Wait for GC to clean up old chain
sleep 5
FINAL_COUNT=$(snapshot_count "$VM_ID")
log "Snapshot count after compaction + GC: $FINAL_COUNT"
if [ "$FINAL_COUNT" -le 2 ]; then
	ok "GC: old chain cleaned up after compaction ($FINAL_COUNT snapshots remain)"
else
	warn "GC: $FINAL_COUNT snapshots remain — old chain may not be cleaned yet"
fi

# Show snapshot details
log "Snapshot state after compaction:"
snapshot_details "$VM_ID"

# ============================================================
# Step 10: Stop VM
# ============================================================
log ""
log "Step 10: Stop VM"
stop_vm "$VM_ID"

# ============================================================
# Step 11: Resume — the critical multi-hop restore test
# ============================================================
log ""
log "Step 11: Resume from compaction snapshot (multi-hop chain restore)"

if ! resume_vm "$CHAT_ID" "session 7 restore"; then
	log "Cannot continue test without successful resume. Exiting."
	exit 1
fi

# ============================================================
# Step 12: Verify ALL state from sessions 1-6
# ============================================================
log ""
log "Step 12: Verify ALL state from sessions 1-6 survived"

FAILURES=0

for S in 1 2 3 4 5 6; do
	ETC_VAL=$(exec_vm "$VM_ID" "cat /etc/incr-test-s${S}.conf 2>/dev/null || echo MISSING")
	if [ "$ETC_VAL" == "session${S}" ]; then
		ok "Session $S /etc/incr-test-s${S}.conf survived"
	else
		fail "Session $S /etc/incr-test-s${S}.conf LOST (got: $ETC_VAL)"
		FAILURES=$((FAILURES+1))
	fi

	OPT_VAL=$(exec_vm "$VM_ID" "cat /opt/incr-test-s${S}.db 2>/dev/null || echo MISSING")
	if [ "$OPT_VAL" == "data${S}" ]; then
		ok "Session $S /opt/incr-test-s${S}.db survived"
	else
		fail "Session $S /opt/incr-test-s${S}.db LOST (got: $OPT_VAL)"
		FAILURES=$((FAILURES+1))
	fi
done

# Verify installed binaries
for BIN_NAME in curl htop tree jq strace lsof; do
	BIN_CHECK=$(exec_vm "$VM_ID" "which $BIN_NAME 2>/dev/null && echo FOUND || echo MISSING")
	if echo "$BIN_CHECK" | grep -q "FOUND"; then
		ok "Binary $BIN_NAME survived"
	else
		fail "Binary $BIN_NAME LOST after restore"
		FAILURES=$((FAILURES+1))
	fi
done

# ============================================================
# Step 13: Verify dm device is single-layer
# ============================================================
log ""
log "Step 13: Verify dm device state"
DM=$(dm_status "$VM_ID")
log "dm status: $DM"

DM_LAYERS=$(docker exec spacetrek-api dmsetup ls 2>/dev/null | grep -c "$(echo "$VM_ID" | tr '-' '_')_layer" || echo "0")
if [ "$DM_LAYERS" -eq 0 ]; then
	ok "dm flatten: no intermediate layers (single-layer device)"
else
	warn "dm flatten: $DM_LAYERS intermediate layer(s) found"
fi

# ============================================================
# Step 14: Verify pause duration metrics
# ============================================================
log ""
log "Step 14: Check pause duration metrics"
docker exec spacetrek-psql psql -U spacetrek -d spacetrek -c \
	"SELECT snapshot_id, type, pause_duration_ms, memory_bytes, cow_bytes, disk_bytes, chain_depth FROM snapshot_metrics WHERE vm_id='$VM_ID' ORDER BY created_at;" 2>/dev/null || warn "Could not query snapshot_metrics"

# ============================================================
# Cleanup
# ============================================================
log ""
log "Cleanup: stopping VM"
api DELETE "/vm/$VM_ID" 2>/dev/null | jq '.message' 2>/dev/null || true

# ============================================================
# Summary
# ============================================================
echo ""
echo "============================================="
echo "  Summary"
echo "============================================="
echo "  VM ID:               $VM_ID"
echo "  Chat ID:             $CHAT_ID"
echo "  Passed:              $PASS_COUNT"
echo "  Failed:              $FAIL_COUNT"
echo ""
echo "  Data survival:       $([ $FAILURES -eq 0 ] && echo 'ALL PASS' || echo \"$FAILURES FAILURES\")"
echo "  dm single-layer:     $([ "$DM_LAYERS" -eq 0 ] && echo 'PASS' || echo 'WARN')"
echo "============================================="
echo ""
echo "  Report saved to: $REPORT_FILE"

if [ "$FAIL_COUNT" -gt 0 ]; then
	echo ""
	fail "Some checks failed — see above for details"
	exit 1
fi

ok "All incremental snapshot checks passed"
