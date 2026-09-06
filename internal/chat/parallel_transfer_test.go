package chat

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func parallelTestIdentity(t *testing.T) (Identity, tls.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	identity := Identity{PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})), CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
	identity.DeviceID = "sender"
	cert, err := identity.TLSCertificate()
	if err != nil {
		t.Fatal(err)
	}
	return identity, cert
}

// Exercise the real TLS join, sender, range writer and final control ACK. A slow
// reader makes ACK spacing exceed the old 200 ms stream-launch threshold.
func TestParallelTransferSlowTLSCompletesAllRanges(t *testing.T) {
	runParallelTLS(t, 36*1024*1024+17, 300*time.Millisecond)
}

func TestParallelTransferLargeTLS(t *testing.T) {
	value := os.Getenv("FLYQPRO_TRANSFER_TEST_BYTES")
	if value == "" {
		t.Skip("set FLYQPRO_TRANSFER_TEST_BYTES for a large TCP/TLS transfer")
	}
	size, err := strconv.Atoi(value)
	if err != nil || size < 4 {
		t.Fatal("invalid FLYQPRO_TRANSFER_TEST_BYTES")
	}
	runParallelTLS(t, size, 0)
}

func runParallelTLS(t *testing.T, size int, delay time.Duration) {
	t.Helper()
	identity, cert := parallelTestIdentity(t)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	source, err := os.Create(filepath.Join(root, "source"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	block := make([]byte, 1024*1024)
	digest := sha256.New()
	for offset := 0; offset < size; {
		if _, err := rand.Read(block); err != nil {
			t.Fatal(err)
		}
		n := min(len(block), size-offset)
		if _, err := source.Write(block[:n]); err != nil {
			t.Fatal(err)
		}
		digest.Write(block[:n])
		offset += n
	}
	target, err := os.Create(filepath.Join(root, "target.part"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err := target.Truncate(int64(size)); err != nil {
		t.Fatal(err)
	}
	message := Message{MessageID: "message", AttachmentID: "attachment", AttachmentSize: int64(size)}
	receiver := NewEngine()
	transfer := &incomingFile{file: target, expected: int64(size), attachmentID: message.AttachmentID, messageID: message.MessageID, senderID: "sender", parallel: true, transferToken: "token", parallelStreamCount: 4, parallelRanges: make(map[int]*parallelRange), parallelSessions: make(map[int]*wireSession)}
	receiver.incoming[message.AttachmentID] = transfer
	var workers sync.WaitGroup
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { conn.Close() })
				defer stop()
				reader := newWireReader(conn)
				var hello, join wireMessage
				if reader.Decode(&hello) != nil {
					return
				}
				if writeWire(conn, wireMessage{Type: "hello_ack", Capabilities: []string{fileParallelCapability}, FriendshipState: "friend"}) != nil {
					return
				}
				if reader.Decode(&join) != nil {
					return
				}
				if join.Type == "test_control" {
					var complete wireMessage
					if reader.Decode(&complete) != nil {
						return
					}
					hash := sha256.New()
					_, copyErr := io.Copy(hash, io.NewSectionReader(target, 0, int64(size)))
					status := "completed"
					if copyErr != nil || fmt.Sprintf("%x", hash.Sum(nil)) != fmt.Sprintf("%x", digest.Sum(nil)) {
						status = "failed"
					}
					_ = writeWire(conn, wireMessage{Type: "file_progress", AttachmentID: message.AttachmentID, Transferred: int64(size), Status: status})
					return
				}
				reader = newWireReader(&slowParallelReader{Reader: reader.reader, delay: delay})
				receiver.receiveParallelStream(reader, conn, hello, join)
			}()
		}
	}()
	defer func() { cancel(); listener.Close(); <-accepted; workers.Wait() }()
	sender := NewEngine()
	sender.identity = identity
	peer := Peer{DeviceID: "receiver", IP: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port}
	config, err := sender.clientTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	controlConn, err := tls.Dial("tcp", listener.Addr().String(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer controlConn.Close()
	stop := context.AfterFunc(ctx, func() { controlConn.Close() })
	defer stop()
	controlReader := newWireReader(controlConn)
	if err := writeWire(controlConn, wireMessage{Type: "hello", DeviceID: "sender"}); err != nil {
		t.Fatal(err)
	}
	var helloAck wireMessage
	if err := controlReader.Decode(&helloAck); err != nil {
		t.Fatal(err)
	}
	if err := writeWire(controlConn, wireMessage{Type: "test_control"}); err != nil {
		t.Fatal(err)
	}
	control := newWireSession(controlConn)
	sender.outgoing[message.AttachmentID] = &outgoingTransfer{session: control}
	started := time.Now()
	if err := sender.transferParallelFile(ctx, peer, message, source, control, controlReader, protocolDialects[0], "token", 4, "dzhgo/2"); err != nil {
		t.Fatal(err)
	}
	transfer.parallelMu.Lock()
	defer transfer.parallelMu.Unlock()
	if len(transfer.parallelRanges) != 4 || transfer.parallelWritten != int64(size) {
		t.Fatalf("incomplete ranges: %d, bytes: %d", len(transfer.parallelRanges), transfer.parallelWritten)
	}
	t.Logf("mode=parallel-binary streams=4 bytes=%d elapsed=%s confirmed=%.1fMB/S sha256=verified", size, time.Since(started), float64(size)/time.Since(started).Seconds()/1e6)
}

func TestParallelLaunchAlwaysSchedulesRemainingRanges(t *testing.T) {
	for _, tc := range []struct {
		launched, completed int
		bytes, diskMs       int64
		want                int
	}{
		{1, 0, 8 * 1024 * 1024, 0, 2},
		{2, 0, 32 * 1024 * 1024, 0, 4},
		{1, 0, 32 * 1024 * 1024, 500, 1},
		{1, 1, 9 * 1024 * 1024, 500, 2},
		{2, 2, 18 * 1024 * 1024, 500, 3},
		{3, 3, 27 * 1024 * 1024, 500, 4},
	} {
		if got := parallelLaunchTarget(tc.launched, tc.completed, 4, tc.bytes, tc.diskMs); got != tc.want {
			t.Fatalf("%+v: got %d", tc, got)
		}
	}
}

func TestParallelAckRejectsInvalidConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		bytes         int64
		status, token string
		valid         bool
	}{
		{"valid", 8, "receiving", "token", true},
		{"final", 16, "stream-complete", "token", true},
		{"duplicate", 4, "receiving", "token", false},
		{"reordered", 3, "receiving", "token", false},
		{"beyond_sent", 17, "receiving", "token", false},
		{"premature_final", 8, "stream-complete", "token", false},
		{"missing_final", 16, "receiving", "token", false},
		{"wrong_token", 8, "receiving", "wrong", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var data bytes.Buffer
			if err := writeWire(&data, wireMessage{Type: "file_stream_ack", AttachmentID: "attachment", StreamID: 1, TransferToken: tc.token, StreamBytes: tc.bytes, Status: tc.status}); err != nil {
				t.Fatal(err)
			}
			_, _, err := readParallelStreamAck(newWireReader(&data), Message{AttachmentID: "attachment"}, "token", 1, 16, 4, 16)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestParallelJoinCancellationInterruptsHandshake(t *testing.T) {
	identity, cert := parallelTestIdentity(t)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	helloRead, serverDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		var hello wireMessage
		if newWireReader(conn).Decode(&hello) != nil {
			return
		}
		close(helloRead)
		_, _ = io.Copy(io.Discard, conn)
	}()
	sender := NewEngine()
	sender.identity = identity
	peer := Peer{DeviceID: "receiver", IP: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port}
	result := make(chan error, 1)
	go func() {
		session, _, err := sender.openParallelDataStream(ctx, peer, protocolDialects[0], Message{AttachmentID: "attachment"}, "token", 0, 1, 0, 1024, parallelChunkSize)
		if session != nil {
			session.close()
		}
		result <- err
	}()
	select {
	case <-helloRead:
	case <-time.After(3 * time.Second):
		t.Fatal("TLS hello did not arrive")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("canceled join succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not interrupt TLS join")
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("canceled TLS connection remains open")
	}
}

func TestParallelFailureAckInterruptsBlockedWrite(t *testing.T) {
	identity, cert := parallelTestIdentity(t)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		stop := context.AfterFunc(ctx, func() { conn.Close() })
		defer stop()
		reader := newWireReader(conn)
		var hello, join wireMessage
		if reader.Decode(&hello) != nil {
			return
		}
		if writeWire(conn, wireMessage{Type: "hello_ack", Capabilities: []string{fileParallelCapability}, FriendshipState: "friend"}) != nil {
			return
		}
		if reader.Decode(&join) != nil {
			return
		}
		if writeWire(conn, wireMessage{Type: "file_stream_join_ack", AttachmentID: join.AttachmentID, TransferToken: join.TransferToken, StreamID: join.StreamID, Status: "accepted"}) != nil {
			return
		}
		if _, err := readBinaryFileFrameHeader(reader.reader); err != nil {
			return
		}
		_ = writeWire(conn, wireMessage{Type: "file_stream_ack", AttachmentID: join.AttachmentID, TransferToken: join.TransferToken, StreamID: join.StreamID, Status: "failed", Reason: "INSUFFICIENT_STORAGE"})
		// Keep the connection open without consuming the remaining payload.
		<-ctx.Done()
	}()
	source, err := os.Create(filepath.Join(t.TempDir(), "source"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	const size = 64 * 1024 * 1024
	if err := source.Truncate(size); err != nil {
		t.Fatal(err)
	}
	sender := NewEngine()
	sender.identity = identity
	message := Message{AttachmentID: "attachment", AttachmentSize: size}
	sender.outgoing[message.AttachmentID] = &outgoingTransfer{session: newWireSession(nil)}
	peer := Peer{DeviceID: "receiver", IP: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port}
	updates := make(chan parallelStreamProgress, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		sender.sendParallelStream(ctx, peer, protocolDialects[0], message, source, "token", 1, 0, 0, size, parallelChunkSize, updates)
	}()
	defer func() { cancel(); listener.Close(); <-done; <-serverDone }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("failure ACK did not interrupt the socket write")
	}
	close(updates)
	failed := false
	for update := range updates {
		failed = failed || update.err != nil
		if update.done || update.confirmed != 0 {
			t.Fatal("failed stream reported unconfirmed bytes as completed")
		}
	}
	if !failed {
		t.Fatal("failed stream did not report an error")
	}
}

type slowParallelReader struct {
	io.Reader
	delay time.Duration
}

func (r *slowParallelReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		time.Sleep(time.Duration(int64(r.delay) * int64(n) / (4 * 1024 * 1024)))
	}
	return n, err
}
