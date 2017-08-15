package pss

import (
	"bytes"
	"crypto/ecdsa"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"
	whisper "github.com/ethereum/go-ethereum/whisper/whisperv5"
)

const (
	defaultSymKeyCacheCapacity    = 512
	defaultDigestCacheTTL         = time.Second
	defaultRecipientAddressLength = 4 // how many bytes to use for routing (the bytes will be unencrypted in transit)
)

// Pss configuration parameters
type PssParams struct {
	Cachettl               time.Duration
	privatekey             *ecdsa.PrivateKey
	SymKeyCacheCapacity    int
	RecipientAddressLength int
}

// Sane defaults for Pss
func NewPssParams(privatekey *ecdsa.PrivateKey) *PssParams {
	return &PssParams{
		Cachettl:               defaultDigestCacheTTL,
		privatekey:             privatekey,
		SymKeyCacheCapacity:    defaultSymKeyCacheCapacity,
		RecipientAddressLength: defaultRecipientAddressLength,
	}
}

// Encapsulates messages transported over pss.
type PssMsg struct {
	To      []byte
	Payload *whisper.Envelope
}

// serializes the message for use in cache
func (msg *PssMsg) serialize() []byte {
	rlpdata, _ := rlp.EncodeToBytes(msg)
	return rlpdata
}

// String representation of PssMsg
func (self *PssMsg) String() string {
	return fmt.Sprintf("PssMsg: Recipient: %x", common.ByteLabel(self.To))
}

// Convenience wrapper for devp2p protocol messages for transport over pss
type ProtocolMsg struct {
	Code       uint64
	Size       uint32
	Payload    []byte
	ReceivedAt time.Time
}

// Creates a ProtocolMsg
func NewProtocolMsg(code uint64, msg interface{}) ([]byte, error) {

	rlpdata, err := rlp.EncodeToBytes(msg)
	if err != nil {
		return nil, err
	}

	// TODO verify that nested structs cannot be used in rlp
	smsg := &ProtocolMsg{
		Code:    code,
		Size:    uint32(len(rlpdata)),
		Payload: rlpdata,
	}

	return rlp.EncodeToBytes(smsg)
}

// Convenience wrapper for sending and receiving pss messages when using the pss API
type APIMsg struct {
	Msg  []byte
	Addr []byte
}

// for debugging, show nice hex version
func (self *APIMsg) String() string {
	return fmt.Sprintf("APIMsg: from: %s..., msg: %s...", common.ByteLabel(self.Msg), common.ByteLabel(self.Addr))
}

// Signature for a message handler function for a PssMsg
//
// Implementations of this type are passed to Pss.Register together with a topic,
type Handler func(msg []byte, p *p2p.Peer, from []byte) error

// For devp2p protocol integration only
//
// Creates a serialized (non-buffered) version of a p2p.Msg, used in the specialized p2p.MsgReadwriter implementations used internally by pss
//
// Should not normally be called outside the pss package hierarchy
func ToP2pMsg(msg []byte) (p2p.Msg, error) {
	payload := &ProtocolMsg{}
	if err := rlp.DecodeBytes(msg, payload); err != nil {
		return p2p.Msg{}, fmt.Errorf("pss protocol handler unable to decode payload as p2p message: %v", err)
	}

	return p2p.Msg{
		Code:       payload.Code,
		Size:       uint32(len(payload.Payload)),
		ReceivedAt: time.Now(),
		Payload:    bytes.NewBuffer(payload.Payload),
	}, nil
}
