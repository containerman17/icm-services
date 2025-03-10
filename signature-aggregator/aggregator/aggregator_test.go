package aggregator

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/message"
	"github.com/ava-labs/avalanchego/proto/pb/sdk"
	"github.com/ava-labs/avalanchego/snow/validators"
	"github.com/ava-labs/avalanchego/subnets"
	"github.com/ava-labs/avalanchego/utils"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/crypto/bls"
	"github.com/ava-labs/avalanchego/utils/logging"
	"github.com/ava-labs/avalanchego/utils/set"
	"github.com/ava-labs/avalanchego/vms/platformvm/warp"
	"github.com/ava-labs/icm-services/peers"
	"github.com/ava-labs/icm-services/peers/mocks"
	"github.com/ava-labs/icm-services/signature-aggregator/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/protobuf/proto"
)

var (
	sigAggMetrics  *metrics.SignatureAggregatorMetrics
	messageCreator message.Creator
)

func instantiateAggregator(t *testing.T) (
	*SignatureAggregator,
	*mocks.MockAppRequestNetwork,
) {
	mockNetwork := mocks.NewMockAppRequestNetwork(gomock.NewController(t))
	if sigAggMetrics == nil {
		sigAggMetrics = metrics.NewSignatureAggregatorMetrics(prometheus.DefaultRegisterer)
	}
	if messageCreator == nil {
		var err error
		messageCreator, err = message.NewCreator(
			logging.NoLog{},
			prometheus.DefaultRegisterer,
			constants.DefaultNetworkCompressionType,
			constants.DefaultNetworkMaximumInboundTimeout,
		)
		require.NoError(t, err)
	}
	aggregator, err := NewSignatureAggregator(
		mockNetwork,
		logging.NewLogger(
			"aggregator_test",
			logging.NewWrappedCore(
				logging.Debug,
				os.Stdout,
				zapcore.NewConsoleEncoder(
					zap.NewProductionEncoderConfig(),
				),
			),
		),
		messageCreator,
		1024,
		sigAggMetrics,
	)
	require.NoError(t, err)
	return aggregator, mockNetwork
}

// Generate the validator values.
type validatorInfo struct {
	nodeID            ids.NodeID
	blsSecretKey      *bls.SecretKey
	blsPublicKey      *bls.PublicKey
	blsPublicKeyBytes []byte
}

func (v validatorInfo) Compare(o validatorInfo) int {
	return bytes.Compare(v.blsPublicKeyBytes, o.blsPublicKeyBytes)
}

func makeConnectedValidators(validatorCount int) (*peers.ConnectedCanonicalValidators, []*bls.SecretKey) {
	validatorValues := make([]validatorInfo, validatorCount)
	for i := 0; i < validatorCount; i++ {
		secretKey, err := bls.NewSecretKey()
		if err != nil {
			panic(err)
		}
		pubKey := bls.PublicFromSecretKey(secretKey)
		nodeID := ids.GenerateTestNodeID()
		validatorValues[i] = validatorInfo{
			nodeID:            nodeID,
			blsSecretKey:      secretKey,
			blsPublicKey:      pubKey,
			blsPublicKeyBytes: bls.PublicKeyToUncompressedBytes(pubKey),
		}
	}

	// Sort the validators by public key to construct the NodeValidatorIndexMap
	utils.Sort(validatorValues)

	// Placeholder for results
	validatorSet := make([]*warp.Validator, validatorCount)
	validatorSecretKeys := make([]*bls.SecretKey, validatorCount)
	nodeValidatorIndexMap := make(map[ids.NodeID]int)
	for i, validator := range validatorValues {
		validatorSecretKeys[i] = validator.blsSecretKey
		validatorSet[i] = &warp.Validator{
			PublicKey:      validator.blsPublicKey,
			PublicKeyBytes: validator.blsPublicKeyBytes,
			Weight:         1,
			NodeIDs:        []ids.NodeID{validator.nodeID},
		}
		nodeValidatorIndexMap[validator.nodeID] = i
	}

	return &peers.ConnectedCanonicalValidators{
		ConnectedWeight:       uint64(validatorCount),
		TotalValidatorWeight:  uint64(validatorCount),
		ValidatorSet:          validatorSet,
		NodeValidatorIndexMap: nodeValidatorIndexMap,
	}, validatorSecretKeys
}

