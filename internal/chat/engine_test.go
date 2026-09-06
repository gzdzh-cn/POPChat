package chat

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"flyqpro/internal/service/db"
)

func TestProtocolDialectAcceptsCanonicalTuple(t *testing.T) {
	message := wireMessage{Protocol: ProtocolName, Major: ProtocolMajor, MinMajor: ProtocolMajor, Magic: DiscoveryMagic}
	got, ok := protocolDialectForMessage(message)
	if !ok || got.Name != ProtocolName || got.Major != ProtocolMajor || got.Magic != DiscoveryMagic {
		t.Fatalf("canonical dialect was not accepted: %+v, %v", got, ok)
	}
	for _, message := range []wireMessage{
		{Protocol: "unknown", Major: 2, Magic: DiscoveryMagic},
		{Protocol: "FlyQPro", Major: 2, Magic: "FLYQPRO_DISCOVERY_V1"},
		{Protocol: "POPChat", Major: 1, Magic: "POPCHAT_DISCOVERY_V1"},
		{Protocol: ProtocolName, Major: ProtocolMajor, Magic: "WRONG_MAGIC"},
	} {
		if _, ok := protocolDialectForMessage(message); ok {
			t.Fatalf("unsupported dialect tuple was accepted: %+v", message)
		}
	}
}

func TestProtocolPeerWithUnknownDialectUsesCanonical(t *testing.T) {
	for _, peer := range []Peer{
		{ProtocolName: "FlyQPro", ProtocolMajor: 2, DiscoveryMagic: "FLYQPRO_DISCOVERY_V1"},
		{ProtocolName: "POPChat", ProtocolMajor: 1, DiscoveryMagic: "POPCHAT_DISCOVERY_V1"},
		{},
	} {
		got := protocolDialectsForPeer(peer)
		if len(got) != 1 || got[0].Name != ProtocolName {
			t.Fatalf("peer should use canonical dialect: %+v", got)
		}
	}
}

func TestHelloMessageUsesCanonicalProtocol(t *testing.T) {
	engine := NewEngine()
	dialect := protocolDialects[0]
	message := engine.helloMessageForDialect("hello", dialect)
	if message.Protocol != ProtocolName || message.Major != ProtocolMajor || message.Magic != DiscoveryMagic {
		t.Fatalf("hello did not use canonical dialect: %+v", message)
	}
	for _, capability := range []string{"text", "image", "file", "file-progress-v1", "file-window-v2", "file-stream-v3", "file-stream-v4", "avatar-sync-v1", "offline-v1", "friend-restore-v2"} {
		if !hasCapability(message.Capabilities, capability) {
			t.Fatalf("capability %q missing: %v", capability, message.Capabilities)
		}
	}
}

func TestParallelStreamRangesCoverFileWithoutOverlap(t *testing.T) {
	const size = int64(2*1024*1024*1024 + 160*1024*1024)
	const streams = 4
	var covered int64
	var previousEnd int64
	for streamID := 0; streamID < streams; streamID++ {
		offset, length, ok := parallelRangeFor(size, streamID, streams)
		if !ok || offset != previousEnd {
			t.Fatalf("invalid range %d: offset=%d length=%d previousEnd=%d", streamID, offset, length, previousEnd)
		}
		covered += length
		previousEnd = offset + length
	}
	if covered != size || previousEnd != size {
		t.Fatalf("ranges covered %d of %d bytes", covered, size)
	}
}

func TestParallelStreamRangesRejectInvalidAndPreserveExactAssignments(t *testing.T) {
	const size = int64(256 * 1024 * 1024)
	for streamID := 0; streamID < parallelMaxStreams; streamID++ {
		offset, length, ok := parallelRangeFor(size, streamID, parallelMaxStreams)
		if !ok || length <= 0 {
			t.Fatalf("stream %d has no valid range", streamID)
		}
		badOffset, badLength, badOK := parallelRangeFor(size, streamID, parallelMaxStreams+1)
		if !badOK || (badOffset == offset && badLength == length) {
			t.Fatalf("stream %d unexpectedly accepted a different stream layout", streamID)
		}
	}
	for _, tc := range []struct {
		fileSize  int64
		streamID  int
		streamCnt int
	}{
		{fileSize: 0, streamID: 0, streamCnt: 1},
		{fileSize: size, streamID: -1, streamCnt: parallelMaxStreams},
		{fileSize: size, streamID: parallelMaxStreams, streamCnt: parallelMaxStreams},
		{fileSize: size, streamID: 0, streamCnt: 0},
	} {
		if _, _, ok := parallelRangeFor(tc.fileSize, tc.streamID, tc.streamCnt); ok {
			t.Fatalf("invalid range accepted: %+v", tc)
		}
	}
}

