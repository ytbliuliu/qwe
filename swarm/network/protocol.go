// Copyright 2016 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package network

/*
bzz implements the swarm wire protocol [bzz] (sister of eth and shh)
the protocol instance is launched on each peer by the network layer if the
bzz protocol handler is registered on the p2p server.

The bzz protocol component speaks the bzz protocol
* handle the protocol handshake
* register peers in the KΛÐΞMLIΛ table via the hive logistic manager
* dispatch to hive for handling the DHT logic
* encode and decode requests for storage and retrieval
* handle sync protocol messages via the syncer
* talks the SWAP payment protocol (swap accounting is done within NetStore)
*/

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/contracts/chequebook"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/p2p"
	bzzswap "github.com/ethereum/go-ethereum/swarm/services/swap"
	"github.com/ethereum/go-ethereum/swarm/services/swap/swap"
	"github.com/ethereum/go-ethereum/swarm/storage"
)

//metrics variables
var (
	storeRequestMsgCounter    = metrics.NewRegisteredCounter("network.protocol.msg.storerequest.count", nil)
	retrieveRequestMsgCounter = metrics.NewRegisteredCounter("network.protocol.msg.retrieverequest.count", nil)
	peersMsgCounter           = metrics.NewRegisteredCounter("network.protocol.msg.peers.count", nil)
	syncRequestMsgCounter     = metrics.NewRegisteredCounter("network.protocol.msg.syncrequest.count", nil)
	unsyncedKeysMsgCounter    = metrics.NewRegisteredCounter("network.protocol.msg.unsyncedkeys.count", nil)
	deliverRequestMsgCounter  = metrics.NewRegisteredCounter("network.protocol.msg.deliverrequest.count", nil)
	paymentMsgCounter         = metrics.NewRegisteredCounter("network.protocol.msg.payment.count", nil)
	invalidMsgCounter         = metrics.NewRegisteredCounter("network.protocol.msg.invalid.count", nil)
	handleStatusMsgCounter    = metrics.NewRegisteredCounter("network.protocol.msg.handlestatus.count", nil)
)

const (
	Version            = 0
	ProtocolLength     = uint64(8)
	ProtocolMaxMsgSize = 10 * 1024 * 1024
	NetworkId          = 3
)

// bzz represents the swarm wire protocol
// an instance is running on each peer
type bzz struct {
	storage    StorageHandler       // handler storage/retrieval related requests coming via the bzz wire protocol
	hive       *Hive                // the logistic manager, peerPool, routing service and peer handler
	dbAccess   *DbAccess            // access to db storage counter and iterator for syncing
	requestDb  *storage.LDBDatabase // db to persist backlog of deliveries to aid syncing
	remoteAddr *peerAddr            // remote peers address
	peer       *p2p.Peer            // the p2p peer object
	rw         p2p.MsgReadWriter    // messageReadWriter to send messages to
	backend    chequebook.Backend
	lastActive time.Time
	NetworkId  uint64

	swap        *swap.Swap          // swap instance for the peer connection
	swapParams  *bzzswap.SwapParams // swap settings both local and remote
	swapEnabled bool                // flag to enable SWAP (will be set via Caps in handshake)
	syncEnabled bool                // flag to enable SYNC (will be set via Caps in handshake)
	syncer      *syncer             // syncer instance for the peer connection
	syncParams  *SyncParams         // syncer params
	syncState   *syncState          // outgoing syncronisation state (contains reference to remote peers db counter)
}

// interface type for handler of storage/retrieval related requests coming
// via the bzz wire protocol
// messages: UnsyncedKeys, DeliveryRequest, StoreRequest, RetrieveRequest
type StorageHandler interface {
	HandleUnsyncedKeysMsg(req *unsyncedKeysMsgData, p *peer) error
	HandleDeliveryRequestMsg(req *deliveryRequestMsgData, p *peer) error
	HandleStoreRequestMsg(req *storeRequestMsgData, p *peer)
	HandleRetrieveRequestMsg(req *retrieveRequestMsgData, p *peer)
}

