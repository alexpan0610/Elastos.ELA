package manager

import (
	"time"

	"github.com/elastos/Elastos.ELA/blockchain"
	"github.com/elastos/Elastos.ELA/common"
	"github.com/elastos/Elastos.ELA/common/config"
	"github.com/elastos/Elastos.ELA/core/types"
	"github.com/elastos/Elastos.ELA/core/types/payload"
	"github.com/elastos/Elastos.ELA/dpos/log"
	"github.com/elastos/Elastos.ELA/dpos/p2p/msg"
	"github.com/elastos/Elastos.ELA/dpos/p2p/peer"
	"github.com/elastos/Elastos.ELA/dpos/state"
)

type DPOSEventConditionHandler interface {
	TryStartNewConsensus(b *types.Block) bool

	ChangeView(firstBlockHash *common.Uint256)

	ProcessProposal(id peer.PID, p *payload.DPOSProposal) (handled bool)
	ProcessAcceptVote(id peer.PID, p *payload.DPOSProposalVote) (succeed bool, finished bool)
	ProcessRejectVote(id peer.PID, p *payload.DPOSProposalVote) (succeed bool, finished bool)
}

type DPOSHandlerConfig struct {
	Network     DPOSNetwork
	Manager     *DPOSManager
	Monitor     *log.EventMonitor
	Arbitrators state.Arbitrators
}

type DPOSHandlerSwitch struct {
	proposalDispatcher *ProposalDispatcher
	consensus          *Consensus
	cfg                DPOSHandlerConfig

	onDutyHandler  *DPOSOnDutyHandler
	normalHandler  *DPOSNormalHandler
	currentHandler DPOSEventConditionHandler

	isAbnormal bool
}

func NewHandler(cfg DPOSHandlerConfig) *DPOSHandlerSwitch {

	h := &DPOSHandlerSwitch{
		cfg:        cfg,
		isAbnormal: false,
	}

	h.normalHandler = &DPOSNormalHandler{h}
	h.onDutyHandler = &DPOSOnDutyHandler{h}

	return h
}

func (h *DPOSHandlerSwitch) IsAbnormal() bool {
	return h.isAbnormal
}

func (h *DPOSHandlerSwitch) Initialize(dispatcher *ProposalDispatcher,
	consensus *Consensus) {
	h.proposalDispatcher = dispatcher
	h.consensus = consensus
	currentArbiter := h.cfg.Manager.GetArbitrators().GetNextOnDutyArbitrator(h.
		consensus.GetViewOffset())
	isDposOnDuty := common.BytesToHexString(currentArbiter) == config.
		Parameters.ArbiterConfiguration.PublicKey
	h.SwitchTo(isDposOnDuty)
}

func (h *DPOSHandlerSwitch) AddListeners(listeners ...log.EventListener) {
	for _, l := range listeners {
		h.cfg.Monitor.RegisterListener(l)
	}
}

func (h *DPOSHandlerSwitch) SwitchTo(onDuty bool) {
	if onDuty {
		h.currentHandler = h.onDutyHandler
	} else {
		h.currentHandler = h.normalHandler
	}
	h.consensus.SetOnDuty(onDuty)
}

func (h *DPOSHandlerSwitch) FinishConsensus() {
	h.proposalDispatcher.FinishConsensus()
}

func (h *DPOSHandlerSwitch) ProcessProposal(id peer.PID, p *payload.DPOSProposal) (handled bool) {
	handled = h.currentHandler.ProcessProposal(id, p)

	proposalEvent := log.ProposalEvent{
		Sponsor:      common.BytesToHexString(p.Sponsor),
		BlockHash:    p.BlockHash,
		ReceivedTime: time.Now(),
		ProposalHash: p.Hash(),
		RawData:      p,
		Result:       false,
	}
	h.cfg.Monitor.OnProposalArrived(&proposalEvent)

	return handled
}

func (h *DPOSHandlerSwitch) ChangeView(firstBlockHash *common.Uint256) {
	h.currentHandler.ChangeView(firstBlockHash)

	viewEvent := log.ViewEvent{
		OnDutyArbitrator: common.BytesToHexString(h.consensus.GetOnDutyArbitrator()),
		StartTime:        time.Now(),
		Offset:           h.consensus.GetViewOffset(),
		Height:           h.proposalDispatcher.CurrentHeight(),
	}
	h.cfg.Monitor.OnViewStarted(&viewEvent)
}