func TestParallelStreamCountStartsSingleAndScalesForLargeFiles(t *testing.T) {
	if got := parallelStreamCount(parallelStreamThreshold - 1); got != parallelInitialStreams {
		t.Fatalf("small file stream count = %d, want %d", got, parallelInitialStreams)
	}
	if got := parallelStreamCount(parallelStreamThreshold); got != parallelMaxStreams {
		t.Fatalf("large file stream count = %d, want %d", got, parallelMaxStreams)
	}
}

func TestTransferTuningNormalizesDefaultsAndBounds(t *testing.T) {
	got := normalizeTransferTuning(transferTuning{})
	if got.chunkSize != defaultTransferChunkSize || got.windowSize != initialTransferWindow {
		t.Fatalf("default tuning = %+v", got)
	}
	got = normalizeTransferTuning(transferTuning{chunkSize: 123, windowSize: maxTransferWindow + 1})
	if got.chunkSize != defaultTransferChunkSize || got.windowSize != initialTransferWindow {
		t.Fatalf("invalid tuning was not reset: %+v", got)
	}
	got = normalizeTransferTuning(transferTuning{chunkSize: maxTransferChunkSize, windowSize: maxTransferWindow})
	if got.chunkSize != maxTransferChunkSize || got.windowSize != maxTransferWindow {
		t.Fatalf("valid tuning was changed: %+v", got)
	}
	got = normalizeTransferTuning(transferTuning{chunkSize: mediumTransferChunkSize, windowSize: 16})
	if got.chunkSize != mediumTransferChunkSize || got.windowSize != 16 {
		t.Fatalf("medium transfer tuning was changed: %+v", got)
	}
}

func TestAdjustTransferTuningAcceleratesAndBacksOff(t *testing.T) {
	tuning, state, _ := adjustTransferTuning(transferTuning{chunkSize: minTransferChunkSize, windowSize: 4}, 10*time.Millisecond, 1, 12*1024*1024, 10*1024*1024, true)
	if state != "accelerating" || tuning.windowSize != 8 || tuning.chunkSize != minTransferChunkSize {
		t.Fatalf("expected window acceleration, got tuning=%+v state=%s", tuning, state)
	}
	tuning, state, _ = adjustTransferTuning(transferTuning{chunkSize: minTransferChunkSize, windowSize: 16}, 10*time.Millisecond, 1, 20*1024*1024, 18*1024*1024, true)
	if state != "accelerating" || tuning.chunkSize != mediumTransferChunkSize || tuning.windowSize != 8 {
		t.Fatalf("expected chunk acceleration, got tuning=%+v state=%s", tuning, state)
	}
	tuning, state, _ = adjustTransferTuning(transferTuning{chunkSize: maxTransferChunkSize, windowSize: 32}, 350*time.Millisecond, 250, 5*1024*1024, 20*1024*1024, true)
	if state != "backing_off" || tuning.chunkSize != mediumTransferChunkSize || tuning.windowSize != 16 {
		t.Fatalf("expected multiplicative backoff, got tuning=%+v state=%s", tuning, state)
	}
	tuning, state, _ = adjustTransferTuning(transferTuning{chunkSize: minTransferChunkSize, windowSize: 16}, 10*time.Millisecond, 1, 20*1024*1024, 18*1024*1024, false)
	if state != "accelerating" || tuning.chunkSize != maxTransferChunkSize || tuning.chunkSize == mediumTransferChunkSize {
		t.Fatalf("JSON compatibility path selected unsupported medium chunk: tuning=%+v state=%s", tuning, state)
	}
}

