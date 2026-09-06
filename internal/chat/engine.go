package chat

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"math"
	"mime"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type Engine struct {
	mu                      sync.RWMutex
	profile                 Profile
	identity                Identity
	listener                net.Listener
	discoveryTCP            net.Listener
	udp                     *net.UDPConn
	stop                    chan struct{}
	done                    chan struct{}
	peers                   map[string]Peer
	incoming                map[string]*incomingFile
	pendingIncoming         map[string]*pendingIncomingOffer
	outgoing                map[string]*outgoingTransfer
	preparing               map[string]*preparingAttachment
	sharedTransfers         map[string]*sharedTransferSession
	lastScan                time.Time
	lastErr                 string
	started                 bool
	serviceStopped          bool
	attachmentMigration     bool
	friendRestoreAt         map[string]time.Time
	discoveryMisses         map[string]int
	discoveryPresenceAt     map[string]int64
	locallyHiddenFriends    map[string]struct{}
	friendRemovalSyncAt     map[string]time.Time
	friendRemovalSyncMu     sync.Mutex
	transferMetricsMu       sync.Mutex
	transferMetrics         map[string]transferMetric
	transferLastBytes       map[string]int64
	transferTuning          map[string]transferTuning
	presenceMu              sync.Mutex
	discoveryScanMu         sync.Mutex
	discoveryMu             sync.Mutex
	activeDiscoveryIDs      map[string]struct{}
	activeDiscoverySeen     map[string]struct{}
	sharedThumbnailProvider func(root, relativePath string) (encoded, mimeType string, err error)
	sharedFoldersProvider   func() ([]SharedFolder, error)
}

type transferMetric struct {
	startedAt     time.Time
	startedBytes  int64
	lastAt        time.Time
	lastBytes     int64
	peakSpeed     float64
	smoothedSpeed float64
	speedSampleAt time.Time
}

type transferTuning struct {
	chunkSize  int
	windowSize int
	binary     bool
}

type binaryFileFrameHeader struct {
	WindowID   uint32
	StartChunk uint32
	ChunkCount uint32
	ChunkSize  uint32
	PayloadLen uint64
}

type wireReader struct {
	reader *bufio.Reader
}

func newWireReader(reader io.Reader) *wireReader {
	return &wireReader{reader: bufio.NewReaderSize(reader, 1024*1024)}
}

func (r *wireReader) Decode(message *wireMessage) error {
	line, err := r.reader.ReadBytes('\n')
	if err != nil {
		return err
	}
	if len(line) > maxWireJSONFrameSize {
		return fmt.Errorf("控制帧过大")
	}
	return json.Unmarshal(line, message)
}

// SetSharedThumbnailProvider lets the desktop service supply a persistent
// cache for thumbnails served to remote friends. The protocol layer remains
// independent from the service package; without a provider it keeps the
// legacy on-demand generation path.
func (e *Engine) SetSharedThumbnailProvider(provider func(root, relativePath string) (encoded, mimeType string, err error)) {
	e.mu.Lock()
	e.sharedThumbnailProvider = provider
	e.mu.Unlock()
}

// SetSharedFoldersProvider lets the desktop service attach cached statistics
// to the authenticated folder-list response without making the chat package
// depend on the service package.
func (e *Engine) SetSharedFoldersProvider(provider func() ([]SharedFolder, error)) {
	e.mu.Lock()
	e.sharedFoldersProvider = provider
	e.mu.Unlock()
}

func (e *Engine) SetAttachmentMigrationActive(active bool) {
	e.mu.Lock()
	e.attachmentMigration = active
	e.mu.Unlock()
}

func (e *Engine) IsAttachmentMigrationActive() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.attachmentMigration
}

func (e *Engine) CancelIncomingForPeer(peerDeviceID string) {
	e.mu.RLock()
	attachmentIDs := make([]string, 0)
	for attachmentID, transfer := range e.incoming {
		if transfer.senderID == peerDeviceID {
			attachmentIDs = append(attachmentIDs, attachmentID)
		}
	}
	for attachmentID, offer := range e.pendingIncoming {
		if offer.senderID == peerDeviceID {
			attachmentIDs = append(attachmentIDs, attachmentID)
		}
	}
	for attachmentID, transfer := range e.outgoing {
		if transfer.peerID == peerDeviceID {
			attachmentIDs = append(attachmentIDs, attachmentID)
		}
	}
	e.mu.RUnlock()
	for _, attachmentID := range attachmentIDs {
		_ = e.CancelAttachment(attachmentID)
	}
}

type wireSession struct {
	conn      net.Conn
	writeMu   sync.Mutex
	closeOnce sync.Once
	stateMu   sync.RWMutex
	canceled  bool
}

func newWireSession(conn net.Conn) *wireSession { return &wireSession{conn: conn} }

func (s *wireSession) write(message wireMessage) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return writeWire(s.conn, message)
}

// writeFileWindow streams one negotiated window while holding the session
// write lock. The receiver can process each JSON frame as it arrives, while
// the buffered writer reduces TLS write and encoder overhead without retaining
// the whole window in memory.
func (s *wireSession) writeFileWindow(file *os.File, attachmentID string, windowID, startIndex, chunkSize, windowSize int) (chunks int, bytes int64, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	writer := bufio.NewWriterSize(s.conn, 1024*1024)
	encoder := json.NewEncoder(writer)
	if err := encoder.Encode(wireMessage{Type: "file_window", AttachmentID: attachmentID, WindowID: windowID, ChunkSize: chunkSize, WindowSize: windowSize, TransferMode: jsonWindowTransferMode}); err != nil {
		return 0, 0, err
	}
	pooled := fileChunkBufferPool.Get().([]byte)
	defer fileChunkBufferPool.Put(pooled)
	buffer := pooled[:chunkSize]
	for index := 0; index < windowSize; index++ {
		if s.isCanceled() {
			return chunks, bytes, errAttachmentCanceled
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			payload := base64.StdEncoding.EncodeToString(buffer[:n])
			if err := encoder.Encode(wireMessage{Type: "file_chunk", AttachmentID: attachmentID, ChunkIndex: startIndex + index, Payload: payload}); err != nil {
				return chunks, bytes, err
			}
			chunks++
			bytes += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return chunks, bytes, readErr
		}
	}
	if err := writer.Flush(); err != nil {
		return chunks, bytes, err
	}
	return chunks, bytes, nil
}

func (s *wireSession) writeBinaryFileWindow(file *os.File, attachmentID string, windowID, startIndex, chunkSize, windowSize int, remaining int64) (chunks int, bytes int64, err error) {
	return s.writeBinaryFileWindowWithAckTarget(file, attachmentID, windowID, startIndex, chunkSize, windowSize, remaining, 0)
}

func (s *wireSession) writeBinaryFileWindowWithAckTarget(file *os.File, attachmentID string, windowID, startIndex, chunkSize, windowSize int, remaining, ackTargetBytes int64) (chunks int, bytes int64, err error) {
	if remaining <= 0 {
		return 0, 0, nil
	}
	windowBytes := int64(chunkSize) * int64(windowSize)
	if remaining < windowBytes {
		windowBytes = remaining
	}
	chunkCount := int((windowBytes + int64(chunkSize) - 1) / int64(chunkSize))
	header := binaryFileFrameHeader{WindowID: uint32(windowID), StartChunk: uint32(startIndex), ChunkCount: uint32(chunkCount), ChunkSize: uint32(chunkSize), PayloadLen: uint64(windowBytes)}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.isCanceled() {
		return 0, 0, errAttachmentCanceled
	}
	writer := bufio.NewWriterSize(s.conn, 1024*1024)
	if err := json.NewEncoder(writer).Encode(wireMessage{Type: "file_window", AttachmentID: attachmentID, WindowID: windowID, ChunkIndex: startIndex, ChunkSize: chunkSize, WindowSize: chunkCount, WindowBytes: windowBytes, AckTargetBytes: ackTargetBytes, TransferMode: binaryTransferMode}); err != nil {
		return 0, 0, err
	}
	if err := writeBinaryFileFrameHeader(writer, header); err != nil {
		return 0, 0, err
	}
	written, copyErr := io.CopyN(writer, file, windowBytes)
	if copyErr != nil {
		return 0, written, copyErr
	}
	if err := writer.Flush(); err != nil {
		return 0, written, err
	}
	return chunkCount, written, nil
}

func (s *wireSession) cancel(attachmentID string) {
	s.stateMu.Lock()
	if s.canceled {
		s.stateMu.Unlock()
		return
	}
	s.canceled = true
	s.stateMu.Unlock()
	if s.writeMu.TryLock() {
		_ = writeWire(s.conn, wireMessage{Type: "file_cancel", AttachmentID: attachmentID, Status: "canceled"})
		s.writeMu.Unlock()
	}
	// Closing the connection interrupts a large in-flight binary window. The
	// receiver cleanup path removes its partial file when the cancel frame could
	// not be written because another writer held the session lock.
	s.close()
}

func (s *wireSession) isCanceled() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.canceled
}

func (s *wireSession) close() { s.closeOnce.Do(func() { _ = s.conn.Close() }) }

type pendingIncomingOffer struct {
	attachment          Attachment
	message             Message
	senderID            string
	session             *wireSession
	createdAt           time.Time
	windowed            bool
	binary              bool
	chunkSize           int
	windowSize          int
	windowBytes         int64
	parallel            bool
	transferToken       string
	parallelStreamCount int
}

type incomingFile struct {
	file                *os.File
	writer              *bufio.Writer
	tempPath            string
	attachmentID        string
	messageID           string
	senderID            string
	fileName            string
	mimeType            string
	expected            int64
	received            int64
	lastProgress        int64
	sha256              string
	digest              hash.Hash
	targetPath          string
	session             *wireSession
	windowed            bool
	windowID            int
	nextWindowID        int
	windowSize          int
	windowChunks        int
	windowBytes         int64
	binaryAckBytes      int64
	binaryAckWindows    int
	binaryAckTarget     int64
	binaryLastAckAt     time.Time
	nextChunk           int
	chunkSize           int
	binary              bool
	binaryPending       bool
	expectedWindowBytes int64
	parallel            bool
	transferToken       string
	parallelStreamCount int
	parallelMu          sync.Mutex
	parallelRanges      map[int]*parallelRange
	parallelSessions    map[int]*wireSession
	parallelWritten     int64
	parallelAcked       int64
}

type parallelRange struct {
	streamID     int
	offset       int64
	length       int64
	received     int64
	acknowledged int64
	nextChunk    int
	lastAckAt    time.Time
	lastAckBytes int64
	completed    bool
}

type outgoingTransfer struct {
	message   Message
	peerID    string
	session   *wireSession
	createdAt time.Time
	dataMu    sync.Mutex
	data      map[int]*wireSession
}

type preparingAttachment struct {
	cancel   chan struct{}
	canceled bool
}

var (
	errAttachmentCanceled = errors.New("attachment transfer canceled")
	errAttachmentRejected = errors.New("attachment transfer rejected")
)

const discoveryMissThreshold = 3

// A discovery scan is only a sampling window. Keep a peer with a recent
// announce visible while one or more scan responses are lost; explicit
// offline/withdraw packets still remove it immediately. The miss threshold
// is applied after this lease expires, so a healthy peer does not flicker.
// Android sends the same heartbeat every 30 seconds. Keep a few heartbeat
// intervals in the lease so scheduling jitter or a single lost broadcast
// cannot make the discovery row blink.
const discoveryLeaseDuration = 90 * time.Second

const discoveryPresencePrefix = "presence:"

const (
	fileWindowCapability     = "file-window-v2"
	fileStreamCapability     = "file-stream-v3"
	fileParallelCapability   = "file-stream-v4"
	binaryTransferMode       = "binary-window"
	parallelBinaryMode       = "parallel-binary"
	jsonWindowTransferMode   = "json-window"
	legacyTransferMode       = "legacy-chunk"
	defaultTransferChunkSize = 256 * 1024
	minTransferChunkSize     = 256 * 1024
	mediumTransferChunkSize  = 512 * 1024
	maxTransferChunkSize     = 1024 * 1024
	defaultBinaryChunkSize   = mediumTransferChunkSize
	binaryInitialWindow      = 16
	initialTransferWindow    = 4
	minTransferWindow        = 1
	// Window growth is bounded by protocol safety, while writeFileWindow keeps
	// memory bounded with a streaming buffer instead of retaining a full window.
	maxTransferWindow         = 256
	minInFlightBytes          = 4 * 1024 * 1024
	initialInFlightBytes      = 16 * 1024 * 1024
	maxInFlightBytes          = 128 * 1024 * 1024
	initialBinaryAckBytes     = 8 * 1024 * 1024
	maxBinaryAckBytes         = 64 * 1024 * 1024
	binaryAckInterval         = 100 * time.Millisecond
	binaryFileFrameHeaderSize = 32
	binaryFileFrameVersion    = 1
	maxBinaryFileFramePayload = 256 * 1024 * 1024
	maxWireJSONFrameSize      = 16 * 1024 * 1024
	parallelInitialStreams    = 1
	parallelMaxStreams        = 4
	parallelChunkSize         = 512 * 1024
	parallelAckBytes          = 16 * 1024 * 1024
	parallelAckInterval       = 100 * time.Millisecond
	parallelStreamThreshold   = 64 * 1024 * 1024
	parallelInitialInFlight   = 32 * 1024 * 1024
	parallelMaxInFlight       = 128 * 1024 * 1024
	parallelProgressInterval  = 250 * time.Millisecond
)

var fileChunkBufferPool = sync.Pool{New: func() any { return make([]byte, maxTransferChunkSize) }}
var parallelFrameBufferPool = sync.Pool{New: func() any { return make([]byte, 4*1024*1024) }}

var binaryFileFrameMagic = [4]byte{'F', 'Q', 'F', '3'}

func validTransferChunkSize(size int) bool {
	return size == minTransferChunkSize || size == mediumTransferChunkSize || size == maxTransferChunkSize
}

func parallelStreamCount(fileSize int64) int {
	if fileSize < parallelStreamThreshold {
		return parallelInitialStreams
	}
	return parallelMaxStreams
}

func parallelRangeFor(fileSize int64, streamID, streamCount int) (offset, length int64, ok bool) {
	if fileSize <= 0 || streamCount <= 0 || streamID < 0 || streamID >= streamCount {
		return 0, 0, false
	}
	base := fileSize / int64(streamCount)
	start := int64(streamID) * base
	end := start + base
	if streamID == streamCount-1 {
		end = fileSize
	}
	return start, end - start, end > start
}

func writeBinaryFileFrameHeader(writer io.Writer, header binaryFileFrameHeader) error {
	var encoded [binaryFileFrameHeaderSize]byte
	copy(encoded[:4], binaryFileFrameMagic[:])
	encoded[4] = binaryFileFrameVersion
	binary.BigEndian.PutUint16(encoded[6:8], binaryFileFrameHeaderSize)
	binary.BigEndian.PutUint32(encoded[8:12], header.WindowID)
	binary.BigEndian.PutUint32(encoded[12:16], header.StartChunk)
	binary.BigEndian.PutUint32(encoded[16:20], header.ChunkCount)
	binary.BigEndian.PutUint32(encoded[20:24], header.ChunkSize)
	binary.BigEndian.PutUint64(encoded[24:32], header.PayloadLen)
	_, err := writer.Write(encoded[:])
	return err
}

func readBinaryFileFrameHeader(reader io.Reader) (binaryFileFrameHeader, error) {
	var encoded [binaryFileFrameHeaderSize]byte
	if _, err := io.ReadFull(reader, encoded[:]); err != nil {
		return binaryFileFrameHeader{}, err
	}
	if string(encoded[:4]) != string(binaryFileFrameMagic[:]) || encoded[4] != binaryFileFrameVersion || binary.BigEndian.Uint16(encoded[6:8]) != binaryFileFrameHeaderSize {
		return binaryFileFrameHeader{}, fmt.Errorf("二进制文件帧头无效")
	}
	header := binaryFileFrameHeader{
		WindowID:   binary.BigEndian.Uint32(encoded[8:12]),
		StartChunk: binary.BigEndian.Uint32(encoded[12:16]),
		ChunkCount: binary.BigEndian.Uint32(encoded[16:20]),
		ChunkSize:  binary.BigEndian.Uint32(encoded[20:24]),
		PayloadLen: binary.BigEndian.Uint64(encoded[24:32]),
	}
	if header.ChunkCount == 0 || header.PayloadLen == 0 || header.PayloadLen > maxBinaryFileFramePayload {
		return binaryFileFrameHeader{}, fmt.Errorf("二进制文件帧大小无效")
	}
	return header, nil
}

func NewEngine() *Engine {
	return &Engine{peers: make(map[string]Peer), incoming: make(map[string]*incomingFile), pendingIncoming: make(map[string]*pendingIncomingOffer), outgoing: make(map[string]*outgoingTransfer), preparing: make(map[string]*preparingAttachment), sharedTransfers: make(map[string]*sharedTransferSession), friendRestoreAt: make(map[string]time.Time), discoveryMisses: make(map[string]int), discoveryPresenceAt: make(map[string]int64), locallyHiddenFriends: make(map[string]struct{}), friendRemovalSyncAt: make(map[string]time.Time), transferMetrics: make(map[string]transferMetric), transferLastBytes: make(map[string]int64), transferTuning: make(map[string]transferTuning)}
}

func configureTCPConnection(conn net.Conn) {
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return
	}
	tcp, ok := tlsConn.NetConn().(*net.TCPConn)
	if !ok {
		return
	}
	_ = tcp.SetNoDelay(true)
	_ = tcp.SetReadBuffer(8 * 1024 * 1024)
	_ = tcp.SetWriteBuffer(8 * 1024 * 1024)
}

func tuneTCPBuffers(conn net.Conn, inFlightBytes int64) {
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return
	}
	tcp, ok := tlsConn.NetConn().(*net.TCPConn)
	if !ok {
		return
	}
	if inFlightBytes < 4*1024*1024 {
		inFlightBytes = 4 * 1024 * 1024
	}
	buffer := inFlightBytes * 2
	if buffer > 64*1024*1024 {
		buffer = 64 * 1024 * 1024
	}
	_ = tcp.SetReadBuffer(int(buffer))
	_ = tcp.SetWriteBuffer(int(buffer))
}

func normalizeTransferTuning(value transferTuning) transferTuning {
	if !validTransferChunkSize(value.chunkSize) {
		value.chunkSize = defaultTransferChunkSize
	}
	if value.windowSize < minTransferWindow || value.windowSize > maxTransferWindow {
		value.windowSize = initialTransferWindow
	}
	return value
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func nextTransferChunkSize(size int) int {
	switch size {
	case minTransferChunkSize:
		return mediumTransferChunkSize
	case mediumTransferChunkSize:
		return maxTransferChunkSize
	default:
		return maxTransferChunkSize
	}
}

func adjustTransferTuning(current transferTuning, ackLatency time.Duration, diskWriteMs int64, throughput, previousThroughput float64, allowMediumChunk bool) (transferTuning, string, string) {
	next := normalizeTransferTuning(current)
	if ackLatency > 150*time.Millisecond || diskWriteMs > 300 || (previousThroughput > 0 && throughput < previousThroughput*0.70) {
		if next.windowSize > minTransferWindow {
			next.windowSize = maxInt(minTransferWindow, next.windowSize/2)
		}
		if (ackLatency > 300*time.Millisecond || diskWriteMs > 200) && next.chunkSize > minTransferChunkSize {
			if allowMediumChunk {
				next.chunkSize = maxInt(minTransferChunkSize, next.chunkSize/2)
			} else {
				next.chunkSize = minTransferChunkSize
			}
		}
		return next, "backing_off", "确认延迟升高或窗口吞吐下降，降低发送窗口"
	}
	if ackLatency <= 75*time.Millisecond && diskWriteMs <= 200 && (previousThroughput == 0 || throughput >= previousThroughput*0.85) {
		if next.chunkSize < maxTransferChunkSize && next.windowSize >= 16 {
			previousBytes := next.chunkSize * next.windowSize
			if allowMediumChunk {
				next.chunkSize = nextTransferChunkSize(next.chunkSize)
			} else {
				next.chunkSize = maxTransferChunkSize
			}
			next.windowSize = maxInt(initialTransferWindow, previousBytes/next.chunkSize)
			return next, "accelerating", "传输稳定，增大分块以降低协议开销"
		}
		if next.windowSize < maxTransferWindow {
			next.windowSize = minInt(maxTransferWindow, next.windowSize*2)
			return next, "accelerating", "确认延迟稳定且吞吐上升，扩大发送窗口"
		}
	}
	return next, "stable", ""
}

func (e *Engine) transferTuningForPeer(deviceID string) transferTuning {
	e.mu.RLock()
	tuning, ok := e.transferTuning[deviceID]
	e.mu.RUnlock()
	if !ok {
		return transferTuning{chunkSize: defaultTransferChunkSize, windowSize: initialTransferWindow}
	}
	return normalizeTransferTuning(tuning)
}

func (e *Engine) hasTransferTuningForPeer(deviceID string) bool {
	e.mu.RLock()
	_, ok := e.transferTuning[deviceID]
	e.mu.RUnlock()
	return ok
}

func (e *Engine) rememberTransferTuning(deviceID string, tuning transferTuning) {
	e.mu.Lock()
	if e.transferTuning == nil {
		e.transferTuning = make(map[string]transferTuning)
	}
	e.transferTuning[deviceID] = normalizeTransferTuning(tuning)
	e.mu.Unlock()
}

func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()
	if err := EnsureDataDirs(); err != nil {
		return err
	}
	if err := EnsureDefaults(ctx, DefaultAttachmentDir()); err != nil {
		return err
	}
	profile, err := GetProfile(ctx)
	if err != nil {
		return err
	}
	if err := MigrateHiddenFriendDevices(ctx); err != nil {
		return err
	}
	if profile.AvatarPath != "" && profile.AvatarHash == "" {
		if data, avatarErr := os.ReadFile(profile.AvatarPath); avatarErr == nil && len(data) > 0 && len(data) <= 5*1024*1024 {
			profile.AvatarHash = sha256Hex(data)
			if saveErr := SaveProfile(ctx, profile); saveErr != nil {
				return saveErr
			}
		}
	}
	identity, err := LoadOrCreateIdentity(ctx)
	if err != nil {
		return err
	}
	log.Printf("设备身份初始化: status=%s, device=%s", identity.IdentityStatus, identity.DeviceID[:minInt(len(identity.DeviceID), 12)])
	if err := RecoverSendingMessages(ctx, identity.DeviceID); err != nil {
		return err
	}
	platform, osVersion := platformInfo()
	identity.Platform = platform
	identity.OSVersion = osVersion
	identity.IP = localIPv4()

	tlsCert, err := identity.TLSCertificate()
	if err != nil {
		return err
	}
	listener, err := tls.Listen("tcp", ":0", &tls.Config{Certificates: []tls.Certificate{tlsCert}, ClientAuth: tls.RequireAnyClientCert, MinVersion: tls.VersionTLS12})
	if err != nil {
		return fmt.Errorf("启动聊天端口失败: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	identity.Port = port
	udp, udpErr := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: DiscoveryPort})
	if udpErr != nil {
		_ = listener.Close()
		return fmt.Errorf("启动局域网发现失败: %w", udpErr)
	}
	discoveryTCP, tcpErr := net.Listen("tcp4", fmt.Sprintf(":%d", DiscoveryPort))
	if tcpErr != nil {
		_ = udp.Close()
		_ = listener.Close()
		return fmt.Errorf("启动 TCP 发现失败: %w", tcpErr)
	}

	e.mu.Lock()
	e.profile, e.identity, e.listener, e.discoveryTCP, e.udp = profile, identity, listener, discoveryTCP, udp
	e.serviceStopped = false
	if peers, peerErr := ListPeers(ctx, ""); peerErr == nil {
		for _, peer := range peers {
			e.peers[peer.DeviceID] = peer
		}
	}
	e.stop, e.done, e.started = make(chan struct{}), make(chan struct{}), true
	e.mu.Unlock()
	go e.acceptLoop()
	go e.discoveryTCPLoop()
	go e.discoveryLoop()
	go e.scanLoop()
	go e.livenessLoop()
	go e.probeKnownPeers()
	go e.scanNetwork(true)
	e.emit("chat:network-status", e.NetworkStatus())
	return nil
}

func (e *Engine) Stop() {
	e.mu.Lock()
	if !e.started {
		e.mu.Unlock()
		return
	}
	stop, listener, discoveryTCP, udp := e.stop, e.listener, e.discoveryTCP, e.udp
	e.started = false
	e.serviceStopped = true
	e.mu.Unlock()

	// Notify peers while the discovery socket is still available. This is an
	// immediate best-effort signal; the liveness probe remains the fallback for
	// crashes, force quits, and network failures.
	e.broadcastPresence("offline")
	close(stop)
	_ = listener.Close()
	_ = discoveryTCP.Close()
	_ = udp.Close()

	e.mu.RLock()
	attachmentIDs := make([]string, 0, len(e.incoming)+len(e.pendingIncoming)+len(e.outgoing))
	for attachmentID := range e.incoming {
		attachmentIDs = append(attachmentIDs, attachmentID)
	}
	for attachmentID := range e.pendingIncoming {
		attachmentIDs = append(attachmentIDs, attachmentID)
	}
	for attachmentID := range e.outgoing {
		attachmentIDs = append(attachmentIDs, attachmentID)
	}
	e.mu.RUnlock()
	for _, attachmentID := range attachmentIDs {
		_ = e.CancelAttachment(attachmentID)
	}
	e.mu.RLock()
	done := e.done
	e.mu.RUnlock()
	e.emit("chat:peer-updated", e.Peers())
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

func (e *Engine) discoveryTCPLoop() {
	for {
		conn, err := e.discoveryTCP.Accept()
		if err != nil {
			select {
			case <-e.stop:
				return
			default:
				time.Sleep(100 * time.Millisecond)
				continue
			}
		}
		go e.handleDiscoveryTCP(conn)
	}
}

func (e *Engine) handleDiscoveryTCP(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(800 * time.Millisecond))
	var message wireMessage
	if err := json.NewDecoder(conn).Decode(&message); err != nil || message.Type != "discover" || message.DeviceID == e.identity.DeviceID {
		return
	}
	dialect, compatible := protocolDialectForMessage(message)
	if !compatible {
		return
	}
	if scope := e.discoveryResponseScope(message.DeviceID); scope != "" {
		response := e.helloMessageForDialect("announce", dialect)
		response.RequestID = message.RequestID
		response.DiscoveryScope = scope
		_ = writeWire(conn, response)
	}
}

func (e *Engine) acceptLoop() {
	for {
		conn, err := e.listener.Accept()
		if err != nil {
			select {
			case <-e.stop:
				close(e.done)
				return
			default:
				time.Sleep(100 * time.Millisecond)
				continue
			}
		}
		go e.handleConnection(conn)
	}
}

func (e *Engine) handleConnection(raw net.Conn) {
	defer raw.Close()
	conn, ok := raw.(*tls.Conn)
	if !ok {
		return
	}
	configureTCPConnection(conn)
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	if err := conn.Handshake(); err != nil {
		return
	}
	var hello wireMessage
	decoder := newWireReader(conn)
	if err := decoder.Decode(&hello); err != nil || hello.Type != "hello" || hello.DeviceID == e.identity.DeviceID {
		_ = writeWire(conn, wireMessage{Type: "error", Status: "PROTOCOL_UNSUPPORTED"})
		return
	}
	dialect, compatible := protocolDialectForMessage(hello)
	if !compatible {
		_ = writeWire(conn, wireMessage{Type: "error", Status: "PROTOCOL_UNSUPPORTED"})
		return
	}
	// The chat port is also probed by peers that still have an old discovery
	// record. Do not let that path make a stranger visible after discovery has
	// been disabled. Friends and peers with an active friend request still need
	// a direct connection for messaging and request responses.
	isFriend := e.isFriend(hello.DeviceID)
	removedFriend, removalErr := IsFriendRemoved(context.Background(), hello.DeviceID)
	if removalErr == nil && removedFriend && !isFriend {
		_, trustedPublicKey, trustedCertFP, _, _ := FriendRemovalInfo(context.Background(), hello.DeviceID)
		if trustedPublicKey != "" && !strings.EqualFold(trustedPublicKey, hello.PublicKey) {
			_ = writeWire(conn, wireMessage{Type: "error", Status: "DEVICE_KEY_CHANGED"})
			return
		}
		if trustedCertFP != "" && !strings.EqualFold(trustedCertFP, hello.CertFP) {
			_ = writeWire(conn, wireMessage{Type: "error", Status: "DEVICE_CERT_CHANGED"})
			return
		}
		// A probe is not an explicit re-add request, so reject it immediately.
		// For a normal connection we continue to hello_ack: handleWire can then
		// reject ordinary messages/files while still allowing a new
		// friend_request to be created after the old relationship was removed.
		if hello.Probe {
			_ = writeWire(conn, wireMessage{Type: "error", Status: "FRIENDSHIP_REQUIRED"})
			return
		}
	}
	if !(removalErr == nil && removedFriend && !isFriend) && !isFriend && !e.hasPendingFriendRequest(hello.DeviceID) && !e.Profile().Discoverable {
		_ = writeWire(conn, wireMessage{Type: "error", Status: "DISCOVERY_DISABLED"})
		return
	}
	existing, existingErr := e.peer(hello.DeviceID)
	if hello.Probe && existingErr != nil && !isFriend {
		_ = writeWire(conn, wireMessage{Type: "error", Status: "PEER_NOT_FOUND"})
		return
	}
	wasOnline := false
	previousAvatarHash := ""
	previousAvatarVersion := int64(0)
	if existing, existingErr := e.peer(hello.DeviceID); existingErr == nil {
		wasOnline = existing.Online
		previousAvatarHash = existing.AvatarHash
		previousAvatarVersion = existing.AvatarVersion
	}
	discoveryVisible := false
	if existingErr == nil {
		discoveryVisible = existing.DiscoveryVisible
	}
	if err := e.upsertWirePeerWithOptions(hello, discoveryVisible); err != nil {
		_ = writeWire(conn, wireMessage{Type: "error", Status: err.Error()})
		return
	}
	peer, peerErr := e.peer(hello.DeviceID)
	if peerErr != nil {
		_ = writeWire(conn, wireMessage{Type: "error", Status: peerErr.Error()})
		return
	}
	avatarChanged := peer.AvatarHash != previousAvatarHash || peer.AvatarVersion != previousAvatarVersion
	// An incoming chat/health-probe connection must not change discoveryVisible.
	// That field belongs to the current discovery scan. Clearing it here made a
	// visible stranger disappear whenever it probed this desktop, then reappear
	// on the next scan.
	if !hello.Probe || !wasOnline || avatarChanged {
		e.emit("chat:peer-updated", e.Peers())
	}
	if verifyErr := verifyPeerCertificate(conn, peer); verifyErr != nil {
		_ = writeWire(conn, wireMessage{Type: "error", Status: verifyErr.Error()})
		return
	}
	e.touchPeer(hello.DeviceID)
	ack := e.helloMessageForDialect("hello_ack", dialect)
	ack.FriendshipState, ack.RelationshipVersion = e.friendshipStateForPeer(hello.DeviceID)
	// Include the current avatar in the authenticated acknowledgement for
	// trusted friends. This makes a desktop avatar change immediately visible
	// to Android on the next connection, while the explicit avatar_request
	// remains the fallback for larger avatars and older peers.
	if peer.Relation == PeerRelation {
		if avatarData, avatarMime := e.avatarPayloadForWire(); len(avatarData) <= 1_500_000 && avatarData != "" {
			ack.AvatarData = avatarData
			ack.AvatarMime = avatarMime
		}
	}
	_ = writeWire(conn, ack)
	session := newWireSession(conn)
	defer e.cleanupAttachmentSession(session)
	_ = conn.SetDeadline(time.Time{})
	for {
		if transfer := e.pendingBinaryTransfer(session); transfer != nil {
			if err := e.receiveBinaryFileWindow(decoder, transfer); err != nil {
				return
			}
			continue
		}
		var message wireMessage
		if err := decoder.Decode(&message); err != nil {
			return
		}
		if message.Type == "file_stream_join" {
			e.receiveParallelStream(decoder, conn, hello, message)
			return
		}
		e.handleWire(conn, hello, message, session)
	}
}

