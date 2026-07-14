#!/usr/bin/env bash
#
# Test snapshot GC + flatten flow:
#   Session 1: install packages → snapshot → stop
#   Session 2: resume → verify packages intact → install more → snapshot (GC deletes old) → stop
#   Session 3: resume → verify ALL packages from both sessions present
#
# Validates the fix for: snapshot GC breaks CoW chain, losing disk writes outside /workspace
#
# Usage: ./scripts/test_snapshot_gc_flatten.sh [BASE_URL]
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
	# Use jq to properly JSON-encode the command string, handling all special
	# characters (quotes, backslashes, newlines, etc.) correctly.
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
	# Debug: log raw response if fields are missing
	local check_type
	check_type=$(echo "$res" | jq -r '.data.type // empty')
	if [ -z "$check_type" ]; then
		warn "Snapshot response missing .data.type — raw: $(echo "$res" | jq -c '.')"
	fi
	echo "$res"
}

snapshot_count() {
	local vm_id="$1"
	docker exec spacetrek-psql psql -U spacetrek -d spacetrek -t -c \
		"SELECT COUNT(*) FROM vm_snapshots WHERE vm_id='$vm_id';" \
		| tr -d ' \n'
}

snapshot_types() {
	local vm_id="$1"
	docker exec spacetrek-psql psql -U spacetrek -d spacetrek -c \
		"SELECT type, pg_size_pretty(size_bytes) as size FROM vm_snapshots WHERE vm_id='$vm_id' ORDER BY created_at;"
}

# snapshot_s3_path returns the S3 prefix for the latest snapshot of a VM.
snapshot_s3_path() {
	local vm_id="$1"
	docker exec spacetrek-psql psql -U spacetrek -d spacetrek -t -c \
		"SELECT snapshot_path FROM vm_snapshots WHERE vm_id='$vm_id' ORDER BY created_at DESC LIMIT 1;" \
		| tr -d ' \n'
}

