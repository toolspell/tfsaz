package main

import (
	"bufio"
	"crypto/des"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"math/bits"
	"net"
	"net/url"
	"os"
	"regexp"
	"time"
)

// RFB encodings: raw pixels and the pseudo-encodings a server may send
// instead of pixels.
const (
	encodingRaw                 int32 = 0
	encodingDesktopSize         int32 = -223
	encodingLastRect            int32 = -224
	encodingExtendedDesktopSize int32 = -308
)

// vncPattern finds the VNC address Tart prints for --vnc-experimental.
var vncPattern = regexp.MustCompile(`vnc://[^\s]+`)

// tartVNC returns the address and password of the VNC server of the last
// Tart start that logFile records.
func tartVNC(logFile string) (address, password string, err error) {
	data, err := os.ReadFile(logFile)
	if err != nil {
		return "", "", err
	}
	all := vncPattern.FindAll(data, -1)
	if len(all) == 0 {
		return "", "", fmt.Errorf("no VNC server in %s; vm stop and vm start the clone", logFile)
	}
	u, err := url.Parse(string(all[len(all)-1]))
	if err != nil {
		return "", "", err
	}
	password, _ = u.User.Password()
	return u.Host, password, nil
}

// vncScreenshot saves one frame of a VNC server's screen as a PNG. It
// speaks just enough RFB 3.8 for that: VNC or no authentication, 32-bit
// true colour and raw rectangles.
func vncScreenshot(address, password, file string) error {
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	r := bufio.NewReader(conn)
	img, err := vncFrame(r, conn, password)
	if err != nil {
		return fmt.Errorf("VNC: %w", err)
	}
	out, err := os.Create(file)
	if err != nil {
		return err
	}
	if err := png.Encode(out, img); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func vncFrame(r io.Reader, w io.Writer, password string) (*image.RGBA, error) {
	version := make([]byte, 12)
	if _, err := io.ReadFull(r, version); err != nil {
		return nil, err
	}
	if string(version[:4]) != "RFB " {
		return nil, errors.New("not a VNC server")
	}
	if _, err := io.WriteString(w, "RFB 003.008\n"); err != nil {
		return nil, err
	}
	var count uint8
	if err := binary.Read(r, binary.BigEndian, &count); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, vncReason(r)
	}
	types := make([]byte, count)
	if _, err := io.ReadFull(r, types); err != nil {
		return nil, err
	}
	switch {
	case containsByte(types, 2):
		if _, err := w.Write([]byte{2}); err != nil {
			return nil, err
		}
		challenge := make([]byte, 16)
		if _, err := io.ReadFull(r, challenge); err != nil {
			return nil, err
		}
		response, err := vncAuth(password, challenge)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(response); err != nil {
			return nil, err
		}
	case containsByte(types, 1):
		if _, err := w.Write([]byte{1}); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("no supported security type in %v", types)
	}
	var result uint32
	if err := binary.Read(r, binary.BigEndian, &result); err != nil {
		return nil, err
	}
	if result != 0 {
		return nil, vncReason(r)
	}
	if _, err := w.Write([]byte{1}); err != nil { // ClientInit, shared
		return nil, err
	}
	var init struct {
		Width, Height uint16
		Format        [16]byte
		NameLength    uint32
	}
	if err := binary.Read(r, binary.BigEndian, &init); err != nil {
		return nil, err
	}
	if _, err := io.CopyN(io.Discard, r, int64(init.NameLength)); err != nil {
		return nil, err
	}
	// SetPixelFormat (32 bits, little endian, true colour, red at bit 16),
	// SetEncodings and a full FramebufferUpdateRequest. Apple's VNC server
	// stops the whole VM when a client offers no desktop-size encoding.
	requests := []byte{0, 0, 0, 0, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0}
	encodings := []int32{encodingRaw, encodingDesktopSize, encodingExtendedDesktopSize, encodingLastRect}
	requests = append(requests, 2, 0, 0, byte(len(encodings)))
	for _, e := range encodings {
		requests = binary.BigEndian.AppendUint32(requests, uint32(e))
	}
	if _, err := w.Write(append(requests, vncUpdateRequest(init.Width, init.Height)...)); err != nil {
		return nil, err
	}
	// Apple's server first reports a placeholder size and sends the real
	// one as a desktop-size rectangle before any pixels, and its first full
	// frame after connecting can still be black; the second one is current.
	img := image.NewRGBA(image.Rect(0, 0, int(init.Width), int(init.Height)))
	frames := 0
	for range 50 {
		var kind uint8
		if err := binary.Read(r, binary.BigEndian, &kind); err != nil {
			return nil, err
		}
		switch kind {
		case 0: // FramebufferUpdate
			covered, size, err := vncRects(r, img)
			if err != nil {
				return nil, err
			}
			if size != (image.Point{}) {
				img, frames = image.NewRGBA(image.Rectangle{Max: size}), 0
			} else if covered >= img.Bounds().Dx()*img.Bounds().Dy() {
				if frames++; frames == 2 {
					return img, nil
				}
			}
			bounds := img.Bounds()
			if _, err := w.Write(vncUpdateRequest(uint16(bounds.Dx()), uint16(bounds.Dy()))); err != nil {
				return nil, err
			}
		case 2: // Bell
		case 3: // ServerCutText
			var head struct {
				Padding [3]byte
				Length  uint32
			}
			if err := binary.Read(r, binary.BigEndian, &head); err != nil {
				return nil, err
			}
			if _, err := io.CopyN(io.Discard, r, int64(head.Length)); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unexpected message %d", kind)
		}
	}
	return nil, errors.New("no full frame after 50 updates")
}