func (e *Engine) pendingBinaryTransfer(session *wireSession) *incomingFile {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, transfer := range e.incoming {
		if transfer.session == session && transfer.binary && transfer.binaryPending {
			return transfer
		}
	}
	return nil
}

func (e *Engine) failBinaryFileWindow(transfer *incomingFile, reason string, err error) error {
	messageID, attachmentID, expected, received := transfer.messageID, transfer.attachmentID, transfer.expected, transfer.received
	session := transfer.session
	e.failIncomingFile(attachmentID, reason)
	_ = session.write(wireMessage{Type: "file_progress", MessageID: messageID, AttachmentID: attachmentID, FileSize: expected, Transferred: received, Status: "failed", Reason: reason, TransferMode: binaryTransferMode})
	return err
}

func (e *Engine) receiveBinaryFileWindow(reader *wireReader, transfer *incomingFile) error {
	header, err := readBinaryFileFrameHeader(reader.reader)
	if err != nil {
		return e.failBinaryFileWindow(transfer, "INVALID_BINARY_FRAME", err)
	}
	payloadLen := int64(header.PayloadLen)
	chunkCount := int(header.ChunkCount)
	chunkSize := int(header.ChunkSize)
	minPayloadLen := int64(chunkCount-1)*int64(chunkSize) + 1
	valid := int(header.WindowID) == transfer.windowID && int(header.StartChunk) == transfer.nextChunk && chunkCount == transfer.windowSize && validTransferChunkSize(chunkSize) && chunkSize == transfer.chunkSize && payloadLen >= minPayloadLen && payloadLen <= int64(chunkCount)*int64(chunkSize) && payloadLen == transfer.expectedWindowBytes && payloadLen <= transfer.expected-transfer.received
	if !valid {
		return e.failBinaryFileWindow(transfer, "INVALID_BINARY_FRAME", fmt.Errorf("二进制文件窗口参数无效"))
	}

	pooled := fileChunkBufferPool.Get().([]byte)
	defer fileChunkBufferPool.Put(pooled)
	remaining := payloadLen
	var diskWriteDuration time.Duration
	for remaining > 0 {
		readSize := int64(len(pooled))
		if remaining < readSize {
			readSize = remaining
		}
		data := pooled[:int(readSize)]
		if _, err := io.ReadFull(reader.reader, data); err != nil {
			return e.failBinaryFileWindow(transfer, "TRUNCATED_BINARY_FRAME", err)
		}
		writeStarted := time.Now()
		written, writeErr := transfer.writer.Write(data)
		diskWriteDuration += time.Since(writeStarted)
		if writeErr != nil || written != len(data) {
			if writeErr == nil {
				writeErr = io.ErrShortWrite
			}
			return e.failBinaryFileWindow(transfer, "INSUFFICIENT_STORAGE", writeErr)
		}
		if transfer.digest != nil {
			_, _ = transfer.digest.Write(data)
		}
		transfer.received += int64(len(data))
		if transfer.received-transfer.lastProgress >= 4*1024*1024 || transfer.received >= transfer.expected {
			e.emitTransferProgress(transfer.messageID, transfer.attachmentID, transfer.senderID, transfer.received, transfer.expected, "receive", "transferring", transferProgressOptions{chunkSize: chunkSize, windowSize: chunkCount, windowBytes: payloadLen, transferMode: binaryTransferMode, transport: "TLS/TCP", protocol: fmt.Sprintf("%s/%d", ProtocolName, ProtocolMajor)})
			transfer.lastProgress = transfer.received
		}
		remaining -= int64(len(data))
	}
	transfer.nextChunk += chunkCount
	transfer.windowChunks = chunkCount
	transfer.windowBytes = payloadLen
	transfer.binaryPending = false
	transfer.binaryAckBytes += payloadLen
	transfer.binaryAckWindows++
	diskWriteMs := diskWriteDuration.Milliseconds()
	options := transferProgressOptions{chunkSize: chunkSize, windowSize: chunkCount, windowBytes: payloadLen, ackTargetBytes: transfer.binaryAckTarget, diskWriteMs: diskWriteMs, transferMode: binaryTransferMode, transport: "TLS/TCP", protocol: fmt.Sprintf("%s/%d", ProtocolName, ProtocolMajor)}
	e.emitTransferProgress(transfer.messageID, transfer.attachmentID, transfer.senderID, transfer.received, transfer.expected, "receive", "transferring", options)
	transfer.lastProgress = transfer.received
	ackDue := transfer.binaryAckTarget <= 0 || transfer.binaryAckBytes >= transfer.binaryAckTarget || transfer.received >= transfer.expected || time.Since(transfer.binaryLastAckAt) >= binaryAckInterval
	if ackDue {
		flushStarted := time.Now()
		if err := transfer.writer.Flush(); err != nil {
			return e.failBinaryFileWindow(transfer, "INSUFFICIENT_STORAGE", err)
		}
		diskWriteDuration += time.Since(flushStarted)
		diskWriteMs = diskWriteDuration.Milliseconds()
		ackBytes := transfer.binaryAckBytes
		ackWindows := transfer.binaryAckWindows
		ack := wireMessage{Type: "file_progress", MessageID: transfer.messageID, AttachmentID: transfer.attachmentID, FileSize: transfer.expected, Transferred: transfer.received, WindowID: transfer.windowID, WindowBytes: ackBytes, ChunkSize: chunkSize, WindowSize: ackWindows, DiskWriteMs: diskWriteMs, TransferMode: binaryTransferMode, Status: "receiving", AckCumulative: transfer.binaryAckTarget > 0}
		if err := transfer.session.write(ack); err != nil {
			return err
		}
		transfer.binaryAckBytes = 0
		transfer.binaryAckWindows = 0
		transfer.binaryLastAckAt = time.Now()
	}
	transfer.windowChunks = 0
	transfer.windowBytes = 0
	return nil
}

func (e *Engine) parallelTransferForJoin(message wireMessage, senderID string) *incomingFile {
	e.mu.RLock()
	defer e.mu.RUnlock()
	transfer := e.incoming[message.AttachmentID]
	if transfer == nil || !transfer.parallel || transfer.senderID != senderID || transfer.transferToken == "" || transfer.transferToken != message.TransferToken {
		return nil
	}
	return transfer
}

func (e *Engine) receiveParallelStream(reader *wireReader, conn net.Conn, hello wireMessage, join wireMessage) {
	transfer := e.parallelTransferForJoin(join, hello.DeviceID)
	dataSession := newWireSession(conn)
	expectedOffset, expectedLength, expectedRangeOK := int64(0), int64(0), false
	if transfer != nil {
		expectedOffset, expectedLength, expectedRangeOK = parallelRangeFor(transfer.expected, join.StreamID, join.StreamCount)
	}
	if transfer == nil || join.StreamCount < parallelInitialStreams || join.StreamCount > parallelMaxStreams || (transfer.parallelStreamCount > 0 && join.StreamCount != transfer.parallelStreamCount) || join.StreamID < 0 || join.StreamID >= join.StreamCount || !expectedRangeOK || join.StreamOffset != expectedOffset || join.StreamLength != expectedLength || join.StreamOffset < 0 || join.StreamLength <= 0 || join.StreamOffset+join.StreamLength > transfer.expected {
		_ = writeWire(conn, wireMessage{Type: "file_stream_join_ack", AttachmentID: join.AttachmentID, TransferToken: join.TransferToken, StreamID: join.StreamID, Status: "failed", Reason: "INVALID_STREAM_JOIN"})
		return
	}
	if !validTransferChunkSize(join.ChunkSize) || join.ChunkSize < parallelChunkSize {
		_ = writeWire(conn, wireMessage{Type: "file_stream_join_ack", AttachmentID: join.AttachmentID, TransferToken: join.TransferToken, StreamID: join.StreamID, Status: "failed", Reason: "INVALID_STREAM_CHUNK"})
		return
	}
	transfer.parallelMu.Lock()
	if _, exists := transfer.parallelRanges[join.StreamID]; exists || len(transfer.parallelRanges) >= join.StreamCount {
		transfer.parallelMu.Unlock()
		_ = writeWire(conn, wireMessage{Type: "file_stream_join_ack", AttachmentID: join.AttachmentID, TransferToken: join.TransferToken, StreamID: join.StreamID, Status: "failed", Reason: "DUPLICATE_STREAM"})
		return
	}
	for _, existing := range transfer.parallelRanges {
		if join.StreamOffset < existing.offset+existing.length && existing.offset < join.StreamOffset+join.StreamLength {
			transfer.parallelMu.Unlock()
			_ = writeWire(conn, wireMessage{Type: "file_stream_join_ack", AttachmentID: join.AttachmentID, TransferToken: join.TransferToken, StreamID: join.StreamID, Status: "failed", Reason: "OVERLAPPING_STREAM"})
			return
		}
	}
	transfer.parallelRanges[join.StreamID] = &parallelRange{streamID: join.StreamID, offset: join.StreamOffset, length: join.StreamLength, lastAckAt: time.Now()}
	transfer.parallelSessions[join.StreamID] = dataSession
	transfer.parallelMu.Unlock()
	defer func() {
		transfer.parallelMu.Lock()
		delete(transfer.parallelSessions, join.StreamID)
		transfer.parallelMu.Unlock()
	}()
	if err := dataSession.write(wireMessage{Type: "file_stream_join_ack", AttachmentID: join.AttachmentID, TransferToken: join.TransferToken, StreamID: join.StreamID, StreamCount: join.StreamCount, Status: "accepted"}); err != nil {
		return
	}
	pooled := parallelFrameBufferPool.Get().([]byte)
	defer parallelFrameBufferPool.Put(pooled)
	for {
		header, err := readBinaryFileFrameHeader(reader.reader)
		if err != nil {
			e.failIncomingFile(join.AttachmentID, "STREAM_DISCONNECTED")
			return
		}
		transfer.parallelMu.Lock()
		state := transfer.parallelRanges[join.StreamID]
		valid := state != nil && int(header.WindowID) == join.StreamID && int(header.StartChunk) == state.nextChunk && validTransferChunkSize(int(header.ChunkSize)) && int(header.ChunkSize) == join.ChunkSize && header.ChunkCount > 0 && int64(header.ChunkCount)*int64(header.ChunkSize) >= int64(header.PayloadLen) && header.PayloadLen <= uint64(state.length-state.received) && header.PayloadLen <= uint64(len(pooled)) && header.PayloadLen <= maxBinaryFileFramePayload
		frameOffset := int64(0)
		if valid {
			frameOffset = state.received
			state.nextChunk += int(header.ChunkCount)
		}
		transfer.parallelMu.Unlock()
		if !valid {
			e.failIncomingFile(join.AttachmentID, "INVALID_PARALLEL_FRAME")
			return
		}
		payload := pooled[:int(header.PayloadLen)]
		if _, err := io.ReadFull(reader.reader, payload); err != nil {
			e.failIncomingFile(join.AttachmentID, "TRUNCATED_PARALLEL_FRAME")
			return
		}
		started := time.Now()
		written, err := transfer.file.WriteAt(payload, join.StreamOffset+frameOffset)
		diskWrite := time.Since(started)
		if err != nil || written != len(payload) {
			e.failIncomingFile(join.AttachmentID, "INSUFFICIENT_STORAGE")
			return
		}
		transfer.parallelMu.Lock()
		state = transfer.parallelRanges[join.StreamID]
		state.received += int64(header.PayloadLen)
		transfer.parallelWritten += int64(header.PayloadLen)
		transfer.received = transfer.parallelWritten
		shouldAck := state.received-state.acknowledged >= parallelAckBytes || time.Since(state.lastAckAt) >= parallelAckInterval || state.received == state.length
		if shouldAck {
			state.acknowledged = state.received
			state.lastAckAt = time.Now()
		}
		streamBytes := state.received
		ackBytes := state.received - state.lastAckBytes
		if shouldAck {
			state.lastAckBytes = state.received
		}
		streamComplete := state.received == state.length
		if streamComplete {
			state.completed = true
		}
		received := transfer.received
		shouldEmit := time.Since(transfer.binaryLastAckAt) >= parallelProgressInterval || received == transfer.expected
		if shouldEmit {
			transfer.lastProgress = received
			transfer.binaryLastAckAt = time.Now()
		}
		transfer.parallelMu.Unlock()
		if shouldEmit {
			e.emitTransferProgress(transfer.messageID, transfer.attachmentID, transfer.senderID, received, transfer.expected, "receive", "transferring", transferProgressOptions{chunkSize: join.ChunkSize, windowSize: join.StreamCount, windowBytes: int64(header.PayloadLen), diskWriteMs: diskWrite.Milliseconds(), transferMode: parallelBinaryMode, transport: "TLS/TCP", protocol: fmt.Sprintf("%s/%d", ProtocolName, ProtocolMajor)})
		}
		if shouldAck || streamComplete {
			status := "receiving"
			if streamComplete {
				status = "stream-complete"
			}
			if err := dataSession.write(wireMessage{Type: "file_stream_ack", AttachmentID: join.AttachmentID, TransferToken: join.TransferToken, StreamID: join.StreamID, StreamBytes: streamBytes, Transferred: streamBytes, WindowBytes: ackBytes, DiskWriteMs: diskWrite.Milliseconds(), Status: status, TransferMode: parallelBinaryMode}); err != nil {
				e.failIncomingFile(join.AttachmentID, "STREAM_ACK_FAILED")
				return
			}
		}
		if streamComplete {
			return
		}
	}
}

func (e *Engine) handleWire(conn net.Conn, hello wireMessage, message wireMessage, session *wireSession) {
	switch message.Type {
	case "ping":
		dialect, ok := protocolDialectForMessage(hello)
		if !ok {
			dialect = protocolDialects[0]
		}
		_ = writeWire(conn, wireMessage{Type: "pong", Protocol: dialect.Name, Major: dialect.Major, Minor: ProtocolMinor})
	case "friend_request":
		// A request is a new relationship proposal, even when an old friend
		// record still exists because the remote removal notification was lost.
		// Only an identical request_id is idempotent; an old accepted request
		// must never auto-accept or hide a new request.
		// Older clients did not always include request_id. Give those requests
		// a durable local identity before deduplication and persistence; an empty
		// primary key would make every legacy request overwrite the same row and
		// could make the request appear to disappear after a restart.
		requestID := strings.TrimSpace(message.RequestID)
		if requestID == "" {
			requestID = newID()
		}
		requests, listErr := listFriendRequestRows(context.Background(), "")
		if listErr != nil {
			if conn != nil {
				_ = writeWire(conn, wireMessage{Type: "error", Status: "REQUEST_STORAGE_UNAVAILABLE"})
			}
			return
		}
		for _, existing := range requests {
			if existing.RequestID != requestID {
				continue
			}
			if conn != nil {
				_ = writeWire(conn, wireMessage{Type: "friend_request_response", RequestID: existing.RequestID, Status: existing.Status, AcceptedAt: existing.AcceptedAt})
			}
			return
		}
		// Re-adding a friend is intentionally subject to the same privacy
		// boundary as discovering a stranger. This also prevents a stale
		// authenticated connection from bypassing the discoverable switch.
		if !e.Profile().Discoverable {
			if conn != nil {
				_ = writeWire(conn, wireMessage{Type: "error", Status: "DISCOVERY_DISABLED"})
			}
			log.Printf("好友申请被拒绝: device=%s, reason=DISCOVERY_DISABLED", hello.DeviceID)
			return
		}
		request := FriendRequest{RequestID: requestID, DeviceID: hello.DeviceID, Nickname: hello.Nickname, Message: message.Content, Status: "pending", Direction: "received", CreatedAt: nowString()}
		if err := SaveFriendRequest(context.Background(), request); err != nil {
			log.Printf("保存好友申请失败: device=%s, request=%s, error=%v", hello.DeviceID, requestID, err)
			if conn != nil {
				_ = writeWire(conn, wireMessage{Type: "error", Status: "REQUEST_STORAGE_UNAVAILABLE"})
			}
			return
		}
		// Persist first. A later peer-state update must never be able to make a
		// newly received request disappear from the next startup. Only older
		// in-flight requests are superseded; terminal history stays intact.
		if err := e.prepareNewFriendRequest(context.Background(), hello.DeviceID, request.Direction, request.RequestID); err != nil {
			log.Printf("更新旧好友申请状态失败: device=%s, error=%v", hello.DeviceID, err)
		}
		// A request received while the relationship is already active is a
		// retransmission/late packet, not a new removal cycle. Downgrading here
		// used to make an accepted friendship turn into “不是好友” after a
		// reconnect delivered an old request.
		if e.isFriend(hello.DeviceID) {
			version := e.currentRelationshipVersion(hello.DeviceID)
			if conn != nil {
				_ = writeWire(conn, wireMessage{Type: "friend_request_response", RequestID: request.RequestID, Status: "accepted", AcceptedAt: nowString(), RelationshipVersion: version})
			}
			return
		}
		e.emit("chat:friend-request", request)
		e.emit("chat:peer-updated", e.Peers())
		if conn != nil {
			_ = writeWire(conn, wireMessage{Type: "friend_request_response", RequestID: request.RequestID, Status: "pending"})
		}
		log.Printf("收到新的好友申请并已保存: device=%s, request=%s", hello.DeviceID, requestID)
	case "friend_request_response":
		status := message.Status
		request, ok := friendRequestByID(message.RequestID)
		if !ok || request.DeviceID != hello.DeviceID {
			// Responses for deleted, superseded, or historical requests must not
			// resurrect a friendship or overwrite a newer request.
			return
		}
		acceptedAt := message.AcceptedAt
		if status == "accepted" {
			if acceptedAt == "" {
				acceptedAt = nowString()
			}
			if !isActiveFriendRequest(request.Status) {
				return
			}
		} else if status == "rejected" || status == "pending" || status == "sent" || status == "queued" {
			if !isActiveFriendRequest(request.Status) {
				return
			}
		} else {
			return
		}
		if status == "accepted" {
			_ = ClearFriendRemoval(context.Background(), hello.DeviceID)
			if err := SetPeerRelation(context.Background(), hello.DeviceID, PeerRelation); err == nil {
				e.updatePeerRelation(hello.DeviceID, PeerRelation)
			}
			version := message.RelationshipVersion
			if version == "" {
				version = newID()
			}
			_ = SetPeerRelationshipVersion(context.Background(), hello.DeviceID, version)
			e.setPeerRelationshipVersion(hello.DeviceID, version)
			_ = SetPeerVisibleInFriends(context.Background(), hello.DeviceID, true)
			e.clearLocallyHiddenFriend(hello.DeviceID)
			e.setPeerVisibleInFriends(hello.DeviceID, true)
			e.setPeerFriendshipState(hello.DeviceID, "")
			_ = UpdateFriendRequestAccepted(context.Background(), request.RequestID, acceptedAt)
		} else {
			_ = UpdateFriendRequest(context.Background(), request.RequestID, status)
		}
		if status == "accepted" || status == "rejected" {
			// Resolving one request ends the shared approval cycle. This is the
			// only point where the opposite active direction is superseded.
			_ = e.supersedeActiveFriendRequestsExcept(context.Background(), hello.DeviceID, request.RequestID)
		}
		updated, _ := friendRequestByID(request.RequestID)
		if updated.RequestID == "" {
			updated = request
			updated.Status = status
			updated.AcceptedAt = acceptedAt
		}
		e.emit("chat:friend-request-updated", updated)
		e.emit("chat:peer-updated", e.Peers())
	case "friend_restore":
		removed, removedErr := IsFriendRemoved(context.Background(), hello.DeviceID)
		if removedErr != nil || removed {
			_ = writeWire(conn, wireMessage{Type: "friend_restore_ack", Status: "rejected", Reason: "FRIENDSHIP_REQUIRED"})
			return
		}
		e.mu.RLock()
		localDeviceID := e.identity.DeviceID
		e.mu.RUnlock()
		if err := verifyFriendRestore(message, hello, localDeviceID); err != nil {
			_ = writeWire(conn, wireMessage{Type: "friend_restore_ack", Status: "rejected"})
			return
		}
		if err := SetPeerRelation(context.Background(), hello.DeviceID, PeerRelation); err != nil {
			_ = writeWire(conn, wireMessage{Type: "friend_restore_ack", Status: "rejected"})
			return
		}
		e.updatePeerRelation(hello.DeviceID, PeerRelation)
		e.setPeerFriendshipState(hello.DeviceID, "")
		e.emit("chat:peer-updated", e.Peers())
		_ = writeWire(conn, wireMessage{Type: "friend_restore_ack", SourceDeviceID: localDeviceID, TargetDeviceID: hello.DeviceID, RestoreVersion: friendRestoreVersion, Status: "accepted"})
	case "friend_restore_ack":
		// Control message only; it has no UI or message side effects.
	case "friend_removed":
		// A contact removal is an authenticated, one-way relationship change.
		// Relationship state and public discovery presence are independent. If
		// a fresh public announce already made the peer visible, this control
		// frame must not hide it until the next announce (which caused a visible
		// announce/remove flicker while tombstones were being synchronized).
		if strings.TrimSpace(hello.DeviceID) == "" {
			return
		}
		knownPeer, peerErr := e.peer(hello.DeviceID)
		knownRemoval, _ := IsFriendRemoved(context.Background(), hello.DeviceID)
		if peerErr != nil || (knownPeer.Relation != PeerRelation && !knownRemoval) {
			log.Printf("忽略未知设备的解除好友关系通知: device=%s", hello.DeviceID)
			return
		}
		if !e.shouldApplyFriendRemoval(hello.DeviceID, message.RelationshipVersion) {
			return
		}
		version := message.RelationshipVersion
		if version == "" {
			version = e.currentRelationshipVersion(hello.DeviceID)
		}
		_ = MarkFriendRemovedWithVersion(context.Background(), hello.DeviceID, version, knownPeer.PublicKeyPEM, knownPeer.CertificateFingerprint)
		_ = SetPeerRelation(context.Background(), hello.DeviceID, DiscoveredState)
		e.showPeerUnlessLocallyHidden(context.Background(), hello.DeviceID)
		e.updatePeerRelation(hello.DeviceID, DiscoveredState)
		e.setPeerFriendshipState(hello.DeviceID, "removed")
		e.emit("chat:peer-updated", e.Peers())
	case "share_list_request":
		e.handleSharedListRequest(conn, hello, message)
	case "share_folders_request":
		e.handleSharedFoldersRequest(conn, hello)
	case "share_thumbnail_request":
		e.handleSharedThumbnailRequest(conn, hello, message)
	case "share_thumbnail_batch_request":
		e.handleSharedThumbnailBatchRequest(conn, hello, message)
	case "share_download_request":
		e.handleSharedDownloadRequest(conn, hello, message, session)
	case "message":
		if !e.isFriend(hello.DeviceID) {
			_ = writeWire(conn, wireMessage{Type: "error", Status: "FRIENDSHIP_REQUIRED"})
			return
		}
		conversationID, err := EnsureConversation(context.Background(), hello.DeviceID)
		if err != nil {
			return
		}
		exists, err := MessageExists(context.Background(), message.MessageID)
		if err != nil {
			return
		}
		_ = writeWire(conn, wireMessage{Type: "ack", MessageID: message.MessageID, Status: "sent"})
		if exists {
			return
		}
		if peer, peerErr := e.peer(hello.DeviceID); peerErr == nil && !peer.VisibleInFriends {
			if err := SetPeerVisibleInFriends(context.Background(), hello.DeviceID, true); err == nil {
				e.clearLocallyHiddenFriend(hello.DeviceID)
				e.setPeerVisibleInFriends(hello.DeviceID, true)
				e.emit("chat:peer-updated", e.Peers())
			}
		}
		messageRecord := Message{MessageID: message.MessageID, ConversationID: conversationID, SenderDeviceID: hello.DeviceID, Kind: message.Kind, Content: message.Content, Status: "sent", CreatedAt: nowString(), QuoteMessageID: message.QuoteMessageID, QuoteContent: message.QuoteContent, ForwardedFrom: message.ForwardedFrom}
		if err := SaveMessage(context.Background(), messageRecord); err == nil {
			_ = IncrementConversationUnread(context.Background(), conversationID)
			e.emit("chat:message", messageRecord)
		}
	case "avatar_request":
		if !e.isFriend(hello.DeviceID) {
			return
		}
		profile := e.Profile()
		if profile.AvatarPath == "" || profile.AvatarHash == "" {
			_ = writeWire(conn, wireMessage{Type: "avatar_response", DeviceID: e.identity.DeviceID})
			return
		}
		data, err := os.ReadFile(profile.AvatarPath)
		if err != nil || len(data) > 5*1024*1024 {
			return
		}
		mimeType := mime.TypeByExtension(filepath.Ext(profile.AvatarPath))
		if mimeType == "" {
			mimeType = "image/png"
		}
		_ = writeWire(conn, wireMessage{Type: "avatar_response", DeviceID: e.identity.DeviceID, AvatarHash: profile.AvatarHash, AvatarVersion: profile.AvatarVersion, AvatarMime: mimeType, AvatarData: base64.StdEncoding.EncodeToString(data)})
	case "avatar_response":
		// hello/hello_ack and the explicit response use the same validated,
		// atomic cache path. This also handles a data URI from Android.
		e.applyPeerAvatar(message, hello.DeviceID)
	case "read_receipt":
		for _, messageID := range message.MessageIDs {
			if err := UpdateMessageStatus(context.Background(), messageID, "read"); err == nil {
				e.emit("chat:message-status", map[string]any{"messageId": messageID, "status": "read"})
			}
		}
	case "file_offer":
		if !e.isFriend(hello.DeviceID) {
			_ = writeWire(conn, wireMessage{Type: "error", Status: "FRIENDSHIP_REQUIRED"})
			return
		}
		if message.FileSize < 0 {
			if hasCapability(hello.Capabilities, "storage-preflight-v1") {
				_ = sessionWrite(session, conn, wireMessage{Type: "file_offer_response", MessageID: message.MessageID, AttachmentID: message.AttachmentID, Status: "rejected", Reason: "INVALID_FILE_SIZE"})
			}
			return
		}
		conversationID, err := EnsureConversation(context.Background(), hello.DeviceID)
		if err != nil {
			return
		}
		attachmentID := message.AttachmentID
		if attachmentID == "" {
			attachmentID = newID()
		}
		if message.MessageID == "" {
			return
		}
		attachmentMime := message.MimeType
		if attachmentMime == "" {
			attachmentMime = mime.TypeByExtension(filepath.Ext(message.FileName))
		}
		if attachmentMime == "" {
			attachmentMime = "application/octet-stream"
		}
		messageAlreadyExists, existsErr := MessageExists(context.Background(), message.MessageID)
		if existsErr != nil {
			return
		}
		if messageAlreadyExists {
			existing, existingErr := GetMessage(context.Background(), message.MessageID)
			if existingErr != nil {
				return
			}
			switch existing.AttachmentStatus {
			case "canceled", "rejected", "failed":
			default:
				return
			}
		}
		thumbnailData, thumbnailMime := validThumbnail(message.ThumbnailData, message.ThumbnailMime)
		messageRecord := Message{MessageID: message.MessageID, ConversationID: conversationID, SenderDeviceID: hello.DeviceID, Kind: "file", Content: message.FileName, Status: "sent", CreatedAt: nowString(), AttachmentID: attachmentID, AttachmentName: message.FileName, AttachmentSize: message.FileSize, AttachmentMime: attachmentMime, AttachmentThumbnail: thumbnailData, AttachmentThumbnailMime: thumbnailMime, AttachmentStatus: "pending"}
		attachment := Attachment{AttachmentID: attachmentID, MessageID: message.MessageID, FileName: message.FileName, MimeType: attachmentMime, FileSize: message.FileSize, SHA256: message.SHA256, ThumbnailData: thumbnailData, ThumbnailMime: thumbnailMime, Status: "pending"}
		parallelMode := hasCapability(hello.Capabilities, fileParallelCapability) && message.TransferMode == parallelBinaryMode && message.TransferToken != ""
		windowed := hasCapability(hello.Capabilities, fileWindowCapability) || parallelMode
		binaryMode := windowed && hasCapability(hello.Capabilities, fileStreamCapability) && message.TransferMode == binaryTransferMode
		windowSize := message.WindowSize
		if windowSize < minTransferWindow || windowSize > maxTransferWindow {
			windowSize = initialTransferWindow
		}
		chunkSize := message.ChunkSize
		if !validTransferChunkSize(chunkSize) {
			chunkSize = defaultTransferChunkSize
		}
		supportsDemand := session != nil && hasCapability(hello.Capabilities, "attachment-demand-v1")
		if supportsDemand && !e.Profile().AutoSave {
			if err := SaveMessage(context.Background(), messageRecord); err != nil {
				return
			}
			if err := UpdateMessageStatus(context.Background(), messageRecord.MessageID, messageRecord.Status); err != nil {
				return
			}
			if !messageAlreadyExists {
				_ = IncrementConversationUnread(context.Background(), conversationID)
			}
			if err := SaveAttachment(context.Background(), attachment); err != nil {
				return
			}
			e.mu.Lock()
			e.pendingIncoming[attachmentID] = &pendingIncomingOffer{attachment: attachment, message: messageRecord, senderID: hello.DeviceID, session: session, createdAt: time.Now(), windowed: windowed, binary: binaryMode, parallel: parallelMode, transferToken: message.TransferToken, parallelStreamCount: message.StreamCount, chunkSize: chunkSize, windowSize: windowSize, windowBytes: message.WindowBytes}
			e.mu.Unlock()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Minute))
			_ = session.write(wireMessage{Type: "file_offer_response", MessageID: message.MessageID, AttachmentID: attachmentID, Status: "pending", Reason: "AWAITING_USER"})
			e.emit("chat:message", messageRecord)
			return
		}
		if !e.canAllocateIncoming(message.FileSize) {
			_ = sessionWrite(session, conn, wireMessage{Type: "file_offer_response", MessageID: message.MessageID, AttachmentID: attachmentID, Status: "rejected", Reason: "INSUFFICIENT_STORAGE"})
			return
		}
		messageRecord.Status, messageRecord.AttachmentStatus = "receiving", "receiving"
		attachment.Status = "receiving"
		targetPath, targetErr := AttachmentTargetPath(e.Profile().FileSavePath, hello.DeviceID, message.FileName)
		if targetErr != nil {
			_ = sessionWrite(session, conn, wireMessage{Type: "file_offer_response", MessageID: message.MessageID, AttachmentID: attachmentID, Status: "rejected", Reason: "STORAGE_UNAVAILABLE"})
			return
		}
		if err := e.beginIncomingFileWithMode(messageRecord, attachment, hello.DeviceID, session, targetPath, true, parallelMode, message.TransferToken); err != nil {
			_ = sessionWrite(session, conn, wireMessage{Type: "file_offer_response", MessageID: message.MessageID, AttachmentID: attachmentID, Status: "rejected", Reason: "STORAGE_UNAVAILABLE"})
			return
		}
		e.mu.Lock()
		if transfer := e.incoming[attachmentID]; transfer != nil {
			transfer.windowed = windowed
			transfer.binary = binaryMode
			transfer.parallel = parallelMode
			transfer.transferToken = message.TransferToken
			transfer.parallelStreamCount = message.StreamCount
			if transfer.parallelStreamCount == 0 {
				transfer.parallelStreamCount = parallelStreamCount(message.FileSize)
			}
			transfer.windowSize = windowSize
			transfer.chunkSize = chunkSize
			transfer.expectedWindowBytes = message.WindowBytes
		}
		e.mu.Unlock()
		if err := sessionWrite(session, conn, wireMessage{Type: "file_offer_response", MessageID: message.MessageID, AttachmentID: attachmentID, Status: "accepted"}); err != nil {
			e.failIncomingFile(attachmentID, "STORAGE_UNAVAILABLE")
			return
		}
		incomingMode := legacyTransferMode
		if hasCapability(hello.Capabilities, fileWindowCapability) {
			incomingMode = jsonWindowTransferMode
		}
		if parallelMode {
			incomingMode = parallelBinaryMode
		} else if hasCapability(hello.Capabilities, fileStreamCapability) && message.TransferMode == binaryTransferMode {
			incomingMode = binaryTransferMode
		}
		e.emitTransferProgress(messageRecord.MessageID, attachmentID, hello.DeviceID, 0, message.FileSize, "receive", "receiving", transferProgressOptions{chunkSize: message.ChunkSize, windowSize: message.WindowSize, windowBytes: message.WindowBytes, transferMode: incomingMode, transport: "TLS/TCP", protocol: fmt.Sprintf("%s/%d", ProtocolName, ProtocolMajor), tuningState: "probing"})
	case "file_thumbnail":
		thumbnailData, thumbnailMime := validThumbnail(message.ThumbnailData, message.ThumbnailMime)
		if thumbnailData == "" || message.AttachmentID == "" {
			return
		}
		if err := UpdateAttachmentThumbnail(context.Background(), message.AttachmentID, thumbnailData, thumbnailMime); err != nil {
			return
		}
		if record, err := GetMessage(context.Background(), message.MessageID); err == nil {
			record.AttachmentThumbnail = thumbnailData
			record.AttachmentThumbnailMime = thumbnailMime
			_ = SaveMessage(context.Background(), record)
			e.emit("chat:message", record)
		}
	case "file_window":
		e.mu.Lock()
		transfer := e.incoming[message.AttachmentID]
		binaryWindow := transfer != nil && transfer.binary && message.TransferMode == binaryTransferMode
		validMode := message.TransferMode == "" || message.TransferMode == jsonWindowTransferMode || (binaryWindow && hasCapability(hello.Capabilities, fileStreamCapability))
		validWindow := transfer != nil && transfer.senderID == hello.DeviceID && hasCapability(hello.Capabilities, fileWindowCapability) && validMode && message.WindowSize >= minTransferWindow && message.WindowSize <= maxTransferWindow && validTransferChunkSize(message.ChunkSize) && message.WindowID == transfer.nextWindowID
		if validWindow {
			transfer.windowed = true
			transfer.windowID = message.WindowID
			transfer.nextWindowID++
			transfer.windowSize = message.WindowSize
			transfer.windowChunks = 0
			transfer.windowBytes = 0
			transfer.chunkSize = message.ChunkSize
			transfer.binaryPending = binaryWindow
			transfer.expectedWindowBytes = message.WindowBytes
			if binaryWindow {
				transfer.binaryAckTarget = message.AckTargetBytes
				if transfer.binaryAckTarget < 0 {
					transfer.binaryAckTarget = 0
				}
				if transfer.binaryLastAckAt.IsZero() {
					transfer.binaryLastAckAt = time.Now()
				}
			}
		}
		e.mu.Unlock()
		if transfer != nil && !validWindow {
			e.failIncomingFile(message.AttachmentID, "INVALID_WINDOW_SEQUENCE")
			_ = sessionWrite(transfer.session, conn, wireMessage{Type: "file_progress", MessageID: transfer.messageID, AttachmentID: message.AttachmentID, FileSize: transfer.expected, Transferred: transfer.received, Status: "failed", Reason: "INVALID_WINDOW_SEQUENCE"})
		}
	case "file_chunk":
		e.mu.RLock()
		transfer := e.incoming[message.AttachmentID]
		e.mu.RUnlock()
		if transfer == nil {
			return
		}
		if message.ChunkIndex != transfer.nextChunk {
			e.failIncomingFile(message.AttachmentID, "INVALID_CHUNK_SEQUENCE")
			_ = sessionWrite(transfer.session, conn, wireMessage{Type: "file_progress", MessageID: transfer.messageID, AttachmentID: message.AttachmentID, FileSize: transfer.expected, Transferred: transfer.received, Status: "failed", Reason: "INVALID_CHUNK_SEQUENCE"})
			return
		}
		data, err := base64.StdEncoding.DecodeString(message.Payload)
		if err != nil {
			return
		}
		if transfer.windowed && int64(len(data)) > int64(transfer.chunkSize) {
			e.failIncomingFile(message.AttachmentID, "CHUNK_SIZE_EXCEEDED")
			_ = sessionWrite(transfer.session, conn, wireMessage{Type: "file_progress", MessageID: transfer.messageID, AttachmentID: message.AttachmentID, FileSize: transfer.expected, Transferred: transfer.received, Status: "failed", Reason: "CHUNK_SIZE_EXCEEDED"})
			return
		}
		if transfer.expected < 0 || transfer.received > transfer.expected || int64(len(data)) > transfer.expected-transfer.received {
			e.failIncomingFile(message.AttachmentID, "FILE_SIZE_EXCEEDED")
			_ = sessionWrite(transfer.session, conn, wireMessage{Type: "file_progress", MessageID: transfer.messageID, AttachmentID: message.AttachmentID, FileSize: transfer.expected, Transferred: transfer.received, Status: "failed", Reason: "FILE_SIZE_EXCEEDED"})
			return
		}
		if _, err := transfer.writer.Write(data); err != nil {
			e.failIncomingFile(message.AttachmentID, "INSUFFICIENT_STORAGE")
			_ = sessionWrite(transfer.session, conn, wireMessage{Type: "file_progress", MessageID: transfer.messageID, AttachmentID: message.AttachmentID, FileSize: transfer.expected, Transferred: transfer.received, Status: "failed", Reason: "INSUFFICIENT_STORAGE"})
			return
		}
		if transfer.digest != nil {
			_, _ = transfer.digest.Write(data)
		}
		transfer.received += int64(len(data))
		transfer.nextChunk++
		transfer.windowChunks++
		transfer.windowBytes += int64(len(data))
		if transfer.received-transfer.lastProgress >= 256*1024 || (transfer.expected > 0 && transfer.received >= transfer.expected) {
			mode := legacyTransferMode
			if transfer.windowed {
				mode = jsonWindowTransferMode
			}
			e.emitTransferProgress(transfer.messageID, message.AttachmentID, transfer.senderID, transfer.received, transfer.expected, "receive", "transferring", transferProgressOptions{chunkSize: transfer.chunkSize, windowSize: transfer.windowSize, windowBytes: transfer.windowBytes, transferMode: mode, transport: "TLS/TCP", protocol: fmt.Sprintf("%s/%d", ProtocolName, ProtocolMajor)})
			transfer.lastProgress = transfer.received
		}
		acknowledge := !transfer.windowed || transfer.windowChunks >= transfer.windowSize || (transfer.expected > 0 && transfer.received >= transfer.expected)
		if acknowledge {
			flushStarted := time.Now()
			if err := transfer.writer.Flush(); err != nil {
				e.failIncomingFile(message.AttachmentID, "INSUFFICIENT_STORAGE")
				_ = sessionWrite(transfer.session, conn, wireMessage{Type: "file_progress", MessageID: transfer.messageID, AttachmentID: message.AttachmentID, FileSize: transfer.expected, Transferred: transfer.received, Status: "failed", Reason: "INSUFFICIENT_STORAGE"})
				return
			}
			diskWriteMs := time.Since(flushStarted).Milliseconds()
			mode := legacyTransferMode
			if transfer.windowed {
				mode = jsonWindowTransferMode
			}
			_ = sessionWrite(transfer.session, conn, wireMessage{Type: "file_progress", MessageID: transfer.messageID, AttachmentID: message.AttachmentID, FileSize: transfer.expected, Transferred: transfer.received, WindowID: transfer.windowID, WindowBytes: transfer.windowBytes, ChunkSize: transfer.chunkSize, WindowSize: transfer.windowSize, DiskWriteMs: diskWriteMs, TransferMode: mode, Status: "receiving"})
			transfer.windowChunks = 0
			transfer.windowBytes = 0
		}
	case "file_complete":
		e.mu.RLock()
		transfer := e.incoming[message.AttachmentID]
		messageID, total := message.MessageID, message.FileSize
		transferMode := legacyTransferMode
		if transfer != nil {
			messageID, total = transfer.messageID, transfer.expected
			if transfer.parallel {
				transferMode = parallelBinaryMode
			} else if transfer.binary {
				transferMode = binaryTransferMode
			} else if transfer.windowed {
				transferMode = jsonWindowTransferMode
			}
		}
		e.mu.RUnlock()
		status := e.finishIncomingFile(message.AttachmentID)
		// finishIncomingFile removes the transfer, so use the message metadata
		// supplied by the sender for the final optional acknowledgement.
		_ = sessionWrite(session, conn, wireMessage{Type: "file_progress", MessageID: messageID, AttachmentID: message.AttachmentID, Transferred: total, FileSize: total, TransferMode: transferMode, Status: status})
	case "file_accept":
		// A receiver sends file_accept on the existing offer connection. The
		// sender side consumes it in transferFileWithDialect.
	case "file_reject", "file_cancel":
		e.cancelIncomingFromRemote(message.AttachmentID, message.Type == "file_reject")
	}
}