func TestAdjustInFlightBudget(t *testing.T) {
	budget, state, reason := adjustInFlightBudget(initialInFlightBytes, minTransferChunkSize, 10*time.Millisecond, 1, 12*1024*1024, 10*1024*1024)
	if state != "accelerating" || reason == "" || budget != initialInFlightBytes*2 {
		t.Fatalf("stable transfer did not grow in-flight budget: budget=%d state=%s reason=%s", budget, state, reason)
	}
	backedOff, state, reason := adjustInFlightBudget(budget, minTransferChunkSize, 500*time.Millisecond, 400, 4*1024*1024, 12*1024*1024)
	if state != "backing_off" || reason == "" || backedOff >= budget {
		t.Fatalf("slow transfer did not back off in-flight budget: budget=%d backedOff=%d state=%s reason=%s", budget, backedOff, state, reason)
	}
}

func TestEffectiveAckLatencyExcludesFlushTime(t *testing.T) {
	if got := effectiveAckLatency(900*time.Millisecond, 700); got != 200*time.Millisecond {
		t.Fatalf("effective ACK latency = %s, want 200ms", got)
	}
	if got := effectiveAckLatency(100*time.Millisecond, 250); got != 0 {
		t.Fatalf("flush time should not produce negative latency: %s", got)
	}
}

func TestBinaryAckTargetTracksInFlightBudget(t *testing.T) {
	if got := binaryAckTargetForBudget(initialInFlightBytes); got != 8*1024*1024 {
		t.Fatalf("initial ACK target = %d, want %d", got, 8*1024*1024)
	}
	if got := binaryAckTargetForBudget(maxInFlightBytes); got != maxBinaryAckBytes {
		t.Fatalf("maximum ACK target = %d, want %d", got, maxBinaryAckBytes)
	}
	if got := binaryAckTargetForBudget(minInFlightBytes); got != minInFlightBytes {
		t.Fatalf("minimum ACK target = %d, want %d", got, minInFlightBytes)
	}
}

func TestSmoothTransferSpeedLimitsWindowJumps(t *testing.T) {
	first := smoothTransferSpeed(0, 8*1024*1024, 0)
	if first != 8*1024*1024 {
		t.Fatalf("first speed sample = %v", first)
	}
	second := smoothTransferSpeed(first, 80*1024*1024, 100*time.Millisecond)
	if second <= first || second >= 80*1024*1024 {
		t.Fatalf("speed jump was not smoothed: first=%v second=%v", first, second)
	}
	third := smoothTransferSpeed(second, 12*1024*1024, 100*time.Millisecond)
	if third >= second || third <= 12*1024*1024 {
		t.Fatalf("speed drop was not smoothed: second=%v third=%v", second, third)
	}
}

func TestEmitTransferProgressPrefersConfirmedRemoteSpeed(t *testing.T) {
	engine := NewEngine()
	engine.emitTransferProgress("message", "attachment", "peer", 0, 100, "remote-receive", "receiving")
	engine.emitTransferProgress("message", "attachment", "peer", 1, 100, "remote-receive", "receiving", transferProgressOptions{windowThroughput: 80, confirmedThroughput: 12})
	engine.transferMetricsMu.Lock()
	metric := engine.transferMetrics["attachment|remote-receive"]
	engine.transferMetricsMu.Unlock()
	if metric.smoothedSpeed != 12 {
		t.Fatalf("remote progress used window speed instead of confirmed speed: %v", metric.smoothedSpeed)
	}
}

func TestEmitTransferProgressDoesNotExposeBinarySocketBurstAsSpeed(t *testing.T) {
	engine := NewEngine()
	engine.emitTransferProgress("message", "attachment", "peer", 0, 100, "send", "transferring", transferProgressOptions{transferMode: binaryTransferMode, windowThroughput: 500 * 1024 * 1024})
	engine.emitTransferProgress("message", "attachment", "peer", 90, 100, "send", "transferring", transferProgressOptions{transferMode: binaryTransferMode, windowThroughput: 500 * 1024 * 1024})
	engine.transferMetricsMu.Lock()
	metric := engine.transferMetrics["attachment|send"]
	engine.transferMetricsMu.Unlock()
	if metric.smoothedSpeed != 0 {
		t.Fatalf("binary socket burst was exposed as display speed: %v", metric.smoothedSpeed)
	}
}

