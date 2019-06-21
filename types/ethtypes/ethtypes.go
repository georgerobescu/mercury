package ethtypes

import (
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

const (
	EthMainnet EthNetwork = 1
	EthKovan   EthNetwork = 42
)

// ErrUnknownEthNetwork is returned when the given bitcoin network is unknwon to us.
var ErrUnknownEthNetwork = errors.New("unknown ethereum network")

func (network EthNetwork) String() string {
	switch network {
	case EthMainnet:
		return "mainnet"
	case EthKovan:
		return "kovan"
	default:
		panic(ErrUnknownEthNetwork)
	}
}

type EthNetwork uint8

type EthSignedTx *types.Transaction
type EthAddr common.Address

func HexStringToEthAddr(addr string) EthAddr {
	return EthAddr(common.HexToAddress(addr))
}

func (addr EthAddr) Hex() string {
	return common.Address(addr).Hex()
}