const attachmentSafetyMargin int64 = 16 * 1024 * 1024

func requiredAttachmentBytes(fileSize int64) int64 {
	if fileSize < 0 || fileSize > int64(^uint64(0)>>1)-attachmentSafetyMargin {
		return int64(^uint64(0) >> 1)
	}
	return fileSize + attachmentSafetyMargin
}

func formatBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	if value < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(value)/1024)
	}
	if value < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(value)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(value)/(1024*1024*1024))
}

func sessionWrite(session *wireSession, conn net.Conn, message wireMessage) error {
	if session != nil {
		return session.write(message)
	}
	return writeWire(conn, message)
}

func validThumbnail(data, mimeType string) (string, string) {
	if data == "" || !strings.HasPrefix(mimeType, "image/") {
		return "", ""
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(decoded) == 0 || len(decoded) > thumbnailMaxSize {
		return "", ""
	}
	return data, mimeType
}

func (e *Engine) canAllocateIncoming(fileSize int64) bool {
	available, err := availableDiskBytes(filepath.Join(AppDataDir(), "temp"))
	return err == nil && available >= requiredAttachmentBytes(fileSize)
}

func (e *Engine) beginIncomingFile(message Message, attachment Attachment, senderID string, session *wireSession, targetPath string, incrementUnread bool) error {
	return e.beginIncomingFileWithMode(message, attachment, senderID, session, targetPath, incrementUnread, false, "")
}

func (e *Engine) beginIncomingFileWithMode(message Message, attachment Attachment, senderID string, session *wireSession, targetPath string, incrementUnread, parallel bool, transferToken string) error {
	tempDir := filepath.Join(AppDataDir(), "temp")
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return err
	}
	tempPath := filepath.Join(tempDir, attachment.AttachmentID+".part")
	flags := os.O_CREATE | os.O_TRUNC | os.O_WRONLY
	if parallel {
		flags = os.O_CREATE | os.O_TRUNC | os.O_RDWR
	}
	file, err := os.OpenFile(tempPath, flags, 0o600)
	if err != nil {
		return err
	}
	if parallel && attachment.FileSize > 0 {
		if err := file.Truncate(attachment.FileSize); err != nil {
			_ = file.Close()
			_ = os.Remove(tempPath)
			return err
		}
	}
	attachment.LocalPath, attachment.Status = tempPath, "receiving"
	if err := SaveMessage(context.Background(), message); err != nil {
		_ = file.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if err := UpdateMessageStatus(context.Background(), message.MessageID, message.Status); err != nil {
		_ = file.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if incrementUnread {
		_ = IncrementConversationUnread(context.Background(), message.ConversationID)
	}
	if err := SaveAttachment(context.Background(), attachment); err != nil {
		_ = file.Close()
		_ = os.Remove(tempPath)
		return err
	}
	transfer := &incomingFile{file: file, tempPath: tempPath, attachmentID: attachment.AttachmentID, messageID: message.MessageID, senderID: senderID, fileName: attachment.FileName, mimeType: attachment.MimeType, expected: attachment.FileSize, sha256: attachment.SHA256, targetPath: targetPath, session: session, windowSize: 1, chunkSize: defaultTransferChunkSize, parallel: parallel, transferToken: transferToken}
	if parallel {
		transfer.parallelRanges = make(map[int]*parallelRange)
		transfer.parallelSessions = make(map[int]*wireSession)
	} else {
		transfer.writer = bufio.NewWriterSize(file, 1024*1024)
		transfer.digest = sha256.New()
	}
	e.mu.Lock()
	e.incoming[attachment.AttachmentID] = transfer
	e.mu.Unlock()
	e.emit("chat:message", message)
	e.emit("chat:attachment", message)
	return nil
}

func (e *Engine) cleanupAttachmentSession(session *wireSession) {
	if session == nil {
		return
	}
	e.mu.Lock()
	pending := make([]*pendingIncomingOffer, 0)
	for id, offer := range e.pendingIncoming {
		if offer.session == session {
			delete(e.pendingIncoming, id)
			pending = append(pending, offer)
		}
	}
	incoming := make([]*incomingFile, 0)
	for id, transfer := range e.incoming {
		if transfer.session == session {
			delete(e.incoming, id)
			incoming = append(incoming, transfer)
		}
	}
	e.mu.Unlock()
	for _, offer := range pending {
		_ = SaveAttachment(context.Background(), Attachment{AttachmentID: offer.attachment.AttachmentID, MessageID: offer.attachment.MessageID, FileName: offer.attachment.FileName, MimeType: offer.attachment.MimeType, FileSize: offer.attachment.FileSize, SHA256: offer.attachment.SHA256, ThumbnailData: offer.attachment.ThumbnailData, ThumbnailMime: offer.attachment.ThumbnailMime, Status: "canceled"})
		e.emitAttachmentStatus(offer.attachment.MessageID, "canceled", "")
	}
	for _, transfer := range incoming {
		closeParallelSessions(transfer)
		_ = transfer.file.Close()
		_ = os.Remove(transfer.tempPath)
		_ = SaveAttachment(context.Background(), Attachment{AttachmentID: transfer.attachmentID, MessageID: transfer.messageID, FileName: transfer.fileName, MimeType: transfer.mimeType, FileSize: transfer.expected, SHA256: transfer.sha256, Status: "canceled"})
		e.emitAttachmentStatus(transfer.messageID, "canceled", "")
	}
}

func (e *Engine) emitAttachmentStatus(messageID, status, localPath string) {
	if message, err := GetMessage(context.Background(), messageID); err == nil {
		_ = UpdateMessageStatus(context.Background(), messageID, status)
		message.Status = status
		message.AttachmentStatus = status
		message.AttachmentPath = localPath
		e.emit("chat:message", message)
		e.emit("chat:attachment", map[string]any{"attachmentId": message.AttachmentID, "messageId": message.MessageID, "conversationId": message.ConversationID, "status": status, "localPath": localPath})
	}
}

func (e *Engine) cancelIncomingFromRemote(attachmentID string, rejected bool) {
	e.mu.Lock()
	pending := e.pendingIncoming[attachmentID]
	delete(e.pendingIncoming, attachmentID)
	transfer := e.incoming[attachmentID]
	delete(e.incoming, attachmentID)
	e.mu.Unlock()
	status := "canceled"
	if rejected {
		status = "rejected"
	}
	if pending != nil {
		_ = SaveAttachment(context.Background(), Attachment{AttachmentID: pending.attachment.AttachmentID, MessageID: pending.attachment.MessageID, FileName: pending.attachment.FileName, MimeType: pending.attachment.MimeType, FileSize: pending.attachment.FileSize, SHA256: pending.attachment.SHA256, ThumbnailData: pending.attachment.ThumbnailData, ThumbnailMime: pending.attachment.ThumbnailMime, Status: status})
		e.emitAttachmentStatus(pending.message.MessageID, status, "")
	}
	if transfer != nil {
		received := transfer.received
		if transfer.parallel {
			transfer.parallelMu.Lock()
			received = transfer.received
			transfer.parallelMu.Unlock()
		}
		closeParallelSessions(transfer)
		_ = transfer.file.Close()
		_ = os.Remove(transfer.tempPath)
		_ = SaveAttachment(context.Background(), Attachment{AttachmentID: attachmentID, MessageID: transfer.messageID, FileName: transfer.fileName, MimeType: transfer.mimeType, FileSize: transfer.expected, SHA256: transfer.sha256, Status: status})
		e.emitAttachmentStatus(transfer.messageID, status, "")
		verified := false
		e.emitTransferProgress(transfer.messageID, attachmentID, transfer.senderID, received, transfer.expected, "receive", status, transferProgressOptions{chunkSize: transfer.chunkSize, windowSize: transfer.windowSize, windowBytes: transfer.windowBytes, verified: &verified})
	}
}

func (e *Engine) failIncomingFile(attachmentID, reason string) {
	e.mu.Lock()
	transfer := e.incoming[attachmentID]
	delete(e.incoming, attachmentID)
	e.mu.Unlock()
	if transfer == nil {
		return
	}
	received := transfer.received
	if transfer.parallel {
		transfer.parallelMu.Lock()
		received = transfer.received
		transfer.parallelMu.Unlock()
	}
	closeParallelSessions(transfer)
	if transfer.file != nil {
		_ = transfer.file.Close()
	}
	_ = os.Remove(transfer.tempPath)
	attachment, _ := GetAttachment(context.Background(), attachmentID)
	_ = SaveAttachment(context.Background(), Attachment{AttachmentID: attachmentID, MessageID: transfer.messageID, FileName: transfer.fileName, MimeType: transfer.mimeType, FileSize: transfer.expected, SHA256: transfer.sha256, ThumbnailData: attachment.ThumbnailData, ThumbnailMime: attachment.ThumbnailMime, Status: "failed"})
	_ = exec(context.Background(), `UPDATE messages SET status=? WHERE message_id=?`, "failed", transfer.messageID)
	e.emit("chat:attachment", map[string]any{"attachmentId": attachmentID, "messageId": transfer.messageID, "fileName": transfer.fileName, "status": "failed", "reason": reason})
	verified := false
	e.emitTransferProgress(transfer.messageID, attachmentID, transfer.senderID, received, transfer.expected, "receive", "failed", transferProgressOptions{chunkSize: transfer.chunkSize, windowSize: transfer.windowSize, windowBytes: transfer.windowBytes, verified: &verified})
}

func closeParallelSessions(transfer *incomingFile) {
	if transfer == nil || !transfer.parallel {
		return
	}
	transfer.parallelMu.Lock()
	sessions := make([]*wireSession, 0, len(transfer.parallelSessions))
	for id, session := range transfer.parallelSessions {
		delete(transfer.parallelSessions, id)
		sessions = append(sessions, session)
	}
	transfer.parallelMu.Unlock()
	for _, session := range sessions {
		if session != nil {
			session.close()
		}
	}
}

func (e *Engine) finishIncomingFile(attachmentID string) string {
	e.mu.Lock()
	transfer := e.incoming[attachmentID]
	delete(e.incoming, attachmentID)
	e.mu.Unlock()
	if transfer == nil {
		return "failed"
	}
	parallelValid := true
	if transfer.parallel {
		transfer.parallelMu.Lock()
		covered := int64(0)
		for _, state := range transfer.parallelRanges {
			covered += state.length
			if !state.completed || state.received != state.length {
				parallelValid = false
			}
		}
		parallelValid = parallelValid && covered == transfer.expected && transfer.parallelWritten == transfer.expected
		transfer.parallelMu.Unlock()
		if parallelValid {
			parallelValid = transfer.file.Sync() == nil
			if parallelValid {
				digest := sha256.New()
				hashFile, err := os.Open(transfer.tempPath)
				if err == nil {
					_, err = io.Copy(digest, hashFile)
					_ = hashFile.Close()
				}
				if err != nil {
					parallelValid = false
				} else {
					transfer.digest = digest
				}
			}
		}
	}
	flushErr := error(nil)
	if transfer.writer != nil {
		flushErr = transfer.writer.Flush()
	}
	closeErr := transfer.file.Close()
	valid := flushErr == nil && closeErr == nil && parallelValid && transfer.digest != nil && hex.EncodeToString(transfer.digest.Sum(nil)) == transfer.sha256 && transfer.received == transfer.expected
	status := "pending"
	localPath := transfer.tempPath
	if valid && transfer.targetPath != "" && !e.IsAttachmentMigrationActive() {
		localPath = transfer.targetPath
		if os.MkdirAll(filepath.Dir(localPath), 0o700) == nil && os.Rename(transfer.tempPath, localPath) == nil {
			status = "saved"
		} else {
			status = "failed"
			localPath = ""
			_ = os.Remove(transfer.tempPath)
		}
	}
	if !valid {
		status = "failed"
		localPath = ""
		_ = os.Remove(transfer.tempPath)
	}
	attachmentMime := transfer.mimeType
	if attachmentMime == "" {
		attachmentMime = mime.TypeByExtension(filepath.Ext(transfer.fileName))
	}
	if attachmentMime == "" {
		attachmentMime = "application/octet-stream"
	}
	attachment, _ := GetAttachment(context.Background(), attachmentID)
	_ = SaveAttachment(context.Background(), Attachment{AttachmentID: attachmentID, MessageID: transfer.messageID, FileName: transfer.fileName, MimeType: attachmentMime, FileSize: transfer.expected, SHA256: transfer.sha256, ThumbnailData: attachment.ThumbnailData, ThumbnailMime: attachment.ThumbnailMime, LocalPath: localPath, Status: status})
	messageStatus := "sent"
	if !valid {
		messageStatus = "failed"
	}
	if messageRecord, messageErr := GetMessage(context.Background(), transfer.messageID); messageErr == nil {
		messageRecord.Status = messageStatus
		messageRecord.AttachmentMime = attachmentMime
		messageRecord.AttachmentThumbnail = attachment.ThumbnailData
		messageRecord.AttachmentThumbnailMime = attachment.ThumbnailMime
		messageRecord.AttachmentStatus = status
		messageRecord.AttachmentPath = localPath
		e.emit("chat:message", messageRecord)
	}
	_ = exec(context.Background(), `UPDATE messages SET status=? WHERE message_id=?`, messageStatus, transfer.messageID)
	e.emit("chat:attachment", map[string]any{"attachmentId": attachmentID, "messageId": transfer.messageID, "fileName": transfer.fileName, "status": status, "localPath": localPath, "valid": valid})
	e.emitTransferProgress(transfer.messageID, attachmentID, transfer.senderID, transfer.received, transfer.expected, "receive", map[bool]string{true: "completed", false: "failed"}[valid], transferProgressOptions{chunkSize: transfer.chunkSize, windowSize: transfer.windowSize, windowBytes: transfer.windowBytes, verified: &valid})
	if valid {
		return "completed"
	}
	return "failed"
}

type transferProgressOptions struct {
	chunkSize           int
	windowSize          int
	windowBytes         int64
	streamCount         int
	activeStreams       int
	streamID            int
	streamOffset        int64
	streamLength        int64
	inFlightBytes       int64
	ackTargetBytes      int64
	socketWriteMs       int64
	ackWaitMs           int64
	confirmedThroughput float64
	ackLatency          time.Duration
	diskWriteMs         int64
	windowThroughput    float64
	transferMode        string
	displayLocalMetrics bool
	transport           string
	protocol            string
	tuningState         string
	tuningReason        string
	verified            *bool
}

const transferSpeedSmoothingWindow = 1500 * time.Millisecond
const transferSpeedSampleInterval = 500 * time.Millisecond

func smoothTransferSpeed(previous, sample float64, elapsed time.Duration) float64 {
	if sample <= 0 {
		return previous
	}
	if previous <= 0 || elapsed <= 0 {
		return sample
	}
	alpha := 1 - math.Exp(-float64(elapsed)/float64(transferSpeedSmoothingWindow))
	if alpha < 0.12 {
		alpha = 0.12
	}
	if alpha > 0.40 {
		alpha = 0.40
	}
	return previous + alpha*(sample-previous)
}

func (e *Engine) emitTransferProgress(messageID, attachmentID, peerDeviceID string, transferred, total int64, direction, phase string, options ...transferProgressOptions) {
	if transferred < 0 {
		transferred = 0
	}
	if total > 0 && transferred > total {
		transferred = total
	}
	percent := 0
	if total > 0 {
		percent = int(transferred * 100 / total)
	}
	value := map[string]any{
		"messageId":    messageID,
		"attachmentId": attachmentID,
		"peerDeviceId": peerDeviceID,
		"transferred":  transferred,
		"total":        total,
		"percent":      percent,
		"direction":    direction,
		"phase":        phase,
		"transport":    "TLS/TCP",
		"protocol":     fmt.Sprintf("%s/%d", ProtocolName, ProtocolMajor),
		"updatedAt":    time.Now().UTC().Format(time.RFC3339Nano),
	}
	var option transferProgressOptions
	if len(options) > 0 {
		option = options[0]
	}
	if option.transport != "" {
		value["transport"] = option.transport
	}
	if option.protocol != "" {
		value["protocol"] = option.protocol
	}
	if option.chunkSize > 0 {
		value["chunkSize"] = option.chunkSize
	}
	if option.windowSize > 0 {
		value["windowSize"] = option.windowSize
	}
	if option.windowBytes > 0 {
		value["windowBytes"] = option.windowBytes
	}
	if option.streamCount > 0 {
		value["streamCount"] = option.streamCount
		if option.activeStreams > 0 || option.transferMode == parallelBinaryMode {
			value["activeStreams"] = option.activeStreams
		}
		value["streamId"] = option.streamID
	}
	if option.streamOffset > 0 {
		value["streamOffset"] = option.streamOffset
	}
	if option.streamLength > 0 {
		value["streamLength"] = option.streamLength
	}
	if option.inFlightBytes > 0 || option.transferMode == parallelBinaryMode {
		value["inFlightBytes"] = option.inFlightBytes
	}
	if option.ackTargetBytes > 0 {
		value["ackTargetBytes"] = option.ackTargetBytes
	}
	if option.socketWriteMs > 0 {
		value["socketWriteMs"] = option.socketWriteMs
	}
	if option.ackWaitMs > 0 {
		value["ackWaitMs"] = option.ackWaitMs
	}
	if option.ackLatency > 0 {
		value["ackLatencyMs"] = option.ackLatency.Milliseconds()
	}
	if option.diskWriteMs > 0 {
		value["diskWriteMs"] = option.diskWriteMs
	}
	if option.windowThroughput > 0 {
		value["windowThroughput"] = int64(option.windowThroughput)
	}
	if option.confirmedThroughput > 0 {
		value["confirmedThroughput"] = int64(option.confirmedThroughput)
	}
	if option.transferMode != "" {
		value["transferMode"] = option.transferMode
	}
	if option.tuningState != "" {
		value["tuningState"] = option.tuningState
	}
	if option.tuningReason != "" {
		value["tuningReason"] = option.tuningReason
	}
	if option.verified != nil {
		value["verified"] = *option.verified
	}
	displayMetrics := direction != "send" || option.displayLocalMetrics
	e.transferMetricsMu.Lock()
	if e.transferMetrics == nil {
		e.transferMetrics = make(map[string]transferMetric)
	}
	metricKey := attachmentID + "|" + direction
	metric, ok := e.transferMetrics[metricKey]
	now := time.Now()
	reset := !ok || transferred < metric.lastBytes || phase == "awaiting_acceptance" || phase == "preparing_thumbnail"
	var rawSpeed float64
	if reset {
		metric = transferMetric{startedAt: now, startedBytes: transferred, lastAt: now, lastBytes: transferred}
	} else if displayMetrics {
		sampleStartedAt := metric.speedSampleAt
		if sampleStartedAt.IsZero() {
			sampleStartedAt = metric.lastAt
		}
		elapsedDuration := now.Sub(sampleStartedAt)
		elapsed := elapsedDuration.Seconds()
		if elapsed > 0 && transferred > metric.lastBytes {
			rawSpeed = option.windowThroughput
			if direction == "remote-receive" && option.confirmedThroughput > 0 {
				rawSpeed = option.confirmedThroughput
			} else if direction == "remote-receive" && (option.transferMode == binaryTransferMode || option.transferMode == parallelBinaryMode) {
				// Binary ACKs can arrive in a burst. Until the sampler has a
				// meaningful interval, retain the previous display speed.
				rawSpeed = 0
			}
			if rawSpeed <= 0 && !(direction == "remote-receive" && (option.transferMode == binaryTransferMode || option.transferMode == parallelBinaryMode)) {
				rawSpeed = float64(transferred-metric.lastBytes) / elapsed
			}
			if rawSpeed > 0 {
				metric.smoothedSpeed = smoothTransferSpeed(metric.smoothedSpeed, rawSpeed, elapsedDuration)
				metric.speedSampleAt = now
				if metric.smoothedSpeed > metric.peakSpeed {
					metric.peakSpeed = metric.smoothedSpeed
				}
			}
		}
		metric.lastAt = now
		metric.lastBytes = transferred
	}
	if displayMetrics && rawSpeed > 0 {
		value["rawSpeed"] = int64(rawSpeed)
	}
	if displayMetrics && metric.smoothedSpeed > 0 {
		value["speed"] = int64(metric.smoothedSpeed)
	}
	if displayMetrics && metric.peakSpeed > 0 {
		value["peakSpeed"] = int64(metric.peakSpeed)
	}
	if displayMetrics && !metric.startedAt.IsZero() {
		elapsedMs := now.Sub(metric.startedAt).Milliseconds()
		if elapsedMs > 0 {
			measuredBytes := transferred - metric.startedBytes
			if measuredBytes < 0 {
				measuredBytes = 0
			}
			average := float64(measuredBytes) / (float64(elapsedMs) / 1000)
			value["averageSpeed"] = int64(average)
			value["elapsedMs"] = elapsedMs
			etaSpeed := metric.smoothedSpeed
			if etaSpeed <= 0 {
				etaSpeed = average
			}
			if total > transferred && etaSpeed > 0 {
				value["etaSeconds"] = int64(float64(total-transferred) / etaSpeed)
			}
		}
	}
	if phase == "completed" || phase == "failed" || phase == "canceled" || phase == "rejected" {
		if e.transferLastBytes == nil {
			e.transferLastBytes = make(map[string]int64)
		}
		e.transferLastBytes[metricKey] = transferred
		delete(e.transferMetrics, metricKey)
	} else {
		if e.transferLastBytes == nil {
			e.transferLastBytes = make(map[string]int64)
		}
		e.transferLastBytes[metricKey] = transferred
		e.transferMetrics[metricKey] = metric
	}
	e.transferMetricsMu.Unlock()
	switch direction {
	case "send":
		value["sent"] = transferred
	case "receive":
		value["received"] = transferred
	case "remote-receive":
		value["remoteReceived"] = transferred
	}
	e.emit("chat:transfer-progress", value)
}

// ArchivePendingAttachments moves files that arrived during a storage
// migration out of the app temp directory after the migration lock is
// released. Manual-receive attachments remain pending and are handled by the
// explicit AcceptAttachment action.
func (e *Engine) ArchivePendingAttachments() {
	if e.IsAttachmentMigrationActive() || !e.Profile().AutoSave {
		return
	}
	profile := e.Profile()
	tempRoot, err := absoluteCleanPath(filepath.Join(AppDataDir(), "temp"))
	if err != nil {
		return
	}
	rows, err := ListAttachmentMigrationRows(context.Background())
	if err != nil {
		return
	}
	for _, row := range rows {
		if row.Status != "pending" {
			continue
		}
		source, sourceErr := absoluteCleanPath(row.LocalPath)
		if sourceErr != nil || !isWithin(source, tempRoot) {
			continue
		}
		if _, statErr := os.Stat(source); statErr != nil {
			continue
		}
		target, targetErr := AttachmentTargetPath(profile.FileSavePath, row.PeerDeviceID, row.FileName)
		if targetErr != nil || os.Rename(source, target) != nil {
			continue
		}
		if err := UpdateAttachmentLocalPath(context.Background(), row.AttachmentID, target); err != nil {
			_ = os.Rename(target, source)
			continue
		}
		_ = SaveAttachment(context.Background(), Attachment{AttachmentID: row.AttachmentID, MessageID: row.MessageID, FileName: row.FileName, FileSize: row.FileSize, SHA256: row.SHA256, LocalPath: target, Status: "saved"})
		e.emit("chat:attachment", map[string]any{"attachmentId": row.AttachmentID, "status": "saved", "localPath": target})
	}
}

func (e *Engine) AcceptIncomingAttachment(ctx context.Context, attachmentID, targetPath string) (Attachment, error) {
	e.mu.Lock()
	offer := e.pendingIncoming[attachmentID]
	if offer != nil {
		delete(e.pendingIncoming, attachmentID)
	}
	e.mu.Unlock()
	if offer == nil {
		return Attachment{}, fmt.Errorf("附件已不可接收")
	}
	if !e.canAllocateIncoming(offer.attachment.FileSize) {
		return offer.attachment, fmt.Errorf("接收空间不足")
	}
	if strings.TrimSpace(targetPath) == "" {
		return offer.attachment, fmt.Errorf("附件保存路径不能为空")
	}
	message := offer.message
	message.Status, message.AttachmentStatus = "receiving", "receiving"
	attachment := offer.attachment
	attachment.Status = "receiving"
	if err := e.beginIncomingFileWithMode(message, attachment, offer.senderID, offer.session, targetPath, false, offer.parallel, offer.transferToken); err != nil {
		e.mu.Lock()
		e.pendingIncoming[attachmentID] = offer
		e.mu.Unlock()
		return offer.attachment, err
	}
	e.mu.Lock()
	if transfer := e.incoming[attachmentID]; transfer != nil {
		transfer.windowed = offer.windowed
		transfer.binary = offer.binary
		transfer.parallel = offer.parallel
		transfer.transferToken = offer.transferToken
		transfer.parallelStreamCount = offer.parallelStreamCount
		if transfer.parallelStreamCount == 0 {
			transfer.parallelStreamCount = parallelStreamCount(attachment.FileSize)
		}
		transfer.chunkSize = offer.chunkSize
		transfer.windowSize = offer.windowSize
		transfer.expectedWindowBytes = offer.windowBytes
	}
	e.mu.Unlock()
	_ = offer.session.conn.SetDeadline(time.Time{})
	if err := offer.session.write(wireMessage{Type: "file_accept", MessageID: message.MessageID, AttachmentID: attachmentID, Status: "accepted"}); err != nil {
		e.cancelIncomingFromRemote(attachmentID, false)
		return offer.attachment, err
	}
	mode := legacyTransferMode
	if offer.windowed {
		mode = jsonWindowTransferMode
	}
	if offer.parallel {
		mode = parallelBinaryMode
	} else if offer.binary {
		mode = binaryTransferMode
	}
	e.emitTransferProgress(message.MessageID, attachmentID, offer.senderID, 0, attachment.FileSize, "receive", "receiving", transferProgressOptions{chunkSize: offer.chunkSize, windowSize: offer.windowSize, windowBytes: offer.windowBytes, transferMode: mode, transport: "TLS/TCP", protocol: fmt.Sprintf("%s/%d", ProtocolName, ProtocolMajor), tuningState: "probing"})
	return GetAttachment(ctx, attachmentID)
}

func (e *Engine) RejectIncomingAttachment(attachmentID string) error {
	e.mu.Lock()
	offer := e.pendingIncoming[attachmentID]
	if offer != nil {
		delete(e.pendingIncoming, attachmentID)
	}
	e.mu.Unlock()
	if offer == nil {
		return fmt.Errorf("附件已不可拒绝")
	}
	_ = offer.session.write(wireMessage{Type: "file_reject", MessageID: offer.message.MessageID, AttachmentID: attachmentID, Status: "rejected"})
	offer.session.close()
	_ = SaveAttachment(context.Background(), Attachment{AttachmentID: offer.attachment.AttachmentID, MessageID: offer.attachment.MessageID, FileName: offer.attachment.FileName, MimeType: offer.attachment.MimeType, FileSize: offer.attachment.FileSize, SHA256: offer.attachment.SHA256, ThumbnailData: offer.attachment.ThumbnailData, ThumbnailMime: offer.attachment.ThumbnailMime, Status: "rejected"})
	e.emitAttachmentStatus(offer.message.MessageID, "rejected", "")
	return nil
}

func (e *Engine) CancelAttachment(attachmentID string) error {
	e.mu.RLock()
	outgoing := e.outgoing[attachmentID]
	preparing := e.preparing[attachmentID]
	e.mu.RUnlock()
	if outgoing != nil {
		outgoing.session.cancel(attachmentID)
		return nil
	}
	if preparing != nil {
		e.mu.Lock()
		if current := e.preparing[attachmentID]; current == preparing {
			if !current.canceled {
				current.canceled = true
				close(current.cancel)
			}
		}
		e.mu.Unlock()
		if attachment, err := GetAttachment(context.Background(), attachmentID); err == nil {
			attachment.Status = "canceled"
			_ = SaveAttachment(context.Background(), attachment)
			e.emitAttachmentStatus(attachment.MessageID, "canceled", "")
		} else {
			return err
		}
		e.emitTransferProgress("", attachmentID, "", 0, 0, "send", "canceled")
		return nil
	}
	e.mu.Lock()
	offer := e.pendingIncoming[attachmentID]
	transfer := e.incoming[attachmentID]
	if offer != nil {
		delete(e.pendingIncoming, attachmentID)
	}
	if transfer != nil {
		delete(e.incoming, attachmentID)
	}
	e.mu.Unlock()
	if offer != nil {
		_ = offer.session.write(wireMessage{Type: "file_cancel", AttachmentID: attachmentID, Status: "canceled"})
		offer.session.close()
		_ = SaveAttachment(context.Background(), Attachment{AttachmentID: offer.attachment.AttachmentID, MessageID: offer.attachment.MessageID, FileName: offer.attachment.FileName, MimeType: offer.attachment.MimeType, FileSize: offer.attachment.FileSize, SHA256: offer.attachment.SHA256, ThumbnailData: offer.attachment.ThumbnailData, ThumbnailMime: offer.attachment.ThumbnailMime, Status: "canceled"})
		e.emitAttachmentStatus(offer.message.MessageID, "canceled", "")
		return nil
	}
	if transfer != nil {
		received := transfer.received
		if transfer.parallel {
			transfer.parallelMu.Lock()
			received = transfer.received
			transfer.parallelMu.Unlock()
		}
		_ = transfer.session.write(wireMessage{Type: "file_cancel", AttachmentID: attachmentID, Status: "canceled"})
		transfer.session.close()
		closeParallelSessions(transfer)
		_ = transfer.file.Close()
		_ = os.Remove(transfer.tempPath)
		_ = SaveAttachment(context.Background(), Attachment{AttachmentID: transfer.attachmentID, MessageID: transfer.messageID, FileName: transfer.fileName, MimeType: transfer.mimeType, FileSize: transfer.expected, SHA256: transfer.sha256, Status: "canceled"})
		e.emitAttachmentStatus(transfer.messageID, "canceled", "")
		e.emitTransferProgress(transfer.messageID, attachmentID, transfer.senderID, received, transfer.expected, "receive", "canceled")
		return nil
	}
	return fmt.Errorf("附件传输不存在")
}

func (e *Engine) discoveryLoop() {
	buffer := make([]byte, 16*1024)
	for {
		_ = e.udp.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, addr, err := e.udp.ReadFromUDP(buffer)
		if err != nil {
			select {
			case <-e.stop:
				return
			default:
				continue
			}
		}
		var message wireMessage
		if json.Unmarshal(buffer[:n], &message) != nil || message.DeviceID == e.identity.DeviceID {
			continue
		}
		dialect, compatible := protocolDialectForMessage(message)
		if !compatible {
			continue
		}
		switch message.Type {
		case "discover":
			if scope := e.discoveryResponseScope(message.DeviceID); scope != "" {
				// Android discovery sends from an ephemeral UDP source port while
				// listening on the canonical discovery port. Always send announces
				// to that fixed port so both desktop and Android can receive them.
				response := e.helloMessageForDialect("announce", dialect)
				response.RequestID = message.RequestID
				response.DiscoveryScope = scope
				_ = e.sendDiscovery(&net.UDPAddr{IP: addr.IP, Port: DiscoveryPort}, response)
			}
		case "announce":
			message.DiscoveryScope = e.compatibilityDiscoveryScope(message)
			if isDiscoveryPresence(message.RequestID) {
				if message.DiscoveryScope != DiscoveryScopePublic {
					continue
				}
				// Presence is an unsolicited heartbeat, not a response to the
				// current scan. It refreshes the lease directly.
				_ = e.handleAnnounce(message)
				continue
			}
			if !e.acceptDiscoveryResponse(message.RequestID, message.DeviceID, message.DiscoveryScope) {
				continue
			}
			message.IP = addr.IP.String()
			e.handleAnnounce(message)
		case "withdraw":
			e.handleWithdraw(message.DeviceID, message.RequestID)
		case "offline":
			e.handleOffline(message.DeviceID, message.RequestID)
		}
	}
}

func (e *Engine) scanLoop() {
	ticker := time.NewTicker(6 * time.Second)
	defer ticker.Stop()
	lastUnicastProbe := time.Now()
	for {
		select {
		case <-ticker.C:
			unicastProbe := time.Since(lastUnicastProbe) >= 30*time.Second
			e.scanNetwork(unicastProbe)
			// Keep discovery presence independent from scan responses. A live
			// peer therefore remains visible even when one scan is lost.
			if e.Profile().Discoverable {
				e.broadcastPresence("announce")
			}
			// Discovery has no explicit "goodbye" packet. Re-publish the
			// computed presence so the UI can turn stale peers offline.
			e.emit("chat:peer-updated", e.Peers())
			if unicastProbe {
				lastUnicastProbe = time.Now()
			}
		case <-e.stop:
			return
		}
	}
}

// Scan sends a discovery request immediately instead of waiting for the
// periodic scan ticker. It is used by the Discover page's manual refresh.
func (e *Engine) Scan() {
	if e.isStarted() {
		e.scanNetwork(true)
	}
}

func (e *Engine) isStarted() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.started
}

func (e *Engine) scanNetwork(includeUnicastProbe bool) {
	// A scan is a snapshot. Serialize manual and periodic scans so a delayed
	// response from an older scan cannot keep a disabled device visible.
	e.discoveryScanMu.Lock()
	defer e.discoveryScanMu.Unlock()

	e.discoveryMu.Lock()
	e.activeDiscoveryIDs = make(map[string]struct{})
	e.activeDiscoverySeen = make(map[string]struct{})
	e.discoveryMu.Unlock()
	defer func() {
		e.discoveryMu.Lock()
		seen := e.activeDiscoverySeen
		e.activeDiscoveryIDs = nil
		e.activeDiscoverySeen = nil
		e.discoveryMu.Unlock()
		e.removeUnseenDiscoveredPeers(seen)
	}()

	// Scanning and being discoverable are independent. A disabled device may
	// still look for devices that opted in; only the receiver decides whether
	// it should answer the discover request. This is also required for Android
	// to find a discoverable device when the desktop setting is disabled.
	targets := broadcastAddresses()
	if len(targets) == 0 {
		targets = []net.UDPAddr{{IP: net.IPv4bcast, Port: DiscoveryPort}}
	}
	var subnetTargets []net.UDPAddr
	if includeUnicastProbe {
		subnetTargets = localSubnetTargets()
		targets = append(targets, subnetTargets...)
	}

	var firstErr error
	for _, dialect := range protocolDialects {
		message := e.helloMessageForDialect("discover", dialect)
		message.RequestID = newID()
		e.discoveryMu.Lock()
		e.activeDiscoveryIDs[message.RequestID] = struct{}{}
		e.discoveryMu.Unlock()
		for index := range targets[:len(targets)-len(subnetTargets)] {
			if err := e.sendDiscovery(&targets[index], message); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		for index := len(targets) - len(subnetTargets); index < len(targets); index++ {
			// Individual hosts can be offline; a failed unicast probe is expected.
			_ = e.sendDiscovery(&targets[index], message)
		}
		if includeUnicastProbe {
			if probeErr := e.probeTCPSubnets(message, subnetTargets); probeErr != nil && firstErr == nil {
				firstErr = probeErr
			}
		}
	}
	// UDP responses are handled by the long-running listener. Desktop peers may
	// answer slightly later while their own scan is running; wait for the
	// response window to settle before removing devices absent from this scan.
	time.Sleep(1 * time.Second)
	e.mu.Lock()
	e.lastScan = time.Now()
	if firstErr != nil {
		e.lastErr = firstErr.Error()
	} else {
		e.lastErr = ""
	}
	e.mu.Unlock()
	e.emit("chat:network-status", e.NetworkStatus())
}

func (e *Engine) scanKnownFriends() error {
	var firstErr error
	for _, peer := range e.PeersByRelation(PeerRelation) {
		ip := net.ParseIP(strings.TrimSpace(peer.IP))
		if ip == nil || ip.To4() == nil {
			continue
		}
		target := &net.UDPAddr{IP: ip.To4(), Port: DiscoveryPort}
		for _, dialect := range protocolDialectsForPeer(peer) {
			if err := e.sendDiscovery(target, e.helloMessageForDialect("discover", dialect)); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (e *Engine) probeTCPSubnets(message wireMessage, targets []net.UDPAddr) error {
	const parallelism = 64
	sem := make(chan struct{}, parallelism)
	var wait sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	for index := range targets {
		target := targets[index]
		wait.Add(1)
		go func() {
			defer wait.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			address := net.JoinHostPort(target.IP.String(), fmt.Sprint(DiscoveryPort))
			conn, err := net.DialTimeout("tcp4", address, 150*time.Millisecond)
			if err != nil {
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
			if err := writeWire(conn, message); err != nil {
				return
			}
			var response wireMessage
			if err := json.NewDecoder(conn).Decode(&response); err != nil || response.Type != "announce" || response.DeviceID == e.identity.DeviceID {
				return
			}
			response.DiscoveryScope = e.compatibilityDiscoveryScope(response)
			if response.RequestID != message.RequestID || !e.acceptDiscoveryResponse(response.RequestID, response.DeviceID, response.DiscoveryScope) {
				return
			}
			if _, ok := protocolDialectForMessage(response); !ok {
				return
			}
			if response.IP == "" {
				response.IP = target.IP.String()
			}
			if err := e.handleAnnounce(response); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
			}
		}()
	}
	wait.Wait()
	return firstErr
}

func (e *Engine) acceptDiscoveryResponse(requestID, deviceID, scope string) bool {
	if strings.TrimSpace(requestID) == "" || strings.TrimSpace(deviceID) == "" {
		return false
	}
	e.discoveryMu.Lock()
	if _, ok := e.activeDiscoveryIDs[requestID]; !ok {
		e.discoveryMu.Unlock()
		return false
	}
	e.discoveryMu.Unlock()
	if scope != DiscoveryScopePublic && scope != DiscoveryScopeFriend {
		return false
	}
	if scope == DiscoveryScopeFriend && !e.isFriend(deviceID) {
		return false
	}
	e.discoveryMu.Lock()
	if scope == DiscoveryScopePublic && e.activeDiscoverySeen != nil {
		e.activeDiscoverySeen[deviceID] = struct{}{}
	}
	e.discoveryMu.Unlock()
	return true
}

// compatibilityDiscoveryScope supports an older desktop peer that already
// uses the current discovery request format but does not yet include the
// discoveryScope field. This fallback is deliberately limited to a device
// that was explicitly removed on this machine; other scope-less responses
// remain ignored so a disabled stranger cannot become visible.
func (e *Engine) compatibilityDiscoveryScope(message wireMessage) string {
	if message.DiscoveryScope != "" {
		return message.DiscoveryScope
	}
	removed, err := IsFriendRemoved(context.Background(), message.DeviceID)
	if err == nil && removed {
		return DiscoveryScopePublic
	}
	return ""
}

func (e *Engine) removeUnseenDiscoveredPeers(seen map[string]struct{}) {
	if seen == nil {
		return
	}
	for _, peer := range e.Peers() {
		if _, ok := seen[peer.DeviceID]; ok {
			e.resetDiscoveryMiss(peer.DeviceID)
			continue
		}
		if peer.DiscoveryVisible && discoveryLeaseIsFresh(peer.LastSeen) {
			// The peer announced recently; this scan likely lost a response.
			// Keep both visibility and online state unchanged until the lease
			// expires or an explicit offline/withdraw is received.
			continue
		}

		e.discoveryMu.Lock()
		if e.discoveryMisses == nil {
			e.discoveryMisses = make(map[string]int)
		}
		misses := e.discoveryMisses[peer.DeviceID] + 1
		e.discoveryMisses[peer.DeviceID] = misses
		e.discoveryMu.Unlock()
		if misses < discoveryMissThreshold {
			continue
		}

		if peer.Relation == PeerRelation {
			e.setPeerDiscoveryVisible(peer.DeviceID, false)
			e.resetDiscoveryMiss(peer.DeviceID)
			continue
		}
		// A removed friendship remains in both local lists so the user can see
		// the relationship state and start a fresh request after rediscovery.
		// Discovery cleanup must not delete that trusted peer row.
		if peer.FriendshipState == "removed" {
			e.setPeerDiscoveryVisible(peer.DeviceID, false)
			e.resetDiscoveryMiss(peer.DeviceID)
			continue
		}
		if e.hasPendingFriendRequest(peer.DeviceID) {
			e.setPeerDiscoveryVisible(peer.DeviceID, false)
			e.resetDiscoveryMiss(peer.DeviceID)
			continue
		}
		e.forgetDiscoveredPeer(peer.DeviceID)
	}
}

func discoveryLeaseIsFresh(lastSeen string) bool {
	seenAt := parseTime(lastSeen)
	return !seenAt.IsZero() && time.Since(seenAt) < discoveryLeaseDuration
}

func (e *Engine) sendDiscovery(addr *net.UDPAddr, message wireMessage) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	e.mu.RLock()
	udp := e.udp
	e.mu.RUnlock()
	if udp == nil {
		return errors.New("discovery_not_started")
	}
	_, err = udp.WriteToUDP(data, addr)
	return err
}

func (e *Engine) helloMessage(kind string) wireMessage {
	return e.helloMessageForDialect(kind, protocolDialects[0])
}

func (e *Engine) helloMessageForDialect(kind string, dialect ProtocolDialect) wireMessage {
	e.mu.RLock()
	identity := e.identity
	e.mu.RUnlock()
	profile := e.Profile()
	capabilities := []string{"text", "image", "file"}
	if dialect.Major >= 2 {
		capabilities = append(capabilities, "file-progress-v1", fileWindowCapability, fileStreamCapability, fileParallelCapability, "attachment-demand-v1", "avatar-sync-v1", "offline-v1", "friend-restore-v2", "storage-preflight-v1")
	}
	capabilities = append(capabilities, sharedDriveCapability)
	capabilities = append(capabilities, sharedThumbnailBatchCapability)
	message := wireMessage{Magic: dialect.Magic, Type: kind, Protocol: dialect.Name, Major: dialect.Major, Minor: ProtocolMinor, MinMajor: dialect.Major, MinMinor: 0, DeviceID: identity.DeviceID, Nickname: profile.Nickname, AvatarHash: profile.AvatarHash, AvatarVersion: profile.AvatarVersion, Platform: identity.Platform, OSVersion: identity.OSVersion, IP: identity.IP, Port: identity.Port, PublicKey: identity.PublicKeyPEM, CertFP: identity.CertificateFingerprint, Capabilities: capabilities}
	if kind == "announce" {
		if data, mimeType, hash := e.avatarPreviewPayloadForWire(); data != "" {
			message.AvatarPreviewData = data
			message.AvatarPreviewMime = mimeType
			message.AvatarPreviewHash = hash
		}
	}
	return message
}

func (e *Engine) upsertWirePeer(message wireMessage, discoveryVisible bool) error {
	return e.upsertWirePeerWithOptions(message, discoveryVisible)
}

func (e *Engine) upsertWirePeerWithOptions(message wireMessage, discoveryVisible bool) error {
	if message.PublicKey != "" && !validDevicePublicKey(message.DeviceID, message.PublicKey) {
		return fmt.Errorf("设备身份校验失败")
	}
	if strings.TrimSpace(message.DeviceID) == "" {
		return fmt.Errorf("设备身份为空")
	}
	avatarChanged := false
	peer := Peer{DeviceID: message.DeviceID, Nickname: message.Nickname, AvatarHash: message.AvatarHash, AvatarVersion: message.AvatarVersion, Platform: message.Platform, OSVersion: message.OSVersion, IP: message.IP, Port: message.Port, PublicKeyPEM: message.PublicKey, CertificateFingerprint: message.CertFP, ProtocolName: message.Protocol, ProtocolMajor: message.Major, DiscoveryMagic: message.Magic, Capabilities: message.Capabilities, DiscoveryVisible: discoveryVisible, Relation: DiscoveredState, LastSeen: nowString()}
	if existing, existingErr := e.peer(message.DeviceID); existingErr == nil {
		if existing.PublicKeyPEM != "" && message.PublicKey != "" && !strings.EqualFold(existing.PublicKeyPEM, message.PublicKey) {
			return fmt.Errorf("DEVICE_KEY_CHANGED")
		}
		if existing.CertificateFingerprint != "" && message.CertFP == "" {
			message.CertFP = existing.CertificateFingerprint
		}
		if peer.PublicKeyPEM == "" {
			peer.PublicKeyPEM = existing.PublicKeyPEM
		}
		if peer.CertificateFingerprint == "" {
			peer.CertificateFingerprint = existing.CertificateFingerprint
		}
		peer.Relation, peer.Remark, peer.AvatarPath = existing.Relation, existing.Remark, existing.AvatarPath
		// Treat an empty previous hash as a real change too. The first custom
		// avatar received from Android used to be cached successfully but did
		// not emit peer-updated because this check required both hashes to be
		// non-empty, leaving the desktop UI on the generated initials until a
		// later refresh.
		avatarChanged = (message.AvatarHash != "" && !strings.EqualFold(message.AvatarHash, existing.AvatarHash)) ||
			(message.AvatarVersion > 0 && message.AvatarVersion != existing.AvatarVersion)
		// A normal hello or discovery packet carries metadata only. Keep the
		// cached image visible until the user explicitly opens the chat/profile
		// and the authenticated avatar bytes have been fetched.
		peer.VisibleInFriends = existing.VisibleInFriends
		peer.RelationshipVersion = existing.RelationshipVersion
		peer.FriendshipState = existing.FriendshipState
		if !discoveryVisible {
			peer.DiscoveryVisible = existing.DiscoveryVisible
		}
		if peer.AvatarHash == "" && message.AvatarVersion <= 0 {
			peer.AvatarHash, peer.AvatarVersion = existing.AvatarHash, existing.AvatarVersion
		}
		// Avatar updates are monotonic. Discovery and hello packets can arrive
		// out of order when a profile is changed twice quickly; never let an
		// older packet erase the newer cached avatar.
		if existing.AvatarVersion > 0 && message.AvatarVersion > 0 && message.AvatarVersion < existing.AvatarVersion {
			peer.AvatarHash = existing.AvatarHash
			peer.AvatarVersion = existing.AvatarVersion
			peer.AvatarPath = existing.AvatarPath
			avatarChanged = false
			message.AvatarData = ""
		}
		if peer.ProtocolName == "" || peer.ProtocolMajor == 0 {
			peer.ProtocolName, peer.ProtocolMajor, peer.DiscoveryMagic, peer.Capabilities = existing.ProtocolName, existing.ProtocolMajor, existing.DiscoveryMagic, existing.Capabilities
		} else if len(peer.Capabilities) == 0 {
			peer.Capabilities = append([]string(nil), existing.Capabilities...)
		}
	}
	// Authenticated traffic may carry the full avatar. Public discovery carries
	// only AvatarPreviewData, which is validated independently and cached as a
	// normal peer avatar so the discovery list can render it immediately.
	avatarMessage := message
	if avatarMessage.AvatarData == "" && avatarMessage.AvatarPreviewData != "" {
		avatarMessage.AvatarData = avatarMessage.AvatarPreviewData
		avatarMessage.AvatarHash = avatarMessage.AvatarPreviewHash
		avatarMessage.AvatarMime = avatarMessage.AvatarPreviewMime
	}
	if avatarMessage.AvatarData != "" && avatarMessage.AvatarHash != "" {
		if avatarPath, avatarErr := e.cachePeerAvatar(avatarMessage, peer.AvatarPath); avatarErr == nil {
			peer.AvatarPath = avatarPath
		}
	}
	if err := UpsertPeer(context.Background(), peer); err != nil {
		return err
	}
	e.mu.Lock()
	if old, exists := e.peers[peer.DeviceID]; exists {
		peer.Relation, peer.Remark = old.Relation, old.Remark
		peer.VisibleInFriends = old.VisibleInFriends
		peer.RelationshipVersion = old.RelationshipVersion
		peer.FriendshipState = old.FriendshipState
		if peer.AvatarPath == "" && !avatarChanged {
			peer.AvatarPath = old.AvatarPath
		}
		if !discoveryVisible {
			peer.DiscoveryVisible = old.DiscoveryVisible
		}
		if peer.AvatarHash == "" && message.AvatarVersion <= 0 {
			peer.AvatarHash, peer.AvatarVersion = old.AvatarHash, old.AvatarVersion
		}
		if peer.ProtocolName == "" || peer.ProtocolMajor == 0 {
			peer.ProtocolName, peer.ProtocolMajor, peer.DiscoveryMagic, peer.Capabilities = old.ProtocolName, old.ProtocolMajor, old.DiscoveryMagic, old.Capabilities
		} else if len(peer.Capabilities) == 0 {
			peer.Capabilities = append([]string(nil), old.Capabilities...)
		}
	}
	peer.Online = true
	e.peers[peer.DeviceID] = peer
	e.mu.Unlock()
	return nil
}

func (e *Engine) avatarPreviewPayloadForWire() (encoded, mimeType, hash string) {
	encoded, sourceMime := e.avatarPayloadForWire()
	if encoded == "" {
		return "", "", ""
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 {
		return "", "", ""
	}
	preview, previewMime, previewBytes, err := buildAvatarPreview(data, sourceMime)
	if err != nil || len(previewBytes) == 0 {
		return "", "", ""
	}
	return preview, previewMime, sha256Hex(previewBytes)
}

func (e *Engine) cachePeerAvatar(message wireMessage, previousPath string) (string, error) {
	encoded := message.AvatarData
	if marker := strings.Index(encoded, "base64,"); marker >= 0 {
		encoded = encoded[marker+len("base64,"):]
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > 5*1024*1024 {
		return "", fmt.Errorf("头像数据无效")
	}
	if sha256Hex(data) != message.AvatarHash {
		return "", fmt.Errorf("头像校验失败")
	}
	cacheDir := filepath.Join(AppDataDir(), "avatar-cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", err
	}
	ext := strings.TrimPrefix(strings.ToLower(message.AvatarMime), "image/")
	if ext == "" || strings.ContainsAny(ext, `/\\.`) {
		ext = "jpeg"
	}
	path := filepath.Join(cacheDir, safeFileName(message.DeviceID)+"."+ext)
	if previousPath == path && message.AvatarVersion > 0 {
		// The path is stable per device, but the file at that path may belong
		// to the previous avatar version. Checking only os.Stat here used to
		// keep the old bytes while saving the new hash, so both platforms could
		// show a stale avatar indefinitely.
		if existing, readErr := os.ReadFile(path); readErr == nil && sha256Hex(existing) == message.AvatarHash {
			return path, nil
		}
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// applyPeerAvatar is the single authenticated avatar write path. Avatar data
// may arrive in hello/hello_ack or in avatar_response; both forms are checked
// for size, hash and version before touching the peer row. Keeping this logic
// in one place prevents a late discovery packet from replacing a newer
// custom avatar and makes the UI event identical for both transport paths.
func (e *Engine) applyPeerAvatar(message wireMessage, deviceID string) bool {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" || message.AvatarData == "" || !e.isFriend(deviceID) {
		return false
	}
	if message.DeviceID == "" {
		message.DeviceID = deviceID
	}
	current, err := e.peer(deviceID)
	if err != nil {
		return false
	}
	if current.AvatarVersion > 0 && message.AvatarVersion > 0 && message.AvatarVersion < current.AvatarVersion {
		return false
	}
	if message.AvatarHash == "" {
		return false
	}
	path, err := e.cachePeerAvatar(message, current.AvatarPath)
	if err != nil {
		return false
	}
	if current.AvatarHash == message.AvatarHash && current.AvatarVersion >= message.AvatarVersion && current.AvatarPath == path {
		return false
	}
	if err := SetPeerAvatar(context.Background(), deviceID, path, message.AvatarHash, message.AvatarVersion); err != nil {
		return false
	}
	if current.AvatarPath != "" && current.AvatarPath != path && strings.HasPrefix(filepath.Clean(current.AvatarPath), filepath.Join(AppDataDir(), "avatar-cache")+string(os.PathSeparator)) {
		_ = os.Remove(current.AvatarPath)
	}
	e.mu.Lock()
	peer := e.peers[deviceID]
	peer.AvatarPath = path
	peer.AvatarHash = message.AvatarHash
	peer.AvatarVersion = message.AvatarVersion
	e.peers[deviceID] = peer
	e.mu.Unlock()
	e.emit("chat:peer-updated", e.Peers())
	return true
}

// handleAnnounce is the only discovery path that may initiate the optional
// friend restoration handshake. TLS health probes use upsertWirePeer directly
// and therefore remain side-effect free.
func (e *Engine) handleAnnounce(message wireMessage) error {
	if !compatibleProtocol(message) {
		return errors.New("PROTOCOL_UNSUPPORTED")
	}
	wasFriend := false
	if existing, err := e.peer(message.DeviceID); err == nil {
		wasFriend = existing.Relation == PeerRelation && existing.FriendshipState != "removed" && e.isFriend(message.DeviceID)
	}
	discoveryVisible := message.DiscoveryScope == DiscoveryScopePublic
	if message.DiscoveryScope == DiscoveryScopeFriend && !e.isFriend(message.DeviceID) {
		return errors.New("FRIEND_DISCOVERY_NOT_ALLOWED")
	}
	if discoveryVisible {
		if isDiscoveryPresence(message.RequestID) {
			if e.stalePresenceControl(message.DeviceID, message.RequestID) {
				return nil
			}
		}
	}
	// A directed friend response is useful for address/online refreshes but it
	// is not an instruction to remove a public discovery entry. Preserve the
	// latest public lease until an explicit withdraw/offline or lease expiry.
	persistedDiscoveryVisible := discoveryVisible
	if !discoveryVisible {
		if existing, err := e.peer(message.DeviceID); err == nil {
			persistedDiscoveryVisible = existing.DiscoveryVisible
		}
	}
	if err := e.upsertWirePeer(message, persistedDiscoveryVisible); err != nil {
		return err
	}
	if discoveryVisible {
		e.resetDiscoveryMiss(message.DeviceID)
		// Only a public announce is authoritative for the discovery list. A
		// friend-scoped announce is a directed health response and must not
		// clear a public presence that was received moments earlier.
		e.setPeerDiscoveryVisible(message.DeviceID, true)
	}
	e.emit("chat:peer-updated", e.Peers())
	if discoveryVisible {
		if peer, err := e.peer(message.DeviceID); err == nil && peer.FriendshipState == "removed" {
			e.maybeSyncFriendRemoval(peer)
		}
	}
	if wasFriend && !isDiscoveryPresence(message.RequestID) {
		if peer, err := e.peer(message.DeviceID); err == nil {
			e.maybeSendFriendRestore(peer)
		}
	}
	return nil
}

// maybeSyncFriendRemoval closes the offline gap. A public announce means the
// removed device is online again; send the tombstone over the authenticated
// channel so the other side converges without waiting for it to send a
// message. The cooldown prevents every announce packet from opening a new
// connection.
func (e *Engine) maybeSyncFriendRemoval(peer Peer) {
	if peer.DeviceID == "" || peer.FriendshipState != "removed" {
		return
	}
	now := time.Now()
	e.friendRemovalSyncMu.Lock()
	last := e.friendRemovalSyncAt[peer.DeviceID]
	if !last.IsZero() && now.Sub(last) < 5*time.Minute {
		e.friendRemovalSyncMu.Unlock()
		return
	}
	e.friendRemovalSyncAt[peer.DeviceID] = now
	e.friendRemovalSyncMu.Unlock()
	go func() {
		if err := e.NotifyFriendRemoved(peer.DeviceID); err != nil {
			log.Printf("解除好友关系离线同步失败: device=%s, err=%v", peer.DeviceID, err)
		}
	}()
}

func compatibleProtocol(message wireMessage) bool {
	_, ok := protocolDialectForMessage(message)
	return ok
}

func isDiscoveryPresence(requestID string) bool {
	return strings.HasPrefix(strings.TrimSpace(requestID), discoveryPresencePrefix)
}

func (e *Engine) maybeSendFriendRestore(peer Peer) {
	e.mu.Lock()
	last := e.friendRestoreAt[peer.DeviceID]
	if !last.IsZero() && time.Since(last) < 30*time.Second {
		e.mu.Unlock()
		return
	}
	e.friendRestoreAt[peer.DeviceID] = time.Now()
	e.mu.Unlock()
	go func() {
		for _, dialect := range protocolDialectsForPeer(peer) {
			message, err := e.friendRestoreMessageForDialect(peer.DeviceID, dialect)
			if err != nil {
				continue
			}
			if e.sendToPeerWithDialect(peer, message, dialect) == nil {
				return
			}
		}
	}()
}

func validDevicePublicKey(deviceID, publicKeyPEM string) bool {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return false
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return false
	}
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return false
	}
	return strings.EqualFold(deviceID, sha256Hex(der))
}

func (e *Engine) friendRequestForDevice(deviceID string) (FriendRequest, bool) {
	requests, err := listFriendRequestRows(context.Background(), "")
	if err != nil {
		return FriendRequest{}, false
	}
	var latest FriendRequest
	for _, request := range requests {
		if request.DeviceID != deviceID {
			continue
		}
		requestActive := isActiveFriendRequest(request.Status)
		latestActive := isActiveFriendRequest(latest.Status)
		if latest.RequestID == "" || (requestActive && !latestActive) || (requestActive == latestActive && requestIsNewer(request, latest)) {
			latest = request
		}
	}
	return latest, latest.RequestID != ""
}

func isActiveFriendRequest(status string) bool {
	return status == "queued" || status == "sent" || status == "pending"
}

func friendRequestByID(requestID string) (FriendRequest, bool) {
	if strings.TrimSpace(requestID) == "" {
		return FriendRequest{}, false
	}
	requests, err := listFriendRequestRows(context.Background(), "")
	if err != nil {
		return FriendRequest{}, false
	}
	for _, request := range requests {
		if request.RequestID == requestID {
			return request, true
		}
	}
	return FriendRequest{}, false
}

// prepareNewFriendRequest collapses only older requests in the same
// direction. The latest opposite-direction request remains active so the UI
// can represent a genuine two-way request as mutual.
func (e *Engine) prepareNewFriendRequest(ctx context.Context, deviceID, direction, keepRequestID string) error {
	rows, err := listFriendRequestRows(ctx, "")
	if err != nil {
		return err
	}
	var preserveOppositeID string
	var latestOpposite FriendRequest
	for _, row := range rows {
		if row.DeviceID != deviceID || row.RequestID == keepRequestID || !isActiveFriendRequest(row.Status) || row.Direction == direction {
			continue
		}
		if preserveOppositeID == "" || requestIsNewer(row, latestOpposite) {
			latestOpposite = row
			preserveOppositeID = row.RequestID
		}
	}
	if err := SupersedeActiveFriendRequestsForNew(ctx, deviceID, direction, keepRequestID, preserveOppositeID); err != nil {
		return err
	}
	for _, row := range rows {
		if row.DeviceID != deviceID || row.RequestID == keepRequestID || !isActiveFriendRequest(row.Status) {
			continue
		}
		if row.Direction == direction || (preserveOppositeID != "" && row.RequestID != preserveOppositeID) {
			if updated, ok := friendRequestByID(row.RequestID); ok {
				e.emit("chat:friend-request-updated", updated)
			}
		}
	}
	return nil
}

func (e *Engine) emitFriendRequestUpdate(deviceID string) {
	if request, ok := e.friendRequestForDevice(deviceID); ok {
		e.emit("chat:friend-request-updated", request)
	}
}

// supersedeActiveFriendRequestsExcept closes the other direction after an
// explicit accept/reject resolution.
func (e *Engine) supersedeActiveFriendRequestsExcept(ctx context.Context, deviceID, keepRequestID string) error {
	rows, err := listFriendRequestRows(ctx, "")
	if err != nil {
		return err
	}
	if err := SupersedeActiveFriendRequestsExcept(ctx, deviceID, keepRequestID); err != nil {
		return err
	}
	for _, row := range rows {
		if row.DeviceID != deviceID || row.RequestID == keepRequestID || !isActiveFriendRequest(row.Status) {
			continue
		}
		if updated, ok := friendRequestByID(row.RequestID); ok {
			e.emit("chat:friend-request-updated", updated)
		}
	}
	return nil
}

func (e *Engine) SendFriendRequest(ctx context.Context, deviceID, message string) (FriendRequest, error) {
	peer, err := e.latestPeer(ctx, deviceID)
	if err != nil {
		return FriendRequest{}, err
	}
	removed, _ := IsFriendRemoved(ctx, deviceID)
	if !removed && e.isFriend(deviceID) {
		return FriendRequest{}, fmt.Errorf("已经是好友")
	}
	if rows, rowsErr := listFriendRequestRows(ctx, ""); rowsErr == nil {
		var sentActive, receivedActive bool
		var latestSent, latestReceived FriendRequest
		for _, row := range rows {
			if row.DeviceID != deviceID || !isActiveFriendRequest(row.Status) {
				continue
			}
			if row.Direction == "sent" {
				sentActive = true
				if latestSent.RequestID == "" || requestIsNewer(row, latestSent) {
					latestSent = row
				}
			} else {
				receivedActive = true
				if latestReceived.RequestID == "" || requestIsNewer(row, latestReceived) {
					latestReceived = row
				}
			}
		}
		if !removed && sentActive && receivedActive {
			return latestReceived, fmt.Errorf("双方已申请，等待任一方处理")
		}
		// A single older outgoing request is deliberately not a hard block.
		// The recipient may have cleared its request history, so the user must
		// be able to send a fresh request_id. prepareNewFriendRequest below
		// supersedes the old outgoing row before persisting the new one.
	}
	// A new outgoing request supersedes only the previous outgoing request;
	// the latest incoming request, if any, remains for mutual projection.
	_ = e.prepareNewFriendRequest(ctx, deviceID, "sent", "")
	request := FriendRequest{RequestID: newID(), DeviceID: deviceID, Nickname: peer.Nickname, Message: strings.TrimSpace(message), Status: "queued", Direction: "sent", CreatedAt: nowString()}
	if err := SaveFriendRequest(ctx, request); err != nil {
		return FriendRequest{}, err
	}
	if err := e.sendToPeer(peer, wireMessage{Type: "friend_request", RequestID: request.RequestID, Content: request.Message}); err != nil {
		if current, ok := friendRequestByID(request.RequestID); ok && isActiveFriendRequest(current.Status) {
			request = current
			request.Status = "failed"
			_ = UpdateFriendRequest(ctx, request.RequestID, "failed")
		}
		e.emit("chat:friend-request-updated", request)
		return request, err
	}
	log.Printf("好友申请已送达: device=%s, request=%s", deviceID, request.RequestID)
	// Keep the removal tombstone until the request is accepted. This prevents
	// a delayed friend_removed frame from being mistaken for a new friendship;
	// AcceptFriendRequest clears it after creating a fresh relationship version.
	if current, ok := friendRequestByID(request.RequestID); ok {
		// A fast response may have already resolved this exact request. Do not
		// let the sender's late bookkeeping overwrite pending/accepted/rejected.
		if current.Status == "queued" {
			_ = UpdateFriendRequest(ctx, request.RequestID, "sent")
			current.Status = "sent"
		}
		request = current
	} else {
		return request, fmt.Errorf("好友申请状态丢失")
	}
	e.emit("chat:friend-request-updated", request)
	return request, nil
}

// NotifyFriendRemoved informs an online peer that this device has ended the
// friendship. It is best-effort: the local removal still completes when the
// peer is offline, and the next ordinary message will receive the authoritative
// FRIENDSHIP_REQUIRED response.
func (e *Engine) NotifyFriendRemoved(deviceID string) error {
	peer, err := e.latestPeer(context.Background(), strings.TrimSpace(deviceID))
	if err != nil {
		return err
	}
	version := peer.RelationshipVersion
	if version == "" {
		version = newID()
		_ = SetPeerRelationshipVersion(context.Background(), peer.DeviceID, version)
		e.setPeerRelationshipVersion(peer.DeviceID, version)
	}
	return e.sendToPeer(peer, wireMessage{Type: "friend_removed", RelationshipVersion: version, RemovedAt: nowString()})
}

func (e *Engine) AcceptFriendRequest(ctx context.Context, requestID string) error {
	requests, err := listFriendRequestRows(ctx, "")
	if err != nil {
		return err
	}
	var target *FriendRequest
	for index := range requests {
		if requests[index].RequestID == requestID {
			target = &requests[index]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("好友申请不存在")
	}
	if target.Status != "pending" || target.Direction == "sent" {
		return fmt.Errorf("好友申请已处理")
	}
	if err := ClearFriendRemoval(ctx, target.DeviceID); err != nil {
		return err
	}
	relationshipVersion := newID()
	if err := SetPeerRelation(ctx, target.DeviceID, PeerRelation); err != nil {
		return err
	}
	if err := SetPeerRelationshipVersion(ctx, target.DeviceID, relationshipVersion); err != nil {
		return err
	}
	if err := SetPeerVisibleInFriends(ctx, target.DeviceID, true); err != nil {
		return err
	}
	e.clearLocallyHiddenFriend(target.DeviceID)
	e.updatePeerRelation(target.DeviceID, PeerRelation)
	e.setPeerVisibleInFriends(target.DeviceID, true)
	acceptedAt := nowString()
	if err := UpdateFriendRequestAccepted(ctx, target.RequestID, acceptedAt); err != nil {
		return err
	}
	if err := e.supersedeActiveFriendRequestsExcept(ctx, target.DeviceID, target.RequestID); err != nil {
		return err
	}
	if peer, peerErr := e.peer(target.DeviceID); peerErr == nil {
		_ = e.sendToPeer(peer, wireMessage{Type: "friend_request_response", RequestID: target.RequestID, Status: "accepted", AcceptedAt: acceptedAt, RelationshipVersion: relationshipVersion})
	}
	if updated, ok := friendRequestByID(target.RequestID); ok {
		e.emit("chat:friend-request-updated", updated)
	}
	e.emit("chat:peer-updated", e.Peers())
	return nil
}

func (e *Engine) RejectFriendRequest(ctx context.Context, requestID string) error {
	requests, err := listFriendRequestRows(ctx, "")
	if err != nil {
		return err
	}
	var target *FriendRequest
	for index := range requests {
		if requests[index].RequestID == requestID {
			target = &requests[index]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("好友申请不存在")
	}
	if target.Status != "pending" || target.Direction == "sent" {
		return fmt.Errorf("好友申请已处理")
	}
	if err := UpdateFriendRequest(ctx, target.RequestID, "rejected"); err != nil {
		return err
	}
	if err := e.supersedeActiveFriendRequestsExcept(ctx, target.DeviceID, target.RequestID); err != nil {
		return err
	}
	if peer, peerErr := e.peer(target.DeviceID); peerErr == nil {
		_ = e.sendToPeer(peer, wireMessage{Type: "friend_request_response", RequestID: target.RequestID, Status: "rejected"})
	}
	if updated, ok := friendRequestByID(target.RequestID); ok {
		e.emit("chat:friend-request-updated", updated)
	}
	return nil
}

func (e *Engine) SendMessage(ctx context.Context, deviceID, content string) (Message, error) {
	return e.SendMessageWithMetadata(ctx, deviceID, content, "", "", "")
}

func (e *Engine) SendMessageWithMetadata(ctx context.Context, deviceID, content, quoteMessageID, quoteContent, forwardedFrom string) (Message, error) {
	if strings.TrimSpace(content) == "" {
		return Message{}, fmt.Errorf("消息不能为空")
	}
	if !e.isFriend(deviceID) {
		return Message{}, fmt.Errorf("不是好友")
	}
	conversationID, err := EnsureConversation(ctx, deviceID)
	if err != nil {
		return Message{}, err
	}
	message := Message{MessageID: newID(), ConversationID: conversationID, SenderDeviceID: e.identity.DeviceID, Kind: "text", Content: content, Status: "sending", CreatedAt: nowString(), QuoteMessageID: quoteMessageID, QuoteContent: quoteContent, ForwardedFrom: forwardedFrom}
	if err := SaveMessage(ctx, message); err != nil {
		return Message{}, err
	}
	e.emit("chat:message", message)
	wire := wireMessage{Type: "message", MessageID: message.MessageID, Kind: "text", Content: content, QuoteMessageID: quoteMessageID, QuoteContent: quoteContent, ForwardedFrom: forwardedFrom}
	peer, err := e.peer(deviceID)
	if err != nil {
		message.Status = "failed"
		_ = UpdateMessageStatus(ctx, message.MessageID, message.Status)
		e.emit("chat:message", message)
		return message, nil
	}
	if err := e.sendToPeer(peer, wire); err != nil {
		message.Status = sendFailureStatus(err)
	} else {
		message.Status = "sent"
	}
	_ = exec(ctx, `UPDATE messages SET status=? WHERE message_id=?`, message.Status, message.MessageID)
	e.emit("chat:message", message)
	return message, nil
}

func (e *Engine) RetryMessage(ctx context.Context, messageID string) (Message, error) {
	message, err := GetMessage(ctx, messageID)
	if err != nil {
		return Message{}, err
	}
	e.mu.RLock()
	localDeviceID := e.identity.DeviceID
	e.mu.RUnlock()
	if message.SenderDeviceID != localDeviceID || message.Kind != "text" {
		return Message{}, fmt.Errorf("该消息不支持重发")
	}
	if message.Status == "sent" {
		return message, nil
	}
	if message.Status == "sending" {
		return Message{}, fmt.Errorf("消息正在发送")
	}
	deviceID := strings.TrimPrefix(message.ConversationID, "conv-")
	if !e.isFriend(deviceID) {
		return Message{}, fmt.Errorf("不是好友")
	}
	message.Status = "sending"
	if err := UpdateMessageStatus(ctx, message.MessageID, message.Status); err != nil {
		return Message{}, err
	}
	e.emit("chat:message", message)
	wire := wireMessage{Type: "message", MessageID: message.MessageID, Kind: "text", Content: message.Content, QuoteMessageID: message.QuoteMessageID, QuoteContent: message.QuoteContent, ForwardedFrom: message.ForwardedFrom}
	peer, err := e.peer(deviceID)
	if err != nil {
		status := sendFailureStatus(err)
		result := e.finishTextRetry(ctx, message, status)
		if status == "not_friend" {
			return result, nil
		}
		return result, err
	}
	if err := e.sendToPeer(peer, wire); err != nil {
		status := sendFailureStatus(err)
		result := e.finishTextRetry(ctx, message, status)
		if status == "not_friend" {
			return result, nil
		}
		return result, err
	}
	return e.finishTextRetry(ctx, message, "sent"), nil
}

func (e *Engine) finishTextRetry(ctx context.Context, message Message, status string) Message {
	message.Status = status
	_ = UpdateMessageStatus(ctx, message.MessageID, status)
	e.emit("chat:message", message)
	return message
}

func (e *Engine) MarkConversationRead(ctx context.Context, deviceID string) error {
	if !e.isFriend(deviceID) {
		return fmt.Errorf("不是好友")
	}
	// Reading a conversation must not change whether the peer is shown in the
	// main friends list.  In particular, an older asynchronous read operation
	// can finish after HideFriendAndClearLocalData and must not resurrect the
	// hidden row.  Restoring a hidden friend is an explicit Contacts action and
	// is handled by RestoreHiddenFriend/SetPeerVisibleInFriends instead.
	conversationID, err := EnsureConversation(ctx, deviceID)
	if err != nil {
		return err
	}
	messages, err := ListMessages(ctx, conversationID)
	if err != nil {
		return err
	}
	readIDs := make([]string, 0)
	for _, message := range messages {
		if message.SenderDeviceID != deviceID || message.Status == "read" {
			continue
		}
		if err := UpdateMessageStatus(ctx, message.MessageID, "read"); err != nil {
			return err
		}
		readIDs = append(readIDs, message.MessageID)
	}
	if len(readIDs) == 0 {
		_ = ClearConversationUnread(ctx, conversationID)
		return nil
	}
	if err := ClearConversationUnread(ctx, conversationID); err != nil {
		return err
	}
	peer, err := e.peer(deviceID)
	if err != nil {
		return err
	}
	return e.sendToPeer(peer, wireMessage{Type: "read_receipt", MessageIDs: readIDs})
}

func (e *Engine) SendFile(ctx context.Context, deviceID, path string) (Message, error) {
	if e.IsAttachmentMigrationActive() {
		return Message{}, fmt.Errorf("附件迁移正在进行")
	}
	if !e.isFriend(deviceID) {
		return Message{}, fmt.Errorf("不是好友")
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return Message{}, fmt.Errorf("文件不存在")
	}
	fileName := safeFileName(filepath.Base(path))
	conversationID, err := EnsureConversation(ctx, deviceID)
	if err != nil {
		return Message{}, err
	}
	messageID, attachmentID := newID(), newID()
	attachmentMime := mime.TypeByExtension(filepath.Ext(fileName))
	if attachmentMime == "" {
		attachmentMime = "application/octet-stream"
	}
	isImage := strings.HasPrefix(attachmentMime, "image/")
	initialStatus := "sending"
	if isImage {
		initialStatus = "preparing_thumbnail"
	}
	message := Message{MessageID: messageID, ConversationID: conversationID, SenderDeviceID: e.identity.DeviceID, Kind: "file", Content: fileName, Status: initialStatus, CreatedAt: nowString(), AttachmentID: attachmentID, AttachmentName: fileName, AttachmentSize: info.Size(), AttachmentMime: attachmentMime, AttachmentStatus: initialStatus, AttachmentPath: path}
	if err := SaveMessage(ctx, message); err != nil {
		return Message{}, err
	}
	if err := SaveAttachment(ctx, Attachment{AttachmentID: attachmentID, MessageID: messageID, FileName: fileName, MimeType: message.AttachmentMime, FileSize: info.Size(), LocalPath: path, Status: initialStatus}); err != nil {
		return Message{}, err
	}
	cancel := make(chan struct{})
	e.mu.Lock()
	e.preparing[attachmentID] = &preparingAttachment{cancel: cancel}
	e.mu.Unlock()
	defer e.removePreparingAttachment(attachmentID)
	e.emit("chat:message", message)
	if isImage {
		e.emitTransferProgress(message.MessageID, attachmentID, deviceID, 0, message.AttachmentSize, "send", "preparing_thumbnail")
	}
	sum, thumbnailData, thumbnailMime, prepareErr := e.prepareOutgoingAttachment(path, message.AttachmentMime, cancel)
	if prepareErr != nil {
		status := "failed"
		if errors.Is(prepareErr, errAttachmentCanceled) {
			status = "canceled"
		}
		result := e.finishAttachmentSend(ctx, message, status)
		if status == "canceled" {
			return result, nil
		}
		return result, prepareErr
	}
	message.AttachmentThumbnail, message.AttachmentThumbnailMime = thumbnailData, thumbnailMime
	message.Status, message.AttachmentStatus = "pending", "pending"
	if e.isPreparingCanceled(attachmentID) {
		result := e.finishAttachmentSend(ctx, message, "canceled")
		return result, nil
	}
	if err := SaveMessage(ctx, message); err != nil {
		return Message{}, err
	}
	if err := SaveAttachment(ctx, Attachment{AttachmentID: attachmentID, MessageID: messageID, FileName: fileName, MimeType: message.AttachmentMime, FileSize: info.Size(), SHA256: sum, ThumbnailData: thumbnailData, ThumbnailMime: thumbnailMime, LocalPath: path, Status: "pending"}); err != nil {
		return Message{}, err
	}
	e.emit("chat:message", message)
	e.emitTransferProgress(message.MessageID, attachmentID, deviceID, 0, message.AttachmentSize, "send", "awaiting_acceptance")
	if err := e.transferFile(ctx, deviceID, message, path, sum); err != nil {
		status := sendFailureStatus(err)
		if errors.Is(err, errAttachmentCanceled) {
			status = "canceled"
		} else if errors.Is(err, errAttachmentRejected) {
			status = "rejected"
		}
		result := e.finishAttachmentSend(ctx, message, status)
		if status == "not_friend" {
			return result, nil
		}
		if status != "failed" {
			return result, nil
		}
		return result, err
	}
	return e.finishAttachmentSend(ctx, message, "sent"), nil
}

type attachmentHashResult struct {
	sum string
	err error
}

type attachmentThumbnailResult struct {
	data string
	mime string
	err  error
}

func (e *Engine) prepareOutgoingAttachment(path, mimeType string, cancel <-chan struct{}) (string, string, string, error) {
	hashResult := make(chan attachmentHashResult, 1)
	go func() {
		sum, err := hashTransferFile(path, cancel)
		hashResult <- attachmentHashResult{sum: sum, err: err}
	}()

	var thumbnailResult attachmentThumbnailResult
	if strings.HasPrefix(mimeType, "image/") {
		thumbnailResultChannel := make(chan attachmentThumbnailResult, 1)
		go func() {
			data, thumbnailMime, err := buildImageThumbnail(path, mimeType)
			thumbnailResultChannel <- attachmentThumbnailResult{data: data, mime: thumbnailMime, err: err}
		}()
		thumbnailResult = <-thumbnailResultChannel
	}
	hash := <-hashResult
	if hash.err != nil {
		if errors.Is(hash.err, errAttachmentCanceled) {
			return "", "", "", errAttachmentCanceled
		}
		return "", "", "", hash.err
	}
	select {
	case <-cancel:
		return "", "", "", errAttachmentCanceled
	default:
	}
	// A thumbnail failure must not prevent the original attachment from being
	// sent. The receiver will show its normal image placeholder in that case.
	if thumbnailResult.err != nil {
		thumbnailResult.data, thumbnailResult.mime = "", ""
	}
	return hash.sum, thumbnailResult.data, thumbnailResult.mime, nil
}

func hashTransferFile(path string, cancel <-chan struct{}) (string, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 256*1024)
	for {
		select {
		case <-cancel:
			return "", errAttachmentCanceled
		default:
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			if _, err := hash.Write(buffer[:count]); err != nil {
				return "", err
			}
		}
		if readErr == io.EOF {
			return hex.EncodeToString(hash.Sum(nil)), nil
		}
		if readErr != nil {
			return "", readErr
		}
	}
}

func (e *Engine) removePreparingAttachment(attachmentID string) {
	e.mu.Lock()
	delete(e.preparing, attachmentID)
	e.mu.Unlock()
}

func (e *Engine) isPreparingCanceled(attachmentID string) bool {
	e.mu.RLock()
	preparing := e.preparing[attachmentID]
	canceled := preparing != nil && preparing.canceled
	e.mu.RUnlock()
	return canceled
}

func (e *Engine) RetryAttachment(ctx context.Context, messageID string) (Message, error) {
	if e.IsAttachmentMigrationActive() {
		return Message{}, fmt.Errorf("附件迁移正在进行")
	}
	message, err := GetMessage(ctx, messageID)
	if err != nil {
		return Message{}, err
	}
	e.mu.RLock()
	localDeviceID := e.identity.DeviceID
	e.mu.RUnlock()
	if message.SenderDeviceID != localDeviceID || message.Kind != "file" || message.AttachmentID == "" {
		return Message{}, fmt.Errorf("该消息不支持重发")
	}
	if message.Status == "sent" {
		return message, nil
	}
	if message.Status == "sending" {
		return Message{}, fmt.Errorf("文件正在发送")
	}
	if !e.isFriend(strings.TrimPrefix(message.ConversationID, "conv-")) {
		return Message{}, fmt.Errorf("不是好友")
	}
	info, sum, err := inspectTransferFile(message.AttachmentPath)
	if err != nil {
		return Message{}, err
	}
	attachment, attachmentErr := GetAttachment(ctx, message.AttachmentID)
	if attachmentErr != nil {
		return Message{}, attachmentErr
	}
	// The original checksum and size are authoritative. A changed source file
	// must be selected again instead of sending different bytes under the same ID.
	if info.Size() != message.AttachmentSize || (attachment.SHA256 != "" && attachment.SHA256 != sum) {
		return Message{}, fmt.Errorf("原文件内容已变化，请重新选择文件")
	}
	message.Status, message.AttachmentStatus = "sending", "sending"
	if err := UpdateMessageStatus(ctx, message.MessageID, message.Status); err != nil {
		return Message{}, err
	}
	if err := SaveAttachment(ctx, Attachment{AttachmentID: message.AttachmentID, MessageID: message.MessageID, FileName: message.AttachmentName, MimeType: message.AttachmentMime, FileSize: message.AttachmentSize, SHA256: sum, ThumbnailData: message.AttachmentThumbnail, ThumbnailMime: message.AttachmentThumbnailMime, LocalPath: message.AttachmentPath, Status: "sending"}); err != nil {
		return Message{}, err
	}
	e.emit("chat:message", message)
	if err := e.transferFile(ctx, strings.TrimPrefix(message.ConversationID, "conv-"), message, message.AttachmentPath, sum); err != nil {
		status := sendFailureStatus(err)
		if errors.Is(err, errAttachmentCanceled) {
			status = "canceled"
		} else if errors.Is(err, errAttachmentRejected) {
			status = "rejected"
		}
		result := e.finishAttachmentSend(ctx, message, status)
		if status == "not_friend" {
			return result, nil
		}
		if status != "failed" {
			return result, nil
		}
		return result, err
	}
	return e.finishAttachmentSend(ctx, message, "sent"), nil
}

func inspectTransferFile(path string) (os.FileInfo, string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, "", fmt.Errorf("文件路径为空")
	}
	info, err := os.Stat(filepath.Clean(path))
	if err != nil || info.IsDir() {
		return nil, "", fmt.Errorf("文件不存在")
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return nil, "", err
	}
	return info, hex.EncodeToString(hash.Sum(nil)), nil
}

func (e *Engine) transferFile(ctx context.Context, deviceID string, message Message, path, sum string) error {
	peer, err := e.peer(deviceID)
	if err != nil {
		return err
	}
	var lastErr error
	for _, dialect := range protocolDialectsForPeer(peer) {
		if err := e.transferFileWithDialect(ctx, peer, message, path, sum, dialect); err == nil {
			return nil
		} else {
			// Cancellation and rejection are intentional terminal outcomes.
			// Do not fall through to another protocol dialect after the user
			// has stopped the transfer or the receiver has refused it.
			if errors.Is(err, errAttachmentCanceled) || errors.Is(err, errAttachmentRejected) {
				return err
			}
			lastErr = err
		}
	}
	return lastErr
}

func (e *Engine) transferFileWithDialect(ctx context.Context, peer Peer, message Message, path, sum string, dialect ProtocolDialect) error {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return err
	}
	defer file.Close()
	clientTLS, err := e.clientTLSConfig()
	if err != nil {
		return err
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", net.JoinHostPort(peer.IP, fmt.Sprint(peer.Port)), clientTLS)
	if err != nil {
		return err
	}
	defer conn.Close()
	configureTCPConnection(conn)
	if err := verifyPeerCertificate(conn, peer); err != nil {
		return err
	}
	session := newWireSession(conn)
	e.mu.Lock()
	e.outgoing[message.AttachmentID] = &outgoingTransfer{message: message, peerID: peer.DeviceID, session: session, createdAt: time.Now(), data: make(map[int]*wireSession)}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		if current := e.outgoing[message.AttachmentID]; current != nil && current.session == session {
			delete(e.outgoing, message.AttachmentID)
		}
		e.mu.Unlock()
	}()
	reader := newWireReader(conn)
	hello := e.helloMessageForDialect("hello", dialect)
	if err := writeWire(conn, hello); err != nil {
		return err
	}
	var response wireMessage
	if err := reader.Decode(&response); err != nil {
		if session.isCanceled() {
			return errAttachmentCanceled
		}
		return fmt.Errorf("对方握手失败")
	}
	if response.Type == "error" {
		if isFriendshipRejection(response.Status) {
			e.handleRemoteFriendshipRequired(peer.DeviceID)
		}
		return fmt.Errorf("对方握手失败: %s", response.Status)
	}
	if response.Type != "hello_ack" {
		return fmt.Errorf("对方握手失败")
	}
	responseDialect, responseCompatible := protocolDialectForMessage(response)
	if !responseCompatible {
		return fmt.Errorf("对方握手协议不兼容")
	}
	if response.FriendshipState == "removed" {
		if e.shouldApplyFriendRemoval(peer.DeviceID, response.RelationshipVersion) {
			e.handleRemoteFriendshipRequired(peer.DeviceID)
		} else {
			log.Printf("忽略旧的远端解除好友状态: device=%s, remote_version=%s, local_version=%s", peer.DeviceID, response.RelationshipVersion, peer.RelationshipVersion)
		}
		return fmt.Errorf("FRIENDSHIP_REQUIRED")
	}
	e.rememberPeerDialect(peer.DeviceID, responseDialect, response.Capabilities)
	supportsProgress := hasCapability(response.Capabilities, "file-progress-v1") && responseDialect.Major >= 2
	supportsWindowed := supportsProgress && hasCapability(response.Capabilities, fileWindowCapability) && responseDialect.Major >= 2
	supportsBinary := supportsWindowed && hasCapability(response.Capabilities, fileStreamCapability)
	supportsParallel := supportsProgress && hasCapability(response.Capabilities, fileParallelCapability) && responseDialect.Major >= 2
	supportsPreflight := hasCapability(response.Capabilities, "storage-preflight-v1") && responseDialect.Major >= 2
	transferMode := legacyTransferMode
	if supportsWindowed {
		transferMode = jsonWindowTransferMode
	}
	if supportsBinary {
		transferMode = binaryTransferMode
	}
	if supportsParallel {
		transferMode = parallelBinaryMode
	}
	hasSavedTuning := e.hasTransferTuningForPeer(peer.DeviceID)
	tuning := e.transferTuningForPeer(peer.DeviceID)
	if !supportsWindowed {
		tuning = transferTuning{chunkSize: defaultTransferChunkSize, windowSize: 1}
	} else if supportsBinary && (!hasSavedTuning || !tuning.binary) {
		tuning = transferTuning{chunkSize: defaultBinaryChunkSize, windowSize: binaryInitialWindow, binary: true}
	} else if supportsBinary {
		// A previous run may have backed off while the peer was temporarily
		// busy. Do not let that transient state become the permanent starting
		// point for every large transfer; binary mode always probes with the
		// current high-throughput baseline and can back off again if needed.
		tuning.binary = true
		if tuning.chunkSize < defaultBinaryChunkSize {
			tuning.chunkSize = defaultBinaryChunkSize
		}
		if tuning.windowSize < binaryInitialWindow {
			tuning.windowSize = binaryInitialWindow
		}
	} else if !supportsBinary {
		tuning.binary = false
		if tuning.chunkSize == mediumTransferChunkSize {
			tuning.chunkSize = minTransferChunkSize
		}
	}
	e.touchPeer(peer.DeviceID)
	if !peer.Online {
		e.emit("chat:peer-updated", e.Peers())
	}
	if response.FriendshipState != "friend" {
		if err := e.writeFriendRestoreIfNeeded(conn, peer, responseDialect); err != nil {
			return err
		}
	}
	if e.isPreparingCanceled(message.AttachmentID) {
		return errAttachmentCanceled
	}
	offer := wireMessage{Type: "file_offer", MessageID: message.MessageID, AttachmentID: message.AttachmentID, FileName: message.AttachmentName, MimeType: message.AttachmentMime, FileSize: message.AttachmentSize, SHA256: sum, ThumbnailData: message.AttachmentThumbnail, ThumbnailMime: message.AttachmentThumbnailMime}
	if supportsWindowed {
		offer.ChunkSize, offer.WindowSize = tuning.chunkSize, tuning.windowSize
		offer.WindowID, offer.WindowBytes = 0, int64(tuning.chunkSize*tuning.windowSize)
		offer.TransferMode = transferMode
	}
	if supportsParallel {
		offer.TransferToken = newID()
		offer.StreamCount = parallelStreamCount(message.AttachmentSize)
		offer.ChunkSize = parallelChunkSize
		offer.WindowBytes = parallelAckBytes
		offer.TransferMode = parallelBinaryMode
	}
	if err := session.write(offer); err != nil {
		return err
	}
	supportsDemand := hasCapability(response.Capabilities, "attachment-demand-v1") && responseDialect.Major >= 2
	if supportsDemand || supportsPreflight {
		offerResponse, err := readFileOfferResponse(reader, message.AttachmentID)
		if err != nil {
			if session.isCanceled() {
				return errAttachmentCanceled
			}
			return err
		}
		if offerResponse.Status == "pending" {
			_ = e.markOutgoingPending(ctx, message)
			_ = conn.SetDeadline(time.Now().Add(10 * time.Minute))
			offerResponse, err = readAttachmentDecision(reader, message.AttachmentID)
			if err != nil {
				if session.isCanceled() {
					return errAttachmentCanceled
				}
				return err
			}
			_ = conn.SetDeadline(time.Time{})
		}
		if offerResponse.Type == "file_cancel" || offerResponse.Status == "canceled" {
			return errAttachmentCanceled
		}
		if offerResponse.Type == "file_reject" || offerResponse.Status == "rejected" {
			return errAttachmentRejected
		}
		if offerResponse.Status != "accepted" {
			if offerResponse.Reason == "INSUFFICIENT_STORAGE" {
				return fmt.Errorf("对方设备存储空间不足（可用 %s，需要 %s）", formatBytes(offerResponse.AvailableBytes), formatBytes(offerResponse.RequiredBytes))
			}
			return fmt.Errorf("对方拒绝接收文件: %s", offerResponse.Reason)
		}
	}
	if session.isCanceled() {
		return errAttachmentCanceled
	}
	protocolLabel := fmt.Sprintf("%s/%d", responseDialect.Name, responseDialect.Major)
	if supportsParallel {
		return e.transferParallelFile(ctx, peer, message, file, session, reader, dialect, offer.TransferToken, offer.StreamCount, protocolLabel)
	}
	progressOptions := transferProgressOptions{chunkSize: tuning.chunkSize, windowSize: tuning.windowSize, windowBytes: int64(tuning.chunkSize * tuning.windowSize), transferMode: transferMode, displayLocalMetrics: transferMode == legacyTransferMode, transport: "TLS/TCP", protocol: protocolLabel, tuningState: "probing"}
	e.emitTransferProgress(message.MessageID, message.AttachmentID, peer.DeviceID, 0, message.AttachmentSize, "send", "transferring", progressOptions)
	if supportsProgress {
		e.emitTransferProgress(message.MessageID, message.AttachmentID, peer.DeviceID, 0, message.AttachmentSize, "remote-receive", "receiving", progressOptions)
	}
	var sent, lastProgress int64
	windowID, chunkIndex := 0, 0
	lastThroughput := 0.0
	var legacyBuffer []byte
	if !supportsWindowed {
		pooled := fileChunkBufferPool.Get().([]byte)
		defer fileChunkBufferPool.Put(pooled)
		legacyBuffer = pooled[:defaultTransferChunkSize]
	}
	acknowledge := func(options transferProgressOptions) (wireMessage, error) {
		progress, err := readFileProgress(reader, message.AttachmentID)
		if err != nil {
			if session.isCanceled() {
				return wireMessage{}, errAttachmentCanceled
			}
			return wireMessage{}, err
		}
		phase := "receiving"
		if progress.Status == "failed" {
			phase = "failed"
		}
		if progress.Status == "canceled" {
			return wireMessage{}, errAttachmentCanceled
		}
		e.emitTransferProgress(message.MessageID, message.AttachmentID, peer.DeviceID, progress.Transferred, message.AttachmentSize, "remote-receive", phase, options)
		if progress.Status == "failed" {
			return wireMessage{}, fmt.Errorf("对方接收文件失败")
		}
		return progress, nil
	}
	if supportsBinary {
		tuning, err = e.transferBinaryFilePipelined(ctx, peer.DeviceID, message, file, session, reader, tuning, protocolLabel)
		if err != nil {
			return err
		}
	} else {
		for {
			if session.isCanceled() {
				return errAttachmentCanceled
			}
			if supportsWindowed {
				windowSize := tuning.windowSize
				windowStart := time.Now()
				var chunks int
				var windowBytes int64
				var writeErr error
				if supportsBinary {
					chunks, windowBytes, writeErr = session.writeBinaryFileWindow(file, message.AttachmentID, windowID, chunkIndex, tuning.chunkSize, windowSize, message.AttachmentSize-sent)
				} else {
					chunks, windowBytes, writeErr = session.writeFileWindow(file, message.AttachmentID, windowID, chunkIndex, tuning.chunkSize, windowSize)
				}
				if writeErr != nil {
					if errors.Is(writeErr, errAttachmentCanceled) || session.isCanceled() {
						return errAttachmentCanceled
					}
					return writeErr
				}
				if chunks == 0 {
					break
				}
				chunkIndex += chunks
				sent += windowBytes
				writeFinished := time.Now()
				progressOptions = transferProgressOptions{chunkSize: tuning.chunkSize, windowSize: windowSize, windowBytes: windowBytes, transferMode: transferMode, displayLocalMetrics: transferMode == legacyTransferMode, transport: "TLS/TCP", protocol: protocolLabel, tuningState: "stable"}
				progress, ackErr := acknowledge(progressOptions)
				if ackErr != nil {
					return ackErr
				}
				if progress.WindowID != windowID {
					return fmt.Errorf("文件窗口确认无效")
				}
				ackLatency := time.Since(writeFinished)
				windowDuration := time.Since(windowStart).Seconds()
				throughput := float64(windowBytes) / windowDuration
				progressOptions.ackLatency = ackLatency
				progressOptions.diskWriteMs = progress.DiskWriteMs
				progressOptions.windowThroughput = throughput
				progressOptions.tuningState = "stable"
				if sent-lastProgress >= int64(tuning.chunkSize) || sent == message.AttachmentSize {
					e.emitTransferProgress(message.MessageID, message.AttachmentID, peer.DeviceID, sent, message.AttachmentSize, "send", "transferring", progressOptions)
					lastProgress = sent
				}
				tuning, progressOptions.tuningState, progressOptions.tuningReason = adjustTransferTuning(tuning, ackLatency, progress.DiskWriteMs, throughput, lastThroughput, supportsBinary)
				if progressOptions.tuningState != "stable" || progressOptions.tuningReason != "" {
					// Publish the decision after measuring this window so the details
					// panel shows the actual adjustment and its reason immediately.
					e.emitTransferProgress(message.MessageID, message.AttachmentID, peer.DeviceID, sent, message.AttachmentSize, "send", "transferring", progressOptions)
					log.Printf("文件传输调优: peer=%s mode=%s chunk=%d window=%d throughput=%.1fMB/s ack=%s disk=%dms state=%s", peer.DeviceID, transferMode, tuning.chunkSize, tuning.windowSize, throughput/(1024*1024), ackLatency, progress.DiskWriteMs, progressOptions.tuningState)
				}
				lastThroughput = throughput
				tuning.binary = false
				e.rememberTransferTuning(peer.DeviceID, tuning)
				windowID++
				if progress.Transferred >= message.AttachmentSize || sent >= message.AttachmentSize {
					break
				}
				continue
			}

			n, readErr := file.Read(legacyBuffer)
			if n > 0 {
				if err := session.write(wireMessage{Type: "file_chunk", AttachmentID: message.AttachmentID, ChunkIndex: chunkIndex, Payload: base64.StdEncoding.EncodeToString(legacyBuffer[:n])}); err != nil {
					if session.isCanceled() {
						return errAttachmentCanceled
					}
					return err
				}
				chunkIndex++
				sent += int64(n)
				if err := func() error {
					if !supportsProgress {
						return nil
					}
					_, err := acknowledge(transferProgressOptions{chunkSize: defaultTransferChunkSize, windowSize: 1, windowBytes: int64(n), transferMode: transferMode, displayLocalMetrics: transferMode == legacyTransferMode, transport: "TLS/TCP", protocol: protocolLabel})
					return err
				}(); err != nil {
					return err
				}
				if sent-lastProgress >= int64(defaultTransferChunkSize) || sent == message.AttachmentSize {
					e.emitTransferProgress(message.MessageID, message.AttachmentID, peer.DeviceID, sent, message.AttachmentSize, "send", "transferring", transferProgressOptions{chunkSize: defaultTransferChunkSize, windowSize: 1, windowBytes: int64(n), transferMode: transferMode, displayLocalMetrics: transferMode == legacyTransferMode, transport: "TLS/TCP", protocol: protocolLabel})
					lastProgress = sent
				}
			}
			if readErr == io.EOF || sent >= message.AttachmentSize {
				break
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	if supportsBinary {
		return nil
	}
	if session.isCanceled() {
		return errAttachmentCanceled
	}
	if err := session.write(wireMessage{Type: "file_complete", MessageID: message.MessageID, AttachmentID: message.AttachmentID, FileSize: message.AttachmentSize}); err != nil {
		if session.isCanceled() {
			return errAttachmentCanceled
		}
		return err
	}
	if supportsProgress {
		progress, err := readFileProgress(reader, message.AttachmentID)
		if err != nil {
			if session.isCanceled() {
				return errAttachmentCanceled
			}
			return err
		}
		phase := "completed"
		if progress.Status == "failed" {
			phase = "failed"
		}
		verified := progress.Status == "completed"
		e.emitTransferProgress(message.MessageID, message.AttachmentID, peer.DeviceID, progress.Transferred, message.AttachmentSize, "remote-receive", phase, transferProgressOptions{chunkSize: tuning.chunkSize, windowSize: tuning.windowSize, windowBytes: progress.WindowBytes, diskWriteMs: progress.DiskWriteMs, transferMode: transferMode, transport: "TLS/TCP", protocol: protocolLabel, verified: &verified})
		if progress.Status == "failed" {
			return fmt.Errorf("对方接收文件失败")
		}
		if progress.Status == "canceled" {
			return errAttachmentCanceled
		}
	}
	return nil
}

type parallelStreamProgress struct {
	streamID     int
	sent         int64
	confirmed    int64
	length       int64
	writeMs      int64
	ackLatencyMs int64
	diskWriteMs  int64
	acknowledged bool
	done         bool
	err          error
}

type parallelStreamAck struct {
	ack        wireMessage
	receivedAt time.Time
	err        error
}

func (e *Engine) openParallelDataStream(ctx context.Context, peer Peer, dialect ProtocolDialect, message Message, token string, streamID, streamCount int, offset, length int64, chunkSize int) (*wireSession, *wireReader, error) {
	clientTLS, err := e.clientTLSConfig()
	if err != nil {
		return nil, nil, err
	}
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: clientTLS}
	rawConn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(peer.IP, fmt.Sprint(peer.Port)))
	if err != nil {
		return nil, nil, err
	}
	conn, ok := rawConn.(*tls.Conn)
	if !ok {
		_ = rawConn.Close()
		return nil, nil, fmt.Errorf("并行数据连接不是 TLS")
	}
	configureTCPConnection(conn)
	tuneTCPBuffers(conn, parallelInitialInFlight)
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = conn.Close()
		}
	}()
	if err := verifyPeerCertificate(conn, peer); err != nil {
		return nil, nil, err
	}
	session := newWireSession(conn)
	reader := newWireReader(conn)
	if err := writeWire(conn, e.helloMessageForDialect("hello", dialect)); err != nil {
		return nil, nil, err
	}
	var response wireMessage
	if err := reader.Decode(&response); err != nil || response.Type != "hello_ack" {
		if err == nil {
			err = fmt.Errorf("并行数据连接握手失败")
		}
		return nil, nil, err
	}
	if response.FriendshipState == "removed" || !hasCapability(response.Capabilities, fileParallelCapability) {
		return nil, nil, fmt.Errorf("对方不支持并行文件传输")
	}
	join := wireMessage{Type: "file_stream_join", AttachmentID: message.AttachmentID, TransferToken: token, StreamID: streamID, StreamCount: streamCount, StreamOffset: offset, StreamLength: length, ChunkSize: chunkSize, TransferMode: parallelBinaryMode}
	if err := session.write(join); err != nil {
		return nil, nil, err
	}
	var ack wireMessage
	if err := reader.Decode(&ack); err != nil {
		return nil, nil, err
	}
	if ack.Type != "file_stream_join_ack" || ack.Status != "accepted" || ack.AttachmentID != message.AttachmentID || ack.TransferToken != token || ack.StreamID != streamID {
		return nil, nil, fmt.Errorf("并行数据流加入失败: %s", ack.Reason)
	}
	closeOnError = false
	_ = conn.SetDeadline(time.Time{})
	return session, reader, nil
}

func (e *Engine) registerOutgoingData(attachmentID string, streamID int, session *wireSession) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	transfer := e.outgoing[attachmentID]
	if transfer == nil || transfer.session.isCanceled() {
		return fmt.Errorf("文件传输已结束")
	}
	transfer.dataMu.Lock()
	defer transfer.dataMu.Unlock()
	if transfer.data == nil {
		transfer.data = make(map[int]*wireSession)
	}
	transfer.data[streamID] = session
	return nil
}

func (e *Engine) closeOutgoingData(attachmentID string) {
	e.mu.RLock()
	transfer := e.outgoing[attachmentID]
	e.mu.RUnlock()
	if transfer == nil {
		return
	}
	transfer.dataMu.Lock()
	sessions := make([]*wireSession, 0, len(transfer.data))
	for id, session := range transfer.data {
		delete(transfer.data, id)
		sessions = append(sessions, session)
	}
	transfer.dataMu.Unlock()
	for _, session := range sessions {
		if session != nil {
			session.close()
		}
	}
}

func readParallelStreamAck(reader *wireReader, message Message, token string, streamID int, sent, confirmed, length int64) (wireMessage, time.Duration, error) {
	started := time.Now()
	for {
		var ack wireMessage
		if err := reader.Decode(&ack); err != nil {
			return wireMessage{}, time.Since(started), err
		}
		if ack.Type != "file_stream_ack" || ack.AttachmentID != message.AttachmentID || ack.TransferToken != token || ack.StreamID != streamID {
			return wireMessage{}, time.Since(started), fmt.Errorf("并行数据流回执无效")
		}
		if ack.Status == "failed" || ack.Status == "canceled" {
			return ack, time.Since(started), fmt.Errorf("对方并行数据流失败: %s", ack.Reason)
		}
		streamBytes := ack.StreamBytes
		if streamBytes == 0 {
			streamBytes = ack.Transferred
		}
		if streamBytes <= confirmed || streamBytes > sent || streamBytes > length || (ack.Status != "receiving" && ack.Status != "stream-complete") || (ack.Status == "stream-complete") != (streamBytes == length) {
			return wireMessage{}, time.Since(started), fmt.Errorf("并行数据流累计回执越界")
		}
		ack.StreamBytes = streamBytes
		return ack, time.Since(started), nil
	}
}

func (e *Engine) sendParallelStream(ctx context.Context, peer Peer, dialect ProtocolDialect, message Message, file *os.File, token string, streamCount int, streamID int, offset, length int64, chunkSize int, progress chan<- parallelStreamProgress) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	report := func(update parallelStreamProgress) {
		select {
		case progress <- update:
		case <-ctx.Done():
		}
	}
	session, reader, err := e.openParallelDataStream(ctx, peer, dialect, message, token, streamID, streamCount, offset, length, chunkSize)
	if err != nil {
		report(parallelStreamProgress{streamID: streamID, length: length, err: err})
		return
	}
	if err := e.registerOutgoingData(message.AttachmentID, streamID, session); err != nil {
		session.close()
		report(parallelStreamProgress{streamID: streamID, length: length, err: err})
		return
	}
	defer func() {
		session.close()
	}()
	stopCancel := context.AfterFunc(ctx, session.close)
	defer stopCancel()
	buffer := parallelFrameBufferPool.Get().([]byte)
	defer parallelFrameBufferPool.Put(buffer)
	const frameBytes = 4 * 1024 * 1024
	writer := bufio.NewWriterSize(session.conn, frameBytes)
	var sent, confirmed int64
	inFlightBudget := int64(parallelInitialInFlight)
	chunkIndex := 0
	type sentFrame struct {
		end       int64
		writtenAt time.Time
	}
	pending := make([]sentFrame, 0, parallelMaxInFlight/frameBytes)
	stableSamples, slowSamples := 0, 0
	acks := make(chan parallelStreamAck, 8)
	ackDone := make(chan struct{})
	defer func() { cancel(); session.close(); <-ackDone }()
	go func() {
		defer close(ackDone)
		var lastConfirmed int64
		for {
			_ = session.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
			ack, _, ackErr := readParallelStreamAck(reader, message, token, streamID, length, lastConfirmed, length)
			if ackErr != nil {
				// Unblock an in-progress socket write even if the peer stopped reading.
				session.close()
			}
			select {
			case acks <- parallelStreamAck{ack: ack, receivedAt: time.Now(), err: ackErr}:
			case <-ctx.Done():
				return
			}
			if ackErr != nil || ack.Status == "stream-complete" {
				return
			}
			lastConfirmed = ack.StreamBytes
		}
	}()
	applyAck := func(result parallelStreamAck) error {
		if result.err != nil {
			if session.isCanceled() || errors.Is(result.err, context.Canceled) {
				return errAttachmentCanceled
			}
			return result.err
		}
		streamBytes := result.ack.StreamBytes
		if streamBytes <= confirmed || streamBytes > sent || streamBytes > length {
			return fmt.Errorf("并行数据流累计回执越界")
		}
		ackIndex := -1
		for i, frame := range pending {
			if frame.end == streamBytes {
				ackIndex = i
				break
			}
		}
		if ackIndex < 0 {
			return fmt.Errorf("并行数据流回执未落在帧边界")
		}
		// Measure after the acknowledged frame's socket write, not from the
		// previous ACK. ACK spacing includes the time spent transferring bytes.
		latency := result.receivedAt.Sub(pending[ackIndex].writtenAt)
		if latency < 0 {
			latency = 0
		}
		pending = pending[ackIndex+1:]
		confirmed = streamBytes
		if latency <= 200*time.Millisecond && result.ack.DiskWriteMs <= 100 {
			stableSamples++
			slowSamples = 0
		} else if latency >= 750*time.Millisecond || result.ack.DiskWriteMs >= 300 {
			slowSamples++
			stableSamples = 0
		} else {
			stableSamples = 0
			slowSamples = 0
		}
		if stableSamples >= 2 && inFlightBudget < parallelMaxInFlight {
			inFlightBudget += 8 * 1024 * 1024
			if inFlightBudget > parallelMaxInFlight {
				inFlightBudget = parallelMaxInFlight
			}
			stableSamples = 0
		} else if slowSamples >= 2 {
			inFlightBudget /= 2
			if inFlightBudget < int64(parallelAckBytes) {
				inFlightBudget = int64(parallelAckBytes)
			}
			slowSamples = 0
		}
		report(parallelStreamProgress{streamID: streamID, sent: sent, confirmed: confirmed, length: length, ackLatencyMs: latency.Milliseconds(), diskWriteMs: result.ack.DiskWriteMs, acknowledged: true})
		return nil
	}
	waitAck := func() error {
		select {
		case result := <-acks:
			return applyAck(result)
		case <-ctx.Done():
			return errAttachmentCanceled
		}
	}
	drainAcks := func() error {
		for {
			select {
			case result := <-acks:
				if err := applyAck(result); err != nil {
					return err
				}
			default:
				return nil
			}
		}
	}
	for sent < length {
		if session.isCanceled() || ctx.Err() != nil {
			report(parallelStreamProgress{streamID: streamID, sent: sent, confirmed: confirmed, length: length, err: errAttachmentCanceled})
			return
		}
		for sent-confirmed >= inFlightBudget {
			if err := waitAck(); err != nil {
				report(parallelStreamProgress{streamID: streamID, sent: sent, confirmed: confirmed, length: length, err: err})
				return
			}
		}
		payloadLen := int64(frameBytes)
		if remaining := length - sent; remaining < payloadLen {
			payloadLen = remaining
		}
		readOffset := int64(0)
		for readOffset < payloadLen {
			readSize := payloadLen - readOffset
			if readSize > int64(len(buffer)) {
				readSize = int64(len(buffer))
			}
			n, readErr := file.ReadAt(buffer[readOffset:readOffset+readSize], offset+sent+readOffset)
			if n != int(readSize) || (readErr != nil && readErr != io.EOF) {
				if readErr == nil {
					readErr = io.ErrUnexpectedEOF
				}
				report(parallelStreamProgress{streamID: streamID, sent: sent, confirmed: confirmed, length: length, err: readErr})
				return
			}
			readOffset += int64(n)
		}
		chunkCount := int((payloadLen + int64(chunkSize) - 1) / int64(chunkSize))
		started := time.Now()
		_ = session.conn.SetWriteDeadline(started.Add(30 * time.Second))
		header := binaryFileFrameHeader{WindowID: uint32(streamID), StartChunk: uint32(chunkIndex), ChunkCount: uint32(chunkCount), ChunkSize: uint32(chunkSize), PayloadLen: uint64(payloadLen)}
		if err := writeBinaryFileFrameHeader(writer, header); err != nil {
			report(parallelStreamProgress{streamID: streamID, sent: sent, confirmed: confirmed, length: length, err: err})
			return
		}
		if _, err := writer.Write(buffer[:payloadLen]); err != nil {
			report(parallelStreamProgress{streamID: streamID, sent: sent, confirmed: confirmed, length: length, err: err})
			return
		}
		if err := writer.Flush(); err != nil {
			report(parallelStreamProgress{streamID: streamID, sent: sent, confirmed: confirmed, length: length, err: err})
			return
		}
		writeMs := time.Since(started).Milliseconds()
		sent += payloadLen
		pending = append(pending, sentFrame{end: sent, writtenAt: time.Now()})
		chunkIndex += chunkCount
		report(parallelStreamProgress{streamID: streamID, sent: sent, confirmed: confirmed, length: length, writeMs: writeMs})
		if err := drainAcks(); err != nil {
			report(parallelStreamProgress{streamID: streamID, sent: sent, confirmed: confirmed, length: length, err: err})
			return
		}
	}
	for confirmed < sent {
		if err := waitAck(); err != nil {
			report(parallelStreamProgress{streamID: streamID, sent: sent, confirmed: confirmed, length: length, err: err})
			return
		}
	}
	report(parallelStreamProgress{streamID: streamID, sent: sent, confirmed: confirmed, length: length, done: true})
}

func parallelLaunchTarget(launched, completed, total int, confirmed, diskWriteMs int64) int {
	target := launched
	// Each unopened connection owns a fixed range. Even when probing is slow,
	// completion of an active range must schedule its unstarted successor.
	if launched == completed && launched < total {
		target++
	}
	if diskWriteMs <= 100 {
		if confirmed >= 8*1024*1024 && target < 2 {
			target = 2
		}
		if launched >= 2 && confirmed >= 32*1024*1024 {
			target = total
		}
	}
	return min(target, total)
}

func (e *Engine) transferParallelFile(ctx context.Context, peer Peer, message Message, file *os.File, control *wireSession, controlReader *wireReader, dialect ProtocolDialect, token string, streamCount int, protocolLabel string) error {
	if streamCount < parallelInitialStreams || streamCount > parallelMaxStreams {
		streamCount = parallelStreamCount(message.AttachmentSize)
	}
	progressOptions := transferProgressOptions{chunkSize: parallelChunkSize, windowSize: 8, windowBytes: 4 * 1024 * 1024, streamCount: streamCount, activeStreams: parallelInitialStreams, inFlightBytes: 0, transferMode: parallelBinaryMode, transport: "TLS/TCP", protocol: protocolLabel, tuningState: "probing"}
	e.emitTransferProgress(message.MessageID, message.AttachmentID, peer.DeviceID, 0, message.AttachmentSize, "send", "transferring", progressOptions)
	e.emitTransferProgress(message.MessageID, message.AttachmentID, peer.DeviceID, 0, message.AttachmentSize, "remote-receive", "receiving", progressOptions)
	updates := make(chan parallelStreamProgress, streamCount*8)
	parallelCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { cancel(); control.close(); e.closeOutgoingData(message.AttachmentID); workers.Wait() }()
	launched := 0
	launchStream := func(streamID int) error {
		offset, length, ok := parallelRangeFor(message.AttachmentSize, streamID, streamCount)
		if !ok {
			return fmt.Errorf("并行数据范围无效")
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			e.sendParallelStream(parallelCtx, peer, dialect, message, file, token, streamCount, streamID, offset, length, parallelChunkSize, updates)
		}()
		launched++
		return nil
	}
	if message.AttachmentSize > 0 {
		if err := launchStream(0); err != nil {
			return err
		}
	}
	type controlResult struct {
		ack wireMessage
		err error
	}
	controlResults := make(chan controlResult, 1)
	stopControl := context.AfterFunc(parallelCtx, control.close)
	defer stopControl()
	workers.Add(1)
	go func() {
		defer workers.Done()
		ack, err := readFileProgress(controlReader, message.AttachmentID)
		controlResults <- controlResult{ack: ack, err: err}
	}()
	sentByStream := make([]int64, streamCount)
	confirmedByStream := make([]int64, streamCount)
	completed := 0
	var sent, confirmed int64
	started := time.Now()
	lastDiagnosticAt := started
	lastDiagnosticSent, lastDiagnosticConfirmed := int64(0), int64(0)
	var lastDiagnosticAck time.Duration
	var lastDiagnosticWriteMs int64
	var lastDiskWriteMs int64
	sampleAt, sampleBytes := started, int64(0)
	confirmedRate := float64(0)
	lastProgressAt := time.Time{}
	for completed < streamCount && message.AttachmentSize > 0 {
		select {
		case <-parallelCtx.Done():
			e.closeOutgoingData(message.AttachmentID)
			return parallelCtx.Err()
		case result := <-controlResults:
			if control.isCanceled() || result.ack.Status == "canceled" {
				return errAttachmentCanceled
			}
			if result.err != nil {
				return result.err
			}
			return fmt.Errorf("并行文件传输提前结束: %s", result.ack.Status)
		case update := <-updates:
			if update.err != nil {
				e.closeOutgoingData(message.AttachmentID)
				if control.isCanceled() {
					return errAttachmentCanceled
				}
				return update.err
			}
			if update.streamID < 0 || update.streamID >= streamCount {
				return fmt.Errorf("并行数据流编号无效")
			}
			sent += update.sent - sentByStream[update.streamID]
			confirmed += update.confirmed - confirmedByStream[update.streamID]
			sentByStream[update.streamID] = update.sent
			confirmedByStream[update.streamID] = update.confirmed
			if update.done {
				completed++
			}
			if update.acknowledged {
				lastDiskWriteMs = update.diskWriteMs
			}
			targetStreams := parallelLaunchTarget(launched, completed, streamCount, confirmed, lastDiskWriteMs)
			if launched < targetStreams {
				for streamID := launched; streamID < targetStreams; streamID++ {
					if err := launchStream(streamID); err != nil {
						cancel()
						return err
					}
				}
			}
			if update.acknowledged {
				lastDiagnosticAck = time.Duration(update.ackLatencyMs) * time.Millisecond
			}
			if update.writeMs > 0 {
				lastDiagnosticWriteMs = update.writeMs
			}
			options := transferProgressOptions{chunkSize: parallelChunkSize, windowSize: 8, windowBytes: 4 * 1024 * 1024, streamCount: streamCount, activeStreams: launched - completed, streamID: update.streamID, inFlightBytes: sent - confirmed, ackTargetBytes: parallelAckBytes, socketWriteMs: lastDiagnosticWriteMs, ackLatency: lastDiagnosticAck, diskWriteMs: update.diskWriteMs, transferMode: parallelBinaryMode, transport: "TLS/TCP", protocol: protocolLabel, tuningState: map[bool]string{true: "stable", false: "probing"}[launched == streamCount]}
			now := time.Now()
			if now.Sub(sampleAt) >= transferSpeedSampleInterval && confirmed > sampleBytes {
				confirmedRate = float64(confirmed-sampleBytes) / now.Sub(sampleAt).Seconds()
				sampleAt, sampleBytes = now, confirmed
			}
			options.confirmedThroughput = confirmedRate
			if update.acknowledged || update.done || now.Sub(lastProgressAt) >= parallelProgressInterval {
				e.emitTransferProgress(message.MessageID, message.AttachmentID, peer.DeviceID, sent, message.AttachmentSize, "send", "transferring", options)
				e.emitTransferProgress(message.MessageID, message.AttachmentID, peer.DeviceID, confirmed, message.AttachmentSize, "remote-receive", "receiving", options)
				lastProgressAt = now
			}
			if now.Sub(lastDiagnosticAt) >= time.Second {
				interval := now.Sub(lastDiagnosticAt).Seconds()
				if interval > 0 {
					log.Printf("并行文件传输诊断: peer=%s attachment=%s mode=%s streams=%d/%d in_flight=%s write=%.1fMB/s confirmed=%.1fMB/s ack=%s write_ms=%d disk_ms=%d", peer.DeviceID, message.AttachmentID, parallelBinaryMode, launched-completed, streamCount, formatBytes(sent-confirmed), float64(sent-lastDiagnosticSent)/interval/(1024*1024), float64(confirmed-lastDiagnosticConfirmed)/interval/(1024*1024), lastDiagnosticAck, lastDiagnosticWriteMs, lastDiskWriteMs)
				}
				lastDiagnosticAt = now
				lastDiagnosticSent, lastDiagnosticConfirmed = sent, confirmed
			}
		}
	}
	if sent != message.AttachmentSize || confirmed != message.AttachmentSize {
		return fmt.Errorf("并行文件传输确认不完整")
	}
	if control.isCanceled() {
		return errAttachmentCanceled
	}
	if err := control.write(wireMessage{Type: "file_complete", MessageID: message.MessageID, AttachmentID: message.AttachmentID, FileSize: message.AttachmentSize, TransferMode: parallelBinaryMode}); err != nil {
		if control.isCanceled() {
			return errAttachmentCanceled
		}
		return err
	}
	_ = control.conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	result := <-controlResults
	if result.err != nil {
		if control.isCanceled() {
			return errAttachmentCanceled
		}
		return result.err
	}
	finalAck := result.ack
	if finalAck.Status == "canceled" {
		return errAttachmentCanceled
	}
	if finalAck.Status != "completed" || finalAck.Transferred != message.AttachmentSize {
		return fmt.Errorf("对方并行文件校验失败: %s", finalAck.Status)
	}
	verified := true
	e.emitTransferProgress(message.MessageID, message.AttachmentID, peer.DeviceID, finalAck.Transferred, message.AttachmentSize, "remote-receive", "completed", transferProgressOptions{chunkSize: parallelChunkSize, windowSize: streamCount, windowBytes: parallelAckBytes, inFlightBytes: 0, transferMode: parallelBinaryMode, transport: "TLS/TCP", protocol: protocolLabel, verified: &verified})
	e.closeOutgoingData(message.AttachmentID)
	return nil
}

type binaryPendingWindow struct {
	id            int
	bytes         int64
	sentAt        time.Time
	writeDuration time.Duration
	chunkSize     int
	windowSize    int
}

type binaryWindowAck struct {
	progress   wireMessage
	receivedAt time.Time
}

func adjustInFlightBudget(current int64, chunkSize int, ackLatency time.Duration, diskWriteMs int64, throughput, previousThroughput float64) (int64, string, string) {
	minimum := int64(maxInt(chunkSize, minInFlightBytes))
	if current < minimum {
		current = minimum
	}
	if ackLatency > 200*time.Millisecond || diskWriteMs > 300 || (previousThroughput > 0 && throughput < previousThroughput*0.75) {
		next := current / 2
		if next < minimum {
			next = minimum
		}
		return next, "backing_off", "确认延迟、写盘耗时或窗口吞吐恶化，降低在途数据"
	}
	if ackLatency <= 250*time.Millisecond && (previousThroughput == 0 || throughput >= previousThroughput*0.90) {
		// Move through explicit bandwidth-delay-product probe steps. A 25%
		// increase takes too many ACK cycles to reach a useful LAN window for
		// multi-gigabyte files, while doubling still keeps every transition at
		// a bounded window boundary.
		next := current * 2
		if next < current+minimum {
			next = current + minimum
		}
		if next > maxInFlightBytes {
			next = maxInFlightBytes
		}
		return next, "accelerating", "确认延迟稳定且吞吐保持上升，增加在途数据"
	}
	return current, "stable", ""
}

// effectiveAckLatency removes the receiver's measured flush time from the
// sender-side wait. The raw wait includes the time needed to persist the
// acknowledged bytes, so treating it as network latency makes a healthy
// transfer halve its in-flight budget after every large window.
func effectiveAckLatency(ackLatency time.Duration, diskWriteMs int64) time.Duration {
	if diskWriteMs <= 0 {
		return ackLatency
	}
	latency := ackLatency - time.Duration(diskWriteMs)*time.Millisecond
	if latency < 0 {
		return 0
	}
	return latency
}

func binaryAckTargetForBudget(budget int64) int64 {
	target := budget / 2
	minimum := int64(minInFlightBytes)
	if budget >= initialInFlightBytes {
		minimum = initialBinaryAckBytes
	}
	if target < minimum {
		target = minimum
	}
	if target > maxBinaryAckBytes {
		target = maxBinaryAckBytes
	}
	return target
}

// transferBinaryFilePipelined keeps several binary windows in flight. TCP
// provides ordering and backpressure; the ACK reader releases the next send
// slot after the receiver has flushed the corresponding window.
func (e *Engine) transferBinaryFilePipelined(ctx context.Context, peerID string, message Message, file *os.File, session *wireSession, reader *wireReader, tuning transferTuning, protocolLabel string) (transferTuning, error) {
	if message.AttachmentSize <= 0 {
		return tuning, fmt.Errorf("文件大小无效")
	}
	tuning = normalizeTransferTuning(tuning)
	tuning.binary = true
	tuning.windowSize = minInt(tuning.windowSize, maxInt(1, int(maxInFlightBytes/int64(tuning.chunkSize))))
	inFlightBudget := int64(initialInFlightBytes)
	if candidate := int64(tuning.chunkSize * tuning.windowSize); candidate > inFlightBudget {
		inFlightBudget = candidate
	}
	tuneTCPBuffers(session.conn, inFlightBudget)
	log.Printf("文件传输开始: peer=%s attachment=%s mode=%s chunk=%d window=%d in_flight=%d ack_target=%d", peerID, message.AttachmentID, binaryTransferMode, tuning.chunkSize, tuning.windowSize, inFlightBudget, binaryAckTargetForBudget(inFlightBudget))
	ackCtx, cancel := context.WithCancel(ctx)
	completed := false
	defer func() {
		cancel()
		if !completed {
			session.close()
		}
	}()
	ackCh := make(chan binaryWindowAck, 64)
	ackErrCh := make(chan error, 1)
	go func() {
		for {
			progress, err := readFileProgress(reader, message.AttachmentID)
			if err != nil {
				select {
				case ackErrCh <- err:
				case <-ackCtx.Done():
				}
				return
			}
			ack := binaryWindowAck{progress: progress, receivedAt: time.Now()}
			select {
			case ackCh <- ack:
			case <-ackCtx.Done():
				return
			}
			if progress.Status == "completed" || progress.Status == "failed" || progress.Status == "canceled" {
				return
			}
		}
	}()

	waitAck := func() (binaryWindowAck, error) {
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case ack := <-ackCh:
			return ack, nil
		case err := <-ackErrCh:
			if session.isCanceled() {
				return binaryWindowAck{}, errAttachmentCanceled
			}
			return binaryWindowAck{}, err
		case <-ctx.Done():
			return binaryWindowAck{}, ctx.Err()
		case <-timer.C:
			return binaryWindowAck{}, fmt.Errorf("文件窗口确认超时")
		}
	}

	pending := make([]binaryPendingWindow, 0, 16)
	var sent, confirmed, inFlight int64
	windowID, chunkIndex := 0, 0
	lastThroughput := 0.0
	stableCycles, degradedCycles := 0, 0
	lastDiagnosticAt := time.Time{}
	rateSampleAt := time.Now()
	var rateSampleBytes int64
	exhausted := false
	for !exhausted || len(pending) > 0 {
		for !exhausted && inFlight < inFlightBudget {
			if session.isCanceled() {
				return tuning, errAttachmentCanceled
			}
			windowSize := tuning.windowSize
			budgetWindowSize := int(inFlightBudget / int64(tuning.chunkSize))
			if budgetWindowSize < 1 {
				budgetWindowSize = 1
			}
			windowSize = minInt(windowSize, budgetWindowSize)
			started := time.Now()
			chunks, windowBytes, err := session.writeBinaryFileWindowWithAckTarget(file, message.AttachmentID, windowID, chunkIndex, tuning.chunkSize, windowSize, message.AttachmentSize-sent, binaryAckTargetForBudget(inFlightBudget))
			writeDuration := time.Since(started)
			if err != nil {
				if errors.Is(err, errAttachmentCanceled) || session.isCanceled() {
					return tuning, errAttachmentCanceled
				}
				return tuning, err
			}
			if chunks == 0 || windowBytes <= 0 {
				return tuning, io.ErrUnexpectedEOF
			}
			pending = append(pending, binaryPendingWindow{id: windowID, bytes: windowBytes, sentAt: started, writeDuration: writeDuration, chunkSize: tuning.chunkSize, windowSize: chunks})
			windowID++
			chunkIndex += chunks
			sent += windowBytes
			inFlight += windowBytes
			if sent >= message.AttachmentSize || chunks < windowSize {
				exhausted = true
			}
			e.emitTransferProgress(message.MessageID, message.AttachmentID, peerID, sent, message.AttachmentSize, "send", "transferring", transferProgressOptions{chunkSize: tuning.chunkSize, windowSize: chunks, windowBytes: windowBytes, inFlightBytes: inFlight, ackTargetBytes: binaryAckTargetForBudget(inFlightBudget), socketWriteMs: writeDuration.Milliseconds(), transferMode: binaryTransferMode, transport: "TLS/TCP", protocol: protocolLabel, tuningState: "probing"})
		}

		if len(pending) == 0 {
			continue
		}
		ack, err := waitAck()
		if err != nil {
			return tuning, err
		}
		if ack.progress.Status == "canceled" {
			return tuning, errAttachmentCanceled
		}
		if ack.progress.Status == "failed" {
			return tuning, fmt.Errorf("对方接收文件失败: %s", ack.progress.Reason)
		}
		ackIndex := -1
		for index := range pending {
			if pending[index].id == ack.progress.WindowID {
				ackIndex = index
				break
			}
		}
		if ackIndex < 0 {
			return tuning, fmt.Errorf("文件窗口确认无效")
		}
		ackedBytes := int64(0)
		for index := 0; index <= ackIndex; index++ {
			window := pending[index]
			if index == 0 && (window.chunkSize != ack.progress.ChunkSize || (!ack.progress.AckCumulative && window.windowSize != ack.progress.WindowSize)) {
				return tuning, fmt.Errorf("文件窗口确认参数无效")
			}
			ackedBytes += window.bytes
		}
		cumulative := ack.progress.AckCumulative || ackIndex > 0 && ack.progress.WindowBytes == ackedBytes
		if cumulative {
			if ack.progress.WindowBytes != ackedBytes || ack.progress.Transferred != confirmed+ackedBytes {
				return tuning, fmt.Errorf("累计文件窗口确认无效")
			}
		} else {
			if ackIndex != 0 || ack.progress.WindowBytes != pending[0].bytes || ack.progress.Transferred < confirmed || ack.progress.Transferred > sent {
				return tuning, fmt.Errorf("文件窗口确认无效")
			}
			ackedBytes = pending[0].bytes
		}
		ackStart := pending[0].sentAt
		ackWindow := pending[ackIndex]
		pending = pending[ackIndex+1:]
		confirmed = ack.progress.Transferred
		inFlight -= ackedBytes
		if inFlight < 0 {
			return tuning, fmt.Errorf("文件窗口在途字节无效")
		}
		elapsed := ack.receivedAt.Sub(ackStart)
		if elapsed <= 0 {
			elapsed = time.Nanosecond
		}
		ackWait := ack.receivedAt.Sub(ackWindow.sentAt.Add(ackWindow.writeDuration))
		if ackWait < 0 {
			ackWait = 0
		}
		throughput := float64(ackedBytes) / elapsed.Seconds()
		confirmedThroughput := float64(0)
		confirmedElapsed := ack.receivedAt.Sub(rateSampleAt)
		if confirmedElapsed >= transferSpeedSampleInterval || confirmed >= message.AttachmentSize {
			confirmedBytes := confirmed - rateSampleBytes
			if confirmedBytes > 0 && confirmedElapsed > 0 {
				confirmedThroughput = float64(confirmedBytes) / confirmedElapsed.Seconds()
				rateSampleAt = ack.receivedAt
				rateSampleBytes = confirmed
			}
		}
		effectiveAckWait := effectiveAckLatency(ackWait, ack.progress.DiskWriteMs)
		healthy := effectiveAckWait <= 250*time.Millisecond && ack.progress.DiskWriteMs <= 300 && (lastThroughput == 0 || throughput >= lastThroughput*0.90)
		if healthy {
			stableCycles++
		} else {
			stableCycles = 0
		}
		// Require two consecutive bad samples. A single delayed Wi-Fi ACK or
		// scheduler pause must not permanently collapse the transfer window.
		degraded := effectiveAckWait > time.Second || ack.progress.DiskWriteMs > 500 || (lastThroughput > 0 && throughput < lastThroughput*0.75)
		if degraded {
			degradedCycles++
		} else {
			degradedCycles = 0
		}
		state, reason := "stable", ""
		if stableCycles >= 2 || degradedCycles >= 2 {
			var tuneReason string
			tuning, state, tuneReason = adjustTransferTuning(tuning, effectiveAckWait, ack.progress.DiskWriteMs, throughput, lastThroughput, true)
			var budgetState, budgetReason string
			inFlightBudget, budgetState, budgetReason = adjustInFlightBudget(inFlightBudget, tuning.chunkSize, effectiveAckWait, ack.progress.DiskWriteMs, throughput, lastThroughput)
			if state == "stable" && budgetState != "stable" {
				state, reason = budgetState, budgetReason
			} else {
				reason = tuneReason
				if reason == "" {
					reason = budgetReason
				}
			}
			if state != "stable" {
				stableCycles = 0
			}
		}
		tuneTCPBuffers(session.conn, inFlightBudget)
		lastThroughput = throughput
		options := transferProgressOptions{chunkSize: tuning.chunkSize, windowSize: ackWindow.windowSize, windowBytes: ackedBytes, inFlightBytes: inFlight, ackTargetBytes: binaryAckTargetForBudget(inFlightBudget), socketWriteMs: ackWindow.writeDuration.Milliseconds(), ackWaitMs: ackWait.Milliseconds(), ackLatency: ackWait, diskWriteMs: ack.progress.DiskWriteMs, windowThroughput: throughput, confirmedThroughput: confirmedThroughput, transferMode: binaryTransferMode, transport: "TLS/TCP", protocol: protocolLabel, tuningState: state, tuningReason: reason}
		e.emitTransferProgress(message.MessageID, message.AttachmentID, peerID, confirmed, message.AttachmentSize, "remote-receive", "receiving", options)
		if reason != "" || lastDiagnosticAt.IsZero() || time.Since(lastDiagnosticAt) >= time.Second {
			log.Printf("文件传输确认: peer=%s attachment=%s mode=%s window=%d chunk=%d ack_bytes=%d in_flight=%d write=%dms ack=%dms throughput=%.1fMB/s state=%s reason=%s", peerID, message.AttachmentID, binaryTransferMode, ackWindow.id, tuning.chunkSize, ackedBytes, inFlight, ackWindow.writeDuration.Milliseconds(), ackWait.Milliseconds(), throughput/(1024*1024), state, reason)
			lastDiagnosticAt = time.Now()
		}
		e.rememberTransferTuning(peerID, tuning)
	}

	if confirmed != message.AttachmentSize || sent != message.AttachmentSize {
		return tuning, fmt.Errorf("文件窗口确认不完整")
	}
	if session.isCanceled() {
		return tuning, errAttachmentCanceled
	}
	if err := session.write(wireMessage{Type: "file_complete", MessageID: message.MessageID, AttachmentID: message.AttachmentID, FileSize: message.AttachmentSize}); err != nil {
		if session.isCanceled() {
			return tuning, errAttachmentCanceled
		}
		return tuning, err
	}
	finalAck, err := waitAck()
	if err != nil {
		return tuning, err
	}
	if finalAck.progress.Status == "canceled" {
		return tuning, errAttachmentCanceled
	}
	if finalAck.progress.Status == "failed" || finalAck.progress.Transferred != message.AttachmentSize {
		return tuning, fmt.Errorf("对方接收文件失败")
	}
	verified := finalAck.progress.Status == "completed"
	e.emitTransferProgress(message.MessageID, message.AttachmentID, peerID, finalAck.progress.Transferred, message.AttachmentSize, "remote-receive", "completed", transferProgressOptions{chunkSize: tuning.chunkSize, windowSize: tuning.windowSize, windowBytes: finalAck.progress.WindowBytes, inFlightBytes: 0, diskWriteMs: finalAck.progress.DiskWriteMs, transferMode: binaryTransferMode, transport: "TLS/TCP", protocol: protocolLabel, verified: &verified})
	completed = true
	return tuning, nil
}

