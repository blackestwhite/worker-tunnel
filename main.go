package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	mathrand "math/rand"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	defaultListenAddr     = "0.0.0.0:443"
	defaultProxyTarget    = "1.1.1.1:443"
	defaultIPs            = "204.12.196.34,63.141.252.203"
	defaultSNI            = "*.workers.dev"
	defaultAuthToken      = "d4TkphgXAsFk-Ij7APvyw3BPjOhQMDZcO9ngJ4o11wY"
	defaultBufferSize     = 4096
	defaultWorkersCount   = 5
	defaultReconnectAfter = 2 * time.Second
	defaultConnectTimeout = 10 * time.Second

	websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

	wsOpcodeContinuation = 0x0
	wsOpcodeText         = 0x1
	wsOpcodeBinary       = 0x2
	wsOpcodeClose        = 0x8
	wsOpcodePing         = 0x9
	wsOpcodePong         = 0xA
)

var (
	errWorkerInactive = errors.New("worker is not active")
	rng               = mathrand.New(mathrand.NewSource(time.Now().UnixNano()))
	rngMu             sync.Mutex
)

type Config struct {
	ListenAddr     string
	ProxyTarget    string
	IPs            []string
	SNI            string
	AuthToken      string
	BufferSize     int
	WorkersCount   int
	ReconnectAfter time.Duration
	ConnectTimeout time.Duration
	TLSSkipVerify  bool
}

type tunnelMessage struct {
	Type   string `json:"type"`
	ID     uint32 `json:"id"`
	Data   string `json:"data,omitempty"`
	Target string `json:"target,omitempty"`
}

type channelBinding struct {
	conn      net.Conn
	closeOnce sync.Once
}

func (b *channelBinding) Write(data []byte) error {
	_, err := b.conn.Write(data)
	return err
}

func (b *channelBinding) Close() {
	b.closeOnce.Do(func() {
		_ = b.conn.Close()
	})
}

type wsConn struct {
	conn      net.Conn
	reader    *bufio.Reader
	writeMu   sync.Mutex
	closeOnce sync.Once
}

func dialWebSocket(ctx context.Context, cfg Config, workerID string) (*wsConn, error) {
	address, hostHeader, err := websocketDialAddress(cfg)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{Timeout: cfg.ConnectTimeout}
	rawConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}

	tlsConn := tls.Client(rawConn, &tls.Config{
		ServerName:         cfg.SNI,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.TLSSkipVerify,
	})

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = rawConn.Close()
		return nil, err
	}

	wsKey, err := randomBase64(16)
	if err != nil {
		_ = tlsConn.Close()
		return nil, err
	}

	request := strings.Join([]string{
		"GET / HTTP/1.1",
		fmt.Sprintf("Host: %s", hostHeader),
		"Upgrade: websocket",
		"Connection: Upgrade",
		"Sec-WebSocket-Version: 13",
		fmt.Sprintf("Sec-WebSocket-Key: %s", wsKey),
		fmt.Sprintf("Authorization: %s", cfg.AuthToken),
		fmt.Sprintf("X-Link-ID: %s", workerID),
		"",
		"",
	}, "\r\n")

	if _, err := io.WriteString(tlsConn, request); err != nil {
		_ = tlsConn.Close()
		return nil, err
	}

	reader := bufio.NewReader(tlsConn)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+hostHeader+"/", nil)
	if err != nil {
		_ = tlsConn.Close()
		return nil, err
	}

	response, err := http.ReadResponse(reader, req)
	if err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusSwitchingProtocols {
		_ = tlsConn.Close()
		return nil, fmt.Errorf("unexpected handshake status: %s", response.Status)
	}

	if !headerContainsToken(response.Header, "Connection", "upgrade") {
		_ = tlsConn.Close()
		return nil, errors.New("websocket handshake rejected: missing connection upgrade")
	}

	if !headerContainsToken(response.Header, "Upgrade", "websocket") {
		_ = tlsConn.Close()
		return nil, errors.New("websocket handshake rejected: missing upgrade websocket")
	}

	expectedAccept := websocketAccept(wsKey)
	if response.Header.Get("Sec-WebSocket-Accept") != expectedAccept {
		_ = tlsConn.Close()
		return nil, errors.New("websocket handshake rejected: invalid accept key")
	}

	return &wsConn{
		conn:   tlsConn,
		reader: reader,
	}, nil
}

