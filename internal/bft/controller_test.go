// Copyright IBM Corp. All Rights Reserved.
//
// SPDX-License-Identifier: Apache-2.0
//

package bft_test

import (
	"io/ioutil"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SmartBFT-Go/consensus/internal/bft"
	"github.com/SmartBFT-Go/consensus/internal/bft/mocks"
	"github.com/SmartBFT-Go/consensus/pkg/types"
	"github.com/SmartBFT-Go/consensus/pkg/wal"
	protos "github.com/SmartBFT-Go/consensus/smartbftprotos"
	"github.com/golang/protobuf/proto"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.uber.org/zap"
)

func TestControllerBasic(t *testing.T) {
	basicLog, err := zap.NewDevelopment()
	assert.NoError(t, err)
	log := basicLog.Sugar()
	app := &mocks.ApplicationMock{}
	app.On("Deliver", mock.Anything, mock.Anything)
	batcher := &mocks.Batcher{}
	batcher.On("Close")
	pool := &mocks.RequestPool{}
	pool.On("Close")
	leaderMon := &mocks.LeaderMonitor{}
	leaderMon.On("ChangeRole", mock.Anything, mock.Anything, mock.Anything)
	leaderMon.On("ProcessMsg", uint64(1), heartbeat)
	leaderMon.On("Close")

	comm := &mocks.CommMock{}
	comm.On("Nodes").Return([]uint64{0, 1, 2, 3})

	controller := &bft.Controller{
		Batcher:       batcher,
		RequestPool:   pool,
		LeaderMonitor: leaderMon,
		ID:            4, // not the leader
		N:             4,
		Logger:        log,
		Application:   app,
		Comm:          comm,
	}
	configureProposerBuilder(controller)

	controller.Start(1, 0)
	controller.ProcessMessages(1, heartbeat)
	controller.ViewChanged(2, 1)
	controller.ViewChanged(3, 2)
	controller.AbortView(3)
	controller.AbortView(3)
	controller.Stop()
	controller.Stop()
}

func TestControllerLeaderBasic(t *testing.T) {
	basicLog, err := zap.NewDevelopment()
	assert.NoError(t, err)
	log := basicLog.Sugar()
	batcher := &mocks.Batcher{}
	batcher.On("Close")
	batcher.On("Closed").Return(false)
	batcherChan := make(chan struct{})
	var once sync.Once
	batcher.On("NextBatch").Run(func(args mock.Arguments) {
		once.Do(func() {
			batcherChan <- struct{}{}
		})
	}).Return([][]byte{})
	pool := &mocks.RequestPool{}
	pool.On("Close")
	leaderMon := &mocks.LeaderMonitor{}
	leaderMon.On("ChangeRole", bft.Leader, mock.Anything, mock.Anything)
	leaderMon.On("Close")
	commMock := &mocks.CommMock{}
	commMock.On("Nodes").Return([]uint64{0, 1, 2, 3})

	controller := &bft.Controller{
		RequestPool:   pool,
		LeaderMonitor: leaderMon,
		ID:            1, // the leader
		N:             4,
		Logger:        log,
		Batcher:       batcher,
		Comm:          commMock,
	}
	configureProposerBuilder(controller)

	controller.Start(1, 0)
	<-batcherChan
	controller.Stop()
	batcher.AssertCalled(t, "NextBatch")
}