/*
main entrypoint, wrappers starting a server that will run the bzz protocol
use this constructor to attach the protocol ("class") to server caps
This is done by node.Node#Register(func(node.ServiceContext) (Service, error))
Service implements Protocols() which is an array of protocol constructors
at node startup the protocols are initialised
the Dev p2p layer then calls Run(p *p2p.Peer, rw p2p.MsgReadWriter) error
on each peer connection
The Run function of the Bzz protocol class creates a bzz instance
which will represent the peer for the swarm hive and all peer-aware components
*/
func Bzz(cloud StorageHandler, backend chequebook.Backend, hive *Hive, dbaccess *DbAccess, sp *bzzswap.SwapParams, sy *SyncParams, networkId uint64) (p2p.Protocol, error) {

	// a single global request db is created for all peer connections
	// this is to persist delivery backlog and aid syncronisation
	requestDb, err := storage.NewLDBDatabase(sy.RequestDbPath)
	if err != nil {
		return p2p.Protocol{}, fmt.Errorf("error setting up request db: %v", err)
	}
	if networkId == 0 {
		networkId = NetworkId
	}
	return p2p.Protocol{
		Name:    "bzz",
		Version: Version,
		Length:  ProtocolLength,
		Run: func(p *p2p.Peer, rw p2p.MsgReadWriter) error {
			return run(requestDb, cloud, backend, hive, dbaccess, sp, sy, networkId, p, rw)
		},
	}, nil
}

/*
the main protocol loop that
 * does the handshake by exchanging statusMsg
 * if peer is valid and accepted, registers with the hive
 * then enters into a forever loop handling incoming messages
 * storage and retrieval related queries coming via bzz are dispatched to StorageHandler
 * peer-related messages are dispatched to the hive
 * payment related messages are relayed to SWAP service
 * on disconnect, unregister the peer in the hive (note RemovePeer in the post-disconnect hook)
 * whenever the loop terminates, the peer will disconnect with Subprotocol error
 * whenever handlers return an error the loop terminates
*/
func run(requestDb *storage.LDBDatabase, depo StorageHandler, backend chequebook.Backend, hive *Hive, dbaccess *DbAccess, sp *bzzswap.SwapParams, sy *SyncParams, networkId uint64, p *p2p.Peer, rw p2p.MsgReadWriter) (err error) {

	self := &bzz{
		storage:     depo,
		backend:     backend,
		hive:        hive,
		dbAccess:    dbaccess,
		requestDb:   requestDb,
		peer:        p,
		rw:          rw,
		swapParams:  sp,
		syncParams:  sy,
		swapEnabled: hive.swapEnabled,
		syncEnabled: true,
		NetworkId:   networkId,
	}

	// handle handshake
	err = self.handleStatus()
	if err != nil {
		return err
	}
	defer func() {
		// if the handler loop exits, the peer is disconnecting
		// deregister the peer in the hive
		self.hive.removePeer(&peer{bzz: self})
		if self.syncer != nil {
			self.syncer.stop() // quits request db and delivery loops, save requests
		}
		if self.swap != nil {
			self.swap.Stop() // quits chequebox autocash etc
		}
	}()

	// the main forever loop that handles incoming requests
	for {
		if self.hive.blockRead {
			log.Warn(fmt.Sprintf("Cannot read network"))
			time.Sleep(100 * time.Millisecond)
			continue
		}
		err = self.handle()
		if err != nil {
			return
		}
	}
}

// TODO: may need to implement protocol drop only? don't want to kick off the peer
// if they are useful for other protocols
func (bzz *bzz) Drop() {
	bzz.peer.Disconnect(p2p.DiscSubprotocolError)
}