func websocketDialAddress(cfg Config) (address string, hostHeader string, err error) {
	hostHeader = cfg.SNI
	host, port, splitErr := net.SplitHostPort(cfg.SNI)
	if splitErr == nil {
		hostHeader = cfg.SNI
		if len(cfg.IPs) > 0 {
			return net.JoinHostPort(randomIP(cfg.IPs), port), hostHeader, nil
		}
		return cfg.SNI, hostHeader, nil
	}

	port = "443"
	host = cfg.SNI
	hostHeader = host

	if len(cfg.IPs) > 0 {
		return net.JoinHostPort(randomIP(cfg.IPs), port), hostHeader, nil
	}

	return net.JoinHostPort(host, port), hostHeader, nil
}

func headerContainsToken(header http.Header, key, token string) bool {
	for _, value := range header.Values(key) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}

	return false
}

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + websocketGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func randomBase64(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(buf), nil
}

func randomHex(size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}

	return hex.EncodeToString(buf)
}

func randomIP(ips []string) string {
	rngMu.Lock()
	defer rngMu.Unlock()
	return ips[rng.Intn(len(ips))]
}

func (w *wsConn) Close() {
	w.closeOnce.Do(func() {
		w.writeMu.Lock()
		defer w.writeMu.Unlock()
		_ = w.writeFrameLocked(wsOpcodeClose, nil)
		_ = w.conn.Close()
	})
}

func (w *wsConn) WriteJSON(message tunnelMessage) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}

	return w.writeFrame(wsOpcodeText, payload)
}

func (w *wsConn) writeFrame(opcode byte, payload []byte) error {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	return w.writeFrameLocked(opcode, payload)
}

func (w *wsConn) writeFrameLocked(opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	payloadLength := len(payload)

	switch {
	case payloadLength < 126:
		header = append(header, byte(0x80|payloadLength))
	case payloadLength <= 0xFFFF:
		header = append(header, 0x80|126)
		extended := make([]byte, 2)
		binary.BigEndian.PutUint16(extended, uint16(payloadLength))
		header = append(header, extended...)
	default:
		header = append(header, 0x80|127)
		extended := make([]byte, 8)
		binary.BigEndian.PutUint64(extended, uint64(payloadLength))
		header = append(header, extended...)
	}

	maskKey := make([]byte, 4)
	if _, err := rand.Read(maskKey); err != nil {
		return err
	}

	header = append(header, maskKey...)
	maskedPayload := make([]byte, payloadLength)
	for index, value := range payload {
		maskedPayload[index] = value ^ maskKey[index%4]
	}

	if _, err := w.conn.Write(header); err != nil {
		return err
	}

	if _, err := w.conn.Write(maskedPayload); err != nil {
		return err
	}

	return nil
}

