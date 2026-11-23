package server

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func upgradeWebSocket(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		return nil, nil, errors.New("websocket upgrade required")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, nil, errors.New("missing websocket key")
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("websocket hijacking unavailable")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	sum := sha1.Sum([]byte(key + websocketGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	_, _ = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n", accept)
	if protocols := r.Header.Get("Sec-WebSocket-Protocol"); protocols != "" {
		_, _ = fmt.Fprintf(rw, "Sec-WebSocket-Protocol: binary\r\n")
	}
	_, _ = fmt.Fprint(rw, "\r\n")
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}

	return conn, rw, nil
}

func readWebSocketFrame(reader *bufio.Reader) (byte, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}
	opcode := header[0] & 0x0f
	masked := header[1]&0x80 != 0
	length := int64(header[1] & 0x7f)
	switch length {
	case 126:
		var extended uint16
		if err := binary.Read(reader, binary.BigEndian, &extended); err != nil {
			return 0, nil, err
		}
		length = int64(extended)
	case 127:
		var extended uint64
		if err := binary.Read(reader, binary.BigEndian, &extended); err != nil {
			return 0, nil, err
		}
		length = int64(extended)
	}
	if length < 0 || length > 16<<20 {
		return 0, nil, errors.New("websocket frame too large")
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(reader, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for index := range payload {
			payload[index] ^= mask[index%4]
		}
	}

	return opcode, payload, nil
}

func writeWebSocketFrame(writer io.Writer, opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode, 0}
	switch {
	case len(payload) < 126:
		header[1] = byte(len(payload))
	case len(payload) <= 65535:
		header[1] = 126
		extended := make([]byte, 2)
		binary.BigEndian.PutUint16(extended, uint16(len(payload)))
		header = append(header, extended...)
	default:
		header[1] = 127
		extended := make([]byte, 8)
		binary.BigEndian.PutUint64(extended, uint64(len(payload)))
		header = append(header, extended...)
	}

	if _, err := writer.Write(header); err != nil {
		return err
	}
	_, err := writer.Write(payload)

	return err
}

func relayWebSocket(ws net.Conn, reader *bufio.Reader, target net.Conn) {
	done := make(chan struct{})
	var closeOnce sync.Once
	var writeMu sync.Mutex
	closeConnections := func() {
		closeOnce.Do(func() {
			close(done)
			_ = target.Close()
			_ = ws.Close()
		})
	}
	writeWS := func(opcode byte, payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()

		return writeWebSocketFrame(ws, opcode, payload)
	}

	go func() {
		defer closeConnections()
		for {
			opcode, payload, err := readWebSocketFrame(reader)
			if err != nil {
				return
			}
			switch opcode {
			case 0x1, 0x2:
				if _, err := target.Write(payload); err != nil {
					return
				}
			case 0x8:
				_ = writeWS(0x8, nil)
				return
			case 0x9:
				if err := writeWS(0xA, payload); err != nil {
					return
				}
			case 0xA:
				continue
			}
		}
	}()

	go func() {
		defer closeConnections()
		buffer := make([]byte, 32*1024)
		for {
			_ = target.SetReadDeadline(time.Now().Add(2 * time.Minute))
			count, err := target.Read(buffer)
			if count > 0 {
				if writeErr := writeWS(0x2, buffer[:count]); writeErr != nil {
					return
				}
			}
			if err != nil {
				_ = writeWS(0x8, nil)
				return
			}
		}
	}()

	<-done
}