// one cycle of the main forever loop that handles and dispatches incoming messages
func (bzz *bzz) handle() error {
	msg, err := bzz.rw.ReadMsg()
	log.Debug(fmt.Sprintf("<- %v", msg))
	if err != nil {
		return err
	}
	if msg.Size > ProtocolMaxMsgSize {
		return fmt.Errorf("message too long: %v > %v", msg.Size, ProtocolMaxMsgSize)
	}
	// make sure that the payload has been fully consumed
	defer msg.Discard()

	switch msg.Code {

	case statusMsg:
		// no extra status message allowed. The one needed already handled by
		// handleStatus
		log.Debug(fmt.Sprintf("Status message: %v", msg))
		return errors.New("extra status message")

	case storeRequestMsg:
		// store requests are dispatched to netStore
		storeRequestMsgCounter.Inc(1)
		var req storeRequestMsgData
		if err := msg.Decode(&req); err != nil {
			return fmt.Errorf("<- %v: %v", msg, err)
		}
		if n := len(req.SData); n < 9 {
			return fmt.Errorf("<- %v: Data too short (%v)", msg, n)
		}
		// last Active time is set only when receiving chunks
		self.lastActive = time.Now()
		log.Trace(fmt.Sprintf("incoming store request: %s", req.String()))
		// swap accounting is done within forwarding
		bzz.storage.HandleStoreRequestMsg(&req, &peer{bzz: bzz})

	case retrieveRequestMsg:
		// retrieve Requests are dispatched to netStore
		retrieveRequestMsgCounter.Inc(1)
		var req retrieveRequestMsgData
		if err := msg.Decode(&req); err != nil {
			return fmt.Errorf("<- %v: %v", msg, err)
		}
		req.from = &peer{bzz: bzz}
		// if request is lookup and not to be delivered
		if req.isLookup() {
			log.Trace(fmt.Sprintf("self lookup for %v: responding with peers only...", req.from))
		} else if req.Key == nil {
			return fmt.Errorf("protocol handler: req.Key == nil || req.Timeout == nil")
		} else {
			// swap accounting is done within netStore
			bzz.storage.HandleRetrieveRequestMsg(&req, &peer{bzz: bzz})
		}
		// direct response with peers, TODO: sort this out
		bzz.hive.peers(&req)

	case peersMsg:
		// response to lookups and immediate response to retrieve requests
		// dispatches new peer data to the hive that adds them to KADDB
		peersMsgCounter.Inc(1)
		var req peersMsgData
		if err := msg.Decode(&req); err != nil {
			return fmt.Errorf("<- %v: %v", msg, err)
		}
		req.from = &peer{bzz: bzz}
		log.Trace(fmt.Sprintf("<- peer addresses: %v", req))
		bzz.hive.HandlePeersMsg(&req, &peer{bzz: bzz})

	case syncRequestMsg:
		syncRequestMsgCounter.Inc(1)
		var req syncRequestMsgData
		if err := msg.Decode(&req); err != nil {
			return fmt.Errorf("<- %v: %v", msg, err)
		}
		log.Debug(fmt.Sprintf("<- sync request: %v", req))
		bzz.lastActive = time.Now()
		bzz.sync(req.SyncState)

	case unsyncedKeysMsg:
		// coming from parent node offering
		unsyncedKeysMsgCounter.Inc(1)
		var req unsyncedKeysMsgData
		if err := msg.Decode(&req); err != nil {
			return fmt.Errorf("<- %v: %v", msg, err)
		}
		log.Debug(fmt.Sprintf("<- unsynced keys : %s", req.String()))
		err := bzz.storage.HandleUnsyncedKeysMsg(&req, &peer{bzz: bzz})
		bzz.lastActive = time.Now()
		if err != nil {
			return fmt.Errorf("<- %v: %v", msg, err)
		}

	case deliveryRequestMsg:
		// response to syncKeysMsg hashes filtered not existing in db
		// also relays the last synced state to the source
		deliverRequestMsgCounter.Inc(1)
		var req deliveryRequestMsgData
		if err := msg.Decode(&req); err != nil {
			return fmt.Errorf("<-msg %v: %v", msg, err)
		}
		log.Debug(fmt.Sprintf("<- delivery request: %s", req.String()))
		err := bzz.storage.HandleDeliveryRequestMsg(&req, &peer{bzz: bzz})
		bzz.lastActive = time.Now()
		if err != nil {
			return fmt.Errorf("<- %v: %v", msg, err)
		}

	case paymentMsg:
		// swap protocol message for payment, Units paid for, Cheque paid with
		paymentMsgCounter.Inc(1)
		if bzz.swapEnabled {
			var req paymentMsgData
			if err := msg.Decode(&req); err != nil {
				return fmt.Errorf("<- %v: %v", msg, err)
			}
			log.Debug(fmt.Sprintf("<- payment: %s", req.String()))
			bzz.swap.Receive(int(req.Units), req.Promise)
		}

	default:
		// no other message is allowed
		invalidMsgCounter.Inc(1)
		return fmt.Errorf("invalid message code: %v", msg.Code)
	}
	return nil
}

