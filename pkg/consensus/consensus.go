// Copyright IBM Corp. All Rights Reserved.
//
// SPDX-License-Identifier: Apache-2.0
//

package consensus

import (
	"time"

	algorithm "github.com/SmartBFT-Go/consensus/internal/bft"
	bft "github.com/SmartBFT-Go/consensus/pkg/api"
	"github.com/SmartBFT-Go/consensus/pkg/types"
	protos "github.com/SmartBFT-Go/consensus/smartbftprotos"
)

const (
	DefaultRequestPoolSize = 200
)

// Consensus submits requests to be total ordered,
// and delivers to the application proposals by invoking Deliver() on it.
// The proposals contain batches of requests assembled together by the Assembler.
type Consensus struct {
	SelfID       uint64
	BatchSize    int
	BatchTimeout time.Duration
	bft.Comm
	Application             bft.Application
	Assembler               bft.Assembler
	WAL                     bft.WriteAheadLog
	WALInitialContent       [][]byte
	Signer                  bft.Signer
	Verifier                bft.Verifier
	RequestInspector        bft.RequestInspector
	Synchronizer            bft.Synchronizer
	Logger                  bft.Logger
	Metadata                protos.ViewMetadata
	LastProposal            types.Proposal
	LastSignatures          []types.Signature
	Scheduler               <-chan time.Time
	ViewChangerTicker       <-chan time.Time
	ViewChangeResendTimeout time.Duration
	ViewChangerTimeout      time.Duration

	viewChanger *algorithm.ViewChanger
	controller  *algorithm.Controller
	state       *algorithm.PersistedState
	n           uint64
}

func (c *Consensus) Complain(stopView bool) {
	c.viewChanger.StartViewChange(stopView)
}

func (c *Consensus) Deliver(proposal types.Proposal, signatures []types.Signature) {
	c.Application.Deliver(proposal, signatures)
}

func (c *Consensus) Start() {
	// requestTimeout := 2 * c.BatchTimeout // Request timeout should be at least as batch timeout
	opts := algorithm.PoolOptions{
		QueueSize:         DefaultRequestPoolSize,
		RequestTimeout:    algorithm.DefaultRequestTimeout / 100,
		LeaderFwdTimeout:  algorithm.DefaultRequestTimeout / 5,
		AutoRemoveTimeout: algorithm.DefaultRequestTimeout,
	}

	c.n = uint64(len(c.Nodes()))

	inFlight := algorithm.InFlightData{}

	c.state = &algorithm.PersistedState{
		InFlightProposal: &inFlight,
		Entries:          c.WALInitialContent,
		Logger:           c.Logger,
		WAL:              c.WAL,
	}

	cpt := types.Checkpoint{}
	cpt.Set(c.LastProposal, c.LastSignatures)

	c.viewChanger = &algorithm.ViewChanger{
		SelfID:      c.SelfID,
		N:           c.n,
		Logger:      c.Logger,
		Comm:        c,
		Signer:      c.Signer,
		Verifier:    c.Verifier,
		Application: c,
		Checkpoint:  &cpt,
		InFlight:    &inFlight,
		// Controller later
		// RequestsTimer later
		Ticker:            c.ViewChangerTicker,
		ResendTimeout:     c.ViewChangeResendTimeout,
		TimeoutViewChange: c.ViewChangerTimeout,
	}

	c.controller = &algorithm.Controller{
		Checkpoint:       &cpt,
		WAL:              c.WAL,
		ID:               c.SelfID,
		N:                c.n,
		Verifier:         c.Verifier,
		Logger:           c.Logger,
		Assembler:        c.Assembler,
		Application:      c,
		FailureDetector:  c,
		Synchronizer:     c.Synchronizer,
		Comm:             c,
		Signer:           c.Signer,
		RequestInspector: c.RequestInspector,
		ViewChanger:      c.viewChanger,
	}

	c.viewChanger.Synchronizer = c.controller

	c.controller.ProposerBuilder = c.proposalMaker()

	pool := algorithm.NewPool(c.Logger, c.RequestInspector, c.controller, opts)
	batchBuilder := algorithm.NewBatchBuilder(pool, c.BatchSize, c.BatchTimeout)
	leaderMonitor := algorithm.NewHeartbeatMonitor(c.Scheduler, c.Logger, algorithm.DefaultHeartbeatTimeout, c, c.controller)
	c.controller.RequestPool = pool
	c.controller.Batcher = batchBuilder
	c.controller.LeaderMonitor = leaderMonitor

	c.viewChanger.Controller = c.controller
	c.viewChanger.RequestsTimer = pool

	// If we delivered to the application proposal with sequence i,
	// then we are expecting to be proposed a proposal with sequence i+1.
	c.viewChanger.Start(c.Metadata.ViewId)
	c.controller.Start(c.Metadata.ViewId, c.Metadata.LatestSequence+1)
}

func (c *Consensus) Stop() {
	c.viewChanger.Stop()
	c.controller.Stop()
}

func (c *Consensus) HandleMessage(sender uint64, m *protos.Message) {
	c.controller.ProcessMessages(sender, m)
}

func (c *Consensus) HandleRequest(sender uint64, req []byte) {
	c.controller.HandleRequest(sender, req)
}

func (c *Consensus) SubmitRequest(req []byte) error {
	c.Logger.Debugf("Submit Request: %s", c.RequestInspector.RequestID(req))
	return c.controller.SubmitRequest(req)
}

func (c *Consensus) BroadcastConsensus(m *protos.Message) {
	for _, node := range c.Comm.Nodes() {
		// Do not send to yourself
		if c.SelfID == node {
			continue
		}
		c.Comm.SendConsensus(node, m)
	}
}

func (c *Consensus) proposalMaker() *algorithm.ProposalMaker {
	return &algorithm.ProposalMaker{
		State:           c.state,
		Comm:            c,
		Decider:         c.controller,
		Logger:          c.Logger,
		Signer:          c.Signer,
		SelfID:          c.SelfID,
		Sync:            c.controller,
		FailureDetector: c,
		Verifier:        c.Verifier,
		N:               c.n,
	}
}