func (w *wsConn) ReadMessage() (byte, []byte, error) {
	var messageOpcode byte
	var fragments []byte

	for {
		firstByte, err := w.reader.ReadByte()
		if err != nil {
			return 0, nil, err
		}

		secondByte, err := w.reader.ReadByte()
		if err != nil {
			return 0, nil, err
		}

		fin := firstByte&0x80 != 0
		opcode := firstByte & 0x0F
		masked := secondByte&0x80 != 0
		payloadLength := uint64(secondByte & 0x7F)

		switch payloadLength {
		case 126:
			extended := make([]byte, 2)
			if _, err := io.ReadFull(w.reader, extended); err != nil {
				return 0, nil, err
			}
			payloadLength = uint64(binary.BigEndian.Uint16(extended))
		case 127:
			extended := make([]byte, 8)
			if _, err := io.ReadFull(w.reader, extended); err != nil {
				return 0, nil, err
			}
			payloadLength = binary.BigEndian.Uint64(extended)
		}

		var maskKey []byte
		if masked {
			maskKey = make([]byte, 4)
			if _, err := io.ReadFull(w.reader, maskKey); err != nil {
				return 0, nil, err
			}
		}

		payload := make([]byte, payloadLength)
		if _, err := io.ReadFull(w.reader, payload); err != nil {
			return 0, nil, err
		}

		if masked {
			for index := range payload {
				payload[index] ^= maskKey[index%4]
			}
		}

		switch opcode {
		case wsOpcodePing:
			if err := w.writeFrame(wsOpcodePong, payload); err != nil {
				return 0, nil, err
			}
			continue
		case wsOpcodePong:
			continue
		case wsOpcodeClose:
			_ = w.writeFrame(wsOpcodeClose, nil)
			return 0, nil, io.EOF
		case wsOpcodeText, wsOpcodeBinary:
			if fin {
				return opcode, payload, nil
			}
			messageOpcode = opcode
			fragments = append(fragments[:0], payload...)
		case wsOpcodeContinuation:
			if messageOpcode == 0 {
				return 0, nil, errors.New("unexpected websocket continuation frame")
			}
			fragments = append(fragments, payload...)
			if fin {
				return messageOpcode, fragments, nil
			}
		default:
			return 0, nil, fmt.Errorf("unsupported websocket opcode: %d", opcode)
		}
	}
}

type Worker struct {
	cfg      Config
	id       string
	mu       sync.RWMutex
	ws       *wsConn
	active   bool
	channels map[uint32]*channelBinding
}

func NewWorker(cfg Config) *Worker {
	return &Worker{
		cfg:      cfg,
		id:       randomHex(16),
		channels: make(map[uint32]*channelBinding),
	}
}

func (w *Worker) ID() string {
	return w.id
}

func (w *Worker) IsActive() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.active
}

func (w *Worker) ChannelCount() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.channels)
}

func (w *Worker) HasChannel(channelID uint32) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, exists := w.channels[channelID]
	return exists
}

func (w *Worker) AddChannel(channelID uint32, binding *channelBinding) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.channels[channelID] = binding
}

func (w *Worker) GetChannel(channelID uint32) *channelBinding {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.channels[channelID]
}

func (w *Worker) RemoveChannel(channelID uint32) *channelBinding {
	w.mu.Lock()
	defer w.mu.Unlock()
	binding := w.channels[channelID]
	delete(w.channels, channelID)
	return binding
}

func (w *Worker) setConnected(conn *wsConn) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ws = conn
	w.active = true
}

func (w *Worker) resetConnection() []*channelBinding {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.active = false
	w.ws = nil

	bindings := make([]*channelBinding, 0, len(w.channels))
	for _, binding := range w.channels {
		bindings = append(bindings, binding)
	}

	w.channels = make(map[uint32]*channelBinding)
	return bindings
}

func (w *Worker) send(message tunnelMessage) error {
	w.mu.RLock()
	conn := w.ws
	active := w.active
	w.mu.RUnlock()

	if !active || conn == nil {
		return errWorkerInactive
	}

	return conn.WriteJSON(message)
}

func (w *Worker) SendOpen(channelID uint32, target string) error {
	return w.send(tunnelMessage{
		Type:   "open",
		ID:     channelID,
		Target: target,
	})
}

func (w *Worker) SendData(channelID uint32, data string) error {
	return w.send(tunnelMessage{
		Type: "data",
		ID:   channelID,
		Data: data,
	})
}

func (w *Worker) SendClose(channelID uint32) error {
	err := w.send(tunnelMessage{
		Type: "close",
		ID:   channelID,
	})

	if binding := w.RemoveChannel(channelID); binding != nil {
		binding.Close()
	}

	return err
}