// vncUpdateRequest asks for the whole screen, not only what changed.
func vncUpdateRequest(width, height uint16) []byte {
	return []byte{3, 0, 0, 0, 0, 0, byte(width >> 8), byte(width), byte(height >> 8), byte(height)}
}

// vncRects reads one update into img. It returns how many pixels it got,
// and the new screen size when the update announces one.
func vncRects(r io.Reader, img *image.RGBA) (covered int, size image.Point, err error) {
	var head struct {
		Padding uint8
		Rects   uint16
	}
	if err := binary.Read(r, binary.BigEndian, &head); err != nil {
		return 0, size, err
	}
	for range head.Rects {
		var rect struct {
			X, Y, W, H uint16
			Encoding   int32
		}
		if err := binary.Read(r, binary.BigEndian, &rect); err != nil {
			return covered, size, err
		}
		switch rect.Encoding {
		case encodingRaw:
		case encodingDesktopSize:
			size = image.Pt(int(rect.W), int(rect.H))
			continue
		case encodingLastRect:
			return covered, size, nil
		case encodingExtendedDesktopSize:
			var screens struct {
				Count   uint8
				Padding [3]byte
			}
			if err := binary.Read(r, binary.BigEndian, &screens); err != nil {
				return covered, size, err
			}
			if _, err := io.CopyN(io.Discard, r, int64(screens.Count)*16); err != nil {
				return covered, size, err
			}
			size = image.Pt(int(rect.W), int(rect.H))
			continue
		default:
			return covered, size, fmt.Errorf("unexpected encoding %d", rect.Encoding)
		}
		row := make([]byte, int(rect.W)*4)
		for y := range int(rect.H) {
			if _, err := io.ReadFull(r, row); err != nil {
				return covered, size, err
			}
			for x := range int(rect.W) {
				i := img.PixOffset(int(rect.X)+x, int(rect.Y)+y)
				if i < 0 || i+3 >= len(img.Pix) {
					continue
				}
				img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = row[4*x+2], row[4*x+1], row[4*x], 255
			}
		}
		covered += int(rect.W) * int(rect.H)
	}
	return covered, size, nil
}

// vncAuth encrypts the challenge with the password as VNC authentication
// does: DES with the bits of each key byte reversed.
func vncAuth(password string, challenge []byte) ([]byte, error) {
	key := make([]byte, 8)
	copy(key, password)
	for i, b := range key {
		key[i] = bits.Reverse8(b)
	}
	block, err := des.NewCipher(key)
	if err != nil {
		return nil, err
	}
	response := make([]byte, 16)
	block.Encrypt(response[:8], challenge[:8])
	block.Encrypt(response[8:], challenge[8:])
	return response, nil
}

func vncReason(r io.Reader) error {
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return err
	}
	reason := make([]byte, min(length, 1024))
	_, _ = io.ReadFull(r, reason)
	return fmt.Errorf("refused: %s", reason)
}

func containsByte(list []byte, b byte) bool {
	for _, x := range list {
		if x == b {
			return true
		}
	}
	return false
}