func TestLeaderPropose(t *testing.T) {
	basicLog, err := zap.NewDevelopment()
	assert.NoError(t, err)
	log := basicLog.Sugar()
	req := []byte{1}
	batcher := &mocks.Batcher{}
	batcher.On("Close")
	batcher.On("Closed").Return(false)
	batcher.On("NextBatch").Return([][]byte{req}).Once()
	batcher.On("NextBatch").Return([][]byte{req}).Once()
	batcher.On("PopRemainder").Return([][]byte{})
	batcher.On("BatchRemainder", mock.Anything)
	verifier := &mocks.VerifierMock{}
	verifier.On("VerifySignature", mock.Anything).Return(nil)
	verifier.On("VerifyRequest", req).Return(types.RequestInfo{}, nil)
	verifier.On("VerificationSequence").Return(uint64(1))
	verifier.On("VerifyProposal", mock.Anything, mock.Anything).Return(nil, nil)
	verifier.On("VerifyConsenterSig", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	assembler := &mocks.AssemblerMock{}
	assembler.On("AssembleProposal", mock.Anything, [][]byte{req}).Return(proposal, [][]byte{}).Once()
	secondProposal := proposal
	secondProposal.Metadata = bft.MarshalOrPanic(&protos.ViewMetadata{
		LatestSequence: 1,
		ViewId:         1,
	})
	assembler.On("AssembleProposal", mock.Anything, [][]byte{req}).Return(secondProposal, [][]byte{}).Once()
	comm := &mocks.CommMock{}
	commWG := sync.WaitGroup{}
	comm.On("Nodes").Return([]uint64{11, 17, 23, 37})
	comm.On("SendConsensus", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		commWG.Done()
	})
	signer := &mocks.SignerMock{}
	signer.On("Sign", mock.Anything).Return(nil)
	signer.On("SignProposal", mock.Anything).Return(&types.Signature{
		Id:    17,
		Value: []byte{4},
	})
	app := &mocks.ApplicationMock{}
	appWG := sync.WaitGroup{}
	app.On("Deliver", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		appWG.Done()
	})
	reqPool := &mocks.RequestPool{}
	reqPool.On("Prune", mock.Anything)
	reqPool.On("Close")
	leaderMon := &mocks.LeaderMonitor{}
	leaderMon.On("ChangeRole", bft.Leader, mock.Anything, mock.Anything)
	leaderMon.On("HeartbeatWasSent")
	leaderMon.On("Close")

	testDir, err := ioutil.TempDir("", "controller-unittest")
	assert.NoErrorf(t, err, "generate temporary test dir")
	defer os.RemoveAll(testDir)
	wal, err := wal.Create(log, testDir, nil)
	assert.NoError(t, err)

	n := 4
	controller := &bft.Controller{
		RequestPool:   reqPool,
		LeaderMonitor: leaderMon,
		WAL:           wal,
		ID:            17, // the leader
		N:             uint64(n),
		Logger:        log,
		Batcher:       batcher,
		Verifier:      verifier,
		Assembler:     assembler,
		Comm:          comm,
		Signer:        signer,
		Application:   app,
		Checkpoint:    &types.Checkpoint{},
		ViewChanger:   &bft.ViewChanger{},
	}
	configureProposerBuilder(controller)

	commWG.Add(2 * (n - 1))
	controller.Start(1, 0)
	commWG.Wait() // propose

	commWG.Add(n - 1)
	controller.ProcessMessages(2, prepare)
	controller.ProcessMessages(3, prepare)
	commWG.Wait()

	controller.ProcessMessages(2, commit2)
	commit3 := proto.Clone(commit2).(*protos.Message)
	commit3Get := commit3.GetCommit()
	commit3Get.Signature.Signer = 23
	appWG.Add(1)            // deliver
	commWG.Add(2 * (n - 1)) // next proposal
	controller.ProcessMessages(23, commit3)
	appWG.Wait()
	commWG.Wait()

	controller.Stop()
	app.AssertNumberOfCalls(t, "Deliver", 1)
	leaderMon.AssertNumberOfCalls(t, "HeartbeatWasSent", 5)

	// Ensure checkpoint was updated
	expected := protos.Proposal{
		Header:               proposal.Header,
		Payload:              proposal.Payload,
		Metadata:             proposal.Metadata,
		VerificationSequence: uint64(proposal.VerificationSequence),
	}
	proposal, signatures := controller.Checkpoint.Get()
	assert.Equal(t, expected, proposal)
	signaturesBySigners := make(map[uint64]protos.Signature)
	for _, sig := range signatures {
		signaturesBySigners[sig.Signer] = *sig
	}
	assert.Equal(t, map[uint64]protos.Signature{
		2:  {Signer: 2, Value: []byte{4}},
		17: {Signer: 17, Value: []byte{4}},
		23: {Signer: 23, Value: []byte{4}},
	}, signaturesBySigners)

	wal.Close()
}

