package fnConsensus

import (
	"fmt"
	"sync"

	"github.com/tendermint/tendermint/crypto"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/p2p/conn"
	"github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/types"

	dbm "github.com/tendermint/tendermint/libs/db"
)

const FnVoteSetChannelID = byte(0x50)

type FnConsensusReactor struct {
	p2p.BaseReactor

	connectedPeers map[p2p.ID]p2p.Peer
	mtx            sync.RWMutex
	state          *ReactorState
	db             dbm.DB
	tmStateDB      dbm.DB
	chainID        string

	fnRegistry *FnRegistry

	nodePrivKey crypto.PrivKey
}

func NewFnConsensusReactor(chainID string, nodePrivKey crypto.PrivKey, fnRegistry *FnRegistry, db dbm.DB, tmStateDB dbm.DB) *FnConsensusReactor {
	reactor := &FnConsensusReactor{
		connectedPeers: make(map[p2p.ID]p2p.Peer),
		db:             db,
		chainID:        chainID,
		tmStateDB:      tmStateDB,
		fnRegistry:     fnRegistry,
		nodePrivKey:    nodePrivKey,
	}

	reactor.BaseReactor = *p2p.NewBaseReactor("FnConsensusReactor", reactor)
	return reactor
}

func (f *FnConsensusReactor) OnStart() error {
	reactorState, err := LoadReactorState(f.db)
	if err != nil {
		return err
	}
	f.state = reactorState
	return nil
}

// GetChannels returns the list of channel descriptors.
func (f *FnConsensusReactor) GetChannels() []*conn.ChannelDescriptor {
	// Priorities are deliberately set to low, to prevent interfering with core TM
	return []*conn.ChannelDescriptor{
		{
			ID:                  FnVoteSetChannelID,
			Priority:            25,
			SendQueueCapacity:   100,
			RecvBufferCapacity:  100,
			RecvMessageCapacity: 10,
		},
	}
}

// AddPeer is called by the switch when a new peer is added.
func (f *FnConsensusReactor) AddPeer(peer p2p.Peer) {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	f.connectedPeers[peer.ID()] = peer
}

// RemovePeer is called by the switch when the peer is stopped (due to error
// or other reason).
func (f *FnConsensusReactor) RemovePeer(peer p2p.Peer, reason interface{}) {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	delete(f.connectedPeers, peer.ID())
}

func (f *FnConsensusReactor) areWeValidator(currentValidatorSet *types.ValidatorSet) bool {
	validatorIndex, _ := currentValidatorSet.GetByAddress(f.nodePrivKey.PubKey().Address())
	return validatorIndex != -1
}

// Receive is called when msgBytes is received from peer.
//
// NOTE reactor can not keep msgBytes around after Receive completes without
// copying.
//
// CONTRACT: msgBytes are not nil.
func (f *FnConsensusReactor) Receive(chID byte, sender p2p.Peer, msgBytes []byte) {
	currentState := state.LoadState(f.tmStateDB)
	var err error

	switch chID {
	case FnVoteSetChannelID:
		remoteVoteSet := &FnVoteSet{}
		if err := remoteVoteSet.Unmarshal(msgBytes); err != nil {
			f.Logger.Error("FnConsensusReactor: Invalid Data passed, ignoring...")
			return
		}

		if !remoteVoteSet.IsValid(f.chainID, currentState.Validators, f.fnRegistry) {
			f.Logger.Error("FnConsensusReactor: Invalid VoteSet specified, ignoring...")
			return
		}

		if remoteVoteSet.IsMaj23(currentState.Validators) {
			f.Logger.Error("FnConsensusReactor: Protocol violation: Received VoteSet with majority of validators signed, Ignoring...")
			return
		}

		hasVoteChanged := false
		fnID := remoteVoteSet.GetFnID()

		// TODO: Check nonce with mainnet before accepting remote vote set

		if f.state.CurrentVoteSets[fnID] == nil {
			f.state.CurrentVoteSets[fnID] = remoteVoteSet
			hasVoteChanged = false
		} else {
			if hasVoteChanged, err = f.state.CurrentVoteSets[fnID].Merge(remoteVoteSet); err != nil {
				f.Logger.Error("FnConsensusReactor: Unable to merge remote vote set into our own.", "error:", err)
				return
			}
		}

		if f.areWeValidator(currentState.Validators) {

			validatorIndex, _ := currentState.Validators.GetByAddress(f.nodePrivKey.PubKey().Address())

			fn := f.fnRegistry.Get(fnID)

			hash, signature, err := fn.GetMessageAndSignature()
			if err != nil {
				f.Logger.Error("FnConsensusReactor: fn.GetMessageAndSignature returned an error, ignoring..")
				return
			}

			err = f.state.CurrentVoteSets[fnID].AddVote(&FnIndividualExecutionResponse{
				Status:          0,
				Error:           "",
				Hash:            hash,
				OracleSignature: signature,
			}, validatorIndex, f.nodePrivKey)
			if err != nil {
				f.Logger.Error("FnConsensusError: unable to add vote to current voteset, ignoring...")
				return
			}

			hasVoteChanged = true

			if f.state.CurrentVoteSets[fnID].IsMaj23(currentState.Validators) {
				fn.SubmitMultiSignedMessage(f.state.CurrentVoteSets[fnID].Payload.Response.Hash, f.state.CurrentVoteSets[fnID].Payload.Response.OracleSignatures)
				return
			}
		}

		marshalledBytes, err := f.state.CurrentVoteSets[fnID].Marshal()
		if err != nil {
			f.Logger.Error(fmt.Sprintf("FnConsensusReactor: Unable to marshal currentVoteSet at FnID: %s", fnID))
			return
		}

		f.mtx.RLock()
		for peerID, peer := range f.connectedPeers {
			if !hasVoteChanged {
				if peerID == sender.ID() {
					continue
				}
			}

			go func() {
				// TODO: Handle timeout
				peer.Send(FnVoteSetChannelID, marshalledBytes)
			}()
		}
		f.mtx.RUnlock()

		break
	default:
		f.Logger.Error("FnConsensusReactor: Unknown channel: %v", chID)
	}
}