func TestLastTransferBytesKeepsTerminalRemoteConfirmation(t *testing.T) {
	engine := NewEngine()
	engine.emitTransferProgress("message", "attachment", "peer", 0, 100, "remote-receive", "receiving", transferProgressOptions{transferMode: binaryTransferMode})
	engine.emitTransferProgress("message", "attachment", "peer", 30, 100, "remote-receive", "failed", transferProgressOptions{transferMode: binaryTransferMode})
	if got := engine.lastTransferBytes("attachment", "remote-receive"); got != 30 {
		t.Fatalf("terminal remote confirmation = %d, want 30", got)
	}
}

func TestSubnetHostTargetsIncludesPeerAndExcludesLocalAndBroadcast(t *testing.T) {
	_, subnet, err := net.ParseCIDR("192.168.43.4/24")
	if err != nil {
		t.Fatal(err)
	}
	subnet.IP = net.ParseIP("192.168.43.4")

	targets := subnetHostTargets(subnet)
	if len(targets) != 253 {
		t.Fatalf("target count = %d, want 253", len(targets))
	}
	if containsIP(targets, "192.168.43.4") {
		t.Fatal("local address must not be probed")
	}
	if containsIP(targets, "192.168.43.0") || containsIP(targets, "192.168.43.255") {
		t.Fatal("network and broadcast addresses must not be probed")
	}
	if !containsIP(targets, "192.168.43.5") {
		t.Fatal("reachable peer address must be probed")
	}
}

func TestSubnetHostTargetsSkipsLargeNetworks(t *testing.T) {
	_, subnet, err := net.ParseCIDR("10.0.0.1/16")
	if err != nil {
		t.Fatal(err)
	}
	subnet.IP = net.ParseIP("10.0.0.1")
	if targets := subnetHostTargets(subnet); len(targets) != 0 {
		t.Fatalf("large subnet produced %d targets", len(targets))
	}
}

func TestHandleDiscoveryTCPRepliesWhenDiscoverable(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	engine := &Engine{
		profile:  Profile{Discoverable: true, Nickname: "测试设备"},
		identity: Identity{DeviceInfo: DeviceInfo{DeviceID: "local-device"}},
	}
	done := make(chan struct{})
	go func() {
		engine.handleDiscoveryTCP(server)
		close(done)
	}()

	if err := writeWire(client, wireMessage{Protocol: ProtocolName, Major: ProtocolMajor, MinMajor: ProtocolMajor, Magic: DiscoveryMagic, Type: "discover", DeviceID: "remote-device"}); err != nil {
		t.Fatal(err)
	}
	var response wireMessage
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Magic != DiscoveryMagic || response.Type != "announce" || response.Nickname != "测试设备" {
		t.Fatalf("unexpected discovery response: %+v", response)
	}
	<-done
}

func TestDiscoveryPermissionKeepsFriendsDirectlyReachableWhenDisabled(t *testing.T) {
	engine := NewEngine()
	engine.profile = Profile{Discoverable: false}
	engine.peers["friend-1"] = Peer{DeviceID: "friend-1", Relation: PeerRelation}

	if engine.discoveryResponseScope("friend-1") != DiscoveryScopeFriend {
		t.Fatal("friends must remain privately discoverable when public discovery is disabled")
	}
	if engine.canRespondToDiscovery("stranger-1") {
		t.Fatal("strangers must not be discoverable when general discovery is disabled")
	}
	if !engine.canAcceptPeerConnection("friend-1") {
		t.Fatal("friends must remain reachable when general discovery is disabled")
	}
}

func TestDiscoveryPermissionAllowsStrangersWhenEnabled(t *testing.T) {
	engine := NewEngine()
	engine.profile = Profile{Discoverable: true}

	if !engine.canRespondToDiscovery("stranger-1") {
		t.Fatal("strangers must be discoverable when general discovery is enabled")
	}
	if !engine.canAcceptPeerConnection("stranger-1") {
		t.Fatal("discoverable devices must accept stranger connections")
	}
}