func TestViewChanged(t *testing.T) {
	basicLog, err := zap.NewDevelopment()
	assert.NoError(t, err)
	log := basicLog.Sugar()
	req := []byte{1}
	batcher := &mocks.Batcher{}
	batcher.On("Close")
	batcher.On("Closed").Return(false)
	batcher.On("Reset")
	batcher.On("NextBatch").Return([][]byte{req})
	verifier := &mocks.VerifierMock{}
	verifier.On("VerifySignature", mock.Anything).Return(nil)
	verifier.On("VerificationSequence").Return(uint64(1))
	verifier.On("VerifyProposal", mock.Anything, mock.Anything).Return(nil, nil)

	secondProposal := proposal
	secondProposal.Metadata = bft.MarshalOrPanic(&protos.ViewMetadata{
		LatestSequence: 0,
		ViewId:         2,
	})

	assembler := &mocks.AssemblerMock{}
	assembler.On("AssembleProposal", mock.Anything, [][]byte{req}).Return(secondProposal, [][]byte{}).Once()
	comm := &mocks.CommMock{}
	commWG := sync.WaitGroup{}
	comm.On("BroadcastConsensus", mock.Anything)
	comm.On("SendConsensus", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		commWG.Done()
	})
	comm.On("Nodes").Return([]uint64{0, 1, 2, 3})
	reqPool := &mocks.RequestPool{}
	reqPool.On("Close")
	leaderMon := &mocks.LeaderMonitor{}
	leaderMon.On("ChangeRole", bft.Follower, mock.Anything, mock.Anything)
	leaderMon.On("ChangeRole", bft.Leader, mock.Anything, mock.Anything)
	leaderMon.On("Close")
	leaderMon.On("HeartbeatWasSent")

	signer := &mocks.SignerMock{}
	signer.On("Sign", mock.Anything).Return(nil)

	testDir, err := ioutil.TempDir("", "controller-unittest")
	assert.NoErrorf(t, err, "generate temporary test dir")
	defer os.RemoveAll(testDir)
	wal, err := wal.Create(log, testDir, nil)
	assert.NoError(t, err)

	n := 4

	controller := &bft.Controller{
		Signer:        signer,
		WAL:           wal,
		ID:            2, // the next leader
		N:             uint64(n),
		Logger:        log,
		Batcher:       batcher,
		Verifier:      verifier,
		Assembler:     assembler,
		Comm:          comm,
		RequestPool:   reqPool,
		LeaderMonitor: leaderMon,
	}
	configureProposerBuilder(controller)

	controller.Start(1, 0)

	commWG.Add(2 * (n - 1))
	controller.ViewChanged(2, 0)
	commWG.Wait()
	batcher.AssertNumberOfCalls(t, "NextBatch", 1)
	assembler.AssertNumberOfCalls(t, "AssembleProposal", 1)
	comm.AssertNumberOfCalls(t, "SendConsensus", 2*(n-1))
	leaderMon.AssertNumberOfCalls(t, "HeartbeatWasSent", 2)
	controller.Stop()
	wal.Close()
}