func hasCapability(capabilities []string, expected string) bool {
	for _, capability := range capabilities {
		if capability == expected {
			return true
		}
	}
	return false
}

func (e *Engine) markOutgoingPending(ctx context.Context, message Message) error {
	message.Status, message.AttachmentStatus = "pending", "pending"
	if err := UpdateMessageStatus(ctx, message.MessageID, "pending"); err != nil {
		return err
	}
	attachment, err := GetAttachment(ctx, message.AttachmentID)
	if err != nil {
		return err
	}
	attachment.Status = "pending"
	if err := SaveAttachment(ctx, attachment); err != nil {
		return err
	}
	e.emit("chat:message", message)
	e.emitTransferProgress(message.MessageID, message.AttachmentID, strings.TrimPrefix(message.ConversationID, "conv-"), 0, message.AttachmentSize, "send", "awaiting_acceptance")
	return nil
}

func readFileProgress(reader *wireReader, attachmentID string) (wireMessage, error) {
	for {
		var progress wireMessage
		if err := reader.Decode(&progress); err != nil {
			return wireMessage{}, err
		}
		// A friend restore acknowledgement may be queued before the first file
		// progress frame. It is a separate optional control message and should
		// not interrupt the attachment transfer.
		if progress.Type == "friend_restore_ack" {
			continue
		}
		if progress.Type == "file_cancel" && progress.AttachmentID == attachmentID {
			progress.Status = "canceled"
			return progress, nil
		}
		if progress.Type != "file_progress" || progress.AttachmentID != attachmentID {
			return wireMessage{}, fmt.Errorf("文件进度回执无效")
		}
		return progress, nil
	}
}