func TestDiscoveryScopeSeparatesFriendVisibilityFromRelationship(t *testing.T) {
	engine := NewEngine()
	engine.peers["friend-1"] = Peer{DeviceID: "friend-1", Relation: PeerRelation}

	engine.profile = Profile{Discoverable: false}
	if got := engine.discoveryResponseScope("friend-1"); got != DiscoveryScopeFriend {
		t.Fatalf("private friend scope = %q, want %q", got, DiscoveryScopeFriend)
	}
	if got := engine.discoveryResponseScope("stranger-1"); got != "" {
		t.Fatalf("disabled stranger scope = %q, want no response", got)
	}

	engine.profile = Profile{Discoverable: true}
	if got := engine.discoveryResponseScope("friend-1"); got != DiscoveryScopePublic {
		t.Fatalf("public friend scope = %q, want %q", got, DiscoveryScopePublic)
	}
	if got := engine.discoveryResponseScope("stranger-1"); got != DiscoveryScopePublic {
		t.Fatalf("public stranger scope = %q, want %q", got, DiscoveryScopePublic)
	}
}

func TestDiscoveryResponseMustBelongToCurrentScan(t *testing.T) {
	engine := NewEngine()
	engine.activeDiscoveryIDs = map[string]struct{}{"current-scan": {}}
	engine.activeDiscoverySeen = make(map[string]struct{})

	if engine.acceptDiscoveryResponse("old-scan", "stale-device", DiscoveryScopePublic) {
		t.Fatal("a delayed response from an old scan must be ignored")
	}
	if !engine.acceptDiscoveryResponse("current-scan", "current-device", DiscoveryScopePublic) {
		t.Fatal("the response for the active scan must be accepted")
	}
	if _, ok := engine.activeDiscoverySeen["stale-device"]; ok {
		t.Fatal("stale device must not be included in the current scan snapshot")
	}
	if _, ok := engine.activeDiscoverySeen["current-device"]; !ok {
		t.Fatal("current device must be included in the current scan snapshot")
	}
}

func TestDiscoveryGracePeriodKeepsStrangerForTwoMisses(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOFLY_DB_PATH", filepath.Join(root, "chat.db"))
	if err := db.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())

	ctx := context.Background()
	if err := UpsertPeer(ctx, Peer{DeviceID: "stranger-grace", Nickname: "设备", Relation: DiscoveredState, DiscoveryVisible: true}); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine()
	for round := 1; round <= discoveryMissThreshold-1; round++ {
		engine.removeUnseenDiscoveredPeers(map[string]struct{}{})
		peers, err := ListPeers(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(peers) != 1 || !peers[0].DiscoveryVisible {
			t.Fatalf("peer disappeared after miss %d: %+v", round, peers)
		}
	}

	engine.removeUnseenDiscoveredPeers(map[string]struct{}{})
	peers, err := ListPeers(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 0 {
		t.Fatalf("stranger should be removed after %d misses: %+v", discoveryMissThreshold, peers)
	}
}

func TestDiscoveryLeaseKeepsRecentlySeenStrangerVisible(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOFLY_DB_PATH", filepath.Join(root, "chat.db"))
	if err := db.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())

	ctx := context.Background()
	if err := UpsertPeer(ctx, Peer{
		DeviceID:         "stranger-lease",
		Nickname:         "设备",
		Relation:         DiscoveredState,
		DiscoveryVisible: true,
		Online:           true,
		LastSeen:         nowString(),
	}); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine()
	for round := 0; round < discoveryMissThreshold+2; round++ {
		engine.removeUnseenDiscoveredPeers(map[string]struct{}{})
	}
	peers, err := ListPeers(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || !peers[0].DiscoveryVisible || !peers[0].Online {
		t.Fatalf("recently announced peer should remain visible during scan loss: %+v", peers)
	}
}

func TestFriendScopedAnnouncePreservesPublicDiscoveryVisibility(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOFLY_DB_PATH", filepath.Join(root, "chat.db"))
	if err := db.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())

	ctx := context.Background()
	if err := UpsertPeer(ctx, Peer{
		DeviceID:         "friend-public-presence",
		Nickname:         "好友设备",
		Relation:         PeerRelation,
		DiscoveryVisible: true,
		Online:           true,
		LastSeen:         nowString(),
	}); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine()
	if err := engine.handleAnnounce(wireMessage{
		Magic:          DiscoveryMagic,
		Protocol:       ProtocolName,
		Major:          ProtocolMajor,
		MinMajor:       ProtocolMajor,
		Type:           "announce",
		DeviceID:       "friend-public-presence",
		Nickname:       "好友设备",
		DiscoveryScope: DiscoveryScopeFriend,
	}); err != nil {
		t.Fatal(err)
	}

	peers, err := ListPeers(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || !peers[0].DiscoveryVisible || peers[0].Relation != PeerRelation {
		t.Fatalf("friend-scoped announce cleared public discovery state: %+v", peers)
	}
}

func TestDiscoveryGracePeriodHidesFriendButKeepsRelation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOFLY_DB_PATH", filepath.Join(root, "chat.db"))
	if err := db.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())

	ctx := context.Background()
	if err := UpsertPeer(ctx, Peer{DeviceID: "friend-grace", Nickname: "好友", Relation: PeerRelation, DiscoveryVisible: true}); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine()
	for round := 1; round <= discoveryMissThreshold; round++ {
		engine.removeUnseenDiscoveredPeers(map[string]struct{}{})
	}
	peers, err := ListPeers(ctx, PeerRelation)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].DiscoveryVisible {
		t.Fatalf("friend relation/discovery state is wrong after grace period: %+v", peers)
	}
}

