package core

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
	"golang.org/x/crypto/ssh"
)

func proxyPathServerChainSeed(server model.Server) string {
	if seed := strings.TrimSpace(server.ChainSecret); seed != "" {
		return seed
	}
	if seed := strings.TrimSpace(server.AgentTokenHash); seed != "" {
		return seed
	}
	// Persisted servers always have ChainSecret. The fallback keeps pure Core
	// callers and fixtures deterministic without depending on Agent enrollment.
	return fmt.Sprintf("server:%d", server.ID)
}

func proxyPathTunnelKeyMaterial(source, target model.Server, reuseKey, label string) []byte {
	mac := hmac.New(sha256.New, []byte(proxyPathServerChainSeed(target)))
	_, _ = mac.Write([]byte("oboard-shared-tunnel\x00"))
	_, _ = mac.Write([]byte(label))
	_, _ = mac.Write([]byte("\x00"))
	_, _ = mac.Write([]byte(proxyPathServerChainSeed(source)))
	_, _ = mac.Write([]byte("\x00"))
	_, _ = mac.Write([]byte(reuseKey))
	return mac.Sum(nil)
}

func proxyPathTrustedForwardKey(source, target model.Server, pathID, processingStepID int64) string {
	reuseKey := fmt.Sprintf("%d:%d", pathID, processingStepID)
	material := proxyPathTunnelKeyMaterial(source, target, reuseKey, "trusted-forward-v1")
	return base64.RawStdEncoding.EncodeToString(material[:32])
}

func proxyPathTrustedForwardReceiverID(pathID, processingStepID int64) string {
	return fmt.Sprintf("path-%d-step-%d", pathID, processingStepID)
}

func proxyPathSharedTrustedForwardKey(source, target model.Server, inboundID int64, processingPosition int) string {
	reuseKey := fmt.Sprintf("inbound:%d:position:%d", inboundID, processingPosition)
	material := proxyPathTunnelKeyMaterial(source, target, reuseKey, "trusted-forward-v1")
	return base64.RawStdEncoding.EncodeToString(material[:32])
}

func proxyPathSharedTrustedForwardReceiverID(inboundID int64, processingPosition int) string {
	return fmt.Sprintf("inbound-%d-transparent-step-%d", inboundID, processingPosition)
}

func proxyPathSSHKeyPair(source, target model.Server, reuseKey string) (string, string, error) {
	seed := proxyPathTunnelKeyMaterial(source, target, reuseKey, "ssh-ed25519")
	privateKey := ed25519.NewKeyFromSeed(seed[:ed25519.SeedSize])
	publicKey, err := ssh.NewPublicKey(privateKey.Public())
	if err != nil {
		return "", "", err
	}
	privatePEM := marshalDeterministicOpenSSHPrivateKey(privateKey, publicKey, binary.BigEndian.Uint32(seed[:4]))
	return string(privatePEM), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(publicKey))), nil
}

func marshalDeterministicOpenSSHPrivateKey(privateKey ed25519.PrivateKey, publicKey ssh.PublicKey, check uint32) []byte {
	privateBlock := make([]byte, 0, 160)
	privateBlock = appendSSHUint32(privateBlock, check)
	privateBlock = appendSSHUint32(privateBlock, check)
	privateBlock = appendSSHString(privateBlock, []byte(ssh.KeyAlgoED25519))
	privateBlock = appendSSHString(privateBlock, privateKey.Public().(ed25519.PublicKey))
	privateBlock = appendSSHString(privateBlock, privateKey)
	privateBlock = appendSSHString(privateBlock, nil)
	for padding := byte(1); len(privateBlock)%8 != 0; padding++ {
		privateBlock = append(privateBlock, padding)
	}

	encoded := append([]byte(nil), []byte("openssh-key-v1\x00")...)
	encoded = appendSSHString(encoded, []byte("none"))
	encoded = appendSSHString(encoded, []byte("none"))
	encoded = appendSSHString(encoded, nil)
	encoded = appendSSHUint32(encoded, 1)
	encoded = appendSSHString(encoded, publicKey.Marshal())
	encoded = appendSSHString(encoded, privateBlock)
	return pem.EncodeToMemory(&pem.Block{Type: "OPENSSH PRIVATE KEY", Bytes: encoded})
}

func appendSSHString(dst, value []byte) []byte {
	// #nosec G115 -- callers pass fixed algorithm names and bounded Ed25519 key blocks.
	dst = appendSSHUint32(dst, uint32(len(value)))
	return append(dst, value...)
}

func appendSSHUint32(dst []byte, value uint32) []byte {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	return append(dst, encoded[:]...)
}

func proxyPathWireGuardKeyPair(source, target model.Server, reuseKey, label string) (string, string, error) {
	raw := append([]byte(nil), proxyPathTunnelKeyMaterial(source, target, reuseKey, label)...)
	raw = raw[:32]
	raw[0] &= 248
	raw[31] &= 127
	raw[31] |= 64
	privateKey, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(raw), base64.StdEncoding.EncodeToString(privateKey.PublicKey().Bytes()), nil
}