func readAttachmentDecision(reader *wireReader, attachmentID string) (wireMessage, error) {
	for {
		var decision wireMessage
		if err := reader.Decode(&decision); err != nil {
			return wireMessage{}, err
		}
		if decision.Type == "friend_restore_ack" {
			continue
		}
		if decision.AttachmentID != attachmentID {
			return wireMessage{}, fmt.Errorf("附件控制消息无效")
		}
		switch decision.Type {
		case "file_accept", "file_reject", "file_cancel":
			return decision, nil
		default:
			return wireMessage{}, fmt.Errorf("附件控制消息无效")
		}
	}
}

func readFileOfferResponse(reader *wireReader, attachmentID string) (wireMessage, error) {
	for {
		var response wireMessage
		if err := reader.Decode(&response); err != nil {
			return wireMessage{}, err
		}
		if response.Type == "friend_restore_ack" {
			continue
		}
		if response.Type != "file_offer_response" || response.AttachmentID != attachmentID {
			return wireMessage{}, fmt.Errorf("文件接收预检查回执无效")
		}
		return response, nil
	}
}

func (e *Engine) finishAttachmentSend(ctx context.Context, message Message, status string) Message {
	confirmedBytes := message.AttachmentSize
	if status != "sent" {
		confirmedBytes = e.lastTransferBytes(message.AttachmentID, "remote-receive")
		if confirmedBytes < 0 {
			confirmedBytes = 0
		}
		if confirmedBytes > message.AttachmentSize {
			confirmedBytes = message.AttachmentSize
		}
	}
	message.Status, message.AttachmentStatus = status, status
	_ = UpdateMessageStatus(ctx, message.MessageID, status)
	_ = SaveAttachment(ctx, Attachment{AttachmentID: message.AttachmentID, MessageID: message.MessageID, FileName: message.AttachmentName, MimeType: message.AttachmentMime, FileSize: message.AttachmentSize, SHA256: messageAttachmentSHA(ctx, message), ThumbnailData: message.AttachmentThumbnail, ThumbnailMime: message.AttachmentThumbnailMime, LocalPath: message.AttachmentPath, Status: status})
	// Thumbnail generation is asynchronous. Reload it here so the completion
	// event cannot overwrite a preview that finished while the file was sent.
	if attachment, err := GetAttachment(ctx, message.AttachmentID); err == nil {
		message.AttachmentThumbnail = attachment.ThumbnailData
		message.AttachmentThumbnailMime = attachment.ThumbnailMime
	}
	e.emit("chat:message", message)
	phase := map[string]string{"sent": "completed", "failed": "failed", "not_friend": "failed", "canceled": "canceled", "rejected": "rejected"}[status]
	if phase != "" {
		verified := status == "sent"
		peerID := strings.TrimPrefix(message.ConversationID, "conv-")
		if status != "sent" {
			e.emitTransferProgress(message.MessageID, message.AttachmentID, peerID, confirmedBytes, message.AttachmentSize, "remote-receive", phase, transferProgressOptions{transferMode: binaryTransferMode, verified: &verified})
		}
		e.emitTransferProgress(message.MessageID, message.AttachmentID, peerID, confirmedBytes, message.AttachmentSize, "send", phase, transferProgressOptions{verified: &verified})
	}
	return message
}