func (bzz *bzz) handleStatus() (err error) {

	handshake := &statusMsgData{
		Version:   uint64(Version),
		ID:        "honey",
		Addr:      bzz.selfAddr(),
		NetworkId: bzz.NetworkId,
		Swap: &bzzswap.SwapProfile{
			Profile:    bzz.swapParams.Profile,
			PayProfile: bzz.swapParams.PayProfile,
		},
	}

	err = p2p.Send(bzz.rw, statusMsg, handshake)
	if err != nil {
		return err
	}

	// read and handle remote status
	var msg p2p.Msg
	msg, err = bzz.rw.ReadMsg()
	if err != nil {
		return err
	}

	if msg.Code != statusMsg {
		return fmt.Errorf("first msg has code %x (!= %x)", msg.Code, statusMsg)
	}

	handleStatusMsgCounter.Inc(1)

	if msg.Size > ProtocolMaxMsgSize {
		return fmt.Errorf("message too long: %v > %v", msg.Size, ProtocolMaxMsgSize)
	}

	var status statusMsgData
	if err := msg.Decode(&status); err != nil {
		return fmt.Errorf("<- %v: %v", msg, err)
	}

	if status.NetworkId != bzz.NetworkId {
		return fmt.Errorf("network id mismatch: %d (!= %d)", status.NetworkId, bzz.NetworkId)
	}

	if Version != status.Version {
		return fmt.Errorf("protocol version mismatch: %d (!= %d)", status.Version, Version)
	}

	bzz.remoteAddr = bzz.peerAddr(status.Addr)
	log.Trace(fmt.Sprintf("bzz: advertised IP: %v, peer advertised: %v, local address: %v\npeer: advertised IP: %v, remote address: %v\n", bzz.selfAddr(), bzz.remoteAddr, bzz.peer.LocalAddr(), status.Addr.IP, bzz.peer.RemoteAddr()))

	if bzz.swapEnabled {
		// set remote profile for accounting
		bzz.swap, err = bzzswap.NewSwap(bzz.swapParams, status.Swap, bzz.backend, bzz)
		if err != nil {
			return err
		}
	}

	log.Info(fmt.Sprintf("Peer %08x is capable (%d/%d)", bzz.remoteAddr.Addr[:4], status.Version, status.NetworkId))
	err = bzz.hive.addPeer(&peer{bzz: bzz})
	if err != nil {
		return err
	}

	// hive sets syncstate so sync should start after node added
	log.Info(fmt.Sprintf("syncronisation request sent with %v", bzz.syncState))
	bzz.syncRequest()

	return nil
}

