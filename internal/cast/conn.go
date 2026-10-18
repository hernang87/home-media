package cast

import (
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// CASTv2 namespaces and ids.
const (
	nsConnection = "urn:x-cast:com.google.cast.tp.connection"
	nsHeartbeat  = "urn:x-cast:com.google.cast.tp.heartbeat"
	nsReceiver   = "urn:x-cast:com.google.cast.receiver"
	nsMedia      = "urn:x-cast:com.google.cast.media"

	senderID   = "sender-homemedia"
	receiverID = "receiver-0"

	maxFrame = 1 << 20 // sanity limit; real messages are a few KB
)

// message is one decoded CastMessage with a JSON string payload.
type message struct {
	Namespace string
	Source    string
	Dest      string
	Data      []byte
}

// header is the part of every JSON payload we route on.
type header struct {
	Type      string `json:"type"`
	RequestID int    `json:"requestId"`
}

// conn is a CASTv2 TLS connection. Writes are serialized; reads are done by readLoop.
type conn struct {
	c      net.Conn
	wmu    sync.Mutex
	nextID int
	idMu   sync.Mutex
}

func dial(addr string, port int, timeout time.Duration) (*conn, error) {
	d := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	// Chromecasts present self-signed certificates; the protocol relies on LAN trust.
	tc, err := tls.DialWithDialer(d, "tcp", net.JoinHostPort(addr, strconv.Itoa(port)), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return nil, fmt.Errorf("connect to chromecast %s:%d: %w", addr, port, err)
	}
	return &conn{c: tc}, nil
}

func (c *conn) close() error { return c.c.Close() }

func (c *conn) requestID() int {
	c.idMu.Lock()
	defer c.idMu.Unlock()
	c.nextID++
	return c.nextID
}

// encodeMessage builds the CastMessage protobuf (fields 1..6, payload type STRING).
func encodeMessage(ns, src, dst string, payload []byte) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, 0) // CASTV2_1_0
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendString(b, src)
	b = protowire.AppendTag(b, 3, protowire.BytesType)
	b = protowire.AppendString(b, dst)
	b = protowire.AppendTag(b, 4, protowire.BytesType)
	b = protowire.AppendString(b, ns)
	b = protowire.AppendTag(b, 5, protowire.VarintType)
	b = protowire.AppendVarint(b, 0) // STRING
	b = protowire.AppendTag(b, 6, protowire.BytesType)
	b = protowire.AppendBytes(b, payload)
	return b
}

func decodeMessage(b []byte) (message, error) {
	var m message
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return m, protowire.ParseError(n)
		}
		b = b[n:]
		switch typ {
		case protowire.VarintType:
			_, n = protowire.ConsumeVarint(b)
		case protowire.BytesType:
			var v []byte
			v, n = protowire.ConsumeBytes(b)
			if n >= 0 {
				switch num {
				case 2:
					m.Source = string(v)
				case 3:
					m.Dest = string(v)
				case 4:
					m.Namespace = string(v)
				case 6:
					m.Data = append([]byte(nil), v...)
				}
			}
		default:
			n = protowire.ConsumeFieldValue(num, typ, b)
		}
		if n < 0 {
			return m, protowire.ParseError(n)
		}
		b = b[n:]
	}
	return m, nil
}

// send marshals payload (a struct or map; it gets a requestId when it has none)
// and writes it as one length-prefixed frame. It returns the request ID used.
func (c *conn) send(ns, dst string, payload map[string]any) (int, error) {
	id := 0
	if _, ok := payload["requestId"]; !ok && ns != nsHeartbeat {
		id = c.requestID()
		payload["requestId"] = id
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	body := encodeMessage(ns, senderID, dst, data)
	frame := make([]byte, 4, 4+len(body))
	binary.BigEndian.PutUint32(frame, uint32(len(body)))
	frame = append(frame, body...)

	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = c.c.Write(frame)
	return id, err
}

// readLoop delivers messages on out until the connection fails, then closes out.
// PINGs are answered here so heartbeats keep working even if nobody reads out.
func (c *conn) readLoop(out chan<- message) {
	defer close(out)
	for {
		var lenBuf [4]byte
		if _, err := io.ReadFull(c.c, lenBuf[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(lenBuf[:])
		if n == 0 || n > maxFrame {
			return
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(c.c, body); err != nil {
			return
		}
		m, err := decodeMessage(body)
		if err != nil {
			continue
		}
		if m.Namespace == nsHeartbeat {
			var h header
			if json.Unmarshal(m.Data, &h) == nil && h.Type == "PING" {
				_, _ = c.send(nsHeartbeat, m.Source, map[string]any{"type": "PONG"})
			}
		}
		out <- m
	}
}

var errClosed = errors.New("chromecast connection closed")