func TestControllerLeaderRequestHandling(t *testing.T) {
	for _, testCase := range []struct {
		description      string
		startViewNum     uint64
		verifyReqReturns error
		shouldEnqueue    bool
		shouldVerify     bool
		waitForLoggedMsg string
	}{
		{
			description:      "not the leader",
			startViewNum:     2,
			waitForLoggedMsg: "Got request from 3 but the leader is 2, dropping request",
		},
		{
			description:      "bad request",
			startViewNum:     1,
			verifyReqReturns: errors.New("unauthorized user"),
			waitForLoggedMsg: "unauthorized user",
			shouldVerify:     true,
		},
		{
			description:      "good request",
			shouldEnqueue:    true,
			startViewNum:     1,
			waitForLoggedMsg: "Got request from 3",
			shouldVerify:     true,
		},
	} {
		t.Run(testCase.description, func(t *testing.T) {
			var submittedToPool sync.WaitGroup

			basicLog, err := zap.NewDevelopment()
			assert.NoError(t, err)

			log := basicLog.Sugar()

			batcher := &mocks.Batcher{}
			batcher.On("Close")
			batcher.On("Reset")
			batcher.On("NextBatch").Run(func(arguments mock.Arguments) {
				time.Sleep(time.Hour)
			})

			pool := &mocks.RequestPool{}
			pool.On("Close")
			leaderMon := &mocks.LeaderMonitor{}
			leaderMon.On("ChangeRole", bft.Follower, mock.Anything, mock.Anything)
			leaderMon.On("ChangeRole", bft.Leader, mock.Anything, mock.Anything)
			leaderMon.On("Close")
			if testCase.shouldEnqueue {
				submittedToPool.Add(1)
				pool.On("Submit", mock.Anything).Return(nil).Run(func(_ mock.Arguments) {
					submittedToPool.Done()
				})
			}

			commMock := &mocks.CommMock{}
			commMock.On("Nodes").Return([]uint64{0, 1, 2, 3})

			verifier := &mocks.VerifierMock{}
			verifier.On("VerifyRequest", mock.Anything).Return(types.RequestInfo{}, testCase.verifyReqReturns)

			controller := &bft.Controller{
				RequestPool:   pool,
				LeaderMonitor: leaderMon,
				ID:            1,
				N:             4,
				Logger:        log,
				Batcher:       batcher,
				Comm:          commMock,
				Verifier:      verifier,
			}

			configureProposerBuilder(controller)
			controller.Start(testCase.startViewNum, 0)

			controller.HandleRequest(3, []byte{1, 2, 3})

			submittedToPool.Wait()

			if !testCase.shouldVerify {
				verifier.AssertNotCalled(t, "VerifyRequest", mock.Anything)
			}
		})
	}
}

func createView(c *bft.Controller, leader, proposalSequence, viewNum uint64, quorumSize int) *bft.View {
	return &bft.View{
		N:                c.N,
		LeaderID:         leader,
		SelfID:           c.ID,
		Quorum:           quorumSize,
		Number:           viewNum,
		Decider:          c,
		FailureDetector:  c.FailureDetector,
		Sync:             c,
		Logger:           c.Logger,
		Comm:             c,
		Verifier:         c.Verifier,
		Signer:           c.Signer,
		ProposalSequence: proposalSequence,
		ViewSequences:    &atomic.Value{},
		State:            &bft.PersistedState{WAL: c.WAL, InFlightProposal: &bft.InFlightData{}},
		InMsgQSize:       int(c.N * 10),
	}
}