func TestCreateSignedMessageFailsWithNoValidators(t *testing.T) {
	aggregator, mockNetwork := instantiateAggregator(t)
	msg, err := warp.NewUnsignedMessage(0, ids.Empty, []byte{})
	require.NoError(t, err)
	mockNetwork.EXPECT().GetSubnetID(ids.Empty).Return(ids.Empty, nil)
	mockNetwork.EXPECT().TrackSubnet(ids.Empty)
	mockNetwork.EXPECT().GetConnectedCanonicalValidators(ids.Empty).Return(
		&peers.ConnectedCanonicalValidators{
			ConnectedWeight:      0,
			TotalValidatorWeight: 0,
			ValidatorSet:         []*warp.Validator{},
		},
		nil,
	)
	_, err = aggregator.CreateSignedMessage(msg, nil, ids.Empty, 80)
	require.ErrorContains(t, err, "no signatures")
}

func TestCreateSignedMessageFailsWithoutSufficientConnectedStake(t *testing.T) {
	aggregator, mockNetwork := instantiateAggregator(t)
	msg, err := warp.NewUnsignedMessage(0, ids.Empty, []byte{})
	require.NoError(t, err)
	mockNetwork.EXPECT().GetSubnetID(ids.Empty).Return(ids.Empty, nil)
	mockNetwork.EXPECT().TrackSubnet(ids.Empty)
	mockNetwork.EXPECT().GetConnectedCanonicalValidators(ids.Empty).Return(
		&peers.ConnectedCanonicalValidators{
			ConnectedWeight:      0,
			TotalValidatorWeight: 1,
			ValidatorSet:         []*warp.Validator{},
		},
		nil,
	).AnyTimes()
	_, err = aggregator.CreateSignedMessage(msg, nil, ids.Empty, 80)
	require.ErrorContains(
		t,
		err,
		"failed to connect to a threshold of stake",
	)
}

func makeAppRequests(
	chainID ids.ID,
	requestID uint32,
	connectedValidators *peers.ConnectedCanonicalValidators,
) []ids.RequestID {
	var appRequests []ids.RequestID
	for _, validator := range connectedValidators.ValidatorSet {
		for _, nodeID := range validator.NodeIDs {
			appRequests = append(
				appRequests,
				ids.RequestID{
					NodeID:    nodeID,
					ChainID:   chainID,
					RequestID: requestID,
					Op: byte(
						message.AppResponseOp,
					),
				},
			)
		}
	}
	return appRequests
}

func TestCreateSignedMessageRetriesAndFailsWithoutP2PResponses(t *testing.T) {
	aggregator, mockNetwork := instantiateAggregator(t)

	var (
		connectedValidators, _ = makeConnectedValidators(2)
		requestID              = aggregator.currentRequestID.Load() + 1
	)

	chainID := ids.GenerateTestID()

	msg, err := warp.NewUnsignedMessage(0, chainID, []byte{})
	require.NoError(t, err)

	subnetID := ids.GenerateTestID()
	mockNetwork.EXPECT().GetSubnetID(chainID).Return(
		subnetID,
		nil,
	)

	mockNetwork.EXPECT().TrackSubnet(subnetID)
	mockNetwork.EXPECT().GetConnectedCanonicalValidators(subnetID).Return(
		connectedValidators,
		nil,
	)

	appRequests := makeAppRequests(chainID, requestID, connectedValidators)
	for _, appRequest := range appRequests {
		mockNetwork.EXPECT().RegisterAppRequest(appRequest).AnyTimes()
	}

	mockNetwork.EXPECT().RegisterRequestID(
		requestID,
		len(appRequests),
	).Return(
		make(chan message.InboundMessage, len(appRequests)),
	).AnyTimes()

	var nodeIDs set.Set[ids.NodeID]
	for _, appRequest := range appRequests {
		nodeIDs.Add(appRequest.NodeID)
	}
	mockNetwork.EXPECT().Send(
		gomock.Any(),
		nodeIDs,
		subnetID,
		subnets.NoOpAllower,
	).AnyTimes()

	_, err = aggregator.CreateSignedMessage(msg, nil, subnetID, 80)
	require.ErrorIs(
		t,
		err,
		errNotEnoughSignatures,
	)
}