func (e *Engine) lastTransferBytes(attachmentID, direction string) int64 {
	e.transferMetricsMu.Lock()
	defer e.transferMetricsMu.Unlock()
	if metric, ok := e.transferMetrics[attachmentID+"|"+direction]; ok {
		return metric.lastBytes
	}
	if bytes, ok := e.transferLastBytes[attachmentID+"|"+direction]; ok {
		return bytes
	}
	return 0
}

func (e *Engine) generateAttachmentThumbnail(message Message, path string) <-chan Message {
	ready := make(chan Message, 1)
	go func() {
		defer close(ready)
		thumbnailData, thumbnailMime, err := buildImageThumbnail(path, message.AttachmentMime)
		if err != nil || thumbnailData == "" {
			ready <- message
			return
		}
		if err := UpdateAttachmentThumbnail(context.Background(), message.AttachmentID, thumbnailData, thumbnailMime); err != nil {
			ready <- message
			return
		}
		message.AttachmentThumbnail = thumbnailData
		message.AttachmentThumbnailMime = thumbnailMime
		e.emit("chat:message", message)
		ready <- message
	}()
	return ready
}

func messageAttachmentSHA(ctx context.Context, message Message) string {
	if message.AttachmentID == "" {
		return ""
	}
	attachment, err := GetAttachment(ctx, message.AttachmentID)
	if err != nil {
		return ""
	}
	return attachment.SHA256
}

