package cloud

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"strings"
)

// Archives are encrypted with AES-256-GCM in fixed-size chunks (the STREAM construction): every chunk is
// authenticated, the nonce carries a counter and a "last chunk" flag, so a reordered, truncated or edited
// archive fails to open instead of restoring part of an instance. The key is the owner's recovery key:
// 32 random bytes shown once; the server keeps it sealed under auth.secret_key (D70).

const (
	magic     = "CODRBK1\x00"
	chunkSize = 64 << 10
	prefixLen = 7
)

// ErrOpen means the archive does not open with this key or is damaged; no plaintext was trusted.
var ErrOpen = errors.New("cloud: archive does not open with this key, or it is damaged")

// NewRecoveryKey returns a fresh key as 64 hex characters.
func NewRecoveryKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ParseRecoveryKey accepts the key with or without spaces/dashes between groups.
func ParseRecoveryKey(s string) ([]byte, error) {
	s = strings.NewReplacer(" ", "", "-", "", "\n", "").Replace(strings.TrimSpace(s))
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return nil, errors.New("cloud: a recovery key is 64 hex characters")
	}
	return b, nil
}

// KeyHint is a short, safe label for a key: its first and last four characters.
func KeyHint(hexKey string) string {
	if len(hexKey) < 8 {
		return ""
	}
	return hexKey[:4] + "…" + hexKey[len(hexKey)-4:]
}

func aead(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("cloud: key must be 32 bytes")
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func nonce(prefix []byte, n uint32, last bool) []byte {
	out := make([]byte, 12)
	copy(out, prefix)
	binary.BigEndian.PutUint32(out[prefixLen:], n)
	if last {
		out[11] = 1
	}
	return out
}

type sealer struct {
	w      io.Writer
	g      cipher.AEAD
	prefix []byte
	n      uint32
	buf    []byte
	closed bool
}

// Seal returns a writer that encrypts into w; Close writes the final chunk and must be called.
func Seal(w io.Writer, key []byte) (io.WriteCloser, error) {
	g, err := aead(key)
	if err != nil {
		return nil, err
	}
	p := make([]byte, prefixLen)
	if _, err := rand.Read(p); err != nil {
		return nil, err
	}
	if _, err := w.Write(append([]byte(magic), p...)); err != nil {
		return nil, err
	}
	return &sealer{w: w, g: g, prefix: p, buf: make([]byte, 0, chunkSize)}, nil
}

func (s *sealer) Write(p []byte) (int, error) {
	if s.closed {
		return 0, errors.New("cloud: write after close")
	}
	written := 0
	for len(p) > 0 {
		room := chunkSize - len(s.buf)
		if room == 0 { // a full chunk is only sealed once we know it is not the last one
			if err := s.flush(false); err != nil {
				return written, err
			}
			room = chunkSize
		}
		n := min(room, len(p))
		s.buf = append(s.buf, p[:n]...)
		p, written = p[n:], written+n
	}
	return written, nil
}

func (s *sealer) flush(last bool) error {
	ct := s.g.Seal(nil, nonce(s.prefix, s.n, last), s.buf, nil)
	s.n++
	s.buf = s.buf[:0]
	_, err := s.w.Write(ct)
	return err
}

func (s *sealer) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.flush(true)
}

type opener struct {
	r      io.Reader
	g      cipher.AEAD
	prefix []byte
	n      uint32
	plain  []byte
	next   []byte // the chunk after the current one, read ahead to know which is last
	done   bool
}

// Open returns a reader of the plaintext; it fails with ErrOpen on a wrong key, an edited chunk or a
// missing end. Every byte it returns has been authenticated.
func Open(r io.Reader, key []byte) (io.Reader, error) {
	g, err := aead(key)
	if err != nil {
		return nil, err
	}
	head := make([]byte, len(magic)+prefixLen)
	if _, err := io.ReadFull(r, head); err != nil || string(head[:len(magic)]) != magic {
		return nil, ErrOpen
	}
	o := &opener{r: r, g: g, prefix: head[len(magic):]}
	if o.next, err = o.readChunk(); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *opener) readChunk() ([]byte, error) {
	b := make([]byte, chunkSize+o.g.Overhead())
	n, err := io.ReadFull(o.r, b)
	if err == io.EOF {
		return nil, nil
	}
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return b[:n], nil
}

func (o *opener) Read(p []byte) (int, error) {
	for len(o.plain) == 0 {
		if o.done {
			return 0, io.EOF
		}
		cur := o.next
		if cur == nil {
			return 0, ErrOpen // the stream ended without a final chunk: truncated
		}
		next, err := o.readChunk()
		if err != nil {
			return 0, err
		}
		last := next == nil
		pt, err := o.g.Open(nil, nonce(o.prefix, o.n, last), cur, nil)
		if err != nil {
			return 0, ErrOpen
		}
		o.n++
		o.plain, o.next, o.done = pt, next, last
	}
	n := copy(p, o.plain)
	o.plain = o.plain[n:]
	return n, nil
}