func (w *Worker) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		conn, err := dialWebSocket(ctx, w.cfg, w.id)
		if err != nil {
			log.Printf("worker %s connect failed: %v", w.ID(), err)
			if !sleepWithContext(ctx, w.cfg.ReconnectAfter) {
				return
			}
			continue
		}

		w.setConnected(conn)
		log.Printf("worker %s connected", w.ID())

		err = w.consume(ctx, conn)
		if err != nil && !errors.Is(err, io.EOF) && ctx.Err() == nil {
			log.Printf("worker %s dropped: %v", w.ID(), err)
		}

		conn.Close()
		for _, binding := range w.resetConnection() {
			binding.Close()
		}

		if !sleepWithContext(ctx, w.cfg.ReconnectAfter) {
			return
		}
	}
}

func (w *Worker) consume(ctx context.Context, conn *wsConn) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		opcode, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		if opcode != wsOpcodeText {
			continue
		}

		var message tunnelMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			log.Printf("worker %s received invalid json: %v", w.ID(), err)
			continue
		}

		switch message.Type {
		case "data":
			binding := w.GetChannel(message.ID)
			if binding == nil {
				continue
			}

			raw, err := base64.StdEncoding.DecodeString(message.Data)
			if err != nil {
				log.Printf("worker %s received invalid base64 on channel %d: %v", w.ID(), message.ID, err)
				continue
			}

			if err := binding.Write(raw); err != nil {
				_ = w.SendClose(message.ID)
			}
		case "close":
			if binding := w.RemoveChannel(message.ID); binding != nil {
				binding.Close()
			}
		}
	}
}

type PoolManager struct {
	cfg         Config
	workers     []*Worker
	lastChannel atomic.Uint32
}

func NewPoolManager(cfg Config) *PoolManager {
	workers := make([]*Worker, 0, cfg.WorkersCount)
	for index := 0; index < cfg.WorkersCount; index++ {
		workers = append(workers, NewWorker(cfg))
	}

	return &PoolManager{
		cfg:     cfg,
		workers: workers,
	}
}

func (p *PoolManager) Run(ctx context.Context) error {
	for _, worker := range p.workers {
		go worker.Run(ctx)
	}

	listener, err := net.Listen("tcp", p.cfg.ListenAddr)
	if err != nil {
		return err
	}
	defer listener.Close()

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}

			log.Printf("accept failed: %v", err)
			continue
		}

		go p.handleClient(conn)
	}
}

func (p *PoolManager) handleClient(conn net.Conn) {
	binding := &channelBinding{conn: conn}
	defer binding.Close()

	worker := p.bestWorker()
	if worker == nil {
		log.Printf("no active worker available")
		return
	}

	channelID := p.nextChannelID()
	worker.AddChannel(channelID, binding)

	if err := worker.SendOpen(channelID, p.cfg.ProxyTarget); err != nil {
		log.Printf("failed to open channel %d on worker %s: %v", channelID, worker.ID(), err)
		_ = worker.SendClose(channelID)
		return
	}

	buffer := make([]byte, p.cfg.BufferSize)
	for {
		readBytes, err := conn.Read(buffer)
		if readBytes > 0 {
			encoded := base64.StdEncoding.EncodeToString(buffer[:readBytes])
			if sendErr := worker.SendData(channelID, encoded); sendErr != nil {
				log.Printf("failed to send data on channel %d: %v", channelID, sendErr)
				_ = worker.SendClose(channelID)
				return
			}
		}

		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				log.Printf("client read failed on channel %d: %v", channelID, err)
			}

			if worker.HasChannel(channelID) {
				_ = worker.SendClose(channelID)
			}
			return
		}
	}
}

func (p *PoolManager) bestWorker() *Worker {
	var best *Worker
	bestLoad := int(^uint(0) >> 1)

	for _, worker := range p.workers {
		if !worker.IsActive() {
			continue
		}

		if load := worker.ChannelCount(); load < bestLoad {
			best = worker
			bestLoad = load
		}
	}

	return best
}

func (p *PoolManager) nextChannelID() uint32 {
	for {
		channelID := p.lastChannel.Add(1)
		if channelID == 0 {
			channelID = p.lastChannel.Add(1)
		}

		if !p.channelInUse(channelID) {
			return channelID
		}
	}
}