func (e *Engine) sendToPeer(peer Peer, message wireMessage) error {
	var lastErr error
	for _, dialect := range protocolDialectsForPeer(peer) {
		if err := e.sendToPeerWithDialect(peer, message, dialect); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("对方握手失败")
}

func (e *Engine) sendToPeerWithDialect(peer Peer, message wireMessage, dialect ProtocolDialect) error {
	if peer.IP == "" || peer.Port == 0 {
		return fmt.Errorf("好友地址不可用")
	}
	clientTLS, err := e.clientTLSConfig()
	if err != nil {
		return err
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", net.JoinHostPort(peer.IP, fmt.Sprint(peer.Port)), clientTLS)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := verifyPeerCertificate(conn, peer); err != nil {
		return err
	}
	if err := writeWire(conn, e.helloMessageForDialect("hello", dialect)); err != nil {
		return err
	}
	decoder := json.NewDecoder(conn)
	var response wireMessage
	if err := decoder.Decode(&response); err != nil {
		return fmt.Errorf("对方握手失败")
	}
	if response.Type == "error" {
		return fmt.Errorf("对方握手失败: %s", response.Status)
	}
	if response.Type != "hello_ack" {
		return fmt.Errorf("对方握手失败")
	}
	responseDialect, responseCompatible := protocolDialectForMessage(response)
	if !responseCompatible {
		return fmt.Errorf("对方握手协议不兼容")
	}
	if response.FriendshipState == "removed" {
		// Relationship-control frames must be allowed to cross the old removal
		// boundary. A re-add request needs to be persisted as pending, and an
		// accepted response is what clears that boundary on the requester. Do
		// not turn either frame into a normal friendship failure or prepend a
		// stale friend_restore frame.
		if message.Type != "friend_request" && message.Type != "friend_request_response" {
			if e.shouldApplyFriendRemoval(peer.DeviceID, response.RelationshipVersion) {
				e.handleRemoteFriendshipRequired(peer.DeviceID)
			} else {
				log.Printf("忽略旧的远端解除好友状态: device=%s, remote_version=%s, local_version=%s", peer.DeviceID, response.RelationshipVersion, peer.RelationshipVersion)
			}
		} else if message.Type == "friend_request" {
			if e.shouldApplyFriendRemoval(peer.DeviceID, response.RelationshipVersion) {
				_ = MarkFriendRemovedWithVersion(context.Background(), peer.DeviceID, peer.RelationshipVersion, peer.PublicKeyPEM, peer.CertificateFingerprint)
				_ = SetPeerRelation(context.Background(), peer.DeviceID, DiscoveredState)
				e.showPeerUnlessLocallyHidden(context.Background(), peer.DeviceID)
				e.updatePeerRelation(peer.DeviceID, DiscoveredState)
				e.setPeerFriendshipState(peer.DeviceID, "removed")
			} else {
				log.Printf("保留当前好友关系，忽略旧的远端解除好友状态: device=%s", peer.DeviceID)
			}
		}
		if !allowsRemovedFriendshipFrame(message.Type) {
			return fmt.Errorf("FRIENDSHIP_REQUIRED")
		}
	}
	// Do not let a stale worker send ordinary traffic after this side has
	// removed the relationship. Send the tombstone first so an older client can
	// converge too; re-add control frames remain the only frames allowed across
	// this boundary.
	if removed, removedErr := IsFriendRemoved(context.Background(), peer.DeviceID); removedErr == nil && removed && !allowsRemovedFriendshipFrame(message.Type) {
		version, _, _, removedAt, _ := FriendRemovalInfo(context.Background(), peer.DeviceID)
		if version == "" {
			version = peer.RelationshipVersion
		}
		if removedAt == "" {
			removedAt = nowString()
		}
		_ = writeWire(conn, wireMessage{Type: "friend_removed", RelationshipVersion: version, RemovedAt: removedAt})
		return fmt.Errorf("FRIENDSHIP_REQUIRED")
	}
	e.rememberPeerDialect(peer.DeviceID, responseDialect, response.Capabilities)
	e.touchPeer(peer.DeviceID)
	if !peer.Online {
		e.emit("chat:peer-updated", e.Peers())
	}
	// A known friend may have reinstalled FlyQPro and lost its local database.
	// Restore the relationship over this already authenticated connection before
	// normal friend traffic. Explicit friend requests must be sent first so a
	// reset receiver can persist them as pending instead of being auto-restored.
	if shouldSendFriendRestore(message.Type) && response.FriendshipState != "friend" {
		if err := e.writeFriendRestoreIfNeeded(conn, peer, responseDialect); err != nil {
			return err
		}
	} else if message.Type == "friend_request" {
		log.Printf("friend_restore skipped for friend_request: device=%s", peer.DeviceID)
	}
	if err := writeWire(conn, message); err != nil {
		return err
	}
	if message.Type == "friend_request" {
		// New receivers intentionally keep pending requests open without an
		// acknowledgement. Read a short optional response so an explicit
		// DISCOVERY_DISABLED rejection is reported to the sender, while remaining
		// compatible with older receivers that simply close or stay silent.
		_ = conn.SetReadDeadline(time.Now().Add(900 * time.Millisecond))
		var requestResponse wireMessage
		if err := decoder.Decode(&requestResponse); err == nil {
			if requestResponse.Type == "error" {
				status := requestResponse.Status
				if status == "" {
					status = "好友申请被对方拒绝"
				}
				return fmt.Errorf("%s", status)
			}
			if requestResponse.Type == "friend_request_response" {
				e.handleWire(conn, wireMessage{DeviceID: peer.DeviceID}, requestResponse, nil)
				if requestResponse.Status == "pending" {
					log.Printf("好友申请已送达并等待处理: device=%s, request=%s", peer.DeviceID, message.RequestID)
				}
			}
		}
		_ = conn.SetReadDeadline(time.Time{})
	}
	if message.Type == "message" {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		for {
			var ack wireMessage
			if err := decoder.Decode(&ack); err != nil {
				return err
			}
			if ack.Type == "friend_restore_ack" {
				continue
			}
			if ack.Type == "error" {
				if isFriendshipRejection(ack.Status) {
					e.handleRemoteFriendshipRequired(peer.DeviceID)
				}
				return fmt.Errorf("%s", ack.Status)
			}
			if ack.Type != "ack" || ack.MessageID != message.MessageID || ack.Status != "sent" {
				return fmt.Errorf("消息确认无效")
			}
			break
		}
	}
	return nil
}

// latestPeer prefers the persisted discovery snapshot over the in-memory
// connection cache. Discovery can update an endpoint immediately before the
// user presses “添加”, while the cache may still contain the previous address
// or certificate fingerprint.
func (e *Engine) latestPeer(ctx context.Context, deviceID string) (Peer, error) {
	if peers, err := ListPeers(ctx, ""); err == nil {
		for _, item := range peers {
			if item.DeviceID == deviceID {
				e.mu.Lock()
				e.peers[deviceID] = item
				e.mu.Unlock()
				return item, nil
			}
		}
	}
	return e.peer(deviceID)
}

// writeFriendRestoreIfNeeded keeps the relationship recoverable after the
// remote app was reinstalled. Signing may be unavailable on a device whose
// secure key has been reset; in that case the normal message path still gets
// a chance to report the real transport result.
func (e *Engine) writeFriendRestoreIfNeeded(conn net.Conn, peer Peer, dialect ProtocolDialect) error {
	if peer.Relation != PeerRelation {
		return nil
	}
	restore, err := e.friendRestoreMessageForDialect(peer.DeviceID, dialect)
	if err != nil {
		return nil
	}
	return writeWire(conn, restore)
}

func (e *Engine) touchPeer(deviceID string) {
	seen := nowString()
	_ = exec(context.Background(), `UPDATE peers SET last_seen=?, updated_at=? WHERE device_id=?`, seen, seen, deviceID)
	e.mu.Lock()
	if peer, ok := e.peers[deviceID]; ok {
		peer.LastSeen = seen
		peer.Online = true
		e.peers[deviceID] = peer
	}
	e.mu.Unlock()
}

func (e *Engine) rememberPeerDialect(deviceID string, dialect ProtocolDialect, capabilities []string) {
	_ = SetPeerProtocol(context.Background(), deviceID, dialect, capabilities)
	e.mu.Lock()
	if peer, ok := e.peers[deviceID]; ok {
		peer.ProtocolName, peer.ProtocolMajor, peer.DiscoveryMagic, peer.Capabilities = dialect.Name, dialect.Major, dialect.Magic, append([]string(nil), capabilities...)
		e.peers[deviceID] = peer
	}
	e.mu.Unlock()
}

func (e *Engine) setPeerOnline(deviceID string, online bool) bool {
	e.mu.Lock()
	peer, ok := e.peers[deviceID]
	if !ok || peer.Online == online {
		e.mu.Unlock()
		return false
	}
	peer.Online = online
	e.peers[deviceID] = peer
	e.mu.Unlock()
	return true
}

func (e *Engine) handleOffline(deviceID string, requestIDs ...string) {
	if len(requestIDs) > 0 && e.stalePresenceControl(deviceID, requestIDs[0]) {
		return
	}
	peer, err := e.peer(deviceID)
	if err != nil {
		return
	}
	// Legacy clients may send an unversioned offline packet while restarting
	// their discovery listener. If a public announce was received recently,
	// that packet is only a transient transport event. New clients include a
	// monotonic presence timestamp and remain authoritative above.
	if (len(requestIDs) == 0 || !isDiscoveryPresence(strings.TrimSpace(requestIDs[0]))) && peer.DiscoveryVisible && discoveryLeaseIsFresh(peer.LastSeen) {
		return
	}
	if peer.Relation != PeerRelation {
		e.forgetDiscoveredPeer(deviceID)
		return
	}
	if e.setPeerOnline(deviceID, false) {
		e.emit("chat:peer-updated", e.Peers())
	}
}

// RefreshPeerAvatar performs the only client-side avatar refresh. It is
// intentionally called by the UI when a friend conversation or profile card
// is opened; discovery, probes and ordinary message transfers do not update
// avatar bytes anymore.
func (e *Engine) RefreshPeerAvatar(deviceID string) error {
	peer, err := e.peer(strings.TrimSpace(deviceID))
	if err != nil {
		return err
	}
	if peer.Relation != PeerRelation || peer.FriendshipState == "removed" || peer.IP == "" || peer.Port == 0 {
		return fmt.Errorf("好友地址不可用")
	}
	var lastErr error
	for _, dialect := range protocolDialectsForPeer(peer) {
		if err := e.refreshPeerAvatarWithDialect(peer, dialect); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

func (e *Engine) refreshPeerAvatarWithDialect(peer Peer, dialect ProtocolDialect) error {
	clientTLS, err := e.clientTLSConfig()
	if err != nil {
		return err
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", net.JoinHostPort(peer.IP, fmt.Sprint(peer.Port)), clientTLS)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := verifyPeerCertificate(conn, peer); err != nil {
		return err
	}
	decoder := json.NewDecoder(conn)
	if err := writeWire(conn, e.helloMessageForDialect("hello", dialect)); err != nil {
		return err
	}
	var response wireMessage
	if err := decoder.Decode(&response); err != nil {
		return fmt.Errorf("对方握手失败")
	}
	if response.Type == "error" {
		return fmt.Errorf("%s", response.Status)
	}
	if response.Type != "hello_ack" {
		return fmt.Errorf("对方握手失败")
	}
	if response.FriendshipState == "removed" || !e.isFriend(peer.DeviceID) {
		return fmt.Errorf("FRIENDSHIP_REQUIRED")
	}
	responseDialect, compatible := protocolDialectForMessage(response)
	if !compatible {
		return fmt.Errorf("对方握手协议不兼容")
	}
	e.rememberPeerDialect(peer.DeviceID, responseDialect, response.Capabilities)
	if response.AvatarData != "" {
		e.applyPeerAvatar(response, peer.DeviceID)
		return nil
	}
	// Older peers may advertise avatar support but omit the bytes from
	// hello_ack. Keep the compatibility request on this explicit UI path only.
	if !hasCapability(response.Capabilities, "avatar-sync-v1") {
		return nil
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetReadDeadline(time.Time{})
	if err := writeWire(conn, wireMessage{Type: "avatar_request"}); err != nil {
		return err
	}
	var avatar wireMessage
	if err := decoder.Decode(&avatar); err != nil {
		return nil
	}
	if avatar.Type == "avatar_response" && avatar.AvatarData != "" {
		e.applyPeerAvatar(avatar, peer.DeviceID)
	}
	return nil
}

func (e *Engine) probePeer(peer Peer) error {
	var lastErr error
	for _, dialect := range protocolDialectsForPeer(peer) {
		if err := e.probePeerWithDialect(peer, dialect); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

func (e *Engine) probePeerWithDialect(peer Peer, dialect ProtocolDialect) error {
	if peer.IP == "" || peer.Port == 0 {
		return fmt.Errorf("好友地址不可用")
	}
	clientTLS, err := e.clientTLSConfig()
	if err != nil {
		return err
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", net.JoinHostPort(peer.IP, fmt.Sprint(peer.Port)), clientTLS)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if err := verifyPeerCertificate(conn, peer); err != nil {
		return err
	}
	hello := e.helloMessageForDialect("hello", dialect)
	hello.Probe = true
	if err := writeWire(conn, hello); err != nil {
		return err
	}
	decoder := json.NewDecoder(conn)
	var response wireMessage
	if err := decoder.Decode(&response); err != nil {
		return err
	}
	if response.Type == "error" {
		if isFriendshipRejection(response.Status) {
			e.handleRemoteFriendshipRequired(peer.DeviceID)
		}
		return fmt.Errorf("%s", response.Status)
	}
	if response.Type != "hello_ack" {
		return fmt.Errorf("对方握手失败")
	}
	responseDialect, responseCompatible := protocolDialectForMessage(response)
	if !responseCompatible {
		return fmt.Errorf("对方握手协议不兼容")
	}
	e.rememberPeerDialect(peer.DeviceID, responseDialect, response.Capabilities)
	e.touchPeer(peer.DeviceID)
	return nil
}

func (e *Engine) avatarPayloadForWire() (string, string) {
	profile := e.Profile()
	if profile.AvatarPath != "" {
		if data, err := os.ReadFile(profile.AvatarPath); err == nil && len(data) > 0 && len(data) <= 5*1024*1024 {
			mimeType := mime.TypeByExtension(filepath.Ext(profile.AvatarPath))
			if mimeType == "" {
				mimeType = "image/png"
			}
			return base64.StdEncoding.EncodeToString(data), mimeType
		}
	}
	if profile.AvatarData != "" {
		encoded := profile.AvatarData
		mimeType := "image/jpeg"
		if marker := strings.Index(encoded, ";base64,"); marker >= 0 {
			if strings.HasPrefix(encoded, "data:") {
				mimeType = strings.TrimPrefix(encoded[:marker], "data:")
			}
			encoded = encoded[marker+len(";base64,"):]
		} else if marker := strings.Index(encoded, "base64,"); marker >= 0 {
			encoded = encoded[marker+len("base64,"):]
		}
		if data, err := base64.StdEncoding.DecodeString(encoded); err == nil && len(data) > 0 && len(data) <= 5*1024*1024 {
			return encoded, mimeType
		}
	}
	return "", ""
}

func (e *Engine) livenessLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			e.probeKnownPeers()
		case <-e.stop:
			return
		}
	}
}

func (e *Engine) probeKnownPeers() {
	peers := e.Peers()
	changed := false
	var changedMu sync.Mutex
	var wait sync.WaitGroup
	for _, peer := range peers {
		peer := peer
		// Discovery peers are kept online by the discovery snapshot. Probing
		// strangers over TLS is only a best-effort connection check and can fail
		// transiently even while UDP discovery is healthy; changing Online here
		// makes the discovery list flicker every liveness interval. Friends still
		// use probes to keep their direct messaging endpoint current.
		if peer.Relation != PeerRelation {
			continue
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			probeErr := e.probePeer(peer)
			online := probeErr == nil
			if probeErr != nil && strings.Contains(probeErr.Error(), "DISCOVERY_DISABLED") && peer.Relation != PeerRelation {
				// A cached stranger explicitly opted out of discovery. Remove the
				// stale result instead of leaving it in the desktop discovery list.
				e.forgetDiscoveredPeer(peer.DeviceID)
				changedMu.Lock()
				changed = true
				changedMu.Unlock()
				return
			}
			// probePeer updates lastSeen (and may optimistically mark the in-memory
			// peer online) before returning. Compare with the snapshot used for the
			// probe so an offline peer loaded from SQLite still produces the online
			// event that the frontend needs after a successful probe.
			stateChanged := peer.Online != online
			e.setPeerOnline(peer.DeviceID, online)
			if stateChanged {
				changedMu.Lock()
				changed = true
				changedMu.Unlock()
			}
		}()
	}
	wait.Wait()
	if changed {
		e.emit("chat:peer-updated", e.Peers())
	}
}

func (e *Engine) clientTLSConfig() (*tls.Config, error) {
	e.mu.RLock()
	identity := e.identity
	e.mu.RUnlock()
	certificate, err := identity.TLSCertificate()
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{certificate}, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, nil
}

func cachedAvatarMatches(peer Peer) bool {
	if peer.AvatarPath == "" || peer.AvatarHash == "" {
		return false
	}
	data, err := os.ReadFile(peer.AvatarPath)
	return err == nil && sha256Hex(data) == peer.AvatarHash
}

func verifyPeerCertificate(conn *tls.Conn, peer Peer) error {
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return fmt.Errorf("对方没有提供证书")
	}
	certificate := state.PeerCertificates[0]
	actual := sha256Hex(certificate.Raw)
	if peer.PublicKeyPEM != "" {
		block, _ := pem.Decode([]byte(peer.PublicKeyPEM))
		if block == nil {
			return fmt.Errorf("设备公钥无效")
		}
		expected, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return fmt.Errorf("设备公钥无效")
		}
		expectedDER, err := x509.MarshalPKIXPublicKey(expected)
		if err != nil {
			return err
		}
		actualDER, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
		if err != nil || !strings.EqualFold(hex.EncodeToString(actualDER), hex.EncodeToString(expectedDER)) {
			return fmt.Errorf("设备身份不匹配")
		}
		return nil
	}
	if peer.CertificateFingerprint != "" && !strings.EqualFold(actual, peer.CertificateFingerprint) {
		return fmt.Errorf("CERTIFICATE_CHANGED")
	}
	return nil
}

func (e *Engine) peer(deviceID string) (Peer, error) {
	e.mu.RLock()
	peer, ok := e.peers[deviceID]
	e.mu.RUnlock()
	if ok {
		// A connection/discovery worker can hold an older in-memory snapshot
		// than SQLite. Re-apply the durable local-hide decision here as well as
		// in Peers(), so permission and restore checks see the same state.
		if hidden, err := IsHiddenFriend(context.Background(), deviceID); err == nil && hidden {
			peer.VisibleInFriends = false
		}
		return peer, nil
	}
	peers, err := ListPeers(context.Background(), "")
	if err != nil {
		return Peer{}, err
	}
	for _, item := range peers {
		if item.DeviceID == deviceID {
			e.mu.Lock()
			e.peers[deviceID] = item
			e.mu.Unlock()
			return item, nil
		}
	}
	return Peer{}, fmt.Errorf("设备不存在")
}

func (e *Engine) isFriend(deviceID string) bool {
	peer, err := e.peer(deviceID)
	if err != nil || peer.Relation != PeerRelation || peer.FriendshipState == "removed" {
		return false
	}
	removed, removalErr := IsFriendRemoved(context.Background(), deviceID)
	if removalErr != nil || !removed {
		return removalErr == nil
	}
	// A tombstone from an older relationship cycle must not override an
	// already accepted relationship with a newer version. Matching (or
	// unversioned legacy) tombstones remain authoritative.
	removalVersion, _, _, _, infoErr := FriendRemovalInfo(context.Background(), deviceID)
	if infoErr == nil && peer.RelationshipVersion != "" && removalVersion != "" && removalVersion != peer.RelationshipVersion {
		return true
	}
	return false
}

// sendFailureStatus keeps a remote friendship rejection distinguishable from
// transport failures. The status is persisted with the outgoing message so
// the UI can explain why a message was not delivered after a friend was
// removed, instead of showing the generic "发送失败" text.
func sendFailureStatus(err error) string {
	if err == nil {
		return "failed"
	}
	if isFriendshipRejection(err.Error()) || strings.Contains(err.Error(), "不是好友") {
		return "not_friend"
	}
	return "failed"
}

func isFriendshipRejection(value string) bool {
	upper := strings.ToUpper(value)
	return strings.Contains(upper, "FRIENDSHIP_REQUIRED") || strings.Contains(upper, "FRIENDSHIP_REMOVED")
}

// handleRemoteFriendshipRequired applies the remote deletion only after the
// remote device has explicitly rejected a message or handshake. This keeps
// the relationship synchronized even when the local deletion happened while
// this device was offline.
func (e *Engine) handleRemoteFriendshipRequired(deviceID string) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return
	}
	peer, peerErr := e.peer(deviceID)
	version := ""
	publicKey, certificateFingerprint := "", ""
	if peerErr == nil {
		version = peer.RelationshipVersion
		publicKey = peer.PublicKeyPEM
		certificateFingerprint = peer.CertificateFingerprint
	}
	_ = MarkFriendRemovedWithVersion(context.Background(), deviceID, version, publicKey, certificateFingerprint)
	_ = SetPeerRelation(context.Background(), deviceID, DiscoveredState)
	e.showPeerUnlessLocallyHidden(context.Background(), deviceID)
	e.updatePeerRelation(deviceID, DiscoveredState)
	e.setPeerFriendshipState(deviceID, "removed")
	e.emit("chat:peer-updated", e.Peers())
}

