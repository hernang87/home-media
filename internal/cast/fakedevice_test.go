package cast

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDevice is a minimal CASTv2 receiver: enough to exercise LOAD, subtitles and transport commands.
type fakeDevice struct {
	t    *testing.T
	ln   net.Listener
	Port int

	mu       sync.Mutex
	received []map[string]any // every JSON payload from the sender, with "_ns" added
	appUp    bool
	state    string
	pos      float64
	active   []int
	media    map[string]any
	mediaID  int  // a new mediaSessionId per LOAD, like a real receiver
	loaded   bool // media is loaded in the running app
	conns    []net.Conn
	accepted atomic.Int32 // connections accepted so far
	dropOn   string       // message type that makes the device hang up
}

func newFakeDevice(t *testing.T) *fakeDevice {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake-cast"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDevice{t: t, ln: ln, Port: ln.Addr().(*net.TCPAddr).Port, state: "IDLE"}
	go d.serve()
	t.Cleanup(func() { ln.Close() })
	return d
}

func (d *fakeDevice) serve() {
	for {
		c, err := d.ln.Accept()
		if err != nil {
			return
		}
		d.accepted.Add(1)
		go d.handle(c)
	}
}

func (d *fakeDevice) reply(c net.Conn, ns, src, dst string, payload map[string]any) {
	data, _ := json.Marshal(payload)
	body := encodeMessage(ns, src, dst, data)
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	_, _ = c.Write(append(frame, body...))
}

func (d *fakeDevice) receiverStatus(c net.Conn, reqID float64) {
	apps := []any{}
	if d.appUp {
		apps = append(apps, map[string]any{"appId": defaultReceiverApp, "transportId": "tr-1", "sessionId": "s-1"})
	}
	d.reply(c, nsReceiver, receiverID, senderID, map[string]any{"type": "RECEIVER_STATUS", "requestId": reqID,
		"status": map[string]any{"applications": apps, "volume": map[string]any{"level": 0.5, "muted": false}}})
}

func (d *fakeDevice) mediaStatus(c net.Conn, reqID float64, idle string) {
	if !d.loaded {
		d.reply(c, nsMedia, "tr-1", senderID, map[string]any{"type": "MEDIA_STATUS", "requestId": reqID, "status": []any{}})
		return
	}
	st := map[string]any{"mediaSessionId": d.mediaID, "playerState": d.state, "currentTime": d.pos, "activeTrackIds": d.active}
	if idle != "" {
		st["idleReason"] = idle
	}
	if m, ok := d.media["media"].(map[string]any); ok {
		st["media"] = map[string]any{"duration": m["duration"]}
	}
	d.reply(c, nsMedia, "tr-1", senderID, map[string]any{"type": "MEDIA_STATUS", "requestId": reqID, "status": []any{st}})
}

func (d *fakeDevice) handle(c net.Conn) {
	defer c.Close()
	d.mu.Lock()
	d.conns = append(d.conns, c)
	d.mu.Unlock()
	for {
		var lb [4]byte
		if _, err := io.ReadFull(c, lb[:]); err != nil {
			return
		}
		body := make([]byte, binary.BigEndian.Uint32(lb[:]))
		if _, err := io.ReadFull(c, body); err != nil {
			return
		}
		m, err := decodeMessage(body)
		if err != nil {
			return
		}
		var p map[string]any
		_ = json.Unmarshal(m.Data, &p)
		p["_ns"] = m.Namespace
		typ, _ := p["type"].(string)
		reqID, _ := p["requestId"].(float64)

		d.mu.Lock()
		d.received = append(d.received, p)
		if typ == d.dropOn {
			d.mu.Unlock()
			return
		}
		switch {
		case m.Namespace == nsHeartbeat && typ == "PING":
			d.reply(c, nsHeartbeat, receiverID, senderID, map[string]any{"type": "PONG"})
		case m.Namespace == nsReceiver && typ == "GET_STATUS":
			d.receiverStatus(c, reqID)
		case m.Namespace == nsReceiver && typ == "LAUNCH":
			d.appUp, d.loaded = true, false
			d.receiverStatus(c, reqID)
		case m.Namespace == nsReceiver && typ == "STOP":
			d.appUp, d.loaded, d.state = false, false, "IDLE"
			d.receiverStatus(c, reqID)
		case m.Namespace == nsMedia && typ == "LOAD":
			d.media = p
			d.mediaID++
			d.loaded = true
			d.state = "PLAYING"
			d.pos, _ = p["currentTime"].(float64)
			d.active = nil
			if ids, ok := p["activeTrackIds"].([]any); ok {
				for _, id := range ids {
					d.active = append(d.active, int(id.(float64)))
				}
			}
			d.mediaStatus(c, reqID, "")
		case m.Namespace == nsMedia && typ == "PAUSE":
			d.state = "PAUSED"
			d.mediaStatus(c, reqID, "")
		case m.Namespace == nsMedia && typ == "PLAY":
			d.state = "PLAYING"
			d.mediaStatus(c, reqID, "")
		case m.Namespace == nsMedia && typ == "SEEK":
			d.pos, _ = p["currentTime"].(float64)
			d.mediaStatus(c, reqID, "")
		case m.Namespace == nsMedia && typ == "EDIT_TRACKS_INFO":
			d.active = nil
			for _, id := range p["activeTrackIds"].([]any) {
				d.active = append(d.active, int(id.(float64)))
			}
			d.mediaStatus(c, reqID, "")
		case m.Namespace == nsMedia && typ == "GET_STATUS":
			d.mediaStatus(c, reqID, "")
		}
		d.mu.Unlock()
	}
}

// finish simulates the media reaching its end by pushing IDLE/FINISHED to the sender.
func (d *fakeDevice) finish() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state = "IDLE"
	for _, c := range d.conns {
		d.mediaStatus(c, 0, "FINISHED")
	}
}

// hangUp closes every connection, like a Wi-Fi drop; playback carries on.
func (d *fakeDevice) hangUp() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.conns {
		c.Close()
	}
	d.conns = nil
}

// hangUpOn makes the device close the connection when it receives typ.
func (d *fakeDevice) hangUpOn(typ string) {
	d.mu.Lock()
	d.dropOn = typ
	d.mu.Unlock()
}

// last returns the most recent payload of a given type, or nil.
func (d *fakeDevice) last(typ string) map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := len(d.received) - 1; i >= 0; i-- {
		if d.received[i]["type"] == typ {
			return d.received[i]
		}
	}
	return nil
}

// count returns how many payloads of a given type the device received.
func (d *fakeDevice) count(typ string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, p := range d.received {
		if p["type"] == typ {
			n++
		}
	}
	return n
}