func configureProposerBuilder(controller *bft.Controller) {
	pb := &mocks.ProposerBuilder{}
	pb.On("NewProposer", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(func(a uint64, b uint64, c uint64, d int) bft.Proposer {
			return createView(controller, a, b, c, d)
		})
	controller.ProposerBuilder = pb
}

func TestSyncInform(t *testing.T) {
	basicLog, err := zap.NewDevelopment()
	assert.NoError(t, err)
	log := basicLog.Sugar()
	req := []byte{1}
	batcher := &mocks.Batcher{}
	batcher.On("Close")
	batcher.On("Closed").Return(false)
	batcher.On("Reset")
	batcher.On("NextBatch").Return([][]byte{req})
	verifier := &mocks.VerifierMock{}
	verifier.On("VerifySignature", mock.Anything).Return(nil)
	verifier.On("VerificationSequence").Return(uint64(1))
	verifier.On("VerifyProposal", mock.Anything, mock.Anything).Return(nil, nil)

	secondProposal := proposal
	secondProposal.Metadata = bft.MarshalOrPanic(&protos.ViewMetadata{
		LatestSequence: 2,
		ViewId:         2,
	})

	assembler := &mocks.AssemblerMock{}
	assembler.On("AssembleProposal", mock.Anything, [][]byte{req}).Return(secondProposal, [][]byte{}).Once()

	comm := &mocks.CommMock{}
	commWG := sync.WaitGroup{}
	comm.On("BroadcastConsensus", mock.Anything)
	comm.On("SendConsensus", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		commWG.Done()
	})
	comm.On("Nodes").Return([]uint64{0, 1, 2, 3})

	commWithChan := &mocks.CommMock{}
	msgChan := make(chan *protos.Message)
	commWithChan.On("BroadcastConsensus", mock.Anything).Run(func(args mock.Arguments) {
		msgChan <- args.Get(0).(*protos.Message)
	})
	commWithChan.On("Nodes").Return([]uint64{0, 1, 2, 3})

	reqPool := &mocks.RequestPool{}
	reqPool.On("Close")
	leaderMon := &mocks.LeaderMonitor{}
	leaderMon.On("ChangeRole", bft.Follower, mock.Anything, mock.Anything)
	leaderMon.On("ChangeRole", bft.Leader, mock.Anything, mock.Anything)
	leaderMon.On("Close")
	leaderMon.On("HeartbeatWasSent")

	signer := &mocks.SignerMock{}
	signer.On("Sign", mock.Anything).Return(nil)

	testDir, err := ioutil.TempDir("", "controller-unittest")
	assert.NoErrorf(t, err, "generate temporary test dir")
	defer os.RemoveAll(testDir)
	wal, err := wal.Create(log, testDir, nil)
	assert.NoError(t, err)

	synchronizer := &mocks.SynchronizerMock{}
	synchronizerWG := sync.WaitGroup{}
	syncToView := uint64(2)
	synchronizer.On("Sync").Run(func(args mock.Arguments) {
		synchronizerWG.Done()
	}).Return(types.Decision{
		Proposal: types.Proposal{
			Metadata: bft.MarshalOrPanic(&protos.ViewMetadata{
				LatestSequence: 1,
				ViewId:         syncToView,
			}),
			VerificationSequence: 1},
		Signatures: nil,
	})

	reqTimer := &mocks.RequestsTimer{}
	reqTimer.On("StopTimers")
	reqTimer.On("RestartTimers")
	controllerMock := &mocks.ViewController{}
	controllerMock.On("AbortView", mock.Anything)

	n := 4

	collector := bft.StateCollector{
		SelfID:         0,
		N:              uint64(n),
		Logger:         log,
		CollectTimeout: 100 * time.Millisecond,
	}
	collector.Start()

	vc := &bft.ViewChanger{
		SelfID:        2,
		N:             uint64(n),
		Logger:        log,
		Comm:          commWithChan,
		RequestsTimer: reqTimer,
		Ticker:        make(chan time.Time),
		Controller:    controllerMock,
		InMsqQSize:    100,
	}

	controller := &bft.Controller{
		Signer:        signer,
		WAL:           wal,
		ID:            2,
		N:             uint64(n),
		Logger:        log,
		Batcher:       batcher,
		Verifier:      verifier,
		Assembler:     assembler,
		Comm:          comm,
		RequestPool:   reqPool,
		LeaderMonitor: leaderMon,
		Synchronizer:  synchronizer,
		ViewChanger:   vc,
		Checkpoint:    &types.Checkpoint{},
		Collector:     &collector,
	}
	configureProposerBuilder(controller)

	controller.Start(1, 0)
	vc.Start(1)

	vc.StartViewChange(1, true)
	msg := <-msgChan
	assert.NotNil(t, msg.GetViewChange())
	assert.Equal(t, uint64(2), msg.GetViewChange().NextView) // view number as expected

	commWG.Add(3 * (n - 1))
	synchronizerWG.Add(1)
	controller.Sync()
	commWG.Wait()
	synchronizerWG.Wait()
	batcher.AssertNumberOfCalls(t, "NextBatch", 1)
	assembler.AssertNumberOfCalls(t, "AssembleProposal", 1)
	comm.AssertNumberOfCalls(t, "SendConsensus", 3*(n-1))

	// There are three broadcasts, the first one is a part of the sync process
	// Afterwards the view changes and this node becomes a leader
	// Then there are two broadcasts, one for preprepare and the other for prepare
	// Where this node is the leader
	leaderMon.AssertNumberOfCalls(t, "HeartbeatWasSent", 2)

	vc.StartViewChange(2, true)
	msg = <-msgChan
	assert.NotNil(t, msg.GetViewChange())
	assert.Equal(t, syncToView+1, msg.GetViewChange().NextView) // view number did change according to info

	controller.Stop()
	vc.Stop()
	collector.Stop()
	wal.Close()
}