func TestCreateSignedMessageSucceeds(t *testing.T) {
	var msg *warp.UnsignedMessage // to be signed
	chainID := ids.GenerateTestID()
	networkID := constants.UnitTestID
	msg, err := warp.NewUnsignedMessage(
		networkID,
		chainID,
		utils.RandomBytes(1234),
	)
	require.NoError(t, err)

	// the signers:
	connectedValidators, validatorSecretKeys := makeConnectedValidators(5)

	// prime the aggregator:

	aggregator, mockNetwork := instantiateAggregator(t)

	subnetID := ids.GenerateTestID()
	mockNetwork.EXPECT().GetSubnetID(chainID).Return(
		subnetID,
		nil,
	)

	mockNetwork.EXPECT().TrackSubnet(subnetID)
	mockNetwork.EXPECT().GetConnectedCanonicalValidators(subnetID).Return(
		connectedValidators,
		nil,
	)

	// prime the signers' responses:

	requestID := aggregator.currentRequestID.Load() + 1

	appRequests := makeAppRequests(chainID, requestID, connectedValidators)
	for _, appRequest := range appRequests {
		mockNetwork.EXPECT().RegisterAppRequest(appRequest).Times(1)
	}

	var nodeIDs set.Set[ids.NodeID]
	responseChan := make(chan message.InboundMessage, len(appRequests))
	for _, appRequest := range appRequests {
		nodeIDs.Add(appRequest.NodeID)
		validatorSecretKey := validatorSecretKeys[connectedValidators.NodeValidatorIndexMap[appRequest.NodeID]]
		responseBytes, err := proto.Marshal(
			&sdk.SignatureResponse{
				Signature: bls.SignatureToBytes(
					bls.Sign(
						validatorSecretKey,
						msg.Bytes(),
					),
				),
			},
		)
		require.NoError(t, err)
		responseChan <- message.InboundAppResponse(
			chainID,
			requestID,
			responseBytes,
			appRequest.NodeID,
		)
	}
	mockNetwork.EXPECT().RegisterRequestID(
		requestID,
		len(appRequests),
	).Return(responseChan).Times(1)

	mockNetwork.EXPECT().Send(
		gomock.Any(),
		nodeIDs,
		subnetID,
		subnets.NoOpAllower,
	).Times(1).Return(nodeIDs)

	// aggregate the signatures:
	var quorumPercentage uint64 = 80
	signedMessage, err := aggregator.CreateSignedMessage(
		msg,
		nil,
		subnetID,
		quorumPercentage,
	)
	require.NoError(t, err)

	// verify the aggregated signature:
	pChainState := newPChainStateStub(
		chainID,
		subnetID,
		1,
		connectedValidators,
	)
	verifyErr := signedMessage.Signature.Verify(
		context.Background(),
		msg,
		networkID,
		pChainState,
		pChainState.currentHeight,
		quorumPercentage,
		100,
	)
	require.NoError(t, verifyErr)
}

type pChainStateStub struct {
	subnetIDByChainID            map[ids.ID]ids.ID
	connectedCanonicalValidators *peers.ConnectedCanonicalValidators
	currentHeight                uint64
}

func newPChainStateStub(
	chainID, subnetID ids.ID,
	currentHeight uint64,
	connectedValidators *peers.ConnectedCanonicalValidators,
) *pChainStateStub {
	subnetIDByChainID := make(map[ids.ID]ids.ID)
	subnetIDByChainID[chainID] = subnetID
	return &pChainStateStub{
		subnetIDByChainID:            subnetIDByChainID,
		connectedCanonicalValidators: connectedValidators,
		currentHeight:                currentHeight,
	}
}

func (p pChainStateStub) GetSubnetID(ctx context.Context, chainID ids.ID) (ids.ID, error) {
	return p.subnetIDByChainID[chainID], nil
}

func (p pChainStateStub) GetMinimumHeight(context.Context) (uint64, error) { return 0, nil }

func (p pChainStateStub) GetCurrentHeight(context.Context) (uint64, error) {
	return p.currentHeight, nil
}

func (p pChainStateStub) GetValidatorSet(
	ctx context.Context,
	height uint64,
	subnetID ids.ID,
) (map[ids.NodeID]*validators.GetValidatorOutput, error) {
	output := make(map[ids.NodeID]*validators.GetValidatorOutput)
	for _, validator := range p.connectedCanonicalValidators.ValidatorSet {
		for _, nodeID := range validator.NodeIDs {
			output[nodeID] = &validators.GetValidatorOutput{
				NodeID:    nodeID,
				PublicKey: validator.PublicKey,
				Weight:    validator.Weight,
			}
		}
	}
	return output, nil
}

func (p pChainStateStub) GetCurrentValidatorSet(
	_ context.Context,
	_ ids.ID,
) (map[ids.ID]*validators.GetCurrentValidatorOutput, uint64, error) {
	return nil, 0, nil
}