func (h *DPOSHandlerSwitch) TryStartNewConsensus(b *types.Block) bool {
	if _, ok := h.cfg.Manager.GetBlockCache().TryGetValue(b.Hash()); ok {
		log.Info("[TryStartNewConsensus] failed, already have the block")
		return false
	}

	if h.proposalDispatcher.IsProcessingBlockEmpty() {
		if h.currentHandler.TryStartNewConsensus(b) {
			c := log.ConsensusEvent{StartTime: time.Now(), Height: b.Height,
				RawData: &b.Header}
			h.cfg.Monitor.OnConsensusStarted(&c)
			return true
		}
	}

	//todo record block into database
	return false
}

func (h *DPOSHandlerSwitch) ProcessAcceptVote(id peer.PID, p *payload.DPOSProposalVote) (bool, bool) {
	succeed, finished := h.currentHandler.ProcessAcceptVote(id, p)

	voteEvent := log.VoteEvent{Signer: common.BytesToHexString(p.Signer),
		ReceivedTime: time.Now(), Result: true, RawData: p}
	h.cfg.Monitor.OnVoteArrived(&voteEvent)

	return succeed, finished
}

func (h *DPOSHandlerSwitch) ProcessRejectVote(id peer.PID, p *payload.DPOSProposalVote) (bool, bool) {
	succeed, finished := h.currentHandler.ProcessRejectVote(id, p)

	voteEvent := log.VoteEvent{Signer: common.BytesToHexString(p.Signer),
		ReceivedTime: time.Now(), Result: false, RawData: p}
	h.cfg.Monitor.OnVoteArrived(&voteEvent)

	return succeed, finished
}

func (h *DPOSHandlerSwitch) ResponseGetBlocks(id peer.PID, startBlockHeight, endBlockHeight uint32) {
	//todo limit max height range (endBlockHeight - startBlockHeight)
	currentHeight := h.proposalDispatcher.CurrentHeight()

	endHeight := endBlockHeight
	if currentHeight < endBlockHeight {
		endHeight = currentHeight
	}
	blockConfirms, err := blockchain.DefaultLedger.GetDposBlocks(startBlockHeight, endHeight)
	if err != nil {
		log.Error(err)
		return
	}

	if currentBlock := h.proposalDispatcher.GetProcessingBlock(); currentBlock != nil {
		blockConfirms = append(blockConfirms, &types.DposBlock{
			Block: currentBlock,
		})
	}

	msg := &msg.ResponseBlocks{Command: msg.CmdResponseBlocks, BlockConfirms: blockConfirms}
	h.cfg.Network.SendMessageToPeer(id, msg)
}

func (h *DPOSHandlerSwitch) RequestAbnormalRecovering() {
	h.proposalDispatcher.RequestAbnormalRecovering()
	h.isAbnormal = true
}

func (h *DPOSHandlerSwitch) HelpToRecoverAbnormal(id peer.PID, height uint32) {
	status := &msg.ConsensusStatus{}
	log.Info("[HelpToRecoverAbnormal] peer id:", common.BytesToHexString(id[:]))

	if err := h.consensus.CollectConsensusStatus(height, status); err != nil {
		log.Error("Error occurred when collect consensus status from consensus object: ", err)
		return
	}

	if err := h.proposalDispatcher.CollectConsensusStatus(height, status); err != nil {
		log.Error("Error occurred when collect consensus status from proposal dispatcher object: ", err)
		return
	}

	msg := &msg.ResponseConsensus{Consensus: *status}
	h.cfg.Network.SendMessageToPeer(id, msg)
}

func (h *DPOSHandlerSwitch) RecoverAbnormal(status *msg.ConsensusStatus) {
	if !h.isAbnormal {
		return
	}

	if err := h.proposalDispatcher.RecoverFromConsensusStatus(status); err != nil {
		log.Error("Error occurred when recover proposal dispatcher object: ", err)
		return
	}

	if err := h.consensus.RecoverFromConsensusStatus(status); err != nil {
		log.Error("Error occurred when recover consensus object: ", err)
		return
	}

	h.isAbnormal = false
}

func (h *DPOSHandlerSwitch) OnViewChanged(isOnDuty bool) {
	h.SwitchTo(isOnDuty)

	firstBlockHash, ok := h.cfg.Manager.GetBlockCache().GetFirstArrivedBlockHash()
	if isOnDuty && !ok {
		log.Warn("[OnViewChanged] firstBlockHash is nil")
		return
	}
	log.Info("OnViewChanged, onduty, getBlock from first block hash:", firstBlockHash)
	h.ChangeView(&firstBlockHash)
}
