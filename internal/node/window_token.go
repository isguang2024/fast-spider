package node

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"
)

const windowTokenTTL = 5 * time.Minute

var ErrWindowTokenInvalid = errors.New("window token is invalid or expired")

func windowTokenKey(privateKey []byte) [32]byte {
	h := sha256.New()
	_, _ = h.Write([]byte("fast-spider-window-token-v1\n"))
	_, _ = h.Write(privateKey)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func makeWindowToken(key [32]byte, workspaceID string, handle uint64, identity [8]byte, now time.Time) string {
	payload := make([]byte, 24)
	binary.BigEndian.PutUint64(payload[0:8], handle)
	binary.BigEndian.PutUint64(payload[8:16], uint64(now.UTC().Add(windowTokenTTL).Unix()))
	copy(payload[16:24], identity[:])
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte(workspaceID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	tag := mac.Sum(nil)[:16]
	raw := append(payload, tag...)
	return "win_" + base64.RawURLEncoding.EncodeToString(raw)
}

func parseWindowToken(key [32]byte, workspaceID, token string, now time.Time) (uint64, [8]byte, error) {
	var identity [8]byte
	if len(token) < 5 || token[:4] != "win_" {
		return 0, identity, ErrWindowTokenInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(token[4:])
	if err != nil || len(raw) != 40 {
		return 0, identity, ErrWindowTokenInvalid
	}
	payload, tag := raw[:24], raw[24:]
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte(workspaceID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	if !hmac.Equal(tag, mac.Sum(nil)[:16]) {
		return 0, identity, ErrWindowTokenInvalid
	}
	handle := binary.BigEndian.Uint64(payload[0:8])
	expires := int64(binary.BigEndian.Uint64(payload[8:16]))
	copy(identity[:], payload[16:24])
	if handle == 0 || expires <= now.UTC().Unix() {
		return 0, identity, ErrWindowTokenInvalid
	}
	return handle, identity, nil
}

func windowIdentity(info nativeWindowInfo) [8]byte {
	hasher := sha256.New()
	var processID [4]byte
	binary.BigEndian.PutUint32(processID[:], info.ProcessID)
	_, _ = hasher.Write(processID[:])
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(info.ClassName))
	var identity [8]byte
	copy(identity[:], hasher.Sum(nil)[:8])
	return identity
}