func TestDiscoverabilityPresenceTransition(t *testing.T) {
	tests := []struct {
		name            string
		wasDiscoverable bool
		discoverable    bool
		want            string
	}{
		{name: "disabled remains disabled", wasDiscoverable: false, discoverable: false},
		{name: "enabled remains enabled", wasDiscoverable: true, discoverable: true},
		{name: "disabled to enabled announces", wasDiscoverable: false, discoverable: true, want: "announce"},
		{name: "enabled to disabled withdraws", wasDiscoverable: true, discoverable: false, want: "withdraw"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := discoverabilityPresenceKind(test.wasDiscoverable, test.discoverable); got != test.want {
				t.Fatalf("presence kind = %q, want %q", got, test.want)
			}
		})
	}
}

func TestUpdatePeerRelationRefreshesCachedPeer(t *testing.T) {
	engine := NewEngine()
	engine.peers["peer-1"] = Peer{DeviceID: "peer-1", Relation: DiscoveredState}
	engine.updatePeerRelation("peer-1", PeerRelation)
	if engine.peers["peer-1"].Relation != PeerRelation {
		t.Fatalf("cached relation = %q, want %q", engine.peers["peer-1"].Relation, PeerRelation)
	}
}

func TestHandleOfflineKeepsFriendAndMarksItOffline(t *testing.T) {
	engine := NewEngine()
	engine.peers["friend-1"] = Peer{DeviceID: "friend-1", Relation: PeerRelation, Online: true}

	engine.handleOffline("friend-1")

	peer, ok := engine.peers["friend-1"]
	if !ok {
		t.Fatal("friend must remain in the peer cache")
	}
	if peer.Relation != PeerRelation {
		t.Fatalf("relation = %q, want %q", peer.Relation, PeerRelation)
	}
	if peer.Online {
		t.Fatal("friend must be marked offline")
	}
}

func TestHandleOfflineRemovesDiscoveredPeer(t *testing.T) {
	engine := NewEngine()
	engine.peers["discovered-1"] = Peer{DeviceID: "discovered-1", Relation: DiscoveredState, Online: true}

	engine.handleOffline("discovered-1")

	if _, ok := engine.peers["discovered-1"]; ok {
		t.Fatal("non-friend must be removed after an offline event")
	}
}

func containsIP(values []net.IP, want string) bool {
	for _, value := range values {
		if value.String() == want {
			return true
		}
	}
	return false
}