func (bzz *bzz) sync(state *syncState) error {
	// syncer setup
	if bzz.syncer != nil {
		return errors.New("sync request can only be sent once")
	}

	cnt := bzz.dbAccess.counter()
	remoteaddr := bzz.remoteAddr.Addr
	start, stop := bzz.hive.kad.KeyRange(remoteaddr)

	// an explicitly received nil syncstate disables syncronisation
	if state == nil {
		bzz.syncEnabled = false
		log.Warn(fmt.Sprintf("syncronisation disabled for peer %v", bzz))
		state = &syncState{DbSyncState: &storage.DbSyncState{}, Synced: true}
	} else {
		state.synced = make(chan bool)
		state.SessionAt = cnt
		if storage.IsZeroKey(state.Stop) && state.Synced {
			state.Start = storage.Key(start[:])
			state.Stop = storage.Key(stop[:])
		}
		log.Debug(fmt.Sprintf("syncronisation requested by peer %v at state %v", bzz, state))
	}
	var err error
	bzz.syncer, err = newSyncer(
		bzz.requestDb,
		storage.Key(remoteaddr[:]),
		bzz.dbAccess,
		bzz.unsyncedKeys, bzz.store,
		bzz.syncParams, state, func() bool { return bzz.syncEnabled },
	)
	if err != nil {
		return nil
	}
	log.Trace(fmt.Sprintf("syncer set for peer %v", bzz))
	return nil
}

func (bzz *bzz) String() string {
	return bzz.remoteAddr.String()
}

// repair reported address if IP missing
func (bzz *bzz) peerAddr(base *peerAddr) *peerAddr {
	if base.IP.IsUnspecified() {
		host, _, _ := net.SplitHostPort(bzz.peer.RemoteAddr().String())
		base.IP = net.ParseIP(host)
	}
	return base
}

// returns self advertised node connection info (listening address w enodes)
// IP will get repaired on the other end if missing
// or resolved via ID by discovery at dialout
func (bzz *bzz) selfAddr() *peerAddr {
	id := bzz.hive.id
	host, port, _ := net.SplitHostPort(bzz.hive.listenAddr())
	intport, _ := strconv.Atoi(port)
	addr := &peerAddr{
		Addr: bzz.hive.addr,
		ID:   id[:],
		IP:   net.ParseIP(host),
		Port: uint16(intport),
	}
	return addr
}

// outgoing messages
// send retrieveRequestMsg
func (bzz *bzz) retrieve(req *retrieveRequestMsgData) error {
	return bzz.send(retrieveRequestMsg, req)
}

// send storeRequestMsg
func (bzz *bzz) store(req *storeRequestMsgData) error {
	return bzz.send(storeRequestMsg, req)
}

func (bzz *bzz) syncRequest() error {
	req := &syncRequestMsgData{}
	if bzz.hive.syncEnabled {
		log.Debug(fmt.Sprintf("syncronisation request to peer %v at state %v", bzz, bzz.syncState))
		req.SyncState = bzz.syncState
	}
	if bzz.syncState == nil {
		log.Warn(fmt.Sprintf("syncronisation disabled for peer %v at state %v", bzz, bzz.syncState))
	}
	return bzz.send(syncRequestMsg, req)
}

// queue storeRequestMsg in request db
func (bzz *bzz) deliveryRequest(reqs []*syncRequest) error {
	req := &deliveryRequestMsgData{
		Deliver: reqs,
	}
	return bzz.send(deliveryRequestMsg, req)
}

// batch of syncRequests to send off
func (bzz *bzz) unsyncedKeys(reqs []*syncRequest, state *syncState) error {
	req := &unsyncedKeysMsgData{
		Unsynced: reqs,
		State:    state,
	}
	return bzz.send(unsyncedKeysMsg, req)
}

// send paymentMsg
func (bzz *bzz) Pay(units int, promise swap.Promise) {
	req := &paymentMsgData{uint(units), promise.(*chequebook.Cheque)}
	bzz.payment(req)
}

// send paymentMsg
func (bzz *bzz) payment(req *paymentMsgData) error {
	return bzz.send(paymentMsg, req)
}

// sends peersMsg
func (bzz *bzz) peers(req *peersMsgData) error {
	return bzz.send(peersMsg, req)
}

func (bzz *bzz) send(msg uint64, data interface{}) error {
	if bzz.hive.blockWrite {
		return fmt.Errorf("network write blocked")
	}
	log.Trace(fmt.Sprintf("-> %v: %v (%T) to %v", msg, data, data, bzz))
	err := p2p.Send(bzz.rw, msg, data)
	if err != nil {
		bzz.Drop()
	}
	return err
}