func (p *PoolManager) channelInUse(channelID uint32) bool {
	for _, worker := range p.workers {
		if worker.HasChannel(channelID) {
			return true
		}
	}

	return false
}

func loadConfig() (Config, error) {
	cfg := Config{
		ListenAddr:     envOrDefault("LISTEN_ADDR", defaultListenAddr),
		ProxyTarget:    envOrDefault("PROXY_TARGET", defaultProxyTarget),
		IPs:            parseCSV(envOrDefault("WORKER_IPS", defaultIPs)),
		SNI:            envOrDefault("SNI", defaultSNI),
		AuthToken:      envOrDefault("AUTH_TOKEN", defaultAuthToken),
		BufferSize:     defaultBufferSize,
		WorkersCount:   defaultWorkersCount,
		ReconnectAfter: defaultReconnectAfter,
		ConnectTimeout: defaultConnectTimeout,
	}

	if value, ok, err := readIntEnv("BUFFER_SIZE"); err != nil {
		return Config{}, err
	} else if ok {
		cfg.BufferSize = value
	}

	if value, ok, err := readIntEnv("WORKERS_COUNT"); err != nil {
		return Config{}, err
	} else if ok {
		cfg.WorkersCount = value
	}

	if value, ok, err := readDurationSeconds("RECONNECT_AFTER_SEC"); err != nil {
		return Config{}, err
	} else if ok {
		cfg.ReconnectAfter = value
	}

	if value, ok, err := readDurationSeconds("CONNECT_TIMEOUT_SEC"); err != nil {
		return Config{}, err
	} else if ok {
		cfg.ConnectTimeout = value
	}

	defaultSkipVerify := strings.Contains(cfg.SNI, "*")
	if value, ok, err := readBoolEnv("TLS_SKIP_VERIFY"); err != nil {
		return Config{}, err
	} else if ok {
		cfg.TLSSkipVerify = value
	} else {
		cfg.TLSSkipVerify = defaultSkipVerify
	}

	if cfg.ListenAddr == "" {
		return Config{}, errors.New("LISTEN_ADDR cannot be empty")
	}

	if cfg.ProxyTarget == "" {
		return Config{}, errors.New("PROXY_TARGET cannot be empty")
	}

	if cfg.SNI == "" {
		return Config{}, errors.New("SNI cannot be empty")
	}

	if cfg.AuthToken == "" {
		return Config{}, errors.New("AUTH_TOKEN cannot be empty")
	}

	if cfg.BufferSize <= 0 {
		return Config{}, errors.New("BUFFER_SIZE must be greater than zero")
	}

	if cfg.WorkersCount <= 0 {
		return Config{}, errors.New("WORKERS_COUNT must be greater than zero")
	}

	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	return value
}

func parseCSV(value string) []string {
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			items = append(items, part)
		}
	}

	return items
}

func readIntEnv(key string) (int, bool, error) {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return 0, false, nil
	}

	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, true, fmt.Errorf("%s must be an integer: %w", key, err)
	}

	return parsed, true, nil
}

func readBoolEnv(key string) (bool, bool, error) {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return false, false, nil
	}

	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, true, fmt.Errorf("%s must be true or false: %w", key, err)
	}

	return parsed, true, nil
}

func readDurationSeconds(key string) (time.Duration, bool, error) {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return 0, false, nil
	}

	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, true, fmt.Errorf("%s must be an integer number of seconds: %w", key, err)
	}

	if seconds < 0 {
		return 0, true, fmt.Errorf("%s cannot be negative", key)
	}

	return time.Duration(seconds) * time.Second, true, nil
}

func sleepWithContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func main() {
	log.SetFlags(log.LstdFlags)

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	manager := NewPoolManager(cfg)

	log.Printf("listening on %s", cfg.ListenAddr)
	log.Printf("proxy target: %s via %d workers", cfg.ProxyTarget, cfg.WorkersCount)

	if cfg.TLSSkipVerify {
		log.Printf("tls verification is disabled for SNI %s", cfg.SNI)
	}

	if err := manager.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