# check_snapshot_format checks whether the latest snapshot uses the new 'disk'
# format or the legacy 'cow' format by querying S3 object existence.
check_snapshot_format() {
	local vm_id="$1"
	local s3_path
	s3_path=$(snapshot_s3_path "$vm_id")
	if [ -z "$s3_path" ]; then
		echo "none"
		return
	fi
	# Check for disk.zst (new format) first, then cow.zst (legacy).
	local has_disk has_cow
	has_disk=$(docker exec spacetrek-api sh -c \
		"test -f /tmp/snapshots/${s3_path}/disk.zst 2>/dev/null && echo yes || echo no" 2>/dev/null || echo "unknown")
	has_cow=$(docker exec spacetrek-api sh -c \
		"test -f /tmp/snapshots/${s3_path}/cow.zst 2>/dev/null && echo yes || echo no" 2>/dev/null || echo "unknown")

	if [ "$has_disk" == "yes" ]; then
		echo "disk"
	elif [ "$has_cow" == "yes" ]; then
		echo "cow"
	else
		echo "unknown"
	fi
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
		# Refresh token in case it expired during the wait.
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

# --- Test Steps ---

REPORT_FILE="test_snapshot_gc_$(date +%Y%m%d_%H%M%S).txt"

echo "" | tee "$REPORT_FILE"
echo "==========================================" | tee -a "$REPORT_FILE"
echo "  Snapshot GC + Flatten Integration Test" | tee -a "$REPORT_FILE"
echo "  $(date -Iseconds)" | tee -a "$REPORT_FILE"
echo "==========================================" | tee -a "$REPORT_FILE"
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
# Step 3 (Session 1): Install packages, write to /etc, /bin, /opt
# ============================================================
log "Step 3 (Session 1): Install packages and write files outside /workspace"

# Install packages via package manager
log "Installing curl and htop..."
INSTALL_OUT=$(exec_vm "$VM_ID" 'apt-get update -qq && apt-get install -y -qq curl htop 2>&1 | tail -1 && echo INSTALL_OK')
if echo "$INSTALL_OUT" | grep -q "INSTALL_OK"; then
	ok "Session 1: curl and htop installed"
else
	warn "Session 1: install output: $INSTALL_OUT"
fi
sleep 2

# Write to /etc (config files)
exec_vm "$VM_ID" "echo 'session1_config' > /etc/test-snapshot-gc.conf && echo WRITTEN" > /dev/null
S1_ETC=$(exec_vm "$VM_ID" "cat /etc/test-snapshot-gc.conf 2>/dev/null || echo MISSING")
if [ "$S1_ETC" == "session1_config" ]; then
	ok "Session 1: /etc/test-snapshot-gc.conf written"
else
	fail "Session 1: failed to write /etc/test-snapshot-gc.conf (got: $S1_ETC)"
fi

# Write to /opt (third-party installs)
exec_vm "$VM_ID" "echo 'session1_data' > /opt/test-data.db && echo WRITTEN" > /dev/null
S1_OPT=$(exec_vm "$VM_ID" "cat /opt/test-data.db 2>/dev/null || echo MISSING")
if [ "$S1_OPT" == "session1_data" ]; then
	ok "Session 1: /opt/test-data.db written"
else
	fail "Session 1: failed to write /opt/test-data.db (got: $S1_OPT)"
fi

# Verify curl works
CURL_CHECK=$(exec_vm "$VM_ID" "which curl 2>/dev/null && echo CURL_FOUND || echo CURL_MISSING")
if echo "$CURL_CHECK" | grep -q "CURL_FOUND"; then
	ok "Session 1: curl binary accessible"
else
	fail "Session 1: curl binary not found"
fi

# ============================================================
# Step 4: Take snapshot 1 (full)
# ============================================================
log "Step 4: Take snapshot 1 (full)"
SNAP1=$(take_snapshot "$VM_ID")
SNAP1_TYPE=$(echo "$SNAP1" | jq -r '.data.type // empty')
SNAP1_SIZE=$(echo "$SNAP1" | jq -r '.data.size_bytes // empty')
SNAP1_ID=$(echo "$SNAP1" | jq -r '.data.id // empty')

if [ -z "$SNAP1_TYPE" ]; then
	SNAP1_ERR=$(echo "$SNAP1" | jq -r '.message // .error // .' 2>/dev/null || echo "$SNAP1")
	fail "Snapshot 1 failed: $SNAP1_ERR"
	log "Raw response: $(echo "$SNAP1" | jq -c '.' 2>/dev/null || echo "$SNAP1")"
	log "Cannot continue without snapshot. Exiting."
	exit 1
fi

log "Snapshot 1: type=$SNAP1_TYPE size=$SNAP1_SIZE id=${SNAP1_ID:0:8}..."

if [ "$SNAP1_TYPE" == "full" ]; then
	ok "Snapshot 1 is 'full'"
else
	fail "Snapshot 1 should be 'full', got '$SNAP1_TYPE'"
fi

# Verify new snapshot uses 'disk' format (self-contained), not 'cow' (delta).
sleep 2  # wait for upload to finish
SNAP1_FORMAT=$(check_snapshot_format "$VM_ID")
log "Snapshot 1 format: $SNAP1_FORMAT"
if [ "$SNAP1_FORMAT" == "disk" ]; then
	ok "Snapshot 1 uses new 'disk' format (self-contained)"
elif [ "$SNAP1_FORMAT" == "cow" ]; then
	warn "Snapshot 1 uses legacy 'cow' format (delta only) — flatten fix may not be deployed"
else
	warn "Could not determine snapshot format ($SNAP1_FORMAT) — S3 check may not be available in this setup"
fi

# Check dm device is still healthy
DM=$(dm_status "$VM_ID")
log "dm status after snapshot 1: $DM"

# ============================================================
# Step 5: Stop VM
# ============================================================
log "Step 5: Stop VM"
stop_vm "$VM_ID"

VM_STATUS=$(get_vm_status "$VM_ID")
log "VM status after stop: $VM_STATUS"

# ============================================================
# Step 6 (Session 2): Resume VM and verify session 1 state
# ============================================================
log "Step 6 (Session 2): Resume VM"
for i in $(seq 1 5); do
	RESUME_RES=$(api POST "/vm/resume" -d "{\"chat_id\":\"$CHAT_ID\"}" 2>&1 || true)
	RESUME_STATUS=$(echo "$RESUME_RES" | jq -r '.status_code // "error"')
	
	if [ "$RESUME_STATUS" == "200" ]; then
		break
	fi
	
	log "Resume attempt $i failed ($RESUME_STATUS), retrying in 5s..."
	sleep 5
done

if [ "$RESUME_STATUS" == "200" ]; then
	RESUMED_VM=$(echo "$RESUME_RES" | jq -r '.data.id')
	ok "VM resumed: $RESUMED_VM"
	VM_ID="$RESUMED_VM"
else
	RESUME_ERR=$(echo "$RESUME_RES" | jq -r '.message // .' 2>/dev/null || echo "$RESUME_RES")
	fail "Resume failed: $RESUME_ERR"
	log "Cannot continue test without successful resume. Exiting."
	exit 1
fi

# Wait for VM to be ready after resume
wait_for_responsive "$VM_ID" "resume"

# ============================================================
# Step 7: Verify session 1 state survived resume
# ============================================================
log "Step 7: Verify session 1 state after resume"

# Check /etc file
S2_ETC=$(exec_vm "$VM_ID" "cat /etc/test-snapshot-gc.conf 2>/dev/null || echo MISSING")
if [ "$S2_ETC" == "session1_config" ]; then
	ok "Session 1 /etc/test-snapshot-gc.conf survived resume"
else
	fail "Session 1 /etc/test-snapshot-gc.conf LOST after resume (got: $S2_ETC)"
fi

# Check /opt file
S2_OPT=$(exec_vm "$VM_ID" "cat /opt/test-data.db 2>/dev/null || echo MISSING")
if [ "$S2_OPT" == "session1_data" ]; then
	ok "Session 1 /opt/test-data.db survived resume"
else
	fail "Session 1 /opt/test-data.db LOST after resume (got: $S2_OPT)"
fi

# Check curl binary
S2_CURL=$(exec_vm "$VM_ID" "which curl 2>/dev/null && echo CURL_FOUND || echo CURL_MISSING")
if echo "$S2_CURL" | grep -q "CURL_FOUND"; then
	ok "Session 1 curl binary survived resume"
else
	fail "Session 1 curl binary LOST after resume"
fi

# Check htop binary
S2_HTOP=$(exec_vm "$VM_ID" "which htop 2>/dev/null && echo HTOP_FOUND || echo HTOP_MISSING")
if echo "$S2_HTOP" | grep -q "HTOP_FOUND"; then
	ok "Session 1 htop binary survived resume"
else
	fail "Session 1 htop binary LOST after resume"
fi

# ============================================================
# Step 8 (Session 2): Install more packages, write more data
# ============================================================
log "Step 8 (Session 2): Install more packages and write more files"

# Install ripgrep
INSTALL_OUT2=$(exec_vm "$VM_ID" 'apt-get install -y -qq ripgrep 2>&1 | tail -1 && echo INSTALL_OK')
if echo "$INSTALL_OUT2" | grep -q "INSTALL_OK"; then
	ok "Session 2: ripgrep installed"
else
	warn "Session 2: ripgrep install output: $INSTALL_OUT2"
fi
sleep 2

# Write to different locations
exec_vm "$VM_ID" "echo 'session2_config' > /etc/test-snapshot-gc-v2.conf" > /dev/null
exec_vm "$VM_ID" "echo 'session2_data' > /opt/test-data-v2.db" > /dev/null
exec_vm "$VM_ID" "echo 'session2_var' > /var/test-var.log" > /dev/null

# Verify session 2 writes
S2_RG=$(exec_vm "$VM_ID" "which rg 2>/dev/null && echo RG_FOUND || echo RG_MISSING")
if echo "$S2_RG" | grep -q "RG_FOUND"; then
	ok "Session 2: ripgrep (rg) binary accessible"
else
	fail "Session 2: ripgrep binary not found"
fi

# ============================================================
# Step 9: Take snapshot 2 — GC should delete snapshot 1
# ============================================================
log "Step 9: Take snapshot 2 (GC should delete snapshot 1)"
BEFORE_COUNT=$(snapshot_count "$VM_ID")
log "Snapshots before snapshot 2: $BEFORE_COUNT"

SNAP2=$(take_snapshot "$VM_ID")
SNAP2_TYPE=$(echo "$SNAP2" | jq -r '.data.type // empty')
SNAP2_SIZE=$(echo "$SNAP2" | jq -r '.data.size_bytes // empty')
SNAP2_ID=$(echo "$SNAP2" | jq -r '.data.id // empty')

if [ -z "$SNAP2_TYPE" ]; then
	SNAP2_ERR=$(echo "$SNAP2" | jq -r '.message // .error // .' 2>/dev/null || echo "$SNAP2")
	fail "Snapshot 2 failed: $SNAP2_ERR"
	log "Raw response: $(echo "$SNAP2" | jq -c '.' 2>/dev/null || echo "$SNAP2")"
else
	log "Snapshot 2: type=$SNAP2_TYPE size=$SNAP2_SIZE id=${SNAP2_ID:0:8}..."
fi

# Verify snapshot 2 also uses 'disk' format.
sleep 2
SNAP2_FORMAT=$(check_snapshot_format "$VM_ID")
log "Snapshot 2 format: $SNAP2_FORMAT"
if [ "$SNAP2_FORMAT" == "disk" ]; then
	ok "Snapshot 2 uses new 'disk' format (self-contained)"
elif [ "$SNAP2_FORMAT" == "cow" ]; then
	fail "Snapshot 2 uses legacy 'cow' format — full disk capture not working"
fi

# Wait for async GC to run
sleep 5

AFTER_COUNT=$(snapshot_count "$VM_ID")
log "Snapshots after snapshot 2 + GC: $AFTER_COUNT"

if [ "$AFTER_COUNT" -eq 1 ]; then
	ok "GC: only 1 snapshot remains after new snapshot (old one deleted)"
else
	if [ "$AFTER_COUNT" -gt 1 ]; then
		warn "GC: $AFTER_COUNT snapshots remain (expected 1) — GC may not have run yet or failed"
	else
		fail "GC: 0 snapshots remain — GC deleted too aggressively"
	fi
fi

# ============================================================
# Step 10: Stop VM again
# ============================================================
log "Step 10: Stop VM (session 2)"
stop_vm "$VM_ID"

# ============================================================
# Step 11 (Session 3): Resume again — the critical test
# ============================================================
log "Step 11 (Session 3): Resume VM — verify ALL state from sessions 1 AND 2"
for i in $(seq 1 5); do
	RESUME2_RES=$(api POST "/vm/resume" -d "{\"chat_id\":\"$CHAT_ID\"}" 2>&1 || true)
	RESUME2_STATUS=$(echo "$RESUME2_RES" | jq -r '.status_code // "error"')
	
	if [ "$RESUME2_STATUS" == "200" ]; then
		break
	fi
	
	log "Session 3 resume attempt $i failed ($RESUME2_STATUS), retrying in 5s..."
	sleep 5
done

if [ "$RESUME2_STATUS" == "200" ]; then
	RESUMED2_VM=$(echo "$RESUME2_RES" | jq -r '.data.id')
	ok "VM resumed for session 3: $RESUMED2_VM"
	VM_ID="$RESUMED2_VM"
else
	RESUME2_ERR=$(echo "$RESUME2_RES" | jq -r '.message // .' 2>/dev/null || echo "$RESUME2_RES")
	fail "Session 3 resume failed: $RESUME2_ERR"
	exit 1
fi

# Wait for VM to be ready
wait_for_responsive "$VM_ID" "session 3 resume"

# ============================================================
# Step 12: THE CRITICAL CHECK — verify ALL state from both sessions
# ============================================================
log "Step 12: Verify ALL state from sessions 1 AND 2"

# --- Session 1 state ---

S3_ETC_V1=$(exec_vm "$VM_ID" "cat /etc/test-snapshot-gc.conf 2>/dev/null || echo MISSING")
if [ "$S3_ETC_V1" == "session1_config" ]; then
	ok "Session 1 /etc/test-snapshot-gc.conf survived (2 resumes)"
else
	fail "Session 1 /etc/test-snapshot-gc.conf LOST after 2 resumes (got: $S3_ETC_V1) — CoW chain flatten NOT working"
fi

S3_OPT_V1=$(exec_vm "$VM_ID" "cat /opt/test-data.db 2>/dev/null || echo MISSING")
if [ "$S3_OPT_V1" == "session1_data" ]; then
	ok "Session 1 /opt/test-data.db survived (2 resumes)"
else
	fail "Session 1 /opt/test-data.db LOST after 2 resumes (got: $S3_OPT_V1) — CoW chain flatten NOT working"
fi

S3_CURL=$(exec_vm "$VM_ID" "which curl 2>/dev/null && echo CURL_FOUND || echo CURL_MISSING")
if echo "$S3_CURL" | grep -q "CURL_FOUND"; then
	ok "Session 1 curl survived (2 resumes)"
else
	fail "Session 1 curl LOST after 2 resumes — CoW chain flatten NOT working"
fi

S3_HTOP=$(exec_vm "$VM_ID" "which htop 2>/dev/null && echo HTOP_FOUND || echo HTOP_MISSING")
if echo "$S3_HTOP" | grep -q "HTOP_FOUND"; then
	ok "Session 1 htop survived (2 resumes)"
else
	fail "Session 1 htop LOST after 2 resumes — CoW chain flatten NOT working"
fi

# --- Session 2 state ---

S3_ETC_V2=$(exec_vm "$VM_ID" "cat /etc/test-snapshot-gc-v2.conf 2>/dev/null || echo MISSING")
if [ "$S3_ETC_V2" == "session2_config" ]; then
	ok "Session 2 /etc/test-snapshot-gc-v2.conf survived (1 resume)"
else
	fail "Session 2 /etc/test-snapshot-gc-v2.conf LOST after resume (got: $S3_ETC_V2)"
fi

S3_OPT_V2=$(exec_vm "$VM_ID" "cat /opt/test-data-v2.db 2>/dev/null || echo MISSING")
if [ "$S3_OPT_V2" == "session2_data" ]; then
	ok "Session 2 /opt/test-data-v2.db survived (1 resume)"
else
	fail "Session 2 /opt/test-data-v2.db LOST after resume (got: $S3_OPT_V2)"
fi

S3_VAR=$(exec_vm "$VM_ID" "cat /var/test-var.log 2>/dev/null || echo MISSING")
if [ "$S3_VAR" == "session2_var" ]; then
	ok "Session 2 /var/test-var.log survived (1 resume)"
else
	fail "Session 2 /var/test-var.log LOST after resume (got: $S3_VAR)"
fi

S3_RG=$(exec_vm "$VM_ID" "which rg 2>/dev/null && echo RG_FOUND || echo RG_MISSING")
if echo "$S3_RG" | grep -q "RG_FOUND"; then
	ok "Session 2 ripgrep survived (1 resume)"
else
	fail "Session 2 ripgrep LOST after resume"
fi

# ============================================================
# Step 13: Verify GC — should be exactly 1 snapshot (post-resume GC fires async)
# ============================================================
log "Step 13: Verify GC state"
sleep 5
FINAL_COUNT=$(snapshot_count "$VM_ID")
log "Final snapshot count: $FINAL_COUNT"

if [ "$FINAL_COUNT" -le 1 ]; then
	ok "GC: at most 1 snapshot remains after resume (bounded storage)"
else
	warn "GC: $FINAL_COUNT snapshots remain — post-resume GC may not have run yet"
fi

# ============================================================
# Step 14: Verify dm device is single-layer (flatten worked)
# ============================================================
log "Step 14: Verify dm device state"
DM=$(dm_status "$VM_ID")
log "dm status: $DM"

# Check for intermediate layers — if flatten worked, there should be no _layer0, _layer1 devices
DM_LAYERS=$(docker exec spacetrek-api dmsetup ls 2>/dev/null | grep -c "$(echo "$VM_ID" | tr '-' '_')_layer" || echo "0")
if [ "$DM_LAYERS" -eq 0 ]; then
	ok "dm flatten: no intermediate layers detected (single-layer device)"
else
	warn "dm flatten: $DM_LAYERS intermediate layer(s) found — flatten may not have run"
fi

# ============================================================
# Cleanup: stop VM
# ============================================================
log "Cleanup: stopping VM"
api DELETE "/vm/$VM_ID" 2>/dev/null | jq '.message' 2>/dev/null || true

# ============================================================
# Summary
# ============================================================
echo ""
echo "==========================================="
echo "  Summary"
echo "==========================================="
echo "  VM ID:               $VM_ID"
echo "  Chat ID:             $CHAT_ID"
echo "  Passed:              $PASS_COUNT"
echo "  Failed:              $FAIL_COUNT"
echo ""
echo "  Critical checks (CoW flatten):"
echo "    /etc/session1:      $([ "$S3_ETC_V1" == "session1_config" ] && echo 'PASS' || echo 'FAIL')"
echo "    /opt/session1:      $([ "$S3_OPT_V1" == "session1_data" ] && echo 'PASS' || echo 'FAIL')"
echo "    curl (session 1):   $(echo "$S3_CURL" | grep -q CURL_FOUND && echo 'PASS' || echo 'FAIL')"
echo "    htop (session 1):   $(echo "$S3_HTOP" | grep -q HTOP_FOUND && echo 'PASS' || echo 'FAIL')"
echo "    /etc/session2:      $([ "$S3_ETC_V2" == "session2_config" ] && echo 'PASS' || echo 'FAIL')"
echo "    /opt/session2:      $([ "$S3_OPT_V2" == "session2_data" ] && echo 'PASS' || echo 'FAIL')"
echo "    /var/session2:      $([ "$S3_VAR" == "session2_var" ] && echo 'PASS' || echo 'FAIL')"
echo "    ripgrep (session2): $(echo "$S3_RG" | grep -q RG_FOUND && echo 'PASS' || echo 'FAIL')"
echo "    GC bounded:         $([ "$FINAL_COUNT" -le 1 ] && echo 'PASS' || echo 'WARN')"
echo "    dm single-layer:    $([ "$DM_LAYERS" -eq 0 ] && echo 'PASS' || echo 'WARN')"
echo "==========================================="

echo ""
echo "  Report saved to: $REPORT_FILE"

if [ "$FAIL_COUNT" -gt 0 ]; then
	echo ""
	fail "Some checks failed — see above for details"
	exit 1
fi

ok "All checks passed"