func (e *Engine) currentRelationshipVersion(deviceID string) string {
	if peer, err := e.peer(deviceID); err == nil {
		return peer.RelationshipVersion
	}
	return ""
}

func (e *Engine) ensureRelationshipVersion(deviceID string) string {
	if version := e.currentRelationshipVersion(deviceID); version != "" {
		return version
	}
	return newID()
}

func (e *Engine) setPeerRelationshipVersion(deviceID, version string) {
	if version == "" {
		return
	}
	e.mu.Lock()
	if peer, ok := e.peers[deviceID]; ok {
		peer.RelationshipVersion = version
		e.peers[deviceID] = peer
	}
	e.mu.Unlock()
}

func (e *Engine) friendshipStateForPeer(deviceID string) (state, version string) {
	if peer, err := e.peer(deviceID); err == nil {
		// A current active relationship is authoritative over a stale removal
		// tombstone. This protects a newly accepted relationship from a delayed
		// friend_removed frame belonging to the previous relationship cycle.
		if peer.Relation == PeerRelation && peer.FriendshipState != "removed" && e.isFriend(deviceID) {
			return "friend", peer.RelationshipVersion
		}
		if peer.Relation == PeerRelation && peer.FriendshipState != "removed" {
			if removalVersion, _, _, _, removalErr := FriendRemovalInfo(context.Background(), deviceID); removalErr == nil && removalVersion != "" && peer.RelationshipVersion != "" && removalVersion != peer.RelationshipVersion {
				return "friend", peer.RelationshipVersion
			}
		}
	}
	if removed, err := IsFriendRemoved(context.Background(), deviceID); err == nil && removed {
		version, _, _, _, _ = FriendRemovalInfo(context.Background(), deviceID)
		return "removed", version
	}
	if peer, err := e.peer(deviceID); err == nil && peer.Relation == PeerRelation && peer.FriendshipState != "removed" {
		return "friend", peer.RelationshipVersion
	}
	return "unknown", ""
}

func (e *Engine) shouldApplyFriendRemoval(deviceID, incomingVersion string) bool {
	peer, err := e.peer(deviceID)
	if err != nil || peer.Relation != PeerRelation || peer.FriendshipState == "removed" {
		return true
	}
	currentVersion := strings.TrimSpace(peer.RelationshipVersion)
	incomingVersion = strings.TrimSpace(incomingVersion)
	// An active version must not be revoked by an empty or different version;
	// those are legacy or delayed removal messages from an older cycle.
	if currentVersion != "" {
		return incomingVersion != "" && incomingVersion == currentVersion
	}
	return true
}

func (e *Engine) hasPendingFriendRequest(deviceID string) bool {
	requests, err := listFriendRequestRows(context.Background(), "")
	if err != nil {
		return false
	}
	for _, request := range requests {
		if request.DeviceID == deviceID && (request.Status == "queued" || request.Status == "sent" || request.Status == "pending") {
			return true
		}
	}
	return false
}

// canRespondToDiscovery is the privacy boundary for the discovery protocol.
// The discoverable switch controls whether this device appears in discovery
// results. Friends do not need discovery to message each other: their saved
// endpoint is accepted by canAcceptPeerConnection below.
func (e *Engine) canRespondToDiscovery(deviceID string) bool {
	return e.discoveryResponseScope(deviceID) != ""
}

func (e *Engine) discoveryResponseScope(deviceID string) string {
	if e.isFriend(deviceID) {
		if e.Profile().Discoverable {
			return DiscoveryScopePublic
		}
		return DiscoveryScopeFriend
	}
	if e.Profile().Discoverable {
		return DiscoveryScopePublic
	}
	return ""
}

// canAcceptPeerConnection also permits the direct response to a friend
// request. This keeps the request/accept flow working when the requester has
// discovery disabled, without making the requester generally discoverable.
func (e *Engine) canAcceptPeerConnection(deviceID string) bool {
	return e.Profile().Discoverable || e.isFriend(deviceID)
}

func (e *Engine) setPeerDiscoveryVisible(deviceID string, visible bool) {
	if err := SetPeerDiscoveryVisible(context.Background(), deviceID, visible); err != nil {
		return
	}
	e.mu.Lock()
	if peer, ok := e.peers[deviceID]; ok {
		peer.DiscoveryVisible = visible
		e.peers[deviceID] = peer
	}
	e.mu.Unlock()
}

func (e *Engine) setPeerVisibleInFriends(deviceID string, visible bool) {
	e.mu.Lock()
	if peer, ok := e.peers[deviceID]; ok {
		peer.VisibleInFriends = visible
		e.peers[deviceID] = peer
	}
	e.mu.Unlock()
}

// locallyHiddenFriends protects the local "hide from friends" action from
// stale peer-updated events and endpoint refreshes.  The database remains the
// source of truth across restarts; this in-memory guard closes the race while
// a discovery/connection goroutine is still publishing an older Peer value.
func (e *Engine) markLocallyHiddenFriend(deviceID string) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return
	}
	e.mu.Lock()
	if e.locallyHiddenFriends == nil {
		e.locallyHiddenFriends = make(map[string]struct{})
	}
	e.locallyHiddenFriends[deviceID] = struct{}{}
	e.mu.Unlock()
}

func (e *Engine) clearLocallyHiddenFriend(deviceID string) {
	e.mu.Lock()
	delete(e.locallyHiddenFriends, strings.TrimSpace(deviceID))
	e.mu.Unlock()
}

func (e *Engine) locallyHiddenFriend(deviceID string) bool {
	e.mu.RLock()
	_, hidden := e.locallyHiddenFriends[strings.TrimSpace(deviceID)]
	e.mu.RUnlock()
	return hidden
}

func (e *Engine) SetPeerVisibleInFriends(ctx context.Context, deviceID string, visible bool) error {
	if !e.isFriend(deviceID) {
		return fmt.Errorf("不是好友")
	}
	return e.setPeerVisibility(ctx, deviceID, visible)
}

// HidePeerFromFriends is the local hide operation without requiring an active
// relationship. A retained peer may already be marked removed after the
// other side deleted the friendship, but it still needs to be removable from
// this device's friends list.
func (e *Engine) HidePeerFromFriends(ctx context.Context, deviceID string) error {
	return e.setPeerVisibility(ctx, deviceID, false)
}

func (e *Engine) setPeerVisibility(ctx context.Context, deviceID string, visible bool) error {
	if err := SetPeerVisibleInFriends(ctx, deviceID, visible); err != nil {
		return err
	}
	if visible {
		e.clearLocallyHiddenFriend(deviceID)
	} else {
		e.markLocallyHiddenFriend(deviceID)
	}
	e.setPeerVisibleInFriends(deviceID, visible)
	e.emit("chat:peer-updated", e.Peers())
	return nil
}

// showPeerUnlessLocallyHidden is used by passive relationship/liveness
// updates. Those events may change relation or discovery state, but they are
// not an explicit request to restore a row hidden from the local friends
// list. Only an explicit contact action, an incoming text message, or an
// accepted friend request may clear the durable hidden marker.
func (e *Engine) showPeerUnlessLocallyHidden(ctx context.Context, deviceID string) {
	hidden, err := IsHiddenFriend(ctx, deviceID)
	if err != nil {
		// A storage error must never turn a hidden row into a visible one.
		e.markLocallyHiddenFriend(deviceID)
		e.setPeerVisibleInFriends(deviceID, false)
		return
	}
	if hidden {
		e.markLocallyHiddenFriend(deviceID)
		e.setPeerVisibleInFriends(deviceID, false)
		return
	}
	if err := SetPeerVisibleInFriends(ctx, deviceID, true); err == nil {
		e.clearLocallyHiddenFriend(deviceID)
		e.setPeerVisibleInFriends(deviceID, true)
	}
}

func (e *Engine) RemovePeerFromMemory(deviceID string) {
	e.mu.Lock()
	delete(e.peers, deviceID)
	e.mu.Unlock()
	e.emit("chat:peer-updated", e.Peers())
}

func (e *Engine) updatePeerRelation(deviceID, relation string) {
	e.mu.Lock()
	if peer, ok := e.peers[deviceID]; ok {
		peer.Relation = relation
		e.peers[deviceID] = peer
	}
	e.mu.Unlock()
}

func (e *Engine) setPeerFriendshipState(deviceID, state string) {
	e.mu.Lock()
	if peer, ok := e.peers[deviceID]; ok {
		peer.FriendshipState = state
		e.peers[deviceID] = peer
	}
	e.mu.Unlock()
}

// MarkPeerRemoved keeps the trusted peer row visible after a full contact
// removal while making the relationship unusable until a new request is
// accepted.
func (e *Engine) MarkPeerRemoved(deviceID string) {
	e.updatePeerRelation(deviceID, DiscoveredState)
	e.clearLocallyHiddenFriend(deviceID)
	e.setPeerVisibleInFriends(deviceID, true)
	e.setPeerFriendshipState(deviceID, "removed")
	e.setPeerDiscoveryVisible(deviceID, false)
	e.emit("chat:peer-updated", e.Peers())
}
func (e *Engine) Profile() Profile { e.mu.RLock(); defer e.mu.RUnlock(); return e.profile }
func (e *Engine) DeviceInfo() DeviceInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	info := e.identity.DeviceInfo
	info.FeiqID = FeiqID(info.DeviceID)
	info.ProtocolName = ProtocolName
	info.ProtocolMajor = ProtocolMajor
	return info
}

func (e *Engine) UpdateProfile(profile Profile) {
	e.mu.Lock()
	previousProfile := e.profile
	wasDiscoverable := e.profile.Discoverable
	e.profile = profile
	e.mu.Unlock()
	if kind := discoverabilityPresenceKind(wasDiscoverable, profile.Discoverable); kind != "" {
		// Presence packets are asynchronous, but each queued packet verifies that
		// the profile is still in the state it represents. This prevents a quick
		// off/on toggle from sending withdraw after announce (or vice versa).
		e.schedulePresence(kind, profile.Discoverable)
	}
	// Keep public nickname discovery behavior, but never use discovery as an
	// avatar transport. Avatar bytes are fetched only by RefreshPeerAvatar.
	if previousProfile.Nickname != profile.Nickname && profile.Discoverable {
		go e.broadcastPresence("announce")
	}
	e.emit("chat:profile-updated", profile)
}

func discoverabilityPresenceKind(wasDiscoverable, discoverable bool) string {
	if wasDiscoverable && !discoverable {
		return "withdraw"
	}
	if !wasDiscoverable && discoverable {
		return "announce"
	}
	return ""
}

func (e *Engine) schedulePresence(kind string, expectedDiscoverable bool) {
	// Do this synchronously, like Android's discoverability update. The
	// packet is tiny and this prevents the UI from reporting the new setting
	// while the old presence is still visible on other devices.
	e.presenceMu.Lock()
	defer e.presenceMu.Unlock()
	if e.Profile().Discoverable != expectedDiscoverable {
		return
	}
	e.broadcastPresence(kind)
}

func (e *Engine) broadcastPresence(kind string) {
	targets := broadcastAddresses()
	targets = append(targets, localSubnetTargets()...)
	if kind == "withdraw" || kind == "offline" {
		// Broadcasts can be filtered by Wi-Fi isolation. Send the control packet
		// directly to known peers as well so closing discovery takes effect
		// immediately on the same LAN.
		targets = append(targets, knownPresenceTargets()...)
	}
	for _, dialect := range protocolDialects {
		if kind == "offline" && dialect.Major < 2 {
			continue
		}
		message := e.helloMessageForDialect(kind, dialect)
		if kind == "announce" || kind == "offline" || kind == "withdraw" {
			// Presence control packets can cross on the network. Put a monotonic
			// sender timestamp in the optional request id so an old offline or
			// withdraw packet cannot erase a newer online heartbeat.
			message.RequestID = fmt.Sprintf("%s%d:%s:%s", discoveryPresencePrefix, time.Now().UnixMilli(), kind, newID())
		}
		if kind == "announce" {
			message.DiscoveryScope = DiscoveryScopePublic
		}
		for index := range targets {
			_ = e.sendDiscovery(&targets[index], message)
		}
	}
}

func knownPresenceTargets() []net.UDPAddr {
	peers, err := ListPeers(context.Background(), "")
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	result := make([]net.UDPAddr, 0, len(peers))
	for _, peer := range peers {
		ip := net.ParseIP(strings.TrimSpace(peer.IP))
		if ip == nil || ip.To4() == nil {
			continue
		}
		key := ip.To4().String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, net.UDPAddr{IP: ip.To4(), Port: DiscoveryPort})
	}
	return result
}

func (e *Engine) resetDiscoveryMiss(deviceID string) {
	e.discoveryMu.Lock()
	if e.discoveryMisses == nil {
		e.discoveryMisses = make(map[string]int)
	}
	delete(e.discoveryMisses, deviceID)
	e.discoveryMu.Unlock()
}

func (e *Engine) handleWithdraw(deviceID string, requestIDs ...string) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return
	}
	if len(requestIDs) > 0 && e.stalePresenceControl(deviceID, requestIDs[0]) {
		return
	}
	e.resetDiscoveryMiss(deviceID)
	peer, err := e.peer(deviceID)
	if err != nil {
		return
	}
	// Do not let a legacy, unversioned withdraw produced during a brief
	// listener restart erase a freshly announced discovery row. Versioned
	// withdraw packets remain authoritative and are handled immediately.
	if (len(requestIDs) == 0 || !isDiscoveryPresence(strings.TrimSpace(requestIDs[0]))) && peer.DiscoveryVisible && discoveryLeaseIsFresh(peer.LastSeen) {
		return
	}
	if peer.Relation == PeerRelation {
		e.setPeerDiscoveryVisible(deviceID, false)
		e.emit("chat:peer-updated", e.Peers())
		return
	}
	e.forgetDiscoveredPeer(deviceID)
}

func (e *Engine) forgetDiscoveredPeer(deviceID string) {
	if strings.TrimSpace(deviceID) == "" {
		return
	}
	peer, err := e.peer(deviceID)
	if err != nil || peer.Relation == PeerRelation {
		return
	}
	e.resetDiscoveryMiss(deviceID)
	_ = exec(context.Background(), `DELETE FROM peers WHERE device_id=? AND relation=?`, deviceID, DiscoveredState)
	e.mu.Lock()
	delete(e.peers, deviceID)
	e.mu.Unlock()
	e.emit("chat:peer-updated", e.Peers())
}

// stalePresenceControl rejects a delayed offline/withdraw control packet when
// a newer heartbeat from the same device has already been observed. Legacy
// packets without the optional timestamp remain supported.
func (e *Engine) stalePresenceControl(deviceID, requestID string) bool {
	timestamp, ok := discoveryPresenceTimestamp(requestID)
	if !ok {
		return false
	}
	e.discoveryMu.Lock()
	defer e.discoveryMu.Unlock()
	if e.discoveryPresenceAt == nil {
		e.discoveryPresenceAt = make(map[string]int64)
	}
	if previous := e.discoveryPresenceAt[deviceID]; previous > timestamp {
		return true
	}
	e.discoveryPresenceAt[deviceID] = timestamp
	return false
}

func discoveryPresenceTimestamp(requestID string) (int64, bool) {
	if !strings.HasPrefix(requestID, discoveryPresencePrefix) {
		return 0, false
	}
	value := strings.TrimPrefix(requestID, discoveryPresencePrefix)
	separator := strings.IndexByte(value, ':')
	if separator < 1 {
		return 0, false
	}
	timestamp, err := strconv.ParseInt(value[:separator], 10, 64)
	return timestamp, err == nil && timestamp > 0
}

func (e *Engine) Peers() []Peer {
	peers, _ := ListPeers(context.Background(), "")
	e.mu.RLock()
	serviceStopped := e.serviceStopped
	onlineStates := make(map[string]bool, len(e.peers))
	for deviceID, peer := range e.peers {
		onlineStates[deviceID] = peer.Online
	}
	e.mu.RUnlock()
	for index := range peers {
		// Keep an explicit local hide effective even if an older connection or
		// discovery refresh supplied a stale visible=true snapshot meanwhile.
		if e.locallyHiddenFriend(peers[index].DeviceID) {
			peers[index].VisibleInFriends = false
		}
		if serviceStopped {
			peers[index].Online = false
		} else if online, ok := onlineStates[peers[index].DeviceID]; ok {
			// Immediate online/offline transitions live in memory; lastSeen
			// remains persisted for restart recovery and display.
			peers[index].Online = online
		}
		if peers[index].AvatarPath == "" {
			continue
		}
		data, err := os.ReadFile(peers[index].AvatarPath)
		if err != nil || len(data) == 0 || len(data) > 5*1024*1024 {
			continue
		}
		mimeType := mime.TypeByExtension(filepath.Ext(peers[index].AvatarPath))
		if mimeType == "" {
			mimeType = "image/png"
		}
		peers[index].AvatarData = "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
	}
	return peers
}

func (e *Engine) PeersByRelation(relation string) []Peer {
	peers := e.Peers()
	filtered := make([]Peer, 0, len(peers))
	for _, peer := range peers {
		if peer.Relation == relation {
			filtered = append(filtered, peer)
		}
	}
	return filtered
}

func (e *Engine) NetworkStatus() NetworkStatus {
	interfaces, _ := net.Interfaces()
	names := make([]string, 0, len(interfaces))
	ips := make([]string, 0)
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		names = append(names, iface.Name)
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			if strings.Contains(address.String(), ".") {
				ips = append(ips, strings.Split(address.String(), "/")[0])
			}
		}
	}
	peers := e.Peers()
	online := 0
	for _, peer := range peers {
		if peer.Online {
			online++
		}
	}
	status := "normal"
	if len(ips) == 0 {
		status = "warning"
	}
	e.mu.RLock()
	chatPort := e.identity.Port
	lastScan := e.lastScan
	lastErr := e.lastErr
	e.mu.RUnlock()
	lastScanAt := ""
	if !lastScan.IsZero() {
		lastScanAt = lastScan.UTC().Format(time.RFC3339Nano)
	}
	return NetworkStatus{Status: status, Interfaces: names, LocalIPs: ips, DiscoveryPort: DiscoveryPort, ChatPort: chatPort, PeerCount: len(peers), OnlineCount: online, LastScanAt: lastScanAt, LastError: lastErr}
}

func (e *Engine) emit(name string, data any) {
	if app := application.Get(); app != nil {
		app.Event.Emit(name, data)
	}
}

func writeWire(writer io.Writer, message wireMessage) error {
	return json.NewEncoder(writer).Encode(message)
}
func readWire(reader io.Reader, message *wireMessage) error {
	return json.NewDecoder(reader).Decode(message)
}

func broadcastAddresses() []net.UDPAddr {
	interfaces, _ := net.Interfaces()
	result := make([]net.UDPAddr, 0)
	seen := make(map[string]struct{})
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			ipNet, ok := address.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil {
				continue
			}
			ip := ipNet.IP.To4()
			mask := ipNet.Mask
			if len(mask) != 4 {
				continue
			}
			broadcast := net.IPv4(ip[0]|^mask[0], ip[1]|^mask[1], ip[2]|^mask[2], ip[3]|^mask[3])
			key := broadcast.String()
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				result = append(result, net.UDPAddr{IP: broadcast, Port: DiscoveryPort})
			}
		}
	}
	return result
}

// localSubnetTargets adds a bounded unicast fallback for networks that block
// UDP broadcasts, such as phone hotspots and restrictive Wi-Fi access points.
func localSubnetTargets() []net.UDPAddr {
	interfaces, _ := net.Interfaces()
	result := make([]net.UDPAddr, 0)
	seen := make(map[string]struct{})
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			ipNet, ok := address.(*net.IPNet)
			if !ok {
				continue
			}
			for _, target := range subnetHostTargets(ipNet) {
				key := target.String()
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				result = append(result, net.UDPAddr{IP: target, Port: DiscoveryPort})
			}
		}
	}
	return result
}

func subnetHostTargets(ipNet *net.IPNet) []net.IP {
	ip := ipNet.IP.To4()
	mask := ipNet.Mask
	ones, bits := mask.Size()
	if ip == nil || bits != 32 || ones < 24 || ones > 30 {
		return nil
	}
	hosts := 1 << (bits - ones)
	if hosts > 256 {
		return nil
	}

	network := ip.Mask(mask).To4()
	result := make([]net.IP, 0, hosts-2)
	for host := 1; host < hosts-1; host++ {
		target := net.IPv4(network[0]|byte(host>>24), network[1]|byte(host>>16), network[2]|byte(host>>8), network[3]|byte(host))
		if !target.Equal(ip) {
			result = append(result, target)
		}
	}
	return result
}

func newID() string { return fmt.Sprintf("%d-%s", time.Now().UnixNano(), randomToken()) }
func randomToken() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())))
	return hex.EncodeToString(sum[:8])
}

func safeFileName(value string) string {
	value = filepath.Base(strings.TrimSpace(value))
	value = strings.Map(func(char rune) rune {
		if char < 0x20 || strings.ContainsRune(`<>:"/\\|?*`, char) {
			return '_'
		}
		return char
	}, value)
	value = strings.Trim(value, " .")
	if value == "" || value == "." || value == ".." {
		return "attachment"
	}
	return value
}
